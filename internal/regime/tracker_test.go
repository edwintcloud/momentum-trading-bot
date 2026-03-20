package regime

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func TestTrackerClassifiesBullishTape(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	tracker := NewTracker(cfg, runtimeState)
	base := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)

	for index := 0; index < 35; index++ {
		price := 100.0 + float64(index)*0.2
		for _, symbol := range cfg.MarketRegimeBenchmarkSymbols {
			tracker.UpdateTick(domain.Tick{
				Symbol:    symbol,
				Price:     price,
				BarOpen:   price - 0.1,
				BarHigh:   price + 0.1,
				BarLow:    price - 0.2,
				Volume:    int64((index + 1) * 1000),
				Timestamp: base.Add(time.Duration(index) * time.Minute),
			})
		}
	}

	snapshot := runtimeState.MarketRegime()
	if snapshot.Regime != domain.MarketRegimeBullish {
		t.Fatalf("expected bullish regime, got %+v", snapshot)
	}
	if len(snapshot.Benchmarks) != 3 {
		t.Fatalf("expected benchmark readings, got %+v", snapshot)
	}
}

func TestTrackerClassifiesRangingTapeWhenSignalsConflict(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	tracker := NewTracker(cfg, runtimeState)
	base := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)

	for index := 0; index < 35; index++ {
		prices := map[string]float64{
			"SPY": 100 + float64(index)*0.2,
			"QQQ": 100 - float64(index)*0.15,
			"IWM": 100 + []float64{0.1, -0.1, 0.05, -0.05}[index%4],
		}
		for _, symbol := range cfg.MarketRegimeBenchmarkSymbols {
			price := prices[symbol]
			tracker.UpdateTick(domain.Tick{
				Symbol:    symbol,
				Price:     price,
				BarOpen:   price,
				BarHigh:   price + 0.1,
				BarLow:    price - 0.1,
				Volume:    int64((index + 1) * 1000),
				Timestamp: base.Add(time.Duration(index) * time.Minute),
			})
		}
	}

	snapshot := runtimeState.MarketRegime()
	if snapshot.Regime != domain.MarketRegimeRanging {
		t.Fatalf("expected ranging regime, got %+v", snapshot)
	}
}
