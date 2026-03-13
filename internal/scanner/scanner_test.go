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
