package scanner

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func TestScannerFiltersMomentumCandidates(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(config.DefaultTradingConfig(), runtimeState)

	engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           4.00,
		Open:            3.85,
		HighOfDay:       4.01,
		GapPercent:      17.8,
		RelativeVolume:  5.9,
		PreMarketVolume: 780_000,
		Volume:          100_000,
		VolumeSpike:     true,
		Timestamp:       time.Now().UTC().Add(-3 * time.Minute),
	})
	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           4.15,
		Open:            3.85,
		HighOfDay:       4.16,
		GapPercent:      18.4,
		RelativeVolume:  6.3,
		PreMarketVolume: 800_000,
		Volume:          260_000,
		VolumeSpike:     true,
		Catalyst:        "FDA update",
		Timestamp:       time.Now().UTC(),
	})
	if !ok {
		t.Fatal("expected tick to pass scanner filters")
	}
	if candidate.Symbol != "APVO" {
		t.Fatalf("unexpected symbol: %s", candidate.Symbol)
	}
	if candidate.OneMinuteReturnPct <= 0 {
		t.Fatalf("expected scanner to compute positive short-term momentum, got %+v", candidate)
	}
	if candidate.VolumeRate <= 1 {
		t.Fatalf("expected scanner to compute accelerating volume, got %+v", candidate)
	}
}

func TestScannerUsesEarlierRejectedTicksForMomentumContext(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(config.DefaultTradingConfig(), runtimeState)
	base := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)

	_, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           11.24,
		Open:            11.10,
		HighOfDay:       11.30,
		GapPercent:      12.3,
		RelativeVolume:  6.0,
		PreMarketVolume: 200_000,
		Volume:          200_000,
		VolumeSpike:     true,
		Timestamp:       base,
	})
	if ok {
		t.Fatal("expected first tick to be rejected on premarket volume")
	}

	_, ok = engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           11.46,
		Open:            11.10,
		HighOfDay:       11.48,
		GapPercent:      14.5,
		RelativeVolume:  6.4,
		PreMarketVolume: 450_000,
		Volume:          450_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(time.Minute),
	})
	if ok {
		t.Fatal("expected second tick to be rejected on premarket volume")
	}

	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           11.70,
		Open:            11.10,
		HighOfDay:       11.72,
		GapPercent:      16.9,
		RelativeVolume:  7.1,
		PreMarketVolume: 850_000,
		Volume:          850_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(2 * time.Minute),
	})
	if !ok {
		t.Fatal("expected first qualifying tick to pass scanner filters")
	}
	if candidate.OneMinuteReturnPct <= 0 {
		t.Fatalf("expected first qualifying candidate to retain one-minute context, got %+v", candidate)
	}
	if candidate.VolumeRate <= 1 {
		t.Fatalf("expected first qualifying candidate to retain volume-rate context, got %+v", candidate)
	}
}

func TestScannerAllowsIntradaySqueezeWithoutPremarketGap(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(config.DefaultTradingConfig(), runtimeState)
	base := time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC)

	engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.10,
		Open:            4.90,
		HighOfDay:       5.12,
		GapPercent:      1.0,
		RelativeVolume:  6.0,
		PreMarketVolume: 0,
		Volume:          300_000,
		VolumeSpike:     true,
		Timestamp:       base,
	})
	engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.35,
		Open:            4.90,
		HighOfDay:       5.36,
		GapPercent:      1.4,
		RelativeVolume:  6.6,
		PreMarketVolume: 0,
		Volume:          520_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(time.Minute),
	})
	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.58,
		Open:            4.90,
		HighOfDay:       5.60,
		GapPercent:      1.8,
		RelativeVolume:  7.2,
		PreMarketVolume: 0,
		Volume:          820_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(2 * time.Minute),
	})
	if !ok {
		t.Fatal("expected intraday squeeze to pass scanner without gap profile")
	}
	if candidate.GapPercent >= config.DefaultTradingConfig().MinGapPercent {
		t.Fatalf("expected test candidate to validate non-gap path, got %+v", candidate)
	}
}

func TestScannerRejectsLowGap(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(config.DefaultTradingConfig(), runtimeState)

	_, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "LCID",
		Price:           2.95,
		HighOfDay:       2.98,
		GapPercent:      3.0,
		RelativeVolume:  6.0,
		PreMarketVolume: 900_000,
		VolumeSpike:     true,
		Timestamp:       time.Now().UTC(),
	})
	if ok {
		t.Fatal("expected low-gap symbol to be rejected")
	}
}

func TestScannerScoreCapsExtremeRelativeVolume(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	engine := NewScanner(cfg, runtimeState)

	base := engine.momentumScore(domain.Tick{
		GapPercent:     12,
		RelativeVolume: cfg.MinRelativeVolume + 15,
	}, 18, 0.5, 1.2, 2.0, 1.8)
	extreme := engine.momentumScore(domain.Tick{
		GapPercent:     12,
		RelativeVolume: 4_000,
	}, 18, 0.5, 1.2, 2.0, 1.8)

	if base != extreme {
		t.Fatalf("expected extreme relative volume to cap at the same score contribution, base=%.2f extreme=%.2f", base, extreme)
	}
}
