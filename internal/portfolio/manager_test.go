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

func TestPortfolioSyncPositionQuantityReconcilesStaleShareCount(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "EONR",
		Side:     "buy",
		Price:    4,
		Quantity: 473,
		FilledAt: time.Now().UTC().Add(-2 * time.Minute),
	})
	manager.MarkPrice("EONR", 3.5)
	manager.SyncPositionQuantity("EONR", 170)

	position, exists := manager.Position("EONR")
	if !exists {
		t.Fatal("expected reconciled position to remain open")
	}
	if position.Quantity != 170 {
		t.Fatalf("expected quantity 170 after reconciliation, got %d", position.Quantity)
	}
	if position.MarketValue != 595 {
		t.Fatalf("expected market value to update with reconciled quantity, got %.2f", position.MarketValue)
	}

	manager.SyncPositionQuantity("EONR", 0)
	if manager.HasPosition("EONR") {
		t.Fatal("expected position removal when broker quantity reaches zero")
	}
}
