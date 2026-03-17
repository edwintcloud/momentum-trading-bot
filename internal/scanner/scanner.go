package scanner

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

var marketLocation = mustLoadLocation("America/New_York")

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
	fifteenMinuteReturn   float64
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
	ema9                  float64
	ema21                 float64
	macd                  float64
	macdSignal            float64
	macdHistogram         float64
	rsi                   float64
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
	if !s.qualifiesMomentumProfile(tick, priceVsOpenPct, metrics) {
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
		FifteenMinuteReturnPct: round2(scoreOrZero(metrics.fifteenMinuteReturn)),
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
		SetupType:             metrics.setupType,
		Score:                 round2(scoreOrZero(score)),
		EMA9:                  round2(scoreOrZero(metrics.ema9)),
		EMA21:                 round2(scoreOrZero(metrics.ema21)),
		MACD:                  round2(metrics.macd),
		MACDSignal:            round2(metrics.macdSignal),
		MACDHistogram:         round2(metrics.macdHistogram),
		RSI:                   round2(scoreOrZero(metrics.rsi)),
		Catalyst:              tick.Catalyst,
		CatalystURL:           tick.CatalystURL,
		Timestamp:             tick.Timestamp,
	}, true, "candidate"
}

func (s *Scanner) momentumScore(tick domain.Tick, priceVsOpenPct, distanceFromHighPct, volumeLeaderPct float64, leaderRank int, metrics scanMetrics) float64 {
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
	if priceVsOpenPct < maxFloat(2.5, s.config.MinGapPercent*0.25) {
		return false
	}
	if metrics.fifteenMinuteReturn < s.config.MinFifteenMinuteReturnPct && metrics.threeMinuteReturn < s.config.MinThreeMinuteReturnPct && metrics.oneMinuteReturn < s.config.MinOneMinuteReturnPct {
		return false
	}
	if metrics.volumeRate < maxFloat(1.0, s.config.MinVolumeRate-0.05) {
		return false
	}
	return tick.RelativeVolume >= s.config.MinRelativeVolume+0.25
}

func (s *Scanner) updateSymbolState(tick domain.Tick) scanMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()

	dayKey := tick.Timestamp.In(marketLocation).Format("2006-01-02")
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
		timestamp:        tick.Timestamp.UTC(),
		open:             barOpen,
		high:             barHigh,
		low:              barLow,
		close:            tick.Price,
		volume:           maxInt64(deltaVolume, 0),
		cumulativeVolume: tick.Volume,
		vwap:             vwap,
	})
	cutoff := tick.Timestamp.UTC().Add(-90 * time.Minute)
	trimmed := state.bars[:0]
	for _, bar := range state.bars {
		if bar.timestamp.Before(cutoff) {
			continue
		}
		trimmed = append(trimmed, bar)
	}
	state.bars = trimmed

	return deriveMetrics(state.bars)
}

func deriveMetrics(bars []symbolBar) scanMetrics {
	if len(bars) == 0 {
		return scanMetrics{}
	}
	current := bars[len(bars)-1]
	metrics := scanMetrics{
		oneMinuteReturn:   lookbackReturn(bars, 1),
		threeMinuteReturn: lookbackReturn(bars, 3),
		fifteenMinuteReturn: lookbackReturn(bars, 15),
		volumeRate:        recentVolumeRate(bars),
		atr:               averageTrueRange(bars, 14),
		vwap:              current.vwap,
		priceVsVWAPPct:    percentChange(current.vwap, current.close),
		closeOffHighPct:   closeOffHighPct(current),
		ema9:              computeEMA(bars, 9),
		ema21:             computeEMA(bars, 21),
		rsi:               computeRSI(bars, 14),
	}
	macdL, macdS, macdH := computeMACD(bars)
	metrics.macd = macdL
	metrics.macdSignal = macdS
	metrics.macdHistogram = macdH

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

	aboveVWAP := metrics.priceVsVWAPPct >= -0.10
	vwapReclaim := false
	if len(completed) > 0 {
		previous := completed[len(completed)-1]
		vwapReclaim = previous.close <= previous.vwap && current.close > current.vwap
	}
	atrPct := 0.0
	if current.close > 0 && metrics.atr > 0 {
		atrPct = (metrics.atr / current.close) * 100
	}
	tightConsolidation := metrics.consolidationRangePct <= maxFloat(atrPct*1.75, 4.5)
	shallowEnoughPullback := metrics.pullbackDepthPct >= maxFloat(atrPct*0.35, 0.40) &&
		metrics.pullbackDepthPct <= maxFloat(atrPct*2.40, 8.0)
	strengthClose := metrics.closeOffHighPct <= 35
	higherLow := priorPullbackLow > 0 && setupLow > priorPullbackLow
	renewedVolume := metrics.volumeRate >= 1.05

	switch {
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

	dayKey := tick.Timestamp.In(marketLocation).Format("2006-01-02")
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
	est := timestamp.In(marketLocation)
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

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

func computeEMA(bars []symbolBar, period int) float64 {
	if len(bars) == 0 {
		return 0
	}
	if len(bars) == 1 {
		return bars[0].close
	}

	multiplier := 2.0 / (float64(period) + 1.0)
	ema := bars[0].close

	for i := 1; i < len(bars); i++ {
		ema = (bars[i].close-ema)*multiplier + ema
	}
	return ema
}

func computeMACD(bars []symbolBar) (macd, signal, histogram float64) {
	if len(bars) < 26 {
		return 0, 0, 0
	}

	macds := make([]float64, 0, len(bars))
	ema12 := bars[0].close
	ema26 := bars[0].close
	m12 := 2.0 / 13.0
	m26 := 2.0 / 27.0

	for i := 1; i < len(bars); i++ {
		ema12 = (bars[i].close-ema12)*m12 + ema12
		ema26 = (bars[i].close-ema26)*m26 + ema26
		macds = append(macds, ema12-ema26)
	}

	if len(macds) == 0 {
		return 0, 0, 0
	}

	signalEMA := macds[0]
	m9 := 2.0 / 10.0
	for i := 1; i < len(macds); i++ {
		signalEMA = (macds[i]-signalEMA)*m9 + signalEMA
	}

	lastMACD := macds[len(macds)-1]
	return lastMACD, signalEMA, lastMACD - signalEMA
}

func computeRSI(bars []symbolBar, period int) float64 {
	if len(bars) <= period {
		return 50.0 // Default center point when there's no data
	}

	sumGain := 0.0
	sumLoss := 0.0

	for i := 1; i <= period; i++ {
		change := bars[i].close - bars[i-1].close
		if change > 0 {
			sumGain += change
		} else {
			sumLoss -= change
		}
	}

	avgGain := sumGain / float64(period)
	avgLoss := sumLoss / float64(period)

	for i := period + 1; i < len(bars); i++ {
		change := bars[i].close - bars[i-1].close
		gain := 0.0
		loss := 0.0
		if change > 0 {
			gain = change
		} else {
			loss = -change
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
	}

	if avgLoss == 0 {
		if avgGain == 0 {
			return 50.0
		}
		return 100.0
	}

	rs := avgGain / avgLoss
	return 100.0 - (100.0 / (1.0 + rs))
}
