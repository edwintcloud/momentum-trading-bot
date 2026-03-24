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

func TestSeedClosedTrades_RestoresTradesAndPnL(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	trades := []domain.ClosedTrade{
		{
			Symbol:           "AAPL",
			Side:             "long",
			Quantity:         100,
			EntryPrice:       150.0,
			ExitPrice:        155.0,
			PnL:              500.0,
			RMultiple:        2.0,
			SetupType:        "breakout",
			ExitReason:       "profit-target",
			MarketRegime:     "trending",
			RegimeConfidence: 0.85,
			Playbook:         "breakout",
			Sector:           "Technology",
			OpenedAt:         time.Date(2026, 3, 23, 9, 35, 0, 0, time.UTC),
			ClosedAt:         time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC),
		},
		{
			Symbol:     "TSLA",
			Side:       "long",
			Quantity:   50,
			EntryPrice: 200.0,
			ExitPrice:  195.0,
			PnL:        -250.0,
			RMultiple:  -1.0,
			ExitReason: "stop-loss",
			OpenedAt:   time.Date(2026, 3, 23, 9, 40, 0, 0, time.UTC),
			ClosedAt:   time.Date(2026, 3, 23, 10, 5, 0, 0, time.UTC),
		},
	}

	m.SeedClosedTrades(trades)

	// Verify trades are returned by GetClosedTrades
	got := m.GetClosedTrades()
	if len(got) != 2 {
		t.Fatalf("expected 2 closed trades, got %d", len(got))
	}
	if got[0].Symbol != "AAPL" {
		t.Errorf("expected first trade AAPL, got %s", got[0].Symbol)
	}
	if got[1].Symbol != "TSLA" {
		t.Errorf("expected second trade TSLA, got %s", got[1].Symbol)
	}

	// Verify day PnL is computed correctly: 500 + (-250) = 250
	if m.DayPnL() != 250.0 {
		t.Errorf("expected dayPnL 250.0, got %.2f", m.DayPnL())
	}

	// Verify RealizedPnL matches
	if m.RealizedPnL() != 250.0 {
		t.Errorf("expected RealizedPnL 250.0, got %.2f", m.RealizedPnL())
	}
}

func TestSeedClosedTrades_EmptySlice(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	m.SeedClosedTrades(nil)

	got := m.GetClosedTrades()
	if len(got) != 0 {
		t.Fatalf("expected 0 closed trades after seeding nil, got %d", len(got))
	}
	if m.DayPnL() != 0 {
		t.Errorf("expected dayPnL 0, got %.2f", m.DayPnL())
	}
}

func TestSeedClosedTrades_ThenNewTradeAppends(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	// Seed with one historical trade
	m.SeedClosedTrades([]domain.ClosedTrade{
		{
			Symbol:     "AAPL",
			Side:       "long",
			Quantity:   100,
			EntryPrice: 150.0,
			ExitPrice:  155.0,
			PnL:        500.0,
			OpenedAt:   time.Date(2026, 3, 23, 9, 35, 0, 0, time.UTC),
			ClosedAt:   time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC),
		},
	})

	ts := time.Date(2026, 3, 23, 10, 30, 0, 0, time.UTC)

	// Open and close a new position
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "NVDA",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        300.0,
		Quantity:     50,
		StopPrice:    290.0,
		RiskPerShare: 10.0,
		FilledAt:     ts,
	})
	m.ApplyExecution(domain.ExecutionReport{
		Symbol:       "NVDA",
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        310.0,
		Quantity:     50,
		Reason:       "trailing-stop",
		FilledAt:     ts.Add(5 * time.Minute),
	})

	got := m.GetClosedTrades()
	if len(got) != 2 {
		t.Fatalf("expected 2 closed trades (1 seeded + 1 new), got %d", len(got))
	}
	if got[0].Symbol != "AAPL" {
		t.Errorf("expected first trade AAPL (seeded), got %s", got[0].Symbol)
	}
	if got[1].Symbol != "NVDA" {
		t.Errorf("expected second trade NVDA (new), got %s", got[1].Symbol)
	}

	// Day PnL: 500 (seeded) + 500 (new: (310-300)*50) = 1000
	if m.DayPnL() != 1000.0 {
		t.Errorf("expected dayPnL 1000.0, got %.2f", m.DayPnL())
	}
}

func TestUpdateSeededPositionRisk_SetsAllFields(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	// Seed a position with zero risk metadata (like broker seeding does)
	m.SeedBrokerPosition(domain.Position{
		Symbol:   "SEED",
		Side:     domain.DirectionLong,
		Quantity: 200,
		AvgPrice: 8.35,
	})

	m.UpdateSeededPositionRisk("SEED", 8.10, 0.125, 200)

	pos, ok := m.GetPosition("SEED")
	if !ok {
		t.Fatal("position should exist")
	}
	if pos.StopPrice != 8.10 {
		t.Errorf("StopPrice = %.2f, want 8.10", pos.StopPrice)
	}
	if pos.InitialStopPrice != 8.10 {
		t.Errorf("InitialStopPrice = %.2f, want 8.10", pos.InitialStopPrice)
	}
	if pos.RiskPerShare != 0.125 {
		t.Errorf("RiskPerShare = %.4f, want 0.125", pos.RiskPerShare)
	}
	if pos.OriginalQuantity != 200 {
		t.Errorf("OriginalQuantity = %d, want 200", pos.OriginalQuantity)
	}
	if pos.EntryATR != 0.125 {
		t.Errorf("EntryATR = %.4f, want 0.125 (ATR proxy)", pos.EntryATR)
	}
	if pos.Playbook != "breakout" {
		t.Errorf("Playbook = %q, want breakout", pos.Playbook)
	}
}

