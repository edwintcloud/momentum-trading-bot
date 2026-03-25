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
	timestamp        time.Time
	open             float64
	high             float64
	low              float64
	close            float64
	volume           int64
	cumulativeVolume int64
	vwap             float64
}

type symbolState struct {
	day                  string
	bars                 []symbolBar
	cumulativeDollarFlow float64
	cumulativeVolume     float64
}

// Scanner scans market ticks for momentum candidates.
type Scanner struct {
	config        config.TradingConfig
	runtime       *runtime.State
	mu            sync.Mutex
	state         map[string]*symbolState
	leaderDay     string
	leaderMetrics map[string]float64 // symbol → cumulative dollar volume for current day
}

// NewScanner creates a scanner with the configured filters.
func NewScanner(cfg config.TradingConfig, runtimeState *runtime.State) *Scanner {
	return &Scanner{
		config:        cfg,
		runtime:       runtimeState,
		state:         make(map[string]*symbolState),
		leaderMetrics: make(map[string]float64),
	}
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
				default:
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
	if tick.Price <= 0 || tick.Volume <= 0 {
		return domain.Candidate{}, false
	}

	s.mu.Lock()
	cfg := s.config
	// Track dollar volume for volume leaders filtering.
	s.trackDollarVolume(tick)
	s.mu.Unlock()
	if tick.Price < cfg.MinPrice || tick.Price > cfg.MaxPrice {
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
	state := s.getOrCreateState(tick)
	s.updateBars(state, tick)
	metrics := s.computeMetrics(state, tick)
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

	setupType := metrics.setupType

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
		VolumeRate:            metrics.volumeRate,
		MinutesSinceOpen:      markethours.MinutesSinceOpen(tick.Timestamp),
		ATR:                   metrics.atr,
		ATRPct:                safePct(metrics.atr, tick.Price) * 100,
		VWAP:                  metrics.vwap,
		PriceVsVWAPPct:        priceVsVWAPPct,
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
		Score:                 1.0,
		MarketRegime:          regime.Regime,
		RegimeConfidence:      regime.Confidence,
		Playbook:              s.selectPlaybookFromSetupType(direction, setupType),
		Float:                 tick.Float,
		PrevDayVolume:         tick.PrevDayVolume,
		Catalyst:              tick.Catalyst,
		CatalystURL:           tick.CatalystURL,
		Timestamp:             tick.Timestamp,
	}

	// Volume leaders gate: only emit candidates for top N symbols by dollar volume.
	s.mu.Lock()
	leader := s.isVolumeLeader(tick.Symbol)
	s.mu.Unlock()
	if !leader {
		return domain.Candidate{}, false
	}

	return candidate, true
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
		state = &symbolState{day: day, bars: make([]symbolBar, 0, 390)}
		s.state[tick.Symbol] = state
	}
	return state
}

func (s *Scanner) updateBars(state *symbolState, tick domain.Tick) {
	bar := symbolBar{
		timestamp:        tick.Timestamp,
		open:             tick.BarOpen,
		high:             tick.BarHigh,
		low:              tick.BarLow,
		close:            tick.Price,
		volume:           tick.Volume,
		cumulativeVolume: tick.Volume,
	}
	if tick.BarHigh > 0 && tick.BarLow > 0 {
		typical := (tick.BarHigh + tick.BarLow + tick.Price) / 3
		// Derive per-bar volume from cumulative session volume
		barVol := tick.Volume
		if len(state.bars) > 0 {
			barVol = tick.Volume - state.bars[len(state.bars)-1].cumulativeVolume
		}
		if barVol < 0 {
			barVol = 0
		}
		state.cumulativeDollarFlow += typical * float64(barVol)
		state.cumulativeVolume += float64(barVol)
		if state.cumulativeVolume > 0 {
			bar.vwap = state.cumulativeDollarFlow / state.cumulativeVolume
		}
	}
	state.bars = append(state.bars, bar)
}

