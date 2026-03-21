package scanner

import (
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
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
}

type scanMetrics struct {
	oneMinuteReturn       float64
	threeMinuteReturn     float64
	volumeRate            float64
	atr                   float64
	vwap                  float64
	priceVsVWAPPct        float64
	breakoutPct           float64
	consolidationRangePct float64
	pullbackDepthPct      float64
	closeOffHighPct       float64
	setupHigh             float64
	setupLow              float64
	setupType             string
	rsiMASlope            float64
	fiveMinRange          float64
	emaFast               float64
	emaSlow               float64
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

// Start evaluates ticks concurrently and emits candidates.
func (s *Scanner) Start(ctx context.Context, in <-chan domain.Tick, out chan<- domain.Candidate) error {
	workerCount := s.config.ScannerWorkers
	if workerCount < 1 {
		workerCount = 1
	}

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case tick, ok := <-in:
					if !ok {
						s.runtime.RecordLog("warn", "scanner", "input channel closed")
						return
					}
					candidate, shouldEmit := s.evaluateTick(tick)
					if !shouldEmit {
						continue
					}
					s.runtime.RecordCandidate(candidate)
					select {
					case <-ctx.Done():
						return
					case out <- candidate:
					}
				}
			}
		}()
	}

	<-ctx.Done()
	workers.Wait()
	return ctx.Err()
}

// EvaluateTick applies the scanner filters and feature extraction to a tick.
func (s *Scanner) EvaluateTick(tick domain.Tick) (domain.Candidate, bool) {
	candidate, ok, _ := s.evaluateTickDetailed(tick)
	return candidate, ok
}

// EvaluateTickDetailed applies the scanner filters and returns the block reason when rejected.
func (s *Scanner) EvaluateTickDetailed(tick domain.Tick) (domain.Candidate, bool, string) {
	return s.evaluateTickDetailed(tick)
}

func (s *Scanner) evaluateTick(tick domain.Tick) (domain.Candidate, bool) {
	candidate, ok, _ := s.evaluateTickDetailed(tick)
	return candidate, ok
}

