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
	leaderMetrics map[string]float64
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
	reason := classifyTickRejection(tick, s.config)
	return candidate, false, reason
}

func classifyTickRejection(tick domain.Tick, cfg config.TradingConfig) string {
	if tick.Price <= 0 || tick.Volume <= 0 {
		return "no-data"
	}
	if tick.Price < cfg.MinPrice || tick.Price > cfg.MaxPrice {
		return "price-filter"
	}
	if tick.GapPercent < cfg.MinGapPercent && tick.GapPercent > -cfg.MinGapPercent {
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
	return "other-filter"
}

func (s *Scanner) evaluate(tick domain.Tick) (domain.Candidate, bool) {
	if tick.Price <= 0 || tick.Volume <= 0 {
		return domain.Candidate{}, false
	}

	cfg := s.config
	if tick.Price < cfg.MinPrice || tick.Price > cfg.MaxPrice {
		return domain.Candidate{}, false
	}
	if tick.GapPercent < cfg.MinGapPercent && tick.GapPercent > -cfg.MinGapPercent {
		return domain.Candidate{}, false
	}
	if tick.RelativeVolume < cfg.MinRelativeVolume {
		return domain.Candidate{}, false
	}
	if tick.PreMarketVolume < cfg.MinPremarketVolume {
		return domain.Candidate{}, false
	}
	if cfg.MaxFloat > 0 && tick.Float > 0 && tick.Float > cfg.MaxFloat {
		return domain.Candidate{}, false
	}
	if cfg.MinFloat > 0 && tick.Float > 0 && tick.Float < cfg.MinFloat {
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
	if tick.GapPercent < 0 && cfg.EnableShorts {
		direction = domain.DirectionShort
	}

	// Phase 3 Change 1: RSI overbought/oversold filter
	if cfg.RSIFilterEnabled {
		if direction == domain.DirectionLong && metrics.rsi > cfg.RSIOverboughtThreshold {
			return domain.Candidate{}, false
		}
		if direction == domain.DirectionShort && metrics.rsi < cfg.RSIOversoldThreshold {
			return domain.Candidate{}, false
		}
	}

	// Score the candidate
	score := s.scoreCandidate(tick, metrics, direction)
	minScore := cfg.MinEntryScore
	if direction == domain.DirectionShort {
		minScore = cfg.ShortMinEntryScore
	}

	// Get market regime
	regime := s.runtime.MarketRegime()

	setupType := metrics.setupType

	// Phase 3 Change 6: Mean-reversion overlay
	if cfg.MeanReversionEnabled {
		if regime.Regime == domain.RegimeMixed || regime.Regime == domain.RegimeNeutral {
			if metrics.adx < cfg.MeanReversionMaxADX && metrics.bbMiddle > 0 {
				if tick.Price <= metrics.bbLower {
					setupType = "mean_reversion_long"
					direction = domain.DirectionLong
					score += 2.0
				} else if tick.Price >= metrics.bbUpper {
					setupType = "mean_reversion_short"
					direction = domain.DirectionShort
					score += 2.0
				}
			}
		}
	}

	if score < minScore {
		return domain.Candidate{}, false
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
		HighOfDay:             tick.HighOfDay,
		PriceVsOpenPct:        safePct(tick.Price, tick.Open),
		DistanceFromHighPct:   safePct(tick.HighOfDay-tick.Price, tick.HighOfDay) * 100,
		OneMinuteReturnPct:    metrics.oneMinuteReturn,
		ThreeMinuteReturnPct:  metrics.threeMinuteReturn,
		VolumeRate:            metrics.volumeRate,
		MinutesSinceOpen:      markethours.MinutesSinceOpen(tick.Timestamp),
		ATR:                   metrics.atr,
		ATRPct:                safePct(metrics.atr, tick.Price) * 100,
		VWAP:                  metrics.vwap,
		PriceVsVWAPPct:        safePct(tick.Price-metrics.vwap, metrics.vwap) * 100,
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
		SetupType:             setupType,
		Score:                 score,
		MarketRegime:          regime.Regime,
		RegimeConfidence:      regime.Confidence,
		Playbook:              s.selectPlaybook(direction, metrics),
		Float:                 tick.Float,
		Catalyst:              tick.Catalyst,
		CatalystURL:           tick.CatalystURL,
		Timestamp:             tick.Timestamp,
	}

	return candidate, true
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
		state.cumulativeDollarFlow += typical * float64(tick.Volume)
		state.cumulativeVolume += float64(tick.Volume)
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
	adx                   float64
	bbUpper               float64
	bbMiddle              float64
	bbLower               float64
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
	}

	return m
}

// continuousScore returns a value in [0, 1] that ramps linearly from 0 at threshold to 1 at saturation.
func continuousScore(value, threshold, saturation float64) float64 {
	if value <= 0 || value < threshold {
		return 0
	}
	if saturation <= threshold {
		return 1
	}
	normalized := (value - threshold) / (saturation - threshold)
	if normalized > 1.0 {
		normalized = 1.0
	}
	return normalized
}

func (s *Scanner) scoreCandidate(tick domain.Tick, m scanMetrics, direction string) float64 {
	cfg := s.config
	score := 0.0

	// Relative Volume: ramp from 2.0 to 8.0, weight 1.5
	score += 1.5 * continuousScore(tick.RelativeVolume, 2.0, 8.0)

	// Volume Rate: ramp from minVolumeRate to minVolumeRate*4, weight 0.5
	score += 0.5 * continuousScore(m.volumeRate, cfg.MinVolumeRate, cfg.MinVolumeRate*4)

	// Gap Percent: ramp from minGap to 15%, weight 1.5
	gapAbs := math.Abs(tick.GapPercent)
	score += 1.5 * continuousScore(gapAbs, cfg.MinGapPercent, 15.0)

	// One-minute return: weight 0.5
	if direction == domain.DirectionLong {
		score += 0.5 * continuousScore(m.oneMinuteReturn, cfg.MinOneMinuteReturnPct, cfg.MinOneMinuteReturnPct*3)
	} else {
		score += 0.5 * continuousScore(-m.oneMinuteReturn, cfg.MinOneMinuteReturnPct, cfg.MinOneMinuteReturnPct*3)
	}

	// Three-minute return: weight 0.5
	if direction == domain.DirectionLong {
		score += 0.5 * continuousScore(m.threeMinuteReturn, cfg.MinThreeMinuteReturnPct, cfg.MinThreeMinuteReturnPct*3)
	} else {
		score += 0.5 * continuousScore(-m.threeMinuteReturn, cfg.MinThreeMinuteReturnPct, cfg.MinThreeMinuteReturnPct*3)
	}

	// Breakout strength: ramp from 0 to 3%, weight 1.0
	if direction == domain.DirectionLong {
		score += 1.0 * continuousScore(m.breakoutPct, 0, 3.0)
	} else {
		score += 1.0 * continuousScore(-m.breakoutPct, 0, 3.0)
	}

	// VWAP alignment: weight 0.5
	if m.vwap > 0 {
		vwapPct := (tick.Price - m.vwap) / m.vwap * 100
		if direction == domain.DirectionLong {
			score += 0.5 * continuousScore(vwapPct, 0, 2.0)
		} else {
			score += 0.5 * continuousScore(-vwapPct, 0, 2.0)
		}
	}

	// EMA alignment: weight 0.5
	if m.emaFast > 0 && m.emaSlow > 0 {
		emaDiff := (m.emaFast - m.emaSlow) / m.emaSlow * 100
		if direction == domain.DirectionLong {
			score += 0.5 * continuousScore(emaDiff, 0, 1.0)
		} else {
			score += 0.5 * continuousScore(-emaDiff, 0, 1.0)
		}
	}

	// RSI momentum alignment: weight 0.5
	if direction == domain.DirectionLong && m.rsiMASlope > 0 {
		score += 0.5 * continuousScore(m.rsiMASlope, 0, 2.0)
	} else if direction == domain.DirectionShort && m.rsiMASlope < 0 {
		score += 0.5 * continuousScore(-m.rsiMASlope, 0, 2.0)
	}

	return score
}

func (s *Scanner) selectPlaybook(direction string, m scanMetrics) string {
	if m.setupType == "breakout" || m.setupType == "breakdown" {
		return "breakout"
	}
	if m.pullbackDepthPct > 30 && m.pullbackDepthPct < 70 {
		return "pullback"
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
	for rank, r := range rankings {
		candidates[r.idx].LeaderRank = rank + 1
		if topComposite > 0 {
			candidates[r.idx].VolumeLeaderPct = (r.composite / topComposite) * 100
		}
		// Top 3 leaders get a scoring bonus
		if rank < 3 {
			leaderBonus := 1.0 + float64(3-rank)*0.05 // rank 0(=1st): +15%, rank 1(=2nd): +10%, rank 2(=3rd): +5%
			candidates[r.idx].Score *= leaderBonus
		}
	}
}

// computeBollingerBands computes upper, middle, and lower Bollinger Bands.
func computeBollingerBands(bars []symbolBar, period int, k float64) (upper, middle, lower float64) {
	if len(bars) < period {
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
	stddev := math.Sqrt(sumSq / float64(period))

	upper = middle + k*stddev
	lower = middle - k*stddev
	return
}

// ComputeBollingerBandsFromPrices computes Bollinger Bands from a slice of prices (exported for strategy use).
func ComputeBollingerBandsFromPrices(prices []float64, period int, k float64) (upper, middle, lower float64) {
	if len(prices) < period {
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
	stddev := math.Sqrt(sumSq / float64(period))

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

