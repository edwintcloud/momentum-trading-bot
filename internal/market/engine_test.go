package market

import (
	"context"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func TestHandleBarIgnoresSymbolsOutsideEligibleUniverse(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(nil, cfg, book, runtimeState)
	engine.eligibleSymbols = map[string]struct{}{
		"AAPL": {},
	}

	tick := engine.handleBar(context.Background(), alpaca.StreamMessage{
		Type:      "b",
		Symbol:    "SPDN",
		Open:      10.00,
		High:      10.20,
		Low:       9.95,
		Close:     10.10,
		Volume:    1000,
		Timestamp: time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC),
	})
	if tick.Symbol != "" {
		t.Fatalf("expected ineligible symbol bar to be ignored, got %+v", tick)
	}
}
