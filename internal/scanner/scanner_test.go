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