func (s *Scanner) evaluateTickDetailed(tick domain.Tick) (domain.Candidate, bool, string) {
	metrics := s.updateSymbolState(tick)
	priceVsOpenPct := percentChange(tick.Open, tick.Price)
	if s.isBenchmarkSymbol(tick.Symbol) {
		return domain.Candidate{}, false, "market-benchmark"
	}

	if tick.Price <= s.config.MinPrice {
		return domain.Candidate{}, false, "min-price"
	}
	if s.config.MaxPrice > 0 && tick.Price > s.config.MaxPrice {
		return domain.Candidate{}, false, "max-price"
	}
	if tick.RelativeVolume <= s.config.MinRelativeVolume {
		return domain.Candidate{}, false, "min-relative-volume"
	}
	if !tick.VolumeSpike {
		return domain.Candidate{}, false, "volume-spike"
	}
	direction := domain.DirectionLong
	shortSetupEnabled := metrics.setupType == "parabolic-failed-reclaim-short"
	switch {
	case shortSetupEnabled && s.qualifiesShortMomentumProfile(tick, priceVsOpenPct, metrics):
		direction = domain.DirectionShort
	case s.qualifiesMomentumProfile(tick, priceVsOpenPct, metrics):
		direction = domain.DirectionLong
	default:
		return domain.Candidate{}, false, "not-gap-or-squeeze"
	}

	volumeLeaderPct, leaderRank := s.updateVolumeLeadership(tick)
	distanceFromHighPct := percentChange(tick.Price, tick.HighOfDay)
	score := s.momentumScore(tick, priceVsOpenPct, distanceFromHighPct, volumeLeaderPct, leaderRank, metrics)
	atrPct := 0.0
	if tick.Price > 0 && metrics.atr > 0 {
		atrPct = (metrics.atr / tick.Price) * 100
	}
	return domain.Candidate{
		Symbol:                tick.Symbol,
		Direction:             direction,
		Price:                 tick.Price,
		Open:                  tick.Open,
		GapPercent:            tick.GapPercent,
		RelativeVolume:        tick.RelativeVolume,
		PreMarketVolume:       tick.PreMarketVolume,
		Volume:                tick.Volume,
		HighOfDay:             tick.HighOfDay,
		PriceVsOpenPct:        round2(scoreOrZero(priceVsOpenPct)),
		DistanceFromHighPct:   round2(scoreOrZero(distanceFromHighPct)),
		OneMinuteReturnPct:    round2(scoreOrZero(metrics.oneMinuteReturn)),
		ThreeMinuteReturnPct:  round2(scoreOrZero(metrics.threeMinuteReturn)),
		VolumeRate:            round2(scoreOrZero(metrics.volumeRate)),
		VolumeLeaderPct:       clampFloat(scoreOrZero(volumeLeaderPct), 0, 1),
		LeaderRank:            leaderRank,
		MinutesSinceOpen:      round2(minutesSinceOpen(tick.Timestamp)),
		ATR:                   round2(scoreOrZero(metrics.atr)),
		ATRPct:                round2(scoreOrZero(atrPct)),
		VWAP:                  round2(scoreOrZero(metrics.vwap)),
		PriceVsVWAPPct:        round2(scoreOrZero(metrics.priceVsVWAPPct)),
		BreakoutPct:           round2(scoreOrZero(metrics.breakoutPct)),
		ConsolidationRangePct: round2(scoreOrZero(metrics.consolidationRangePct)),
		PullbackDepthPct:      round2(scoreOrZero(metrics.pullbackDepthPct)),
		CloseOffHighPct:       round2(scoreOrZero(metrics.closeOffHighPct)),
		SetupHigh:             round2(scoreOrZero(metrics.setupHigh)),
		SetupLow:              round2(scoreOrZero(metrics.setupLow)),
		RSIMASlope:            round2(scoreOrZero(metrics.rsiMASlope)),
		FiveMinRange:          round2(scoreOrZero(metrics.fiveMinRange)),
		EMAFast:               round2(scoreOrZero(metrics.emaFast)),
		EMASlow:               round2(scoreOrZero(metrics.emaSlow)),
		SetupType:             metrics.setupType,
		Score:                 round2(scoreOrZero(score)),
		Catalyst:              tick.Catalyst,
		CatalystURL:           tick.CatalystURL,
		Timestamp:             tick.Timestamp,
	}, true, "candidate"
}

func (s *Scanner) momentumScore(tick domain.Tick, priceVsOpenPct, distanceFromHighPct, volumeLeaderPct float64, leaderRank int, metrics scanMetrics) float64 {
	direction := domain.DirectionLong
	if metrics.setupType == "parabolic-failed-reclaim-short" {
		direction = domain.DirectionShort
	}
	if domain.IsShort(direction) {
		return s.shortMomentumScore(tick, priceVsOpenPct, distanceFromHighPct, volumeLeaderPct, leaderRank, metrics)
	}
	return s.longMomentumScore(tick, priceVsOpenPct, distanceFromHighPct, volumeLeaderPct, leaderRank, metrics)
}

