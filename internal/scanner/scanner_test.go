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

	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           4.15,
		HighOfDay:       4.16,
		GapPercent:      18.4,
		RelativeVolume:  6.3,
		PreMarketVolume: 800_000,
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