func TestUpdateSeededPositionRisk_DoesNotOverwriteNonZero(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	// Seed a position that already has some risk metadata
	m.SeedBrokerPosition(domain.Position{
		Symbol:           "EXISTING",
		Side:             domain.DirectionLong,
		Quantity:         100,
		AvgPrice:         10.00,
		StopPrice:        9.50,
		RiskPerShare:     0.50,
		OriginalQuantity: 100,
		EntryATR:         0.40,
		Playbook:         "pullback",
	})

	// Try to overwrite with different values
	m.UpdateSeededPositionRisk("EXISTING", 9.00, 1.00, 200)

	pos, ok := m.GetPosition("EXISTING")
	if !ok {
		t.Fatal("position should exist")
	}
	// All values should remain unchanged since they were non-zero
	if pos.StopPrice != 9.50 {
		t.Errorf("StopPrice = %.2f, want 9.50 (preserved)", pos.StopPrice)
	}
	if pos.RiskPerShare != 0.50 {
		t.Errorf("RiskPerShare = %.2f, want 0.50 (preserved)", pos.RiskPerShare)
	}
	if pos.OriginalQuantity != 100 {
		t.Errorf("OriginalQuantity = %d, want 100 (preserved)", pos.OriginalQuantity)
	}
	if pos.EntryATR != 0.40 {
		t.Errorf("EntryATR = %.2f, want 0.40 (preserved)", pos.EntryATR)
	}
	if pos.Playbook != "pullback" {
		t.Errorf("Playbook = %q, want pullback (preserved)", pos.Playbook)
	}
}

func TestUpdateSeededPositionRisk_FallbackStopComputation(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	// Seed a position at $8.35 with no risk info
	m.SeedBrokerPosition(domain.Position{
		Symbol:   "FALLBACK",
		Side:     domain.DirectionLong,
		Quantity: 100,
		AvgPrice: 8.35,
	})

	// Simulate the fallback computation: 1.5% fallback → risk = $8.35 * 0.015 = $0.12525
	// stop = $8.35 - $0.12525 * 1.5 (ATR multiplier) = $8.35 - $0.1879 ≈ $8.16
	riskPct := 0.015
	riskPerShare := 8.35 * riskPct
	stopPrice := 8.35 - riskPerShare*1.5

	m.UpdateSeededPositionRisk("FALLBACK", stopPrice, riskPerShare, 100)

	pos, ok := m.GetPosition("FALLBACK")
	if !ok {
		t.Fatal("position should exist")
	}
	if pos.StopPrice < 8.15 || pos.StopPrice > 8.17 {
		t.Errorf("StopPrice = %.4f, want ~8.16", pos.StopPrice)
	}
	if pos.RiskPerShare < 0.12 || pos.RiskPerShare > 0.13 {
		t.Errorf("RiskPerShare = %.4f, want ~0.125", pos.RiskPerShare)
	}
}

func TestUpdateSeededPositionRisk_ShortPosition(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	m.SeedBrokerPosition(domain.Position{
		Symbol:   "SHORT",
		Side:     domain.DirectionShort,
		Quantity: 100,
		AvgPrice: 50.00,
	})

	// Short stop should be above entry: stop = 50 + risk
	stopPrice := 51.00
	riskPerShare := 1.00
	m.UpdateSeededPositionRisk("SHORT", stopPrice, riskPerShare, 100)

	pos, ok := m.GetPosition("SHORT")
	if !ok {
		t.Fatal("position should exist")
	}
	if pos.StopPrice != 51.00 {
		t.Errorf("StopPrice = %.2f, want 51.00", pos.StopPrice)
	}
}

func TestUpdateSeededPositionRisk_NonExistentSymbol(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	m := NewManager(cfg)

	// Should not panic on non-existent symbol
	m.UpdateSeededPositionRisk("NONE", 10.0, 1.0, 100)
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

func TestRemoveStalePosition(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	ts := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)

	// Open a long position
	m.OpenPosition(domain.ExecutionReport{
		Symbol:       "STALE",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        50.0,
		Quantity:     100,
		StopPrice:    48.0,
		RiskPerShare: 2.0,
		FilledAt:     ts,
	})

	// Simulate price update
	m.UpdatePrice("STALE", 52.0)

	// Remove stale position (broker no longer has it)
	m.RemoveStalePosition("STALE")

	// Position should be gone
	if m.HasPosition("STALE") {
		t.Fatal("position should be removed after RemoveStalePosition")
	}

	// Should have a closed trade recorded
	trades := m.GetClosedTrades()
	if len(trades) != 1 {
		t.Fatalf("expected 1 closed trade, got %d", len(trades))
	}

	trade := trades[0]
	if trade.Symbol != "STALE" {
		t.Errorf("expected trade for STALE, got %s", trade.Symbol)
	}
	if trade.ExitReason != "reconcile-stale" {
		t.Errorf("expected exit reason reconcile-stale, got %s", trade.ExitReason)
	}
	// ExitPrice should be LastPrice (52.0), PnL = (52-50)*100 = 200
	if trade.ExitPrice != 52.0 {
		t.Errorf("expected exit price 52.0, got %.2f", trade.ExitPrice)
	}
	if trade.PnL != 200.0 {
		t.Errorf("expected PnL 200.0, got %.2f", trade.PnL)
	}
}

func TestRemoveStalePosition_Nonexistent(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	m := NewManager(cfg)

	// Should not panic when removing a position that doesn't exist
	m.RemoveStalePosition("NOPE")
}