func (s *Scanner) longMomentumScore(tick domain.Tick, priceVsOpenPct, distanceFromHighPct, volumeLeaderPct float64, leaderRank int, metrics scanMetrics) float64 {
	score := (clampFloat(tick.GapPercent, -10, 35) * 0.15) +
		(clampFloat(tick.RelativeVolume, 0, 18) * 1.25) +
		(clampFloat(priceVsOpenPct, -5, 30) * 0.45) +
		(clampFloat(metrics.oneMinuteReturn, -3, 4.5) * 1.35) +
		(clampFloat(metrics.threeMinuteReturn, -5, 8) * 1.10) +
		(clampFloat(metrics.volumeRate, 0.5, 4) * 1.20) +
		(clampFloat(metrics.priceVsVWAPPct, -4, 8) * 1.10) +
		(clampFloat(metrics.breakoutPct, -4, 5) * 1.55) +
		(clampFloat(metrics.pullbackDepthPct, 0, 8) * 0.30) -
		(clampFloat(distanceFromHighPct, 0, 6) * 1.40) -
		(clampFloat(metrics.consolidationRangePct, 0, 8) * 0.55) -
		(clampFloat(metrics.closeOffHighPct, 0, 100) * 0.05) +
		(clampFloat(volumeLeaderPct, 0, 1) * 5.50)

	switch leaderRank {
	case 1:
		score += 2.50
	case 2:
		score += 1.50
	case 3:
		score += 0.75
	}

	switch metrics.setupType {
	case "consolidation-breakout":
		score += 8
	case "higher-low-reclaim":
		score += 7
	case "vwap-reclaim":
		score += 6.5
	case "opening-range-breakout":
		score += 5
	}
	return score
}

func (s *Scanner) qualifiesMomentumProfile(tick domain.Tick, priceVsOpenPct float64, metrics scanMetrics) bool {
	if tick.GapPercent >= s.config.MinGapPercent && tick.PreMarketVolume >= s.config.MinPremarketVolume {
		return true
	}
	if metrics.setupType == "" {
		return false
	}
	if priceVsOpenPct < maxFloat(s.config.ScannerMinPriceVsOpenPctFloor, s.config.MinGapPercent*s.config.ScannerMinPriceVsOpenGapMultiplier) {
		return false
	}
	if metrics.threeMinuteReturn < s.config.MinThreeMinuteReturnPct && metrics.oneMinuteReturn < s.config.MinOneMinuteReturnPct {
		return false
	}
	if metrics.volumeRate < maxFloat(1.0, s.config.MinVolumeRate+s.config.ScannerMinSetupVolumeRateOffset) {
		return false
	}
	return tick.RelativeVolume >= s.config.MinRelativeVolume+s.config.ScannerMinSetupRelativeVolumeExtra
}

