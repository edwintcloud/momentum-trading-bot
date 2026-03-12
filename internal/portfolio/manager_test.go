package portfolio

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func TestPortfolioTracksPositionAndClosedTrade(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "buy",
		Price:    5,
		Quantity: 100,
		FilledAt: time.Now().UTC().Add(-2 * time.Minute),
	})
	manager.MarkPrice("SOUN", 5.8)

	positions := manager.GetPositions()
	if len(positions) != 1 {
		t.Fatalf("expected one position, got %d", len(positions))
	}
	if positions[0].UnrealizedPnL <= 0 {
		t.Fatalf("expected unrealized profit, got %+v", positions[0])
	}

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "sell",
		Price:    5.9,
		Quantity: 100,
		Reason:   "profit-target",
		FilledAt: time.Now().UTC(),
	})

	if len(manager.GetPositions()) != 0 {
		t.Fatal("expected position to be closed")
	}
	if len(manager.GetClosedTrades()) != 1 {
		t.Fatal("expected one closed trade")
	}
	if manager.RealizedPnL() <= 0 {
		t.Fatal("expected positive realized pnl")
	}
}
