package scanner

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

type symbolBar struct {
	timestamp time.Time
	open      float64
	high      float64
	low       float64
	close     float64
	volume    int64
	vwap      float64
}

type symbolState struct {
	day             string
	bars            []symbolBar
	bars5           []symbolBar
	vwapDollarFlow  float64
	vwapTotalVolume float64
}

type structuredSetup struct {
	setupType   string
	setupHigh   float64
	setupLow    float64
	breakoutPct float64
}

// Scanner scans market ticks for momentum candidates.
type Scanner struct {
	config         config.TradingConfig
	runtime        *runtime.State
	mu             sync.Mutex
	state          map[string]*symbolState
	blockedSymbols map[string]string
	leaderDay      string
	leaderMetrics  map[string]float64 // symbol → cumulative dollar volume for current day
}

// NewScanner creates a scanner with the configured filters.
func NewScanner(cfg config.TradingConfig, runtimeState *runtime.State) *Scanner {
	return &Scanner{
		config:         cfg,
		runtime:        runtimeState,
		state:          make(map[string]*symbolState),
		blockedSymbols: make(map[string]string),
		leaderMetrics:  make(map[string]float64),
	}
}

// SetBlockedSymbols installs a hard blocklist for symbols the scanner should never trade.
func (s *Scanner) SetBlockedSymbols(blocked map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(blocked) == 0 {
		s.blockedSymbols = make(map[string]string)
		return
	}
	copyMap := make(map[string]string, len(blocked))
	for symbol, reason := range blocked {
		copyMap[symbol] = reason
	}
	s.blockedSymbols = copyMap
}

// isVolumeLeader returns true if the symbol ranks within the top MaxVolumeLeaders
// by cumulative dollar volume for the current day. Must be called with s.mu held.
func (s *Scanner) isVolumeLeader(symbol string) bool {
	limit := s.config.MaxVolumeLeaders
	if limit <= 0 {
		return true // disabled
	}
	myVol := s.leaderMetrics[symbol]
	if myVol <= 0 {
		return false
	}
	// Count how many symbols have strictly higher dollar volume.
	rank := 1
	for sym, vol := range s.leaderMetrics {
		if sym != symbol && vol > myVol {
			rank++
			if rank > limit {
				return false
			}
		}
	}
	return true
}

// volumeLeaderStats returns the daily dollar-volume rank and normalized strength
// for a symbol using the scanner's running intraday leaderboard.
func (s *Scanner) volumeLeaderStats(symbol string) (int, float64) {
	myVol := s.leaderMetrics[symbol]
	if myVol <= 0 {
		return 0, 0
	}
	rank := 1
	topVol := myVol
	for sym, vol := range s.leaderMetrics {
		if vol > topVol {
			topVol = vol
		}
		if sym != symbol && vol > myVol {
			rank++
		}
	}
	strengthPct := 0.0
	if topVol > 0 {
		strengthPct = (myVol / topVol) * 100
	}
	return rank, strengthPct
}

// trackDollarVolume accumulates dollar volume for the symbol on the current day.
// Must be called with s.mu held.
func (s *Scanner) trackDollarVolume(tick domain.Tick) {
	day := tick.Timestamp.Format("2006-01-02")
	if day != s.leaderDay {
		s.leaderDay = day
		s.leaderMetrics = make(map[string]float64)
	}
	s.leaderMetrics[tick.Symbol] += tick.Price * float64(tick.Volume)
}

// UpdateConfig replaces the scanner's trading config.
func (s *Scanner) UpdateConfig(cfg config.TradingConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = cfg
}