func (s *Scanner) qualifiesShortMomentumProfile(tick domain.Tick, priceVsOpenPct float64, metrics scanMetrics) bool {
	if !s.config.EnableShorts {
		return false
	}
	if metrics.setupType != "parabolic-failed-reclaim-short" {
		return false
	}
	if tick.RelativeVolume < s.config.MinRelativeVolume+s.config.ScannerMinSetupRelativeVolumeExtra {
		return false
	}
	vwapLimit := s.config.ShortVWAPBreakMinPct
	if metrics.setupType == "parabolic-failed-reclaim-short" {
		vwapLimit = 2.0
	}
	if metrics.priceVsVWAPPct > vwapLimit {
		return false
	}
	if metrics.oneMinuteReturn >= -maxFloat(0.25, s.config.MinOneMinuteReturnPct*0.50) {
		return false
	}
	if metrics.threeMinuteReturn >= -maxFloat(0.50, s.config.MinThreeMinuteReturnPct*0.75) {
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
	return priceVsOpenPct >= maxFloat(2.0, s.config.MinGapPercent*0.5)
}

func (s *Scanner) shortMomentumScore(tick domain.Tick, priceVsOpenPct, distanceFromHighPct, volumeLeaderPct float64, leaderRank int, metrics scanMetrics) float64 {
	peakExtensionPct := 0.0
	if tick.Open > 0 && tick.HighOfDay > tick.Open {
		peakExtensionPct = ((tick.HighOfDay - tick.Open) / tick.Open) * 100
	}
	score := (clampFloat(tick.GapPercent, 0, 35) * 0.20) +
		(clampFloat(tick.RelativeVolume, 0, 18) * 1.30) +
		(clampFloat(priceVsOpenPct, 0, 30) * 0.20) +
		(clampFloat(peakExtensionPct, 0, 35) * 0.55) +
		(clampFloat(-metrics.oneMinuteReturn, 0, 4.5) * 1.45) +
		(clampFloat(-metrics.threeMinuteReturn, 0, 8) * 1.20) +
		(clampFloat(metrics.volumeRate, 0.5, 4) * 1.15) +
		(clampFloat(-metrics.priceVsVWAPPct, 0, 8) * 1.35) +
		(clampFloat(-metrics.breakoutPct, 0, 5) * 1.65) +
		(clampFloat(distanceFromHighPct, 0, 12) * 0.55) +
		(clampFloat(metrics.closeOffHighPct, 0, 100) * 0.08) +
		(clampFloat(volumeLeaderPct, 0, 1) * 5.50)

	if leaderRank == 1 {
		score += 2.5
	} else if leaderRank == 2 {
		score += 1.25
	}
	if metrics.setupType == "parabolic-failed-reclaim-short" {
		score += 8.5
	}
	return score
}

func (s *Scanner) updateSymbolState(tick domain.Tick) scanMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()

	dayKey := tick.Timestamp.In(markethours.Location()).Format("2006-01-02")
	state := s.state[tick.Symbol]
	if state == nil {
		state = &symbolState{}
		s.state[tick.Symbol] = state
	}
	if state.day != dayKey {
		state.day = dayKey
		state.bars = nil
		state.cumulativeDollarFlow = 0
	}

	deltaVolume := tick.Volume
	if count := len(state.bars); count > 0 {
		deltaVolume = tick.Volume - state.bars[count-1].cumulativeVolume
		if deltaVolume < 0 {
			deltaVolume = tick.Volume
		}
	}
	barOpen := firstNonZero(tick.BarOpen, tick.Price)
	barHigh := maxFloat(tick.BarHigh, tick.Price)
	barLow := firstNonZero(tick.BarLow, tick.Price)
	if barLow > tick.Price {
		barLow = tick.Price
	}
	typicalPrice := (barHigh + barLow + tick.Price) / 3
	state.cumulativeDollarFlow += typicalPrice * float64(maxInt64(deltaVolume, 0))
	vwap := tick.Price
	if tick.Volume > 0 && state.cumulativeDollarFlow > 0 {
		vwap = state.cumulativeDollarFlow / float64(tick.Volume)
	}

	state.bars = append(state.bars, symbolBar{
		timestamp:        tick.Timestamp,
		open:             barOpen,
		high:             barHigh,
		low:              barLow,
		close:            tick.Price,
		volume:           maxInt64(deltaVolume, 0),
		cumulativeVolume: tick.Volume,
		vwap:             vwap,
	})
	cutoff := tick.Timestamp.Add(-90 * time.Minute)
	trimmed := state.bars[:0]
	for _, bar := range state.bars {
		if bar.timestamp.Before(cutoff) {
			continue
		}
		trimmed = append(trimmed, bar)
	}
	state.bars = trimmed

	return deriveMetrics(state.bars, s.config)
}

