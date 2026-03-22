package regime

import (
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
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

// Tracker maintains a benchmark-driven market regime snapshot.
type Tracker struct {
	config      config.TradingConfig
	runtime     *runtime.State
	benchmarks  map[string]*benchmarkState
	hmmDetector *HMMRegimeDetector
}

// NewTracker creates a regime tracker.
func NewTracker(cfg config.TradingConfig, runtimeState *runtime.State) *Tracker {
	benchmarks := make(map[string]*benchmarkState, len(cfg.MarketRegimeBenchmarkSymbols))
	for _, symbol := range cfg.MarketRegimeBenchmarkSymbols {
		normalized := strings.ToUpper(strings.TrimSpace(symbol))
		if normalized == "" {
			continue
		}
		benchmarks[normalized] = &benchmarkState{}
	}
	t := &Tracker{
		config:     cfg,
		runtime:    runtimeState,
		benchmarks: benchmarks,
	}
	if cfg.HMMRegimeEnabled {
		t.hmmDetector = NewHMMRegimeDetector()
	}
	return t
}

// IsBenchmark returns true if the symbol is a regime benchmark.
func (t *Tracker) IsBenchmark(symbol string) bool {
	_, exists := t.benchmarks[strings.ToUpper(strings.TrimSpace(symbol))]
	return exists
}

// UpdateTick updates the regime tracker with a new tick.
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

	// Update EMAs
	fastMult := 2.0 / float64(t.config.MarketRegimeEMAFastPeriod+1)
	slowMult := 2.0 / float64(t.config.MarketRegimeEMASlowPeriod+1)
	if state.emaFast == 0 {
		state.emaFast = tick.Price
	} else {
		state.emaFast = (tick.Price-state.emaFast)*fastMult + state.emaFast
	}
	if state.emaSlow == 0 {
		state.emaSlow = tick.Price
	} else {
		state.emaSlow = (tick.Price-state.emaSlow)*slowMult + state.emaSlow
	}

	// Update bar history
	state.bars = append(state.bars, barPoint{timestamp: tick.Timestamp, close: tick.Price})
	lookbackDuration := time.Duration(t.config.MarketRegimeReturnLookbackMin) * time.Minute
	cutoff := tick.Timestamp.Add(-lookbackDuration * 2)
	trimmed := state.bars[:0]
	for _, b := range state.bars {
		if b.timestamp.After(cutoff) {
			trimmed = append(trimmed, b)
		}
	}
	state.bars = trimmed

	// Feed HMM detector with benchmark returns
	if t.hmmDetector != nil && len(state.bars) >= 2 {
		prev := state.bars[len(state.bars)-2].close
		if prev > 0 {
			ret := (tick.Price - prev) / prev
			t.hmmDetector.Update(ret)
		}
	}

	// Recompute regime
	t.recompute()
}

func (t *Tracker) recompute() {
	var bullish, bearish, total int
	benchmarks := make([]domain.MarketRegimeBenchmark, 0, len(t.benchmarks))

	for symbol, state := range t.benchmarks {
		if state.emaFast == 0 || state.emaSlow == 0 {
			continue
		}
		total++

		returnPct := float64(0)
		if len(state.bars) >= 2 {
			lookbackDuration := time.Duration(t.config.MarketRegimeReturnLookbackMin) * time.Minute
			latest := state.bars[len(state.bars)-1]
			for i := len(state.bars) - 2; i >= 0; i-- {
				if latest.timestamp.Sub(state.bars[i].timestamp) >= lookbackDuration {
					if state.bars[i].close > 0 {
						returnPct = (latest.close - state.bars[i].close) / state.bars[i].close * 100
					}
					break
				}
			}
		}

		vwapPct := float64(0)
		if state.vwap > 0 {
			latestPrice := float64(0)
			if len(state.bars) > 0 {
				latestPrice = state.bars[len(state.bars)-1].close
			}
			vwapPct = (latestPrice - state.vwap) / state.vwap * 100
		}

		if state.emaFast > state.emaSlow && returnPct > 0 {
			bullish++
		} else if state.emaFast < state.emaSlow && returnPct < 0 {
			bearish++
		}

		benchmarks = append(benchmarks, domain.MarketRegimeBenchmark{
			Symbol:            symbol,
			PriceVsVwapPct:    vwapPct,
			EMAFast:           state.emaFast,
			EMASlow:           state.emaSlow,
			ReturnLookbackPct: returnPct,
		})
	}

	regimeLabel, confidence := domain.ClassifyRegime(bullish, bearish, total)

	// Override with HMM regime when enabled and confident
	if t.hmmDetector != nil {
		hmmRegime, hmmConf := t.hmmDetector.CurrentRegime()
		if hmmConf >= t.config.HMMConfidenceMin {
			regimeLabel = hmmRegime
			confidence = hmmConf
		}
	}

	t.runtime.SetMarketRegime(domain.MarketRegimeSnapshot{
		Regime:     regimeLabel,
		Confidence: confidence,
		Benchmarks: benchmarks,
		Timestamp:  time.Now(),
	})
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func firstPositive(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
}