// Start evaluates ticks concurrently and emits candidates.
func (s *Scanner) Start(ctx context.Context, in <-chan domain.Tick, out chan<- domain.Candidate) error {
	workerCount := s.config.ScannerWorkers
	if workerCount < 1 {
		workerCount = 1
	}

	work := make(chan domain.Tick, 256)
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tick := range work {
				if candidate, ok := s.evaluate(tick); ok {
					select {
					case out <- candidate:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	go func() {
		defer close(work)
		for {
			select {
			case <-ctx.Done():
				return
			case tick, ok := <-in:
				if !ok {
					return
				}
				select {
				case work <- tick:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	wg.Wait()
	return ctx.Err()
}

// Evaluate tests a tick against scanner filters. Exported for backtesting.
func (s *Scanner) Evaluate(tick domain.Tick) (domain.Candidate, bool) {
	return s.evaluate(tick)
}

// EvaluateTickDetailed tests a tick against scanner filters and returns
// the rejection reason when the tick is not a candidate.
func (s *Scanner) EvaluateTickDetailed(tick domain.Tick) (domain.Candidate, bool, string) {
	if reason, blocked := s.instrumentBlockReason(tick.Symbol); blocked {
		return domain.Candidate{}, false, reason
	}
	candidate, ok := s.evaluate(tick)
	if ok {
		return candidate, true, ""
	}
	s.mu.Lock()
	cfg := s.config
	s.mu.Unlock()
	reason := classifyTickRejection(tick, cfg)
	return candidate, false, reason
}

func classifyTickRejection(tick domain.Tick, cfg config.TradingConfig) string {
	if tick.Price <= 0 || tick.Volume <= 0 {
		return "no-data"
	}
	if tick.Price < cfg.MinPrice || tick.Price > cfg.MaxPrice {
		return "price-filter"
	}

	gapQualified := tick.GapPercent >= cfg.MinGapPercent || tick.GapPercent <= -cfg.MinGapPercent

	// Check HOD momo qualification for diagnostics
	hodMomoQualified := false
	if cfg.HODMomoEnabled && tick.Open > 0 && tick.Price > 0 {
		intradayPct := (tick.Price - tick.Open) / tick.Open * 100
		minutesSinceOpen := markethours.MinutesSinceOpen(tick.Timestamp)
		hodMomoQualified = intradayPct >= cfg.HODMomoMinIntradayPct &&
			tick.RelativeVolume >= cfg.HODMomoMinRelativeVolume &&
			minutesSinceOpen >= cfg.HODMomoMinMinutesSinceOpen
		if hodMomoQualified && tick.HighOfDay > 0 {
			distFromHigh := (tick.HighOfDay - tick.Price) / tick.HighOfDay * 100
			pullbackMaxDist := cfg.HODMomoPullbackMaxDist
			if pullbackMaxDist <= 0 {
				pullbackMaxDist = cfg.HODMomoMaxDistFromHigh
			}
			if cfg.HODMomoMaxDistFromHigh > 0 && distFromHigh > cfg.HODMomoMaxDistFromHigh {
				if pullbackMaxDist > 0 && distFromHigh <= pullbackMaxDist {
					// qualifies as pullback
				} else {
					hodMomoQualified = false
				}
			}
		}
	}

	if !gapQualified && !hodMomoQualified {
		// Provide more informative reason when HOD momo is enabled
		if cfg.HODMomoEnabled && tick.Open > 0 && tick.Price > 0 {
			intradayPct := (tick.Price - tick.Open) / tick.Open * 100
			if intradayPct > 0 && intradayPct < cfg.HODMomoMinIntradayPct {
				return "hod-momo-below-threshold"
			}
		}
		return "gap-filter"
	}

	if tick.RelativeVolume < cfg.MinRelativeVolume {
		return "relative-volume"
	}
	if tick.PreMarketVolume < cfg.MinPremarketVolume {
		return "premarket-volume"
	}
	if cfg.MaxFloat > 0 && tick.Float > 0 && tick.Float > cfg.MaxFloat {
		return "float-too-high"
	}
	if cfg.MinFloat > 0 && tick.Float > 0 && tick.Float < cfg.MinFloat {
		return "float-too-low"
	}
	if cfg.MinPrevDayVolume > 0 && tick.PrevDayVolume > 0 && tick.PrevDayVolume < cfg.MinPrevDayVolume {
		return "daily-volume"
	}
	return "other-filter"
}

func (s *Scanner) evaluate(tick domain.Tick) (domain.Candidate, bool) {
	if _, blocked := s.instrumentBlockReason(tick.Symbol); blocked {
		return domain.Candidate{}, false
	}
	if tick.Price <= 0 || tick.Volume <= 0 {
		return domain.Candidate{}, false
	}

	s.mu.Lock()
	cfg := s.config
	s.mu.Unlock()
	if tick.Price < cfg.MinPrice || tick.Price > cfg.MaxPrice {
		return domain.Candidate{}, false
	}

	if cfg.MinFiveMinuteVolume > 0 && tick.FiveMinuteVolume < cfg.MinFiveMinuteVolume {
		return domain.Candidate{}, false
	}
	// Path 1: Traditional gap scanner (existing)
	gapQualified := tick.GapPercent >= cfg.MinGapPercent || tick.GapPercent <= -cfg.MinGapPercent

	// Path 2: HOD Momo scanner (new) — stock is making a big intraday move
	hodMomoQualified := false
	hodMomoPullback := false // true when qualified via pullback distance (beyond breakout range)
	intradayReturnPct := 0.0
	if cfg.HODMomoEnabled && tick.Open > 0 && tick.Price > 0 {
		intradayReturnPct = (tick.Price - tick.Open) / tick.Open * 100
		minutesSinceOpen := markethours.MinutesSinceOpen(tick.Timestamp)

		hodMomoQualified = intradayReturnPct >= cfg.HODMomoMinIntradayPct &&
			tick.RelativeVolume >= cfg.HODMomoMinRelativeVolume &&
			minutesSinceOpen >= cfg.HODMomoMinMinutesSinceOpen

		// Two-tier distance check: breakout (tight) and pullback (wider)
		if hodMomoQualified && tick.HighOfDay > 0 {
			distFromHigh := (tick.HighOfDay - tick.Price) / tick.HighOfDay * 100
			pullbackMaxDist := cfg.HODMomoPullbackMaxDist
			if pullbackMaxDist <= 0 {
				pullbackMaxDist = cfg.HODMomoMaxDistFromHigh // fallback to breakout distance
			}
			if cfg.HODMomoMaxDistFromHigh > 0 && distFromHigh > cfg.HODMomoMaxDistFromHigh {
				// Beyond breakout range — check pullback range
				if pullbackMaxDist > 0 && distFromHigh <= pullbackMaxDist {
					hodMomoPullback = true // qualifies as pullback
				} else {
					hodMomoQualified = false // too far from HOD
				}
			}
		}
	}

	// Must qualify via at least one path
	if !gapQualified && !hodMomoQualified {
		return domain.Candidate{}, false
	}

	// Apply remaining filters based on qualification path
	if gapQualified {
		// Traditional filters apply as-is
		if tick.RelativeVolume < cfg.MinRelativeVolume {
			return domain.Candidate{}, false
		}
		if tick.PreMarketVolume < cfg.MinPremarketVolume {
			return domain.Candidate{}, false
		}
	}
	// HOD momo path: relative volume already checked above, skip premarket volume

	// Float filters apply to both paths
	if cfg.MaxFloat > 0 && tick.Float > 0 && tick.Float > cfg.MaxFloat {
		return domain.Candidate{}, false
	}
	if cfg.MinFloat > 0 && tick.Float > 0 && tick.Float < cfg.MinFloat {
		return domain.Candidate{}, false
	}

	// Minimum daily volume filter — reject thinly traded stocks
	if cfg.MinPrevDayVolume > 0 && tick.PrevDayVolume > 0 && tick.PrevDayVolume < cfg.MinPrevDayVolume {
		return domain.Candidate{}, false
	}

	// Update bar state
	s.mu.Lock()
	s.trackDollarVolume(tick)
	state := s.getOrCreateState(tick)
	s.updateBars(state, tick)
	metrics := s.computeMetrics(state, tick)
	leaderRank, leaderStrengthPct := s.volumeLeaderStats(tick.Symbol)
	s.mu.Unlock()

	// Determine direction
	direction := domain.DirectionLong
	if s.config.EnableShorts && metrics.setupType == "parabolic-failed-reclaim-short" {
		direction = domain.DirectionShort
	}
	priceVsVWAPPct := safePct(tick.Price-metrics.vwap, metrics.vwap) * 100
	if s.qualifiesShortMomentumProfile(tick, priceVsVWAPPct, metrics) {
		direction = domain.DirectionShort
	}
	// HOD momo qualified stocks with positive intraday return are always long
	if hodMomoQualified && !gapQualified && intradayReturnPct > 0 {
		direction = domain.DirectionLong
	}

	if metrics.setupType == "early" {
		return domain.Candidate{}, false
	}

	distFromHighPct := safePct(tick.HighOfDay-tick.Price, tick.HighOfDay) * 100

	// HOD proximity filter: longs must be within MaxDistanceFromHighPct of high of day
	// Skip for HOD momo qualified stocks — they use their own distance thresholds
	if cfg.MaxDistanceFromHighPct > 0 && direction == domain.DirectionLong && !hodMomoQualified {
		if distFromHighPct > cfg.MaxDistanceFromHighPct || distFromHighPct == 0 {
			return domain.Candidate{}, false
		}
	}

	// RSI momentum slope: longs need positive momentum
	if direction == domain.DirectionLong && metrics.rsiMASlope < 0 {
		return domain.Candidate{}, false
	}
	if direction == domain.DirectionShort && metrics.rsiMASlope > 0 {
		return domain.Candidate{}, false
	}

	if cfg.RSIFilterEnabled {
		if direction == domain.DirectionLong && metrics.rsi > cfg.RSIOverboughtThreshold {
			return domain.Candidate{}, false
		}
		if direction == domain.DirectionShort && metrics.rsi < cfg.RSIOversoldThreshold {
			return domain.Candidate{}, false
		}
	}

	// Compute intraday return for candidate
	if tick.Open > 0 && tick.Price > 0 {
		intradayReturnPct = (tick.Price - tick.Open) / tick.Open * 100
	}

	// Get market regime
	regime := s.runtime.MarketRegime()

	// HOD breakout/pullback detection: price near session high with strong intraday move
	if cfg.HODMomoEnabled && tick.Open > 0 && tick.HighOfDay > 0 {
		intradayPct := (tick.Price - tick.Open) / tick.Open * 100
		distFromHOD := (tick.HighOfDay - tick.Price) / tick.HighOfDay * 100
		if intradayPct >= cfg.HODMomoMinIntradayPct {
			if distFromHOD < 1.0 {
				metrics.setupType = "hod_breakout"
			} else if hodMomoPullback {
				metrics.setupType = "hod_pullback"
			}
		}
	}

	if direction == domain.DirectionLong {
		if structured, ok := s.classifyStructuredLongSetup(state, tick, metrics, cfg); ok {
			metrics.setupType = structured.setupType
			metrics.setupHigh = structured.setupHigh
			metrics.setupLow = structured.setupLow
			metrics.breakoutPct = structured.breakoutPct
		}
	}

	setupType := metrics.setupType
	score := computeCandidateScore(cfg, tick, metrics, direction, setupType, intradayReturnPct, leaderRank, leaderStrengthPct)

	candidate := domain.Candidate{
		Symbol:                tick.Symbol,
		Direction:             direction,
		Price:                 tick.Price,
		Open:                  tick.Open,
		GapPercent:            tick.GapPercent,
		RelativeVolume:        tick.RelativeVolume,
		PreMarketVolume:       tick.PreMarketVolume,
		Volume:                tick.Volume,
		HighOfDay:             tick.HighOfDay,
		PriceVsOpenPct:        safePct(tick.Price, tick.Open),
		DistanceFromHighPct:   distFromHighPct,
		OneMinuteReturnPct:    metrics.oneMinuteReturn,
		ThreeMinuteReturnPct:  metrics.threeMinuteReturn,
		FiveMinuteReturnPct:   metrics.fiveMinuteReturn,
		VolumeRate:            metrics.volumeRate,
		MinutesSinceOpen:      markethours.MinutesSinceOpen(tick.Timestamp),
		ATR:                   metrics.atr,
		ATRPct:                safePct(metrics.atr, tick.Price) * 100,
		VWAP:                  metrics.vwap,
		PriceVsVWAPPct:        priceVsVWAPPct,
		VolumeLeaderPct:       leaderStrengthPct,
		LeaderRank:            leaderRank,
		BreakoutPct:           metrics.breakoutPct,
		ConsolidationRangePct: metrics.consolidationRangePct,
		PullbackDepthPct:      metrics.pullbackDepthPct,
		CloseOffHighPct:       metrics.closeOffHighPct,
		SetupHigh:             metrics.setupHigh,
		SetupLow:              metrics.setupLow,
		RSI:                   metrics.rsi,
		RSIMASlope:            metrics.rsiMASlope,
		FiveMinRange:          metrics.fiveMinRange,
		PriceVsEMA9Pct:        safePct(tick.Price-metrics.ema9, metrics.ema9) * 100,
		EMAFast:               metrics.emaFast,
		EMASlow:               metrics.emaSlow,
		MACDHistogram:         metrics.macdHistogram,
		IntradayReturnPct:     intradayReturnPct,
		SetupType:             setupType,
		Score:                 score,
		MarketRegime:          regime.Regime,
		RegimeConfidence:      regime.Confidence,
		Playbook:              s.selectPlaybookFromSetupType(direction, setupType),
		Float:                 tick.Float,
		PrevDayVolume:         tick.PrevDayVolume,
		Catalyst:              tick.Catalyst,
		CatalystURL:           tick.CatalystURL,
		Timestamp:             tick.Timestamp,
	}

	return candidate, true
}

func computeCandidateScore(cfg config.TradingConfig, tick domain.Tick, metrics scanMetrics, direction string, setupType string, intradayReturnPct float64, leaderRank int, leaderStrengthPct float64) float64 {
	score := 0.0
	distFromHighPct := safePct(tick.HighOfDay-tick.Price, tick.HighOfDay) * 100

	if domain.IsLong(direction) {
		switch setupType {
		case "orb_breakout":
			score += 1.35
		case "orb_reclaim":
			score += 1.55
		case "hod_breakout", "breakout":
			score += 1.25
		case "hod_pullback":
			score += 0.9
		case "pullback":
			score += 0.2
		}

		switch {
		case tick.HighOfDay > 0 && distFromHighPct <= 0.5:
			score += 1.25
		case tick.HighOfDay > 0 && distFromHighPct <= 1.5:
			score += 0.95
		case tick.HighOfDay > 0 && distFromHighPct <= 3.0:
			score += 0.45
		}

		switch {
		case metrics.fiveMinuteReturn >= 2.5:
			score += 1.1
		case metrics.fiveMinuteReturn >= 1.2:
			score += 0.8
		case metrics.fiveMinuteReturn >= max(cfg.MinThreeMinuteReturnPct, 0.6):
			score += 0.35
		}

		switch {
		case intradayReturnPct >= 15:
			score += 1.1
		case intradayReturnPct >= 8:
			score += 0.75
		case intradayReturnPct >= max(cfg.MinGapPercent, 4.0):
			score += 0.35
		}

		switch {
		case tick.RelativeVolume >= 10:
			score += 1.0
		case tick.RelativeVolume >= 5:
			score += 0.75
		case tick.RelativeVolume >= max(cfg.MinRelativeVolume, 2.5):
			score += 0.35
		}

		switch {
		case metrics.volumeRate >= 20000:
			score += 0.35
		case metrics.volumeRate >= 10000:
			score += 0.2
		}

		switch {
		case tick.Float > 0 && tick.Float <= 50_000_000:
			score += 0.9
		case tick.Float > 0 && tick.Float <= 100_000_000:
			score += 0.55
		case tick.Float > 0 && tick.Float <= 250_000_000:
			score += 0.2
		case tick.Float > 250_000_000:
			score -= 0.6
		default:
			score -= 0.15
		}

		switch {
		case tick.Price >= cfg.MinPrice && tick.Price <= 15:
			score += 0.55
		case tick.Price > 15 && tick.Price <= 25:
			score += 0.2
		case tick.Price > 25:
			score -= 0.2
		}

		if leaderRank > 0 {
			switch {
			case leaderRank <= 3:
				score += 0.55
			case leaderRank <= max(cfg.MaxVolumeLeaders, 5):
				score += 0.3
			}
			if leaderStrengthPct >= 60 {
				score += 0.15
			}
		}

		if metrics.macdHistogram > 0 {
			score += 0.25
		}
		if metrics.ema9 > 0 && tick.Price > metrics.ema9 {
			score += 0.2
		}
		if metrics.vwap > 0 && tick.Price > metrics.vwap {
			score += 0.15
		}
		if tick.Catalyst != "" {
			score += 0.35
		}
		if markethours.IsMarketOpen(tick.Timestamp) && setupType == "hod_pullback" {
			score -= 0.35
		}
	} else {
		switch setupType {
		case "parabolic-failed-reclaim-short":
			score += 1.0
		case "breakdown":
			score += 0.6
		}
		if metrics.oneMinuteReturn <= -0.5 {
			score += 0.5
		}
		if metrics.threeMinuteReturn <= -1.0 {
			score += 0.5
		}
		if metrics.macdHistogram < 0 {
			score += 0.3
		}
		if metrics.vwap > 0 && tick.Price < metrics.vwap {
			score += 0.2
		}
	}

	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	if score < 0 {
		return 0
	}
	return score
}

func (s *Scanner) instrumentBlockReason(symbol string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason, blocked := s.blockedSymbols[symbol]
	return reason, blocked
}

func (s *Scanner) qualifiesShortMomentumProfile(tick domain.Tick, priceVsVWAPPCT float64, metrics scanMetrics) bool {
	if !s.config.EnableShorts {
		return false
	}
	if metrics.setupType != "parabolic-failed-reclaim-short" {
		return false
	}
	if tick.RelativeVolume < s.config.MinRelativeVolume {
		return false
	}
	vwapLimit := 2.0 // parabolic-failed-reclaim uses fixed VWAP tolerance
	if priceVsVWAPPCT > vwapLimit {
		return false
	}
	if metrics.oneMinuteReturn >= -max(0.25, s.config.MinOneMinuteReturnPct*0.50) {
		return false
	}
	if metrics.threeMinuteReturn >= -max(0.50, s.config.MinThreeMinuteReturnPct*0.75) {
		return false
	}
	if metrics.breakoutPct > -0.05 {
		return false
	}
	if tick.Open <= 0 || tick.HighOfDay <= 0 {
		return false
	}
	peakExtensionPct := ((tick.HighOfDay - tick.Open) / tick.Open) * 100
	if peakExtensionPct < s.config.ShortPeakExtensionMinPct {
		return false
	}
	if tick.GapPercent < s.config.MinGapPercent {
		return false
	}
	// priceVsOpenPct from caller is a ratio (price/open), convert to pct change for threshold
	pctAboveOpen := 0.0
	if tick.Open > 0 {
		pctAboveOpen = (tick.Price - tick.Open) / tick.Open * 100
	}
	return pctAboveOpen >= max(2.0, s.config.MinGapPercent*0.5)
}

func (s *Scanner) getOrCreateState(tick domain.Tick) *symbolState {
	day := markethours.TradingDay(tick.Timestamp)
	state, ok := s.state[tick.Symbol]
	if !ok || state.day != day {
		state = &symbolState{
			day:   day,
			bars:  make([]symbolBar, 0, 390),
			bars5: make([]symbolBar, 0, 80),
		}
		s.state[tick.Symbol] = state
	}
	return state
}

func (s *Scanner) updateBars(state *symbolState, tick domain.Tick) {
	bar := symbolBar{
		timestamp: tick.Timestamp,
		open:      tick.BarOpen,
		high:      tick.BarHigh,
		low:       tick.BarLow,
		close:     tick.Price,
		volume:    tick.BarVolume,
	}
	if len(state.bars) > 0 && state.bars[len(state.bars)-1].timestamp.Equal(tick.Timestamp) {
		prev := state.bars[len(state.bars)-1]
		state.bars[len(state.bars)-1] = bar
		s.updateVWAP(state, &prev)
		s.updateFiveMinuteBars(state)
	} else {
		state.bars = append(state.bars, bar)
		s.updateVWAP(state, nil)
		s.updateFiveMinuteBars(state)
	}
}

func (s *Scanner) updateVWAP(state *symbolState, prev *symbolBar) {
	if prev != nil && prev.high > 0 && prev.low > 0 && prev.volume > 0 {
		prevTypical := (prev.high + prev.low + prev.close) / 3
		state.vwapDollarFlow -= prevTypical * float64(prev.volume)
		state.vwapTotalVolume -= float64(prev.volume)
		if state.vwapTotalVolume < 0 {
			state.vwapTotalVolume = 0
		}
	}

	last := &state.bars[len(state.bars)-1]
	last.vwap = 0
	if last.high > 0 && last.low > 0 && last.volume > 0 {
		typical := (last.high + last.low + last.close) / 3
		state.vwapDollarFlow += typical * float64(last.volume)
		state.vwapTotalVolume += float64(last.volume)
	}
	if state.vwapTotalVolume > 0 {
		last.vwap = state.vwapDollarFlow / state.vwapTotalVolume
	}
}

func (s *Scanner) updateFiveMinuteBars(state *symbolState) {
	last := state.bars[len(state.bars)-1]
	bucket := fiveMinuteBucketStart(last.timestamp)
	n5 := len(state.bars5)
	if n5 == 0 || !state.bars5[n5-1].timestamp.Equal(bucket) {
		state.bars5 = append(state.bars5, symbolBar{
			timestamp: bucket,
			open:      last.open,
			high:      last.high,
			low:       last.low,
			close:     last.close,
			volume:    max(last.volume, 0),
		})
		return
	}
	state.bars5[n5-1] = buildFiveMinuteBar(state.bars, bucket)
}

func buildFiveMinuteBar(bars []symbolBar, bucket time.Time) symbolBar {
	agg := symbolBar{timestamp: bucket}
	started := false
	for i := len(bars) - 1; i >= 0; i-- {
		if !fiveMinuteBucketStart(bars[i].timestamp).Equal(bucket) {
			if started {
				break
			}
			continue
		}
		if !started {
			agg.close = bars[i].close
			started = true
		}
		agg.open = bars[i].open
		if bars[i].high > agg.high {
			agg.high = bars[i].high
		}
		if agg.low == 0 || (bars[i].low > 0 && bars[i].low < agg.low) {
			agg.low = bars[i].low
		}
		agg.volume += max(bars[i].volume, 0)
	}
	return agg
}

type scanMetrics struct {
	oneMinuteReturn            float64
	threeMinuteReturn          float64
	fiveMinuteReturn           float64
	volumeRate                 float64
	atr                        float64
	vwap                       float64
	breakoutPct                float64
	consolidationRangePct      float64
	pullbackDepthPct           float64
	closeOffHighPct            float64
	setupHigh                  float64
	setupLow                   float64
	setupType                  string
	rsi                        float64
	rsiMASlope                 float64
	fiveMinRange               float64
	ema9                       float64
	emaFast                    float64
	emaSlow                    float64
	macdHistogram              float64
	adx                        float64
	bbUpper                    float64
	bbMiddle                   float64
	bbLower                    float64
	volumeDecreasingOnPullback bool
}

func (s *Scanner) computeMetrics(state *symbolState, tick domain.Tick) scanMetrics {
	var m scanMetrics
	bars := state.bars
	n := len(bars)
	bars5 := state.bars5
	n5 := len(bars5)

	// Returns
	if n >= 2 {
		m.oneMinuteReturn = safePct(tick.Price-bars[n-2].close, bars[n-2].close) * 100
	}
	if n >= 4 {
		m.threeMinuteReturn = safePct(tick.Price-bars[n-4].close, bars[n-4].close) * 100
	}
	if n5 >= 1 && bars5[n5-1].open > 0 {
		m.fiveMinuteReturn = safePct(bars5[n5-1].close-bars5[n5-1].open, bars5[n5-1].open) * 100
	}

	// Volume rate
	if n >= 2 {
		elapsed := tick.Timestamp.Sub(bars[0].timestamp).Minutes()
		if elapsed > 0 {
			m.volumeRate = float64(tick.Volume) / elapsed
		}
	}

	// ATR (14-period) on aligned 5-minute candles — require minimum bars for reliable ATR
	m.atr = computeATR(bars5, 14)
	if n5 < s.config.MinATRBars {
		m.atr = 0 // force percentage fallback in strategy
	}

	// VWAP
	if n > 0 {
		m.vwap = bars[n-1].vwap
	}

	// EMAs and MACD histogram on aligned 5-minute candles
	m.ema9, m.emaFast, m.emaSlow, m.macdHistogram = computeEMAsAndMACDHistogram(
		bars5,
		9,
		s.config.MarketRegimeEMAFastPeriod,
		s.config.MarketRegimeEMASlowPeriod,
		s.config.MACDFastPeriod,
		s.config.MACDSlowPeriod,
		s.config.MACDSignalPeriod,
	)

	// RSI and RSI MA Slope (14-period Wilder RSI) on aligned 5-minute candles
	m.rsi = computeRSI(bars5, 14)
	if n5 >= 15 {
		rsiValues := make([]float64, 0, 10)
		start := n5 - 10
		if start < 0 {
			start = 0
		}
		for i := start; i < n5; i++ {
			subBars := bars5[:i+1]
			rsiValues = append(rsiValues, computeRSI(subBars, 14))
		}
		if len(rsiValues) >= 2 {
			m.rsiMASlope = (rsiValues[len(rsiValues)-1] - rsiValues[0]) / float64(len(rsiValues))
		}
	}

	// ADX for mean-reversion detection
	m.adx = computeADX(bars5, 14)

	// Bollinger Bands
	bbPeriod := s.config.BollingerPeriod
	if bbPeriod == 0 {
		bbPeriod = 20
	}
	bbK := s.config.BollingerK
	if bbK == 0 {
		bbK = 2.0
	}
	m.bbUpper, m.bbMiddle, m.bbLower = computeBollingerBands(bars5, bbPeriod, bbK)

	// Setup detection
	m.setupType = "early" // default when insufficient bars
	if n >= 5 {
		high5 := bars[n-5].high
		low5 := bars[n-5].low
		for i := n - 4; i < n; i++ {
			if bars[i].high > high5 {
				high5 = bars[i].high
			}
			if bars[i].low < low5 {
				low5 = bars[i].low
			}
		}
		m.setupHigh = high5
		m.setupLow = low5
		m.fiveMinRange = high5 - low5
		if high5 > 0 {
			m.consolidationRangePct = (high5 - low5) / high5 * 100
		}
		if tick.Price > high5 {
			m.breakoutPct = safePct(tick.Price-high5, high5) * 100
			m.setupType = "breakout"
		} else if tick.Price < low5 {
			m.breakoutPct = safePct(low5-tick.Price, low5) * 100
			m.setupType = "breakdown"
		} else {
			depth := high5 - tick.Price
			if high5-low5 > 0 {
				m.pullbackDepthPct = depth / (high5 - low5) * 100
			}
			m.setupType = "pullback"
		}
		if high5 > 0 {
			m.closeOffHighPct = (high5 - tick.Price) / high5 * 100
		}

		// Parabolic-failed-reclaim-short detection (old repo used len(bars) >= 4; lastNBars handles short slices)
		{
			completed := bars[:n-1]
			peakHigh := maxBarHigh(lastNBars(completed, 12))
			peakExtensionPct := 0.0
			if bars[0].open > 0 {
				peakExtensionPct = (peakHigh - bars[0].open) / bars[0].open * 100
			}
			reclaimFailureHigh := maxBarHigh(lastNBars(completed, 3))
			breakdownLow := minBarLow(lastNBars(completed, 3))
			current := bars[n-1]
			barCloseOffHigh := 0.0
			if current.high > current.low {
				barCloseOffHigh = (current.high - current.close) / (current.high - current.low) * 100
			}
			weakClose := barCloseOffHigh >= 60

			if peakExtensionPct >= s.config.ShortPeakExtensionMinPct &&
				m.oneMinuteReturn <= -0.35 &&
				m.threeMinuteReturn <= -0.75 &&
				weakClose &&
				breakdownLow > 0 &&
				current.close < breakdownLow &&
				reclaimFailureHigh > current.close &&
				reclaimFailureHigh < peakHigh {
				m.setupType = "parabolic-failed-reclaim-short"
				m.setupHigh = reclaimFailureHigh
				m.setupLow = breakdownLow
				m.breakoutPct = safePct(current.close-breakdownLow, breakdownLow) * 100
			}
		}

		// Volume-on-pullback: check if recent bars have decreasing volume
		if m.setupType == "pullback" && n >= 5 {
			barVols := make([]int64, n)
			for i := 0; i < n; i++ {
				barVols[i] = max(bars[i].volume, 0)
			}
			peakVolIdx := n - 5
			for i := n - 4; i < n; i++ {
				if barVols[i] > barVols[peakVolIdx] {
					peakVolIdx = i
				}
			}
			if peakVolIdx < n-1 {
				volumeDecreasing := true
				for i := peakVolIdx + 1; i < n; i++ {
					if barVols[i] > barVols[i-1]*115/100 { // allow 15% tolerance
						volumeDecreasing = false
						break
					}
				}
				m.volumeDecreasingOnPullback = volumeDecreasing
			}
		}
	}

	return m
}

func (s *Scanner) selectPlaybookFromSetupType(direction string, setupType string) string {
	switch setupType {
	case "orb_breakout", "hod_breakout", "breakout", "breakdown":
		return "breakout"
	case "orb_reclaim":
		return "continuation"
	case "hod_pullback", "pullback":
		return "pullback"
	case "parabolic-failed-reclaim-short":
		return "reversal"
	}
	if direction == domain.DirectionLong {
		return "continuation"
	}
	return "reversal"
}

func (s *Scanner) classifyStructuredLongSetup(state *symbolState, tick domain.Tick, metrics scanMetrics, cfg config.TradingConfig) (structuredSetup, bool) {
	if !markethours.IsMarketOpen(tick.Timestamp) {
		return structuredSetup{}, false
	}
	minutesSinceOpen := markethours.MinutesSinceOpen(tick.Timestamp)
	if minutesSinceOpen < 5 || minutesSinceOpen > 90 {
		return structuredSetup{}, false
	}

	window := cfg.ORBWindowMinutes
	if window <= 0 {
		window = 5
	}
	if window < 5 {
		window = 5
	}
	if window > 15 {
		window = 15
	}

	bufferPct := cfg.ORBBufferPct
	if bufferPct <= 0 {
		bufferPct = 0.001
	}

	bars := regularSessionBars(state.bars)
	if len(bars) <= window {
		return structuredSetup{}, false
	}

	rangeBars := bars[:window]
	rangeHigh := maxBarHigh(rangeBars)
	rangeLow := minBarLow(rangeBars)
	rangeWidth := rangeHigh - rangeLow
	if rangeHigh <= 0 || rangeWidth <= 0 {
		return structuredSetup{}, false
	}

	avgRangeVolume := averageBarVolume(rangeBars)
	volumeMultiplier := cfg.ORBVolumeMultiplier
	if volumeMultiplier <= 0 {
		volumeMultiplier = 1.2
	}
	volumeThreshold := avgRangeVolume * max(volumeMultiplier*0.75, 1.0)
	breakoutLevel := rangeHigh * (1 + bufferPct)
	currentIdx := len(bars) - 1
	currentBar := bars[currentIdx]

	breakoutIdx := -1
	for i := window; i < len(bars); i++ {
		if bars[i].close > breakoutLevel && float64(max(bars[i].volume, 0)) >= volumeThreshold {
			breakoutIdx = i
			break
		}
	}
	if breakoutIdx == -1 {
		return structuredSetup{}, false
	}

	if breakoutIdx == currentIdx {
		breakoutPct := safePct(currentBar.close-breakoutLevel, breakoutLevel) * 100
		if metrics.oneMinuteReturn > 0 &&
			metrics.threeMinuteReturn > 0 &&
			metrics.fiveMinuteReturn >= math.Max(cfg.MinThreeMinuteReturnPct, 1.0) &&
			breakoutPct <= 3.5 {
			return structuredSetup{
				setupType:   "orb_breakout",
				setupHigh:   rangeHigh,
				setupLow:    rangeLow,
				breakoutPct: breakoutPct,
			}, true
		}
		return structuredSetup{}, false
	}

	if currentIdx-breakoutIdx < 2 {
		return structuredSetup{}, false
	}

	postBreakoutBars := bars[breakoutIdx : currentIdx+1]
	peakHigh := maxBarHigh(postBreakoutBars)
	pullbackLow := minBarLow(bars[breakoutIdx+1 : currentIdx])
	if peakHigh <= 0 || pullbackLow <= 0 || pullbackLow >= peakHigh {
		return structuredSetup{}, false
	}

	pullbackDepthPct := safePct(peakHigh-pullbackLow, peakHigh) * 100
	if pullbackDepthPct < 0.5 || pullbackDepthPct > 4.5 {
		return structuredSetup{}, false
	}
	if pullbackLow < rangeHigh*(1-0.003) {
		return structuredSetup{}, false
	}

	reclaimLookback := 3
	if currentIdx-breakoutIdx < reclaimLookback {
		reclaimLookback = currentIdx - breakoutIdx
	}
	if reclaimLookback < 2 {
		return structuredSetup{}, false
	}
	reclaimHigh := maxBarHigh(bars[currentIdx-reclaimLookback : currentIdx])
	if reclaimHigh <= 0 {
		return structuredSetup{}, false
	}

	reclaimPct := safePct(currentBar.close-reclaimHigh, reclaimHigh) * 100
	if currentBar.close > reclaimHigh &&
		currentBar.close > breakoutLevel &&
		metrics.oneMinuteReturn > 0 &&
		metrics.fiveMinuteReturn >= math.Max(cfg.MinThreeMinuteReturnPct*0.75, 0.5) &&
		tick.Price > metrics.vwap &&
		reclaimPct <= 2.5 {
		return structuredSetup{
			setupType:   "orb_reclaim",
			setupHigh:   reclaimHigh,
			setupLow:    pullbackLow,
			breakoutPct: reclaimPct,
		}, true
	}

	return structuredSetup{}, false
}

func regularSessionBars(bars []symbolBar) []symbolBar {
	sessionBars := make([]symbolBar, 0, len(bars))
	loc := markethours.Location()
	for _, bar := range bars {
		barET := bar.timestamp.In(loc)
		minutes := float64(barET.Hour()*60 + barET.Minute())
		if minutes < 570 || minutes >= 960 {
			continue
		}
		sessionBars = append(sessionBars, bar)
	}
	return sessionBars
}

func averageBarVolume(bars []symbolBar) float64 {
	if len(bars) == 0 {
		return 0
	}
	total := int64(0)
	for _, bar := range bars {
		total += max(bar.volume, 0)
	}
	return float64(total) / float64(len(bars))
}

func computeATR(bars []symbolBar, period int) float64 {
	n := len(bars)
	if n < 2 {
		return 0
	}
	start := n - period
	if start < 1 {
		start = 1
	}
	sum := 0.0
	count := 0
	for i := start; i < n; i++ {
		tr := math.Max(bars[i].high-bars[i].low,
			math.Max(math.Abs(bars[i].high-bars[i-1].close),
				math.Abs(bars[i].low-bars[i-1].close)))
		sum += tr
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// aggregate5MinBars collapses 1-minute bars into aligned 5-minute OHLCV candles.
func aggregate5MinBars(bars []symbolBar) []symbolBar {
	if len(bars) == 0 {
		return nil
	}
	out := make([]symbolBar, 0, len(bars)/5+1)
	for _, bar := range bars {
		bucket := fiveMinuteBucketStart(bar.timestamp)
		if len(out) == 0 || !out[len(out)-1].timestamp.Equal(bucket) {
			out = append(out, symbolBar{
				timestamp: bucket,
				open:      bar.open,
				high:      bar.high,
				low:       bar.low,
				close:     bar.close,
				volume:    max(bar.volume, 0),
			})
			continue
		}
		agg := &out[len(out)-1]
		if bar.high > agg.high {
			agg.high = bar.high
		}
		if bar.low < agg.low || agg.low == 0 {
			agg.low = bar.low
		}
		agg.close = bar.close
		agg.volume += max(bar.volume, 0)
	}
	return out
}

func fiveMinuteBucketStart(ts time.Time) time.Time {
	et := ts.In(markethours.Location())
	minute := et.Minute() - (et.Minute() % 5)
	return time.Date(et.Year(), et.Month(), et.Day(), et.Hour(), minute, 0, 0, markethours.Location())
}

func computeEMAsAndMACDHistogram(
	bars []symbolBar,
	ema9Period, regimeFastPeriod, regimeSlowPeriod, macdFastPeriod, macdSlowPeriod, macdSignalPeriod int,
) (ema9, regimeFast, regimeSlow, macdHistogram float64) {
	if len(bars) == 0 {
		return 0, 0, 0, 0
	}

	ema9 = bars[0].close
	regimeFast = bars[0].close
	regimeSlow = bars[0].close
	macdFast := bars[0].close
	macdSlow := bars[0].close
	macdLine := 0.0
	signal := 0.0

	ema9Mult := 0.0
	if ema9Period > 0 {
		ema9Mult = 2.0 / float64(ema9Period+1)
	}
	regimeFastMult := 0.0
	if regimeFastPeriod > 0 {
		regimeFastMult = 2.0 / float64(regimeFastPeriod+1)
	}
	regimeSlowMult := 0.0
	if regimeSlowPeriod > 0 {
		regimeSlowMult = 2.0 / float64(regimeSlowPeriod+1)
	}
	macdFastMult := 0.0
	if macdFastPeriod > 0 {
		macdFastMult = 2.0 / float64(macdFastPeriod+1)
	}
	macdSlowMult := 0.0
	if macdSlowPeriod > 0 {
		macdSlowMult = 2.0 / float64(macdSlowPeriod+1)
	}
	signalMult := 0.0
	if macdSignalPeriod > 0 {
		signalMult = 2.0 / float64(macdSignalPeriod+1)
	}

	for i := 1; i < len(bars); i++ {
		close := bars[i].close
		if ema9Mult > 0 {
			ema9 = (close-ema9)*ema9Mult + ema9
		}
		if regimeFastMult > 0 {
			regimeFast = (close-regimeFast)*regimeFastMult + regimeFast
		}
		if regimeSlowMult > 0 {
			regimeSlow = (close-regimeSlow)*regimeSlowMult + regimeSlow
		}
		if macdFastMult > 0 {
			macdFast = (close-macdFast)*macdFastMult + macdFast
		}
		if macdSlowMult > 0 {
			macdSlow = (close-macdSlow)*macdSlowMult + macdSlow
		}
		macdLine = macdFast - macdSlow
		if i == 1 {
			signal = macdLine
			continue
		}
		if signalMult > 0 {
			signal = (macdLine-signal)*signalMult + signal
		}
	}
	macdHistogram = macdLine - signal
	return ema9, regimeFast, regimeSlow, macdHistogram
}

// computeMACDHistogram returns the MACD histogram (MACD line - signal line).
func computeMACDHistogram(bars []symbolBar, fastPeriod, slowPeriod, signalPeriod int) float64 {
	n := len(bars)
	if n == 0 || fastPeriod <= 0 || slowPeriod <= 0 || signalPeriod <= 0 {
		return 0
	}
	// Compute running fast and slow EMAs, then MACD line at each bar
	fastMult := 2.0 / float64(fastPeriod+1)
	slowMult := 2.0 / float64(slowPeriod+1)
	emaFast := bars[0].close
	emaSlow := bars[0].close
	macdValues := make([]float64, n)
	for i := 0; i < n; i++ {
		if i > 0 {
			emaFast = (bars[i].close-emaFast)*fastMult + emaFast
			emaSlow = (bars[i].close-emaSlow)*slowMult + emaSlow
		}
		macdValues[i] = emaFast - emaSlow
	}
	// Signal line = EMA of MACD values
	sigMult := 2.0 / float64(signalPeriod+1)
	signal := macdValues[0]
	for i := 1; i < n; i++ {
		signal = (macdValues[i]-signal)*sigMult + signal
	}
	return macdValues[n-1] - signal
}

func computeEMA(bars []symbolBar, period int) float64 {
	n := len(bars)
	if n == 0 || period <= 0 {
		return 0
	}
	multiplier := 2.0 / float64(period+1)
	ema := bars[0].close
	for i := 1; i < n; i++ {
		ema = (bars[i].close-ema)*multiplier + ema
	}
	return ema
}

// computeRSI computes the 14-period Wilder RSI from a series of bars.
func computeRSI(bars []symbolBar, period int) float64 {
	if len(bars) < period+1 {
		return 50.0 // neutral default
	}

	var avgGain, avgLoss float64
	// Initial average over first 'period' changes
	for i := 1; i <= period; i++ {
		change := bars[i].close - bars[i-1].close
		if change > 0 {
			avgGain += change
		} else {
			avgLoss += -change
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	// Wilder's smoothing for remaining bars
	for i := period + 1; i < len(bars); i++ {
		change := bars[i].close - bars[i-1].close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100.0
	}
	rs := avgGain / avgLoss
	return 100.0 - (100.0 / (1.0 + rs))
}

// RankCandidates applies cross-sectional volume leader ranking to a batch of candidates.
func RankCandidates(candidates []domain.Candidate) {
	if len(candidates) == 0 {
		return
	}

	type ranked struct {
		idx       int
		composite float64
	}
	rankings := make([]ranked, len(candidates))
	for i, c := range candidates {
		rankings[i] = ranked{idx: i, composite: c.RelativeVolume * math.Abs(c.GapPercent)}
	}

	sort.Slice(rankings, func(i, j int) bool {
		return rankings[i].composite > rankings[j].composite
	})

	topComposite := rankings[0].composite
	for i, r := range rankings {
		candidates[r.idx].LeaderRank = i + 1
		if topComposite > 0 {
			candidates[r.idx].VolumeLeaderPct = (r.composite / topComposite) * 100
		}
	}
}

// computeBollingerBands computes upper, middle, and lower Bollinger Bands.
func computeBollingerBands(bars []symbolBar, period int, k float64) (upper, middle, lower float64) {
	if len(bars) < period || period < 2 {
		return 0, 0, 0
	}

	recent := bars[len(bars)-period:]

	var sum float64
	for _, b := range recent {
		sum += b.close
	}
	middle = sum / float64(period)

	var sumSq float64
	for _, b := range recent {
		diff := b.close - middle
		sumSq += diff * diff
	}
	stddev := math.Sqrt(sumSq / float64(period-1))

	upper = middle + k*stddev
	lower = middle - k*stddev
	return
}

// ComputeBollingerBandsFromPrices computes Bollinger Bands from a slice of prices (exported for strategy use).
func ComputeBollingerBandsFromPrices(prices []float64, period int, k float64) (upper, middle, lower float64) {
	if len(prices) < period || period < 2 {
		return 0, 0, 0
	}

	recent := prices[len(prices)-period:]

	var sum float64
	for _, p := range recent {
		sum += p
	}
	middle = sum / float64(period)

	var sumSq float64
	for _, p := range recent {
		diff := p - middle
		sumSq += diff * diff
	}
	stddev := math.Sqrt(sumSq / float64(period-1))

	upper = middle + k*stddev
	lower = middle - k*stddev
	return
}

// computeADX computes the Average Directional Index.
func computeADX(bars []symbolBar, period int) float64 {
	n := len(bars)
	if n < period*2+1 {
		return 50 // default to trending if insufficient data
	}

	// Compute True Range, +DM, -DM
	trValues := make([]float64, n-1)
	plusDM := make([]float64, n-1)
	minusDM := make([]float64, n-1)

	for i := 1; i < n; i++ {
		high := bars[i].high
		low := bars[i].low
		prevHigh := bars[i-1].high
		prevLow := bars[i-1].low
		prevClose := bars[i-1].close

		tr := math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
		trValues[i-1] = tr

		upMove := high - prevHigh
		downMove := prevLow - low

		if upMove > downMove && upMove > 0 {
			plusDM[i-1] = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM[i-1] = downMove
		}
	}

	// Wilder's smoothing for first period
	var smoothTR, smoothPlusDM, smoothMinusDM float64
	for i := 0; i < period; i++ {
		smoothTR += trValues[i]
		smoothPlusDM += plusDM[i]
		smoothMinusDM += minusDM[i]
	}

	// Compute DX values
	dxValues := make([]float64, 0, n-period)
	for i := period; i < len(trValues); i++ {
		if i > period {
			smoothTR = smoothTR - smoothTR/float64(period) + trValues[i]
			smoothPlusDM = smoothPlusDM - smoothPlusDM/float64(period) + plusDM[i]
			smoothMinusDM = smoothMinusDM - smoothMinusDM/float64(period) + minusDM[i]
		}

		var plusDI, minusDI float64
		if smoothTR > 0 {
			plusDI = (smoothPlusDM / smoothTR) * 100
			minusDI = (smoothMinusDM / smoothTR) * 100
		}

		diSum := plusDI + minusDI
		if diSum > 0 {
			dx := (math.Abs(plusDI-minusDI) / diSum) * 100
			dxValues = append(dxValues, dx)
		}
	}

	if len(dxValues) < period {
		return 50
	}

	// First ADX is SMA of first period DX values
	var adxSum float64
	for i := 0; i < period; i++ {
		adxSum += dxValues[i]
	}
	adx := adxSum / float64(period)

	// Smooth remaining
	for i := period; i < len(dxValues); i++ {
		adx = (adx*float64(period-1) + dxValues[i]) / float64(period)
	}

	return adx
}

// ComputeSlippage computes percentage-based slippage by liquidity tier.
func ComputeSlippage(price float64, avgDailyVolume float64, liquidBps, midBps, illiquidBps float64) float64 {
	var spreadPct float64
	switch {
	case avgDailyVolume > 5_000_000:
		spreadPct = liquidBps / 10000.0
	case avgDailyVolume > 500_000:
		spreadPct = midBps / 10000.0
	default:
		spreadPct = illiquidBps / 10000.0
	}
	return price * spreadPct
}

func safePct(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}

func lastNBars(bars []symbolBar, n int) []symbolBar {
	if len(bars) <= n {
		return bars
	}
	return bars[len(bars)-n:]
}

func maxBarHigh(bars []symbolBar) float64 {
	if len(bars) == 0 {
		return 0
	}
	h := bars[0].high
	for _, b := range bars[1:] {
		if b.high > h {
			h = b.high
		}
	}
	return h
}

func minBarLow(bars []symbolBar) float64 {
	if len(bars) == 0 {
		return 0
	}
	l := bars[0].low
	for _, b := range bars[1:] {
		if b.low > 0 && b.low < l {
			l = b.low
		}
	}
	return l
}