func deriveMetrics(bars []symbolBar, cfg config.TradingConfig) scanMetrics {
	if len(bars) == 0 {
		return scanMetrics{}
	}
	current := bars[len(bars)-1]
	emaFast, emaSlow := computeEMAPair(bars, 8, 21)
	metrics := scanMetrics{
		oneMinuteReturn:   lookbackReturn(bars, 1),
		threeMinuteReturn: lookbackReturn(bars, 3),
		volumeRate:        recentVolumeRate(bars),
		atr:               averageTrueRange(bars, 14),
		vwap:              current.vwap,
		priceVsVWAPPct:    percentChange(current.vwap, current.close),
		closeOffHighPct:   closeOffHighPct(current),
		rsiMASlope:        computeRSIMASlope(bars, 14, 5, 3),
		fiveMinRange:      fiveMinuteRange(bars),
		emaFast:           emaFast,
		emaSlow:           emaSlow,
	}

	if len(bars) < 4 {
		return metrics
	}
	completed := bars[:len(bars)-1]
	recentSetupBars := lastNBars(completed, 3)
	impulseBars := lastNBars(completed, 8)
	setupHigh := maxBarHigh(recentSetupBars)
	setupLow := minBarLow(recentSetupBars)
	impulseHigh := maxBarHigh(impulseBars)
	priorBars := completed
	if len(priorBars) > 1 {
		priorBars = priorBars[:len(priorBars)-1]
	}
	priorPullbackLow := minBarLow(lastNBars(priorBars, 3))
	metrics.setupHigh = setupHigh
	metrics.setupLow = setupLow
	metrics.breakoutPct = percentChange(setupHigh, current.close)
	metrics.consolidationRangePct = rangePct(setupLow, setupHigh)
	metrics.pullbackDepthPct = drawdownPct(impulseHigh, setupLow)

	aboveVWAP := metrics.priceVsVWAPPct >= cfg.ScannerVWAPTolerancePct
	vwapReclaim := false
	if len(completed) > 0 {
		previous := completed[len(completed)-1]
		vwapReclaim = previous.close <= previous.vwap && current.close > current.vwap
	}
	atrPct := 0.0
	if current.close > 0 && metrics.atr > 0 {
		atrPct = (metrics.atr / current.close) * 100
	}
	tightConsolidation := metrics.consolidationRangePct <= maxFloat(atrPct*cfg.ScannerConsolidationATRMultiplier, cfg.ScannerConsolidationMaxPct)
	shallowEnoughPullback := metrics.pullbackDepthPct >= maxFloat(atrPct*cfg.ScannerPullbackDepthMinATRMultiplier, cfg.ScannerPullbackDepthMinPct) &&
		metrics.pullbackDepthPct <= maxFloat(atrPct*cfg.ScannerPullbackDepthMaxATRMultiplier, cfg.ScannerPullbackDepthMaxPct)
	strengthClose := metrics.closeOffHighPct <= 35
	higherLow := priorPullbackLow > 0 && setupLow > priorPullbackLow
	renewedVolume := metrics.volumeRate >= cfg.ScannerRenewedVolumeRateMin
	peakHigh := maxBarHigh(lastNBars(completed, 12))
	peakExtensionPct := percentChange(bars[0].open, peakHigh)
	reclaimFailureHigh := maxBarHigh(lastNBars(completed, 3))
	breakdownLow := minBarLow(lastNBars(completed, 3))
	weakClose := metrics.closeOffHighPct >= 60

	switch {
	case peakExtensionPct >= cfg.ShortPeakExtensionMinPct &&
		metrics.oneMinuteReturn <= -0.35 &&
		metrics.threeMinuteReturn <= -0.75 &&
		weakClose &&
		breakdownLow > 0 &&
		current.close < breakdownLow &&
		reclaimFailureHigh > current.close &&
		reclaimFailureHigh < peakHigh:
		metrics.setupType = "parabolic-failed-reclaim-short"
		metrics.setupHigh = reclaimFailureHigh
		metrics.setupLow = breakdownLow
		metrics.breakoutPct = percentChange(breakdownLow, current.close)
	case metrics.breakoutPct >= -0.15 && tightConsolidation && shallowEnoughPullback && aboveVWAP && strengthClose:
		metrics.setupType = "consolidation-breakout"
	case metrics.breakoutPct >= -maxFloat(atrPct*0.60, 0.45) &&
		aboveVWAP &&
		higherLow &&
		renewedVolume &&
		strengthClose:
		metrics.setupType = "higher-low-reclaim"
	case vwapReclaim && metrics.breakoutPct >= -maxFloat(atrPct*0.45, 0.35) && shallowEnoughPullback && metrics.closeOffHighPct <= 40:
		metrics.setupType = "vwap-reclaim"
	case minutesSinceOpen(current.timestamp) <= 30 && metrics.breakoutPct >= 0 && aboveVWAP && metrics.closeOffHighPct <= 30:
		metrics.setupType = "opening-range-breakout"
	}

	return metrics
}

