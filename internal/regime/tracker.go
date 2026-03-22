package regime

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

type benchmarkState struct {
	lastVolume           int64
	cumulativeDollarFlow float64
	vwap                 float64
	emaFast              float64
	emaSlow              float64
	bars                 []barPoint
}

type barPoint struct {
	timestamp time.Time
	close     float64
}

// Tracker maintains a simple benchmark-driven market regime snapshot.
type Tracker struct {
	config     config.TradingConfig
	runtime    *runtime.State
	benchmarks map[string]*benchmarkState
}

func NewTracker(cfg config.TradingConfig, runtimeState *runtime.State) *Tracker {
	benchmarks := make(map[string]*benchmarkState, len(cfg.MarketRegimeBenchmarkSymbols))
	for _, symbol := range cfg.MarketRegimeBenchmarkSymbols {
		normalized := strings.ToUpper(strings.TrimSpace(symbol))
		if normalized == "" {
			continue
		}
		benchmarks[normalized] = &benchmarkState{}
	}
	return &Tracker{
		config:     cfg,
		runtime:    runtimeState,
		benchmarks: benchmarks,
	}
}

func (t *Tracker) IsBenchmark(symbol string) bool {
	_, exists := t.benchmarks[strings.ToUpper(strings.TrimSpace(symbol))]
	return exists
}

func (t *Tracker) UpdateTick(tick domain.Tick) {
	state, exists := t.benchmarks[strings.ToUpper(strings.TrimSpace(tick.Symbol))]
	if !exists || tick.Price <= 0 {
		return
	}

	deltaVolume := tick.Volume - state.lastVolume
	if deltaVolume < 0 {
		deltaVolume = tick.Volume
	}
	state.lastVolume = tick.Volume

	barHigh := maxFloat(tick.BarHigh, tick.Price)
	barLow := firstPositive(tick.BarLow, tick.Price)
	if barLow > tick.Price {
		barLow = tick.Price
	}
	typicalPrice := (barHigh + barLow + tick.Price) / 3
	if deltaVolume > 0 {
		state.cumulativeDollarFlow += typicalPrice * float64(deltaVolume)
	}
	if tick.Volume > 0 && state.cumulativeDollarFlow > 0 {
		state.vwap = state.cumulativeDollarFlow / float64(tick.Volume)
	} else {
		state.vwap = tick.Price
	}

	state.emaFast = updateEMA(state.emaFast, tick.Price, t.config.MarketRegimeEMAFastPeriod)
	state.emaSlow = updateEMA(state.emaSlow, tick.Price, t.config.MarketRegimeEMASlowPeriod)
	state.bars = append(state.bars, barPoint{timestamp: tick.Timestamp, close: tick.Price})
	cutoff := tick.Timestamp.Add(-2 * time.Hour)
	trimmed := state.bars[:0]
	for _, point := range state.bars {
		if point.timestamp.Before(cutoff) {
			continue
		}
		trimmed = append(trimmed, point)
	}
	state.bars = trimmed

	t.runtime.SetMarketRegime(t.snapshot(tick.Timestamp))
}

func (t *Tracker) snapshot(at time.Time) domain.MarketRegimeSnapshot {
	readings := make([]domain.BenchmarkRegimeReading, 0, len(t.benchmarks))
	bullPrice := 0
	bearPrice := 0
	bullEMA := 0
	bearEMA := 0
	bullReturn := 0
	bearReturn := 0
	validBenchmarks := 0

	for symbol, state := range t.benchmarks {
		if state == nil || state.emaFast <= 0 || state.emaSlow <= 0 || len(state.bars) == 0 {
			continue
		}
		lastPrice := state.bars[len(state.bars)-1].close
		lookbackReturn := returnLookback(state.bars, t.config.MarketRegimeReturnLookbackMin)
		readings = append(readings, domain.BenchmarkRegimeReading{
			Symbol:            symbol,
			Price:             round2(lastPrice),
			VWAP:              round2(state.vwap),
			PriceVsVWAPPct:    round2(percentChange(state.vwap, lastPrice)),
			EMAFast:           round2(state.emaFast),
			EMASlow:           round2(state.emaSlow),
			ReturnLookbackPct: round2(lookbackReturn),
		})
		validBenchmarks++
		if lastPrice >= state.vwap {
			bullPrice++
		} else {
			bearPrice++
		}
		if state.emaFast >= state.emaSlow {
			bullEMA++
		} else {
			bearEMA++
		}
		if lookbackReturn >= 0 {
			bullReturn++
		} else {
			bearReturn++
		}
	}
	sort.Slice(readings, func(i, j int) bool {
		return readings[i].Symbol < readings[j].Symbol
	})

	minBenchmarks := t.config.MarketRegimeMinBenchmarks
	if minBenchmarks < 1 {
		minBenchmarks = 2
	}
	regime := domain.MarketRegimeRanging
	confidence := 0.33
	switch {
	case bullPrice >= minBenchmarks && bullEMA >= minBenchmarks && bullReturn >= minBenchmarks:
		regime = domain.MarketRegimeBullish
		confidence = float64(bullPrice+bullEMA+bullReturn) / float64(maxInt(validBenchmarks*3, 1))
	case bearPrice >= minBenchmarks && bearEMA >= minBenchmarks && bearReturn >= minBenchmarks:
		regime = domain.MarketRegimeBearish
		confidence = float64(bearPrice+bearEMA+bearReturn) / float64(maxInt(validBenchmarks*3, 1))
	default:
		maxAligned := maxInt(bullPrice+bullEMA+bullReturn, bearPrice+bearEMA+bearReturn)
		confidence = 1 - (float64(maxAligned) / float64(maxInt(validBenchmarks*3, 1)))
		if confidence < 0.34 {
			confidence = 0.34
		}
	}

	return domain.MarketRegimeSnapshot{
		Regime:     regime,
		Confidence: round2(confidence),
		Timestamp:  at,
		Benchmarks: readings,
	}
}

func updateEMA(previous, price float64, period int) float64 {
	if price <= 0 {
		return previous
	}
	if previous <= 0 || period <= 1 {
		return price
	}
	multiplier := 2.0 / (float64(period) + 1.0)
	return previous + ((price - previous) * multiplier)
}

func returnLookback(bars []barPoint, lookbackMin int) float64 {
	if len(bars) == 0 {
		return 0
	}
	if lookbackMin < 1 {
		lookbackMin = 30
	}
	latest := bars[len(bars)-1]
	cutoff := latest.timestamp.Add(-time.Duration(lookbackMin) * time.Minute)
	baseline := latest.close
	for _, point := range bars {
		if point.timestamp.Before(cutoff) {
			continue
		}
		baseline = point.close
		break
	}
	return percentChange(baseline, latest.close)
}

func percentChange(base, value float64) float64 {
	if base <= 0 {
		return 0
	}
	return ((value - base) / base) * 100
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

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
