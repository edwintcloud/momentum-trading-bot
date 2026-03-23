package portfolio

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

func TestApplyExecution_PartialRoutesToReducePosition(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	ts := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)

	// Open a position of 100 shares
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "TEST",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        10.0,
		Quantity:     100,
		StopPrice:    9.0,
		RiskPerShare: 1.0,
		FilledAt:     ts,
	})

	pos, exists := m.GetPosition("TEST")
	if !exists {
		t.Fatal("position should exist after open")
	}
	if pos.Quantity != 100 {
		t.Fatalf("expected 100 shares, got %d", pos.Quantity)
	}

	// Partial exit of 50 shares using intent="partial"
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "TEST",
		Side:         domain.SideSell,
		Intent:       domain.IntentPartial,
		PositionSide: domain.DirectionLong,
		Price:        12.0,
		Quantity:     50,
		Reason:       "partial-1",
		FilledAt:     ts.Add(5 * time.Minute),
	})

	// Position should still exist with 50 shares remaining
	pos, exists = m.GetPosition("TEST")
	if !exists {
		t.Fatal("position should still exist after partial exit")
	}
	if pos.Quantity != 50 {
		t.Errorf("expected 50 shares remaining after partial exit, got %d", pos.Quantity)
	}

	// Should have 1 closed trade for the partial
	closed := m.GetClosedTrades()
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed trade after partial exit, got %d", len(closed))
	}
	if closed[0].Quantity != 50 {
		t.Errorf("closed trade quantity should be 50, got %d", closed[0].Quantity)
	}
	if closed[0].ExitReason != "partial-1" {
		t.Errorf("expected exit reason 'partial-1', got %q", closed[0].ExitReason)
	}

	// Now close the remaining 50 shares
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "TEST",
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        13.0,
		Quantity:     50,
		Reason:       "trailing-stop",
		FilledAt:     ts.Add(10 * time.Minute),
	})

	// Position should be gone
	_, exists = m.GetPosition("TEST")
	if exists {
		t.Error("position should be closed after full exit")
	}

	// Should have 2 closed trades total
	closed = m.GetClosedTrades()
	if len(closed) != 2 {
		t.Fatalf("expected 2 closed trades, got %d", len(closed))
	}

	// Verify PnL: partial: (12-10)*50 = $100, final: (13-10)*50 = $150
	if closed[0].PnL != 100.0 {
		t.Errorf("partial trade PnL should be 100, got %.2f", closed[0].PnL)
	}
	if closed[1].PnL != 150.0 {
		t.Errorf("final trade PnL should be 150, got %.2f", closed[1].PnL)
	}
}

func TestApplyExecution_CloseIntent_FullyCloses(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	ts := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)

	// Open position
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "ABC",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        5.0,
		Quantity:     200,
		StopPrice:    4.5,
		RiskPerShare: 0.5,
		FilledAt:     ts,
	})

	// Close with intent="close" — should fully close
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "ABC",
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        6.0,
		Quantity:     200,
		Reason:       "profit-target",
		FilledAt:     ts.Add(5 * time.Minute),
	})

	_, exists := m.GetPosition("ABC")
	if exists {
		t.Error("position should be fully closed with intent=close")
	}
}

func TestApplyExecution_PartialExitExceedingQty_FullyCloses(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	ts := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)

	// Open 100 shares
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "XYZ",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        10.0,
		Quantity:     100,
		StopPrice:    9.0,
		RiskPerShare: 1.0,
		FilledAt:     ts,
	})

	// Partial exit requesting 100 shares (equal to position) — ReducePosition
	// delegates to ClosePosition when exitQty >= pos.Quantity
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "XYZ",
		Side:         domain.SideSell,
		Intent:       domain.IntentPartial,
		PositionSide: domain.DirectionLong,
		Price:        11.0,
		Quantity:     100,
		Reason:       "partial-all",
		FilledAt:     ts.Add(5 * time.Minute),
	})

	// Position should be fully closed since partial qty >= position qty
	_, exists := m.GetPosition("XYZ")
	if exists {
		t.Error("position should be closed when partial exit qty equals position qty")
	}
}
