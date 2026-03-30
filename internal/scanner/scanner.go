package scanner

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/signals"
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
	regularBars     []symbolBar
	vwapDollarFlow  float64
	vwapTotalVolume float64
	orbState        openingRangeState
}

type openingRangeState struct {
	window        int
	bufferPct     float64
	rangeHigh     float64
	rangeLow      float64
	avgRangeVol   float64
	breakoutLevel float64
	breakoutIdx   int
	processedBars int
	ready         bool
}

type structuredSetup struct {
	setupType             string
	setupHigh             float64
	setupLow              float64
	breakoutPct           float64
	consolidationRangePct float64
	pullbackDepthPct      float64
	closeOffHighPct       float64
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
	signalLookup   func(symbol string) []signals.Signal
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

// SetSignalLookup installs a function that returns the latest alpha signals for a symbol.
func (s *Scanner) SetSignalLookup(fn func(symbol string) []signals.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signalLookup = fn
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
	var prevTickMap sync.Map // symbol → time of last logged rejection
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tick := range work {
				candidate, shouldEmit, reason := s.evaluateDetailed(tick)
				if shouldEmit {
					select {
					case out <- candidate:
					case <-ctx.Done():
						return
					}
				}
				if !shouldEmit && reason != "" && reason != "market-closed" && reason != "system-paused" && tick.Symbol != "" {
					prev, _ := prevTickMap.LoadOrStore(tick.Symbol, time.Now())
					if time.Since(prev.(time.Time)) > 30*time.Second {
						s.runtime.RecordLog("debug", "scanner", fmt.Sprintf("candidate rejected: %s reason=%s", tick.Symbol, reason))
					}
					prevTickMap.Store(tick.Symbol, time.Now())
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
	candidate, ok, _ := s.evaluateDetailed(tick)
	return candidate, ok
}

// EvaluateTickDetailed tests a tick against scanner filters and returns
// the rejection reason when the tick is not a candidate.
func (s *Scanner) EvaluateTickDetailed(tick domain.Tick) (domain.Candidate, bool, string) {
	return s.evaluateDetailed(tick)
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
		hodMomoQualified = intradayPct >= cfg.HODMomoMinIntradayPct &&
			tick.RelativeVolume >= cfg.HODMomoMinRelativeVolume
		if hodMomoQualified && tick.HighOfDay > 0 {
			distFromHigh := (tick.HighOfDay - tick.Price) / tick.HighOfDay * 100
			pullbackMaxDist := dynamicHODPullbackMaxDist(cfg, intradayPct, tick.RelativeVolume)
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

func (s *Scanner) evaluateDetailed(tick domain.Tick) (domain.Candidate, bool, string) {
	if _, blocked := s.instrumentBlockReason(tick.Symbol); blocked {
		return domain.Candidate{}, false, "instrument-blocked"
	}
	if tick.Price <= 0 || tick.Volume <= 0 {
		return domain.Candidate{}, false, "no-data"
	}

	s.mu.Lock()
	cfg := s.config
	s.mu.Unlock()
	if tick.Price < cfg.MinPrice || tick.Price > cfg.MaxPrice {
		return domain.Candidate{}, false, "price-filter"
	}

	if cfg.MinFiveMinuteVolume > 0 && tick.FiveMinuteVolume < cfg.MinFiveMinuteVolume {
		return domain.Candidate{}, false, "five-minute-volume"
	}

	// Update bar state before qualification so structured reclaim setups can be
	// recognized as soon as the pattern forms, even if the broader HOD-momo
	// threshold has not quite been crossed yet.
	s.mu.Lock()
	state := s.getOrCreateState(tick)
	s.updateBars(state, tick)
	metrics := s.computeMetrics(state, tick)
	s.mu.Unlock()
	referenceHigh := effectiveReferenceHigh(state, tick)

	// Path 1: Traditional gap scanner (existing)
	gapQualified := tick.GapPercent >= cfg.MinGapPercent || tick.GapPercent <= -cfg.MinGapPercent

	// Path 2: HOD Momo scanner (new) — stock is making a big intraday move
	hodMomoQualified := false
	hodMomoPullback := false // true when qualified via pullback distance (beyond breakout range)
	intradayReturnPct := 0.0
	if cfg.HODMomoEnabled && tick.Open > 0 && tick.Price > 0 {
		intradayReturnPct = (tick.Price - tick.Open) / tick.Open * 100
		minIntradayPct := cfg.HODMomoMinIntradayPct
		if markethours.IsMarketOpen(tick.Timestamp) &&
			referenceHigh > 0 &&
			metrics.vwap > 0 &&
			tick.Price > metrics.vwap &&
			tick.RelativeVolume >= math.Max(cfg.HODMomoMinRelativeVolume*2, 8.0) &&
			metrics.oneMinuteReturn > 0 &&
			metrics.threeMinuteReturn > 0 &&
			metrics.fiveMinuteReturn >= math.Max(cfg.MinThreeMinuteReturnPct, 1.0) {
			distFromHigh := safePct(referenceHigh-tick.Price, referenceHigh) * 100
			pullbackMaxDist := dynamicHODPullbackMaxDist(cfg, intradayReturnPct, tick.RelativeVolume)
			if pullbackMaxDist > 0 && distFromHigh <= pullbackMaxDist {
				minIntradayPct = math.Max(cfg.HODMomoMinIntradayPct*0.65, 3.5)
			}
		}
		hodMomoQualified = intradayReturnPct >= minIntradayPct &&
			tick.RelativeVolume >= cfg.HODMomoMinRelativeVolume

		// Two-tier distance check: breakout (tight) and pullback (wider)
		if hodMomoQualified && referenceHigh > 0 {
			distFromHigh := safePct(referenceHigh-tick.Price, referenceHigh) * 100
			pullbackMaxDist := dynamicHODPullbackMaxDist(cfg, intradayReturnPct, tick.RelativeVolume)
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

	// Path 3: Mean Reversion — stock at Bollinger Band extreme with low trend strength
	meanRevQualified := false
	if cfg.MeanReversionEnabled && metrics.bbUpper > 0 && metrics.bbLower > 0 && metrics.adx > 0 {
		if metrics.adx <= cfg.MeanReversionMaxADX && tick.RelativeVolume >= cfg.MinRelativeVolume {
			if tick.Price <= metrics.bbLower || tick.Price >= metrics.bbUpper {
				meanRevQualified = true
			}
		}
	}

	// Path 4: Gap Fade — large gap (>threshold) losing momentum, price reverting toward VWAP
	gapFadeQualified := false
	if cfg.GapFadeEnabled && markethours.IsMarketOpen(tick.Timestamp) {
		minGap := cfg.GapFadeMinGapPct
		if minGap <= 0 {
			minGap = 4.0
		}
		maxRelVol := cfg.GapFadeMaxRelVol
		if maxRelVol <= 0 {
			maxRelVol = 6.0
		}
		absGap := tick.GapPercent
		if absGap < 0 {
			absGap = -absGap
		}
		if absGap >= minGap && tick.RelativeVolume >= cfg.MinRelativeVolume && tick.RelativeVolume <= maxRelVol {
			if metrics.vwap > 0 {
				// Gap up fading: price dropping back toward VWAP from above
				if tick.GapPercent >= minGap && tick.Price < tick.Open && tick.Price > metrics.vwap {
					gapFadeQualified = true
				}
				// Gap down fading: price recovering toward VWAP from below
				if tick.GapPercent <= -minGap && tick.Price > tick.Open && tick.Price < metrics.vwap {
					gapFadeQualified = true
				}
			}
		}
	}

	// Must qualify via at least one path
	if !gapQualified && !hodMomoQualified && !meanRevQualified && !gapFadeQualified {
		return domain.Candidate{}, false, classifyTickRejection(tick, cfg)
	}

	// Apply remaining filters based on qualification path
	if gapQualified {
		// Traditional filters apply as-is
		if tick.RelativeVolume < cfg.MinRelativeVolume {
			return domain.Candidate{}, false, "relative-volume"
		}
		if tick.PreMarketVolume < cfg.MinPremarketVolume {
			return domain.Candidate{}, false, "premarket-volume"
		}
	}
	// HOD momo path: relative volume already checked above, skip premarket volume

	// Float filters apply to both paths
	if cfg.MaxFloat > 0 && tick.Float > 0 && tick.Float > cfg.MaxFloat {
		return domain.Candidate{}, false, "float-too-high"
	}
	if cfg.MinFloat > 0 && tick.Float > 0 && tick.Float < cfg.MinFloat {
		return domain.Candidate{}, false, "float-too-low"
	}

	// Minimum daily volume filter — reject thinly traded stocks, but allow
	// exceptional same-day squeeze leaders to pass once the live tape has clearly
	// taken over from the stale prior-day volume baseline.
	if cfg.MinPrevDayVolume > 0 &&
		tick.PrevDayVolume > 0 &&
		tick.PrevDayVolume < cfg.MinPrevDayVolume &&
		!qualifiesExplosiveSqueezeVolumeException(tick, metrics, intradayReturnPct, hodMomoQualified, cfg) {
		return domain.Candidate{}, false, "daily-volume"
	}

	s.mu.Lock()
	s.trackDollarVolume(tick)
	leaderRank, leaderStrengthPct := s.volumeLeaderStats(tick.Symbol)
	s.mu.Unlock()

	// Determine direction
	direction := domain.DirectionLong
	if metrics.setupType == "parabolic-failed-reclaim-short" {
		direction = domain.DirectionShort
		if !cfg.EnableShorts {
			return domain.Candidate{}, false, "shorts-disabled"
		}
	}
	priceVsVWAPPct := safePct(tick.Price-metrics.vwap, metrics.vwap) * 100
	if s.qualifiesShortMomentumProfile(tick, priceVsVWAPPct, metrics) {
		direction = domain.DirectionShort
	}
	// HOD momo qualified stocks with positive intraday return are always long
	if hodMomoQualified && !gapQualified && intradayReturnPct > 0 {
		direction = domain.DirectionLong
	}

	// HOD breakout/pullback detection: price near session high with strong intraday move
	if cfg.HODMomoEnabled && tick.Open > 0 && referenceHigh > 0 {
		intradayPct := (tick.Price - tick.Open) / tick.Open * 100
		distFromHOD := safePct(referenceHigh-tick.Price, referenceHigh) * 100
		if intradayPct >= cfg.HODMomoMinIntradayPct {
			if distFromHOD < 1.0 {
				metrics.setupType = "hod_breakout"
			} else if hodMomoPullback {
				metrics.setupType = "hod_pullback"
			}
		}
	}

	if direction == domain.DirectionLong {
		if cfg.HODMomoEnabled {
			if structured, ok := s.classifyHODPullbackReclaim(state, tick, metrics, cfg); ok {
				metrics.setupType = structured.setupType
				metrics.setupHigh = structured.setupHigh
				metrics.setupLow = structured.setupLow
				metrics.breakoutPct = structured.breakoutPct
				metrics.consolidationRangePct = structured.consolidationRangePct
				metrics.pullbackDepthPct = structured.pullbackDepthPct
				metrics.closeOffHighPct = structured.closeOffHighPct
			}
		}
		if structured, ok := s.classifyStructuredLongSetup(state, tick, metrics, cfg); ok {
			metrics.setupType = structured.setupType
			metrics.setupHigh = structured.setupHigh
			metrics.setupLow = structured.setupLow
			metrics.breakoutPct = structured.breakoutPct
			if structured.consolidationRangePct > 0 {
				metrics.consolidationRangePct = structured.consolidationRangePct
			}
			if structured.pullbackDepthPct > 0 {
				metrics.pullbackDepthPct = structured.pullbackDepthPct
			}
			if structured.closeOffHighPct > 0 {
				metrics.closeOffHighPct = structured.closeOffHighPct
			}
		}
	}

	// New playbook setup type overrides — only apply when qualified via their
	// specific path and existing classifiers haven't already assigned a structured type.
	if meanRevQualified && (metrics.setupType == "early" || metrics.setupType == "pullback" || metrics.setupType == "breakdown") {
		if tick.Price <= metrics.bbLower {
			metrics.setupType = "mean_reversion_long"
			direction = domain.DirectionLong
		} else if tick.Price >= metrics.bbUpper {
			if cfg.EnableShorts {
				metrics.setupType = "mean_reversion_short"
				direction = domain.DirectionShort
			}
		}
	}

	if gapFadeQualified && (metrics.setupType == "early" || metrics.setupType == "breakout" || metrics.setupType == "breakdown") {
		if tick.GapPercent > 0 && tick.Price < tick.Open {
			if cfg.EnableShorts {
				metrics.setupType = "gap_fade_short"
				direction = domain.DirectionShort
			}
		} else if tick.GapPercent < 0 && tick.Price > tick.Open {
			metrics.setupType = "gap_fade_long"
			direction = domain.DirectionLong
		}
	}

	if metrics.setupType == "early" {
		return domain.Candidate{}, false, "setup-early"
	}

	distFromHighPct := safePct(referenceHigh-tick.Price, referenceHigh) * 100
	breakoutBypass := direction == domain.DirectionLong && shouldBypassLongBreakoutFilters(cfg, metrics, distFromHighPct, referenceHigh)

	// HOD proximity filter: longs must be within MaxDistanceFromHighPct of high of day.
	// True breakout setups are allowed to print at the exact high instead of being rejected.
	if cfg.MaxDistanceFromHighPct > 0 && direction == domain.DirectionLong && !hodMomoQualified && !breakoutBypass {
		if distFromHighPct > cfg.MaxDistanceFromHighPct {
			return domain.Candidate{}, false, "distance-from-high"
		}
	}

	// RSI momentum slope: longs require positive RSI SMA slope, shorts require negative.
	// When insufficient bars exist to compute the slope it defaults to 0 and is rejected.
	if direction == domain.DirectionLong && metrics.rsiMASlope < 0 {
		return domain.Candidate{}, false, "rsi-slope"
	}
	if direction == domain.DirectionShort && metrics.rsiMASlope > 0 {
		return domain.Candidate{}, false, "rsi-slope"
	}

	// Compute intraday return for candidate
	if tick.Open > 0 && tick.Price > 0 {
		intradayReturnPct = (tick.Price - tick.Open) / tick.Open * 100
	}

	// Get market regime
	regime := s.runtime.MarketRegime()

	setupType := metrics.setupType
	stockSelectionScore := 0.0
	if domain.IsLong(direction) {
		stockSelectionScore = computeRossStockSelectionScore(tick, metrics, intradayReturnPct, leaderRank, leaderStrengthPct, distFromHighPct, referenceHigh)
	} else {
		stockSelectionScore = computeShortSelectionScore(tick, metrics, intradayReturnPct, leaderRank, leaderStrengthPct, distFromHighPct, s.config.ShortVWAPBreakMinPct)
	}
	score := computeCandidateScore(cfg, tick, metrics, direction, setupType, intradayReturnPct, leaderRank, leaderStrengthPct, distFromHighPct, referenceHigh, stockSelectionScore)

	// Look up alpha signals (OFI, VPIN) and boost score when signals confirm direction.
	var ofiDir int
	var ofiStrength float64
	var vpinDir int
	var vpinStrength float64
	if s.signalLookup != nil {
		for _, sig := range s.signalLookup(tick.Symbol) {
			switch sig.Type {
			case signals.SignalTypeOFI:
				ofiDir = int(sig.Direction)
				ofiStrength = sig.Strength
				if (domain.IsLong(direction) && sig.Direction == signals.DirectionLong) ||
					(domain.IsShort(direction) && sig.Direction == signals.DirectionShort) {
					score += 0.5 * sig.Strength // confirming OFI boosts score
				}
			case signals.SignalTypeVPIN:
				vpinDir = int(sig.Direction)
				vpinStrength = sig.Strength
				if (domain.IsLong(direction) && sig.Direction == signals.DirectionLong) ||
					(domain.IsShort(direction) && sig.Direction == signals.DirectionShort) {
					score += 0.35 * sig.Strength // confirming VPIN boosts score
				}
			}
		}
	}

	candidate := domain.Candidate{
		Symbol:                tick.Symbol,
		Direction:             direction,
		Price:                 tick.Price,
		Open:                  tick.Open,
		GapPercent:            tick.GapPercent,
		RelativeVolume:        tick.RelativeVolume,
		PreMarketVolume:       tick.PreMarketVolume,
		Volume:                tick.Volume,
		HighOfDay:             referenceHigh,
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
		StockSelectionScore:   stockSelectionScore,
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
		OFIDirection:          ofiDir,
		OFIStrength:           ofiStrength,
		VPINDirection:         vpinDir,
		VPINStrength:          vpinStrength,
		Timestamp:             tick.Timestamp,
	}

	return candidate, true, ""
}

func dynamicHODPullbackMaxDist(cfg config.TradingConfig, intradayPct, relativeVolume float64) float64 {
	pullbackMaxDist := cfg.HODMomoPullbackMaxDist
	if pullbackMaxDist <= 0 {
		pullbackMaxDist = cfg.HODMomoMaxDistFromHigh
	}
	if intradayPct >= 15 && relativeVolume >= 10 {
		dynamicCap := intradayPct * 0.45
		if dynamicCap > pullbackMaxDist && dynamicCap < 8.0 {
			pullbackMaxDist = dynamicCap
		}
	}
	if intradayPct >= 20 && relativeVolume >= 20 && pullbackMaxDist < 9.0 {
		pullbackMaxDist = 9.0
	}
	return pullbackMaxDist
}

func effectiveReferenceHigh(state *symbolState, tick domain.Tick) float64 {
	referenceHigh := tick.HighOfDay
	if !markethours.IsMarketOpen(tick.Timestamp) || state == nil || len(state.regularBars) == 0 {
		return referenceHigh
	}
	regularHigh := maxBarHigh(state.regularBars)
	if regularHigh > 0 {
		referenceHigh = regularHigh
	}
	return referenceHigh
}

func shouldBypassLongBreakoutFilters(cfg config.TradingConfig, metrics scanMetrics, distFromHighPct, referenceHigh float64) bool {
	if referenceHigh <= 0 {
		return false
	}
	switch metrics.setupType {
	case "breakout", "hod_breakout", "orb_breakout", "orb_reclaim":
	default:
		return false
	}
	if metrics.oneMinuteReturn <= 0 {
		return false
	}
	if metrics.fiveMinuteReturn < math.Max(cfg.MinThreeMinuteReturnPct, 0.5) {
		return false
	}
	maxDist := math.Max(cfg.MaxDistanceFromHighPct, 1.0)
	return distFromHighPct <= maxDist
}

func shouldBypassLeaderPullbackFilters(tick domain.Tick, metrics scanMetrics, intradayReturnPct float64) bool {
	if metrics.setupType != "hod_pullback" {
		return false
	}
	if intradayReturnPct < 15 {
		return false
	}
	if tick.RelativeVolume < 10 {
		return false
	}
	if metrics.vwap <= 0 || tick.Price <= metrics.vwap {
		return false
	}
	if metrics.oneMinuteReturn < 0 {
		return false
	}
	if metrics.threeMinuteReturn < 0 {
		return false
	}
	return true
}

func computeRossStockSelectionScore(tick domain.Tick, metrics scanMetrics, intradayReturnPct float64, leaderRank int, leaderStrengthPct float64, distFromHighPct float64, referenceHigh float64) float64 {
	gapPillar := 0.0
	expansionPct := math.Max(math.Abs(tick.GapPercent), intradayReturnPct)
	switch {
	case expansionPct >= 20:
		gapPillar = 1.0
	case expansionPct >= 10:
		gapPillar = 0.85
	case expansionPct >= 6:
		gapPillar = 0.65
	case expansionPct >= 4:
		gapPillar = 0.45
	case expansionPct >= 2.5:
		gapPillar = 0.25
	}

	volumePillar := 0.0
	switch {
	case tick.RelativeVolume >= 20:
		volumePillar += 0.55
	case tick.RelativeVolume >= 10:
		volumePillar += 0.45
	case tick.RelativeVolume >= 5:
		volumePillar += 0.35
	case tick.RelativeVolume >= 2.5:
		volumePillar += 0.20
	}
	switch {
	case tick.PreMarketVolume >= 1_000_000:
		volumePillar += 0.45
	case tick.PreMarketVolume >= 500_000:
		volumePillar += 0.35
	case tick.PreMarketVolume >= 200_000:
		volumePillar += 0.20
	case tick.PreMarketVolume >= 50_000:
		volumePillar += 0.10
	}
	if metrics.volumeRate >= 20_000 {
		volumePillar += 0.10
	} else if metrics.volumeRate >= 10_000 {
		volumePillar += 0.05
	}
	if volumePillar > 1.0 {
		volumePillar = 1.0
	}

	floatPillar := 0.0
	switch {
	case tick.Float > 0 && tick.Float <= 10_000_000:
		floatPillar = 1.0
	case tick.Float > 0 && tick.Float <= 20_000_000:
		floatPillar = 0.9
	case tick.Float > 0 && tick.Float <= 50_000_000:
		floatPillar = 0.75
	case tick.Float > 0 && tick.Float <= 100_000_000:
		floatPillar = 0.55
	case tick.Float > 0 && tick.Float <= 200_000_000:
		floatPillar = 0.25
	case tick.Float > 0:
		floatPillar = 0.05
	default:
		floatPillar = 0.20
	}

	technicalPillar := 0.0
	switch {
	case referenceHigh > 0 && distFromHighPct <= 0.5:
		technicalPillar += 0.40
	case referenceHigh > 0 && distFromHighPct <= 1.25:
		technicalPillar += 0.30
	case referenceHigh > 0 && distFromHighPct <= 2.5:
		technicalPillar += 0.15
	}
	if metrics.vwap > 0 && tick.Price > metrics.vwap {
		technicalPillar += 0.20
		if safePct(tick.Price-metrics.vwap, metrics.vwap)*100 >= 1.0 {
			technicalPillar += 0.05
		}
	}
	switch {
	case metrics.fiveMinuteReturn >= 2.5:
		technicalPillar += 0.25
	case metrics.fiveMinuteReturn >= 1.0:
		technicalPillar += 0.18
	case metrics.fiveMinuteReturn >= 0.5:
		technicalPillar += 0.10
	}
	if metrics.oneMinuteReturn > 0 && metrics.threeMinuteReturn > 0 {
		technicalPillar += 0.10
	}
	if technicalPillar > 1.0 {
		technicalPillar = 1.0
	}

	leadershipPillar := 0.0
	switch {
	case leaderRank == 1:
		leadershipPillar += 0.55
	case leaderRank > 0 && leaderRank <= 3:
		leadershipPillar += 0.45
	case leaderRank > 0 && leaderRank <= 5:
		leadershipPillar += 0.30
	}
	switch {
	case leaderStrengthPct >= 75:
		leadershipPillar += 0.30
	case leaderStrengthPct >= 50:
		leadershipPillar += 0.20
	case leaderStrengthPct >= 25:
		leadershipPillar += 0.10
	}
	if intradayReturnPct >= 10 {
		leadershipPillar += 0.15
	} else if intradayReturnPct >= 5 {
		leadershipPillar += 0.05
	}
	if leadershipPillar > 1.0 {
		leadershipPillar = 1.0
	}

	score := gapPillar + volumePillar + floatPillar + technicalPillar + leadershipPillar
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	return score
}

func computeShortSelectionScore(tick domain.Tick, metrics scanMetrics, intradayReturnPct float64, leaderRank int, leaderStrengthPct float64, distFromHighPct float64, shortVWAPBreakMinPct float64) float64 {
	peakExtensionPct := 0.0
	if tick.Open > 0 && tick.HighOfDay > 0 {
		peakExtensionPct = safePct(tick.HighOfDay-tick.Open, tick.Open) * 100
	}
	vwapBreakPct := 0.0
	if metrics.vwap > 0 && tick.Price < metrics.vwap {
		vwapBreakPct = safePct(metrics.vwap-tick.Price, metrics.vwap) * 100
	}
	minVWAPBreak := math.Max(shortVWAPBreakMinPct, 1.0)

	extensionPillar := 0.0
	switch {
	case peakExtensionPct >= 40:
		extensionPillar = 1.0
	case peakExtensionPct >= 25:
		extensionPillar = 0.85
	case peakExtensionPct >= 15:
		extensionPillar = 0.65
	case peakExtensionPct >= 10:
		extensionPillar = 0.45
	case peakExtensionPct >= 6:
		extensionPillar = 0.25
	}

	volumePillar := 0.0
	switch {
	case tick.RelativeVolume >= 20:
		volumePillar += 0.50
	case tick.RelativeVolume >= 10:
		volumePillar += 0.40
	case tick.RelativeVolume >= 5:
		volumePillar += 0.28
	case tick.RelativeVolume >= 2.5:
		volumePillar += 0.15
	}
	switch {
	case tick.PreMarketVolume >= 1_000_000:
		volumePillar += 0.30
	case tick.PreMarketVolume >= 300_000:
		volumePillar += 0.20
	case tick.PreMarketVolume >= 100_000:
		volumePillar += 0.10
	}
	switch {
	case metrics.volumeRate >= 20_000:
		volumePillar += 0.20
	case metrics.volumeRate >= 10_000:
		volumePillar += 0.10
	}
	if volumePillar > 1.0 {
		volumePillar = 1.0
	}

	failurePillar := 0.0
	switch {
	case distFromHighPct >= 35:
		failurePillar += 0.40
	case distFromHighPct >= 20:
		failurePillar += 0.28
	case distFromHighPct >= 10:
		failurePillar += 0.16
	case distFromHighPct >= 6:
		failurePillar += 0.08
	}
	switch {
	case vwapBreakPct >= minVWAPBreak+4.0:
		failurePillar += 0.28
	case vwapBreakPct >= minVWAPBreak+1.5:
		failurePillar += 0.20
	case vwapBreakPct >= minVWAPBreak:
		failurePillar += 0.12
	}
	switch {
	case metrics.breakoutPct <= -2.0:
		failurePillar += 0.20
	case metrics.breakoutPct <= -0.75:
		failurePillar += 0.12
	case metrics.breakoutPct <= -0.25:
		failurePillar += 0.06
	}
	if failurePillar > 1.0 {
		failurePillar = 1.0
	}

	technicalPillar := 0.0
	switch {
	case metrics.oneMinuteReturn <= -4.0:
		technicalPillar += 0.25
	case metrics.oneMinuteReturn <= -1.5:
		technicalPillar += 0.18
	case metrics.oneMinuteReturn <= -0.75:
		technicalPillar += 0.10
	}
	switch {
	case metrics.threeMinuteReturn <= -8.0:
		technicalPillar += 0.25
	case metrics.threeMinuteReturn <= -3.0:
		technicalPillar += 0.18
	case metrics.threeMinuteReturn <= -1.5:
		technicalPillar += 0.10
	}
	switch {
	case metrics.fiveMinuteReturn <= -4.0:
		technicalPillar += 0.20
	case metrics.fiveMinuteReturn <= -2.0:
		technicalPillar += 0.12
	case metrics.fiveMinuteReturn <= -1.0:
		technicalPillar += 0.06
	}
	if metrics.macdHistogram < 0 {
		technicalPillar += 0.15
	}
	if metrics.ema9 > 0 && tick.Price < metrics.ema9 {
		technicalPillar += 0.10
	}
	if metrics.vwap > 0 && tick.Price < metrics.vwap {
		technicalPillar += 0.05
	}
	if technicalPillar > 1.0 {
		technicalPillar = 1.0
	}

	leadershipPillar := 0.0
	if leaderRank > 0 && leaderRank <= 3 {
		if distFromHighPct >= 15 {
			leadershipPillar += 0.20
		} else if distFromHighPct < 8 {
			leadershipPillar -= 0.10
		}
	}
	if leaderStrengthPct >= 50 && distFromHighPct >= 15 {
		leadershipPillar += 0.10
	}
	if intradayReturnPct >= 2 {
		leadershipPillar += 0.10
	}

	score := extensionPillar + volumePillar + failurePillar + technicalPillar + leadershipPillar
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	if score < 0 {
		return 0
	}
	return score
}

func computeCandidateScore(cfg config.TradingConfig, tick domain.Tick, metrics scanMetrics, direction string, setupType string, intradayReturnPct float64, leaderRank int, leaderStrengthPct float64, distFromHighPct float64, referenceHigh float64, stockSelectionScore float64) float64 {
	score := 0.0

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
		case referenceHigh > 0 && distFromHighPct <= 0.5:
			score += 1.25
		case referenceHigh > 0 && distFromHighPct <= 1.5:
			score += 0.95
		case referenceHigh > 0 && distFromHighPct <= 3.0:
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

		score += stockSelectionScore * 0.45

		if metrics.macdHistogram > 0 {
			score += 0.25
		}
		if metrics.ema9 > 0 && tick.Price > metrics.ema9 {
			score += 0.2
		}
		if metrics.vwap > 0 && tick.Price > metrics.vwap {
			score += 0.15
		}
	} else {
		peakExtensionPct := 0.0
		if tick.Open > 0 && tick.HighOfDay > 0 {
			peakExtensionPct = safePct(tick.HighOfDay-tick.Open, tick.Open) * 100
		}
		vwapBreakPct := 0.0
		if metrics.vwap > 0 && tick.Price < metrics.vwap {
			vwapBreakPct = safePct(metrics.vwap-tick.Price, metrics.vwap) * 100
		}
		switch setupType {
		case "parabolic-failed-reclaim-short":
			score += 1.2
		case "breakdown":
			score += 0.8
		}
		switch {
		case metrics.oneMinuteReturn <= -2.0:
			score += 0.6
		case metrics.oneMinuteReturn <= -0.75:
			score += 0.35
		}
		switch {
		case metrics.threeMinuteReturn <= -3.0:
			score += 0.6
		case metrics.threeMinuteReturn <= -1.5:
			score += 0.35
		}
		switch {
		case metrics.fiveMinuteReturn <= -2.0:
			score += 0.45
		case metrics.fiveMinuteReturn <= -1.0:
			score += 0.2
		}
		if metrics.macdHistogram < 0 {
			score += 0.3
		}
		switch {
		case vwapBreakPct >= math.Max(cfg.ShortVWAPBreakMinPct, 1.0)+2.0:
			score += 0.45
		case vwapBreakPct >= math.Max(cfg.ShortVWAPBreakMinPct, 1.0):
			score += 0.25
		case metrics.vwap > 0 && tick.Price < metrics.vwap:
			score += 0.1
		}
		switch {
		case distFromHighPct >= 20:
			score += 0.35
		case distFromHighPct >= 10:
			score += 0.2
		}
		switch {
		case peakExtensionPct >= 20:
			score += 0.35
		case peakExtensionPct >= cfg.ShortPeakExtensionMinPct:
			score += 0.2
		}
		score += stockSelectionScore * 0.55
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
	if metrics.vwap <= 0 {
		return false
	}
	vwapBreakMinPct := math.Max(s.config.ShortVWAPBreakMinPct, 1.0)
	if priceVsVWAPPCT > -vwapBreakMinPct {
		return false
	}
	if metrics.oneMinuteReturn >= -max(0.50, s.config.MinOneMinuteReturnPct*0.75) {
		return false
	}
	if metrics.threeMinuteReturn >= -max(1.00, s.config.MinThreeMinuteReturnPct) {
		return false
	}
	if metrics.fiveMinuteReturn >= -max(0.75, s.config.MinThreeMinuteReturnPct*0.75) {
		return false
	}
	if metrics.breakoutPct > -0.25 {
		return false
	}
	if tick.Open <= 0 || tick.HighOfDay <= 0 {
		return false
	}
	peakExtensionPct := ((tick.HighOfDay - tick.Open) / tick.Open) * 100
	if peakExtensionPct < s.config.ShortPeakExtensionMinPct {
		return false
	}
	distFromHighPct := safePct(tick.HighOfDay-tick.Price, tick.HighOfDay) * 100
	minFailureDistPct := math.Max(6.0, s.config.ShortPeakExtensionMinPct*0.60)
	if distFromHighPct < minFailureDistPct &&
		metrics.oneMinuteReturn > -1.0 &&
		metrics.breakoutPct > -4.0 {
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
			day:         day,
			bars:        make([]symbolBar, 0, 390),
			bars5:       make([]symbolBar, 0, 80),
			regularBars: make([]symbolBar, 0, 390),
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
		s.updateRegularSessionBars(state)
	} else {
		state.bars = append(state.bars, bar)
		s.updateVWAP(state, nil)
		s.updateFiveMinuteBars(state)
		s.updateRegularSessionBars(state)
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

func (s *Scanner) updateRegularSessionBars(state *symbolState) {
	last := state.bars[len(state.bars)-1]
	if !isRegularSessionBar(last.timestamp) {
		return
	}
	n := len(state.regularBars)
	if n > 0 && state.regularBars[n-1].timestamp.Equal(last.timestamp) {
		state.regularBars[n-1] = last
		return
	}
	state.regularBars = append(state.regularBars, last)
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

func qualifiesExplosiveSqueezeVolumeException(tick domain.Tick, metrics scanMetrics, intradayReturnPct float64, hodMomoQualified bool, cfg config.TradingConfig) bool {
	if !hodMomoQualified {
		return false
	}
	if !markethours.IsMarketOpen(tick.Timestamp) {
		return false
	}
	if intradayReturnPct < math.Max(cfg.HODMomoMinIntradayPct*3, 12.0) {
		return false
	}
	if tick.RelativeVolume < math.Max(cfg.HODMomoMinRelativeVolume*2, 10.0) {
		return false
	}
	if cfg.MinFiveMinuteVolume > 0 && tick.FiveMinuteVolume < max(cfg.MinFiveMinuteVolume*2, int64(20000)) {
		return false
	}
	if metrics.vwap <= 0 || tick.Price <= metrics.vwap || tick.Price <= tick.Open {
		return false
	}
	if metrics.oneMinuteReturn <= 0 || metrics.threeMinuteReturn <= 0 {
		return false
	}
	return metrics.fiveMinuteReturn >= math.Max(cfg.MinThreeMinuteReturnPct, 1.0)
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
	// RSI MA Slope: slope of SMA(14) of RSI computed from completed 5-minute bars only.
	// The current in-progress bar is excluded to avoid transient spikes within a candle.
	// Need 15 consecutive RSI values to form two overlapping SMA(14) windows.
	completedBars5 := bars5[:max(n5-1, 0)] // exclude in-progress bar
	nc := len(completedBars5)
	if nc >= 29 { // 14 bars for RSI warm-up + 15 RSI data points
		rsiValues := make([]float64, 15)
		for i := 0; i < 15; i++ {
			rsiValues[i] = computeRSI(completedBars5[:nc-14+i], 14)
		}
		var smaPrev, smaCurr float64
		for i := 0; i < 14; i++ {
			smaPrev += rsiValues[i]
			smaCurr += rsiValues[i+1]
		}
		m.rsiMASlope = (smaCurr - smaPrev) / 14
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
				s.config.EnableShorts &&
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
	case "mean_reversion_long", "mean_reversion_short":
		return "mean_reversion"
	case "gap_fade_long", "gap_fade_short":
		return "gap_fade"
	case "power_hour_long", "power_hour_short":
		return "power_hour"
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

	bars := state.regularBars
	if len(bars) <= window {
		return structuredSetup{}, false
	}

	orbState, ready := s.refreshOpeningRangeState(state, bars, cfg, window, bufferPct)
	if !ready {
		return structuredSetup{}, false
	}

	rangeHigh := orbState.rangeHigh
	rangeLow := orbState.rangeLow
	rangeWidth := rangeHigh - rangeLow
	if rangeHigh <= 0 || rangeWidth <= 0 {
		return structuredSetup{}, false
	}
	volumeMultiplier := cfg.ORBVolumeMultiplier
	if volumeMultiplier <= 0 {
		volumeMultiplier = 1.2
	}
	breakoutLevel := orbState.breakoutLevel
	currentIdx := len(bars) - 1
	currentBar := bars[currentIdx]

	breakoutIdx := orbState.breakoutIdx
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
	retracePct := (peakHigh - pullbackLow) / rangeWidth * 100
	if currentBar.close > reclaimHigh &&
		currentBar.close > breakoutLevel &&
		metrics.oneMinuteReturn > 0 &&
		metrics.fiveMinuteReturn >= math.Max(cfg.MinThreeMinuteReturnPct*0.75, 0.5) &&
		tick.Price > metrics.vwap &&
		reclaimPct <= 2.5 {
		return structuredSetup{
			setupType:             "orb_reclaim",
			setupHigh:             reclaimHigh,
			setupLow:              pullbackLow,
			breakoutPct:           reclaimPct,
			consolidationRangePct: safePct(reclaimHigh-pullbackLow, reclaimHigh) * 100,
			pullbackDepthPct:      retracePct,
			closeOffHighPct:       safePct(peakHigh-currentBar.close, peakHigh) * 100,
		}, true
	}

	return structuredSetup{}, false
}

func (s *Scanner) classifyHODPullbackReclaim(state *symbolState, tick domain.Tick, metrics scanMetrics, cfg config.TradingConfig) (structuredSetup, bool) {
	if !markethours.IsMarketOpen(tick.Timestamp) {
		return structuredSetup{}, false
	}
	referenceHigh := effectiveReferenceHigh(state, tick)
	if tick.Open <= 0 || tick.Price <= 0 || referenceHigh <= 0 {
		return structuredSetup{}, false
	}
	if metrics.vwap > 0 && tick.Price <= metrics.vwap {
		return structuredSetup{}, false
	}

	bars := state.regularBars
	n := len(bars)
	if n < 6 {
		return structuredSetup{}, false
	}

	currentIdx := n - 1
	currentBar := bars[currentIdx]
	start := max(0, n-12)
	peakIdx := -1
	peakHigh := 0.0
	for i := start; i < currentIdx; i++ {
		if bars[i].high >= peakHigh {
			peakHigh = bars[i].high
			peakIdx = i
		}
	}
	if peakIdx < start || currentIdx-peakIdx < 2 || peakHigh <= 0 {
		return structuredSetup{}, false
	}

	impulseBase := minBarLow(bars[start : peakIdx+1])
	if impulseBase <= 0 || impulseBase >= peakHigh {
		return structuredSetup{}, false
	}
	impulsePct := safePct(peakHigh-impulseBase, impulseBase) * 100
	minImpulsePct := max(cfg.HODMomoMinIntradayPct*0.5, 2.5)
	if impulsePct < minImpulsePct {
		return structuredSetup{}, false
	}

	pullbackBars := bars[peakIdx+1 : currentIdx]
	pullbackLow := minBarLow(pullbackBars)
	if pullbackLow <= 0 || pullbackLow >= peakHigh {
		return structuredSetup{}, false
	}

	pullbackDepthPct := safePct(peakHigh-pullbackLow, peakHigh) * 100
	if pullbackDepthPct < 0.6 || pullbackDepthPct > 5.5 {
		return structuredSetup{}, false
	}

	impulseRange := peakHigh - impulseBase
	if impulseRange <= 0 {
		return structuredSetup{}, false
	}
	retracePct := (peakHigh - pullbackLow) / impulseRange * 100
	if retracePct < 15 || retracePct > 80 {
		return structuredSetup{}, false
	}

	reclaimLookback := min(3, currentIdx-peakIdx)
	if reclaimLookback < 2 {
		return structuredSetup{}, false
	}
	reclaimHigh := maxBarHigh(bars[currentIdx-reclaimLookback : currentIdx])
	if reclaimHigh <= 0 || currentBar.close <= reclaimHigh {
		return structuredSetup{}, false
	}

	reclaimPct := safePct(currentBar.close-reclaimHigh, reclaimHigh) * 100
	if reclaimPct > 4.0 {
		return structuredSetup{}, false
	}

	barCloseOffHighPct := 0.0
	if currentBar.high > currentBar.low {
		barCloseOffHighPct = (currentBar.high - currentBar.close) / (currentBar.high - currentBar.low) * 100
	}
	if barCloseOffHighPct > 35 {
		return structuredSetup{}, false
	}

	distFromHOD := safePct(referenceHigh-tick.Price, referenceHigh) * 100
	pullbackMaxDist := cfg.HODMomoPullbackMaxDist
	if pullbackMaxDist <= 0 {
		pullbackMaxDist = max(cfg.HODMomoMaxDistFromHigh, 3.0)
	}
	if distFromHOD > pullbackMaxDist {
		return structuredSetup{}, false
	}

	if metrics.oneMinuteReturn <= 0 {
		return structuredSetup{}, false
	}
	if metrics.threeMinuteReturn < 0.25 {
		return structuredSetup{}, false
	}
	if metrics.fiveMinuteReturn < math.Max(cfg.MinThreeMinuteReturnPct*0.6, 0.35) {
		return structuredSetup{}, false
	}

	return structuredSetup{
		setupType:             "hod_pullback",
		setupHigh:             peakHigh,
		setupLow:              pullbackLow,
		breakoutPct:           reclaimPct,
		consolidationRangePct: pullbackDepthPct,
		pullbackDepthPct:      retracePct,
		closeOffHighPct:       safePct(peakHigh-currentBar.close, peakHigh) * 100,
	}, true
}

func (s *Scanner) refreshOpeningRangeState(
	state *symbolState,
	bars []symbolBar,
	cfg config.TradingConfig,
	window int,
	bufferPct float64,
) (openingRangeState, bool) {
	if len(bars) <= window {
		return openingRangeState{}, false
	}

	cache := &state.orbState
	reset := !cache.ready || cache.window != window || cache.bufferPct != bufferPct || cache.processedBars > len(bars)
	if reset {
		rangeBars := bars[:window]
		rangeHigh := maxBarHigh(rangeBars)
		rangeLow := minBarLow(rangeBars)
		rangeWidth := rangeHigh - rangeLow
		if rangeHigh <= 0 || rangeWidth <= 0 {
			cache.ready = false
			return openingRangeState{}, false
		}
		cache.window = window
		cache.bufferPct = bufferPct
		cache.rangeHigh = rangeHigh
		cache.rangeLow = rangeLow
		cache.avgRangeVol = averageBarVolume(rangeBars)
		cache.breakoutLevel = rangeHigh * (1 + bufferPct)
		cache.breakoutIdx = -1
		cache.processedBars = window
		cache.ready = true
	}

	volumeMultiplier := cfg.ORBVolumeMultiplier
	if volumeMultiplier <= 0 {
		volumeMultiplier = 1.2
	}
	volumeThreshold := cache.avgRangeVol * max(volumeMultiplier*0.75, 1.0)
	start := max(window, cache.processedBars-1)
	if cache.breakoutIdx == len(bars)-1 {
		cache.breakoutIdx = -1
		start = max(window, len(bars)-1)
	}
	if cache.breakoutIdx == -1 {
		for i := start; i < len(bars); i++ {
			if bars[i].close > cache.breakoutLevel && float64(max(bars[i].volume, 0)) >= volumeThreshold {
				cache.breakoutIdx = i
				break
			}
		}
	}
	cache.processedBars = len(bars)
	return *cache, cache.ready
}

func isRegularSessionBar(ts time.Time) bool {
	et := ts.In(markethours.Location())
	minutes := et.Hour()*60 + et.Minute()
	return minutes >= 570 && minutes < 960
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