type scanMetrics struct {
	oneMinuteReturn       float64
	threeMinuteReturn     float64
	volumeRate            float64
	atr                   float64
	vwap                  float64
	breakoutPct           float64
	consolidationRangePct float64
	pullbackDepthPct      float64
	closeOffHighPct       float64
	setupHigh             float64
	setupLow              float64
	setupType             string
	rsi                   float64
	rsiMASlope            float64
	fiveMinRange          float64
	ema9                  float64
	emaFast               float64
	emaSlow               float64
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

	// Returns
	if n >= 2 {
		m.oneMinuteReturn = safePct(tick.Price-bars[n-2].close, bars[n-2].close) * 100
	}
	if n >= 4 {
		m.threeMinuteReturn = safePct(tick.Price-bars[n-4].close, bars[n-4].close) * 100
	}

	// Volume rate
	if n >= 2 {
		elapsed := tick.Timestamp.Sub(bars[0].timestamp).Minutes()
		if elapsed > 0 {
			m.volumeRate = float64(tick.Volume) / elapsed
		}
	}

	// ATR (14-period) — require minimum bars for reliable ATR
	m.atr = computeATR(bars, 14)
	if n < s.config.MinATRBars {
		m.atr = 0 // force percentage fallback in strategy
	}

	// VWAP
	if n > 0 {
		m.vwap = bars[n-1].vwap
	}

	// EMAs
	m.ema9 = computeEMA(bars, 9)
	m.emaFast = computeEMA(bars, s.config.MarketRegimeEMAFastPeriod)
	m.emaSlow = computeEMA(bars, s.config.MarketRegimeEMASlowPeriod)

	// MACD histogram (standard 12, 26, 9 on 5-minute candles)
	bars5 := aggregate5MinBars(bars)
	m.macdHistogram = computeMACDHistogram(bars5, s.config.MACDFastPeriod, s.config.MACDSlowPeriod, s.config.MACDSignalPeriod)

	// RSI and RSI MA Slope (14-period Wilder RSI)
	m.rsi = computeRSI(bars, 14)
	if n >= 15 {
		rsiValues := make([]float64, 0, 10)
		start := n - 10
		if start < 0 {
			start = 0
		}
		for i := start; i < n; i++ {
			subBars := bars[:i+1]
			rsiValues = append(rsiValues, computeRSI(subBars, 14))
		}
		if len(rsiValues) >= 2 {
			m.rsiMASlope = (rsiValues[len(rsiValues)-1] - rsiValues[0]) / float64(len(rsiValues))
		}
	}

	// ADX for mean-reversion detection
	m.adx = computeADX(bars, 14)

	// Bollinger Bands
	bbPeriod := s.config.BollingerPeriod
	if bbPeriod == 0 {
		bbPeriod = 20
	}
	bbK := s.config.BollingerK
	if bbK == 0 {
		bbK = 2.0
	}
	m.bbUpper, m.bbMiddle, m.bbLower = computeBollingerBands(bars, bbPeriod, bbK)

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
			// Derive per-bar volumes from cumulative session volumes
			barVols := make([]int64, n)
			barVols[0] = bars[0].volume
			for i := 1; i < n; i++ {
				barVols[i] = bars[i].volume - bars[i-1].volume
				if barVols[i] < 0 {
					barVols[i] = 0
				}
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
	case "hod_breakout", "breakout", "breakdown":
		return "breakout"
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

// aggregate5MinBars collapses 1-minute bars into 5-minute OHLCV candles.
func aggregate5MinBars(bars []symbolBar) []symbolBar {
	if len(bars) == 0 {
		return nil
	}
	out := make([]symbolBar, 0, len(bars)/5+1)
	for i := 0; i < len(bars); i += 5 {
		end := i + 5
		if end > len(bars) {
			end = len(bars)
		}
		chunk := bars[i:end]
		agg := symbolBar{
			timestamp: chunk[0].timestamp,
			open:      chunk[0].open,
			high:      chunk[0].high,
			low:       chunk[0].low,
			close:     chunk[len(chunk)-1].close,
			volume:    0,
		}
		for _, b := range chunk {
			if b.high > agg.high {
				agg.high = b.high
			}
			if b.low < agg.low || agg.low == 0 {
				agg.low = b.low
			}
			agg.volume += b.volume
		}
		out = append(out, agg)
	}
	return out
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