func (s *Scanner) updateVolumeLeadership(tick domain.Tick) (float64, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dayKey := tick.Timestamp.In(markethours.Location()).Format("2006-01-02")
	if s.leaderDay != dayKey {
		s.leaderDay = dayKey
		s.leaderMetrics = make(map[string]float64)
	}
	metric := momentumLeaderMetric(tick)
	if metric <= 0 {
		return 1, 1
	}
	s.leaderMetrics[tick.Symbol] = metric

	leaderMetric := 0.0
	rank := 1
	for symbol, candidateMetric := range s.leaderMetrics {
		if candidateMetric > leaderMetric {
			leaderMetric = candidateMetric
		}
		if symbol != tick.Symbol && candidateMetric > metric {
			rank++
		}
	}
	if leaderMetric <= 0 {
		return 1, rank
	}
	return metric / leaderMetric, rank
}

func momentumLeaderMetric(tick domain.Tick) float64 {
	relativeVolume := clampFloat(tick.RelativeVolume, 1, 25)
	return tick.Price * float64(tick.Volume) * relativeVolume
}

func (s *Scanner) isBenchmarkSymbol(symbol string) bool {
	if !s.config.EnableMarketRegime {
		return false
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	for _, benchmark := range s.config.MarketRegimeBenchmarkSymbols {
		if normalized == strings.ToUpper(strings.TrimSpace(benchmark)) {
			return true
		}
	}
	return false
}

func lookbackReturn(bars []symbolBar, lookback int) float64 {
	if len(bars) < lookback+1 {
		return 0
	}
	baseline := bars[len(bars)-1-lookback].close
	if baseline <= 0 {
		return 0
	}
	return percentChange(baseline, bars[len(bars)-1].close)
}

func recentVolumeRate(bars []symbolBar) float64 {
	if len(bars) < 2 {
		return 1
	}
	window := lastNBars(bars, 6)
	if len(window) < 2 {
		return 1
	}
	latest := float64(window[len(window)-1].volume)
	var total float64
	for _, bar := range window[:len(window)-1] {
		total += float64(bar.volume)
	}
	average := total / float64(len(window)-1)
	if average <= 0 {
		return 1
	}
	return latest / average
}

func averageTrueRange(bars []symbolBar, period int) float64 {
	if len(bars) < 2 {
		return 0
	}
	window := lastNBars(bars, period+1)
	if len(window) < 2 {
		return 0
	}
	var total float64
	var count int
	for index := 1; index < len(window); index++ {
		prevClose := window[index-1].close
		current := window[index]
		trueRange := maxFloat(current.high-current.low, math.Abs(current.high-prevClose))
		trueRange = maxFloat(trueRange, math.Abs(current.low-prevClose))
		total += trueRange
		count++
	}
	if count == 0 {
		return 0
	}
	atr := total / float64(count)
	// Floor: 1-minute bars produce unrealistically small ATR values for
	// low-volatility minutes. Enforce a minimum of 1% of price so that
	// downstream stop placement and position sizing stay reasonable.
	lastPrice := window[len(window)-1].close
	if lastPrice > 0 {
		minATR := lastPrice * 0.01
		if atr < minATR {
			atr = minATR
		}
	}
	return atr
}

func lastNBars(bars []symbolBar, count int) []symbolBar {
	if count <= 0 || len(bars) <= count {
		return bars
	}
	return bars[len(bars)-count:]
}

func maxBarHigh(bars []symbolBar) float64 {
	high := 0.0
	for _, bar := range bars {
		if bar.high > high {
			high = bar.high
		}
	}
	return high
}

func minBarLow(bars []symbolBar) float64 {
	low := 0.0
	for _, bar := range bars {
		if low == 0 || bar.low < low {
			low = bar.low
		}
	}
	return low
}

func rangePct(low, high float64) float64 {
	if low <= 0 || high <= low {
		return 0
	}
	return ((high - low) / low) * 100
}

func drawdownPct(high, low float64) float64 {
	if high <= 0 || low <= 0 || low >= high {
		return 0
	}
	return ((high - low) / high) * 100
}

func closeOffHighPct(bar symbolBar) float64 {
	barRange := bar.high - bar.low
	if barRange <= 0 {
		return 0
	}
	return ((bar.high - bar.close) / barRange) * 100
}

func percentChange(from, to float64) float64 {
	if from == 0 {
		return 0
	}
	return ((to - from) / from) * 100
}

func minutesSinceOpen(timestamp time.Time) float64 {
	est := timestamp.In(markethours.Location())
	minutes := est.Hour()*60 + est.Minute()
	// Premarket: return minutes since 4:00 AM ET as a negative offset
	// so time-based filters can distinguish premarket from regular session.
	if minutes < 9*60+30 {
		preOpen := time.Date(est.Year(), est.Month(), est.Day(), 4, 0, 0, 0, est.Location())
		sincePre := est.Sub(preOpen).Minutes()
		if sincePre < 0 {
			return 0
		}
		return sincePre
	}
	open := time.Date(est.Year(), est.Month(), est.Day(), 9, 30, 0, 0, est.Location())
	return maxFloat(0, est.Sub(open).Minutes())
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func firstNonZero(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func scoreOrZero(value float64) float64 {
	if value != value {
		return 0
	}
	return value
}

// computeRSIMASlope computes the slope of a moving average of RSI values.
// Positive slope = upward momentum building; negative = declining.
func computeRSIMASlope(bars []symbolBar, rsiPeriod, maPeriod, slopeLookback int) float64 {
	needRSI := maPeriod + slopeLookback
	minBars := rsiPeriod + needRSI + 1
	if len(bars) < minBars {
		return 0
	}
	rsiValues := make([]float64, needRSI)
	for i := 0; i < needRSI; i++ {
		endIdx := len(bars) - needRSI + i + 1
		rsiValues[i] = computeRSI(bars[:endIdx], rsiPeriod)
	}
	currentMA := 0.0
	for i := needRSI - maPeriod; i < needRSI; i++ {
		currentMA += rsiValues[i]
	}
	currentMA /= float64(maPeriod)

	pastMA := 0.0
	for i := needRSI - maPeriod - slopeLookback; i < needRSI-slopeLookback; i++ {
		pastMA += rsiValues[i]
	}
	pastMA /= float64(maPeriod)

	return (currentMA - pastMA) / float64(slopeLookback)
}

// fiveMinuteRange returns the high-low range over the last 5 bars as a
// percentage of price. Measures recent activity/volatility.
func fiveMinuteRange(bars []symbolBar) float64 {
	if len(bars) < 5 {
		return 0
	}
	window := lastNBars(bars, 5)
	high := maxBarHigh(window)
	low := minBarLow(window)
	if low <= 0 {
		return 0
	}
	return ((high - low) / low) * 100
}

// computeRSI calculates a Wilder-smoothed RSI over the given period.
// Returns 50 (neutral) when there is insufficient data.
func computeRSI(bars []symbolBar, period int) float64 {
	if len(bars) < period+1 {
		return 50
	}
	window := lastNBars(bars, period+1)
	var avgGain, avgLoss float64
	for i := 1; i < len(window); i++ {
		change := window[i].close - window[i-1].close
		if change > 0 {
			avgGain += change
		} else {
			avgLoss -= change
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)
	if avgLoss == 0 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs))
}

// computeEMAPair returns (emaFast, emaSlow) over the given bars using
// exponential moving average of close prices.
func computeEMAPair(bars []symbolBar, fastPeriod, slowPeriod int) (float64, float64) {
	if len(bars) < 2 {
		return 0, 0
	}
	fast := bars[0].close
	slow := bars[0].close
	fastMul := 2.0 / (float64(fastPeriod) + 1.0)
	slowMul := 2.0 / (float64(slowPeriod) + 1.0)
	for i := 1; i < len(bars); i++ {
		price := bars[i].close
		fast += (price - fast) * fastMul
		slow += (price - slow) * slowMul
	}
	return fast, slow
}
