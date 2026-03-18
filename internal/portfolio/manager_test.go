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

func TestStatusSnapshotIncludesBrokerDayPnL(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)

	manager.SyncBrokerAccount(93809.87, 100000)
	status := manager.StatusSnapshot()

	if status.BrokerEquity != 93809.87 {
		t.Fatalf("expected broker equity 93809.87, got %.2f", status.BrokerEquity)
	}
	if status.DayPnL != -6190.13 {
		t.Fatalf("expected day pnl -6190.13, got %.2f", status.DayPnL)
	}
}

func TestStatusSnapshotUsesActualTradeCount(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)
	base := time.Date(2026, time.March, 16, 13, 0, 0, 0, time.UTC)

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "buy",
		Price:    5,
		Quantity: 100,
		FilledAt: base,
	})
	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "sell",
		Price:    5.1,
		Quantity: 100,
		Reason:   "profit-target",
		FilledAt: base.Add(time.Minute),
	})

	status := manager.StatusSnapshot()
	if status.TradesToday != 2 {
		t.Fatalf("expected status trades today to count both fills, got %d", status.TradesToday)
	}
	if manager.EntriesToday() != 1 {
		t.Fatalf("expected entries today to remain entry-only, got %d", manager.EntriesToday())
	}
	if manager.TradesToday() != 2 {
		t.Fatalf("expected internal trade counter to track both fills, got %d", manager.TradesToday())
	}
}

func TestStatusSnapshotPrefersBrokerTradeCountWhenAvailable(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)
	base := time.Date(2026, time.March, 16, 13, 0, 0, 0, time.UTC)

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "buy",
		Price:    5,
		Quantity: 100,
		FilledAt: base,
	})
	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "sell",
		Price:    5.1,
		Quantity: 100,
		Reason:   "profit-target",
		FilledAt: base.Add(time.Minute),
	})
	manager.SyncBrokerTradesToday(7)

	status := manager.StatusSnapshot()
	if status.TradesToday != 7 {
		t.Fatalf("expected status trades today to prefer broker count, got %d", status.TradesToday)
	}
	if manager.TradesToday() != 2 {
		t.Fatalf("expected local trade counter to remain available for internal bookkeeping, got %d", manager.TradesToday())
	}
}

func TestStatusSnapshotIncludesOptimizerMetadata(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	runtimeState.SetOptimizerStatus(runtime.OptimizerStatus{
		ActiveProfileName:         "baseline_breakout",
		ActiveProfileVersion:      "built-in",
		PendingProfileName:        "high_conviction_breakout",
		PendingProfileVersion:     "20260320-high-conviction_breakout",
		LastOptimizerRun:          time.Date(2026, time.March, 20, 21, 0, 0, 0, time.UTC),
		LastPaperValidationResult: "pending-paper-validation",
	})
	manager := NewManager(cfg, runtimeState)

	status := manager.StatusSnapshot()
	if status.ActiveProfile != "baseline_breakout" || status.ActiveVersion != "built-in" {
		t.Fatalf("expected active optimizer metadata in status, got %+v", status)
	}
	if status.PendingProfile != "high_conviction_breakout" || status.PendingVersion == "" {
		t.Fatalf("expected pending optimizer metadata in status, got %+v", status)
	}
	if status.PaperValidation != "pending-paper-validation" {
		t.Fatalf("expected paper validation status, got %+v", status)
	}
}

func TestStatusSnapshotLockedCompletesWithQueuedWriter(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)
	manager.SyncBrokerAccount(250000, 249000)

	readerHolding := make(chan struct{})
	proceed := make(chan struct{})
	result := make(chan domain.StatusSnapshot, 1)

	go func() {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		close(readerHolding)
		<-proceed
		result <- manager.statusSnapshotLocked()
	}()

	<-readerHolding

	writerAttempting := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		close(writerAttempting)
		manager.mu.Lock()
		manager.startingCapital = 123456
		manager.mu.Unlock()
		close(writerDone)
	}()

	<-writerAttempting
	close(proceed)

	select {
	case status := <-result:
		if status.BrokerEquity != 250000 {
			t.Fatalf("expected broker equity in snapshot, got %+v", status)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("status snapshot blocked with queued writer")
	}

	select {
	case <-writerDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("writer did not complete after status snapshot released its read lock")
	}
}

func TestPortfolioResetsDailyTradeCounterByTradingDay(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)
	firstDay := time.Date(2026, time.March, 10, 15, 30, 0, 0, time.UTC)

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "buy",
		Price:    5,
		Quantity: 100,
		FilledAt: firstDay,
	})
	if manager.TradesToday() != 1 {
		t.Fatalf("expected one trade on first day, got %d", manager.TradesToday())
	}
	if manager.EntriesToday() != 1 {
		t.Fatalf("expected one entry on first day, got %d", manager.EntriesToday())
	}

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "SOUN",
		Side:     "sell",
		Price:    5.1,
		Quantity: 100,
		Reason:   "profit-target",
		FilledAt: firstDay.Add(10 * time.Minute),
	})
	if manager.TradesToday() != 2 {
		t.Fatalf("expected two fills on first day, got %d", manager.TradesToday())
	}
	if manager.EntriesToday() != 1 {
		t.Fatalf("expected entries to remain entry-only after exit, got %d", manager.EntriesToday())
	}

	manager.MarkPriceAt("SOUN", 5.2, firstDay.Add(20*time.Hour))
	if manager.TradesToday() != 0 {
		t.Fatalf("expected fill counter reset on next trading day, got %d", manager.TradesToday())
	}
	if manager.EntriesToday() != 0 {
		t.Fatalf("expected entry counter reset on next trading day, got %d", manager.EntriesToday())
	}
}

func TestPortfolioCountsPartialBuyFillsAsSingleEntry(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)
	base := time.Date(2026, time.March, 16, 13, 0, 0, 0, time.UTC)

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "TZA",
		Side:     "buy",
		Price:    7.33,
		Quantity: 700,
		FilledAt: base,
	})
	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:   "TZA",
		Side:     "buy",
		Price:    7.33,
		Quantity: 437,
		FilledAt: base.Add(1 * time.Second),
	})

	if manager.EntriesToday() != 1 {
		t.Fatalf("expected one entry for partial buy fills, got %d", manager.EntriesToday())
	}
	position, ok := manager.Position("TZA")
	if !ok {
		t.Fatal("expected open position")
	}
	if position.Quantity != 1137 {
		t.Fatalf("expected aggregated quantity 1137, got %d", position.Quantity)
	}
}

func TestPortfolioMergesPartialSellFillsIntoSingleClosedTrade(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	manager := NewManager(cfg, runtimeState)
	base := time.Date(2026, time.March, 16, 13, 0, 0, 0, time.UTC)

	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:       "PRSO",
		Side:         "buy",
		Price:        1.77,
		Quantity:     3566,
		StopPrice:    1.73,
		RiskPerShare: 0.04,
		EntryATR:     0.03,
		SetupType:    "consolidation-breakout",
		FilledAt:     base,
	})
	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:       "PRSO",
		Side:         "sell",
		Price:        1.81,
		Quantity:     3459,
		Reason:       "trailing-stop",
		RiskPerShare: 0.04,
		FilledAt:     base.Add(10 * time.Minute),
	})
	manager.ApplyExecution(domain.ExecutionReport{
		Symbol:       "PRSO",
		Side:         "sell",
		Price:        1.81,
		Quantity:     107,
		Reason:       "trailing-stop",
		RiskPerShare: 0.04,
		FilledAt:     base.Add(10*time.Minute + 1*time.Second),
	})

	closed := manager.GetClosedTrades()
	if len(closed) != 1 {
		t.Fatalf("expected one merged closed trade row, got %d", len(closed))
	}
	if closed[0].Quantity != 3566 {
		t.Fatalf("expected merged close quantity 3566, got %d", closed[0].Quantity)
	}
	if closed[0].ExitReason != "trailing-stop" {
		t.Fatalf("unexpected exit reason: %s", closed[0].ExitReason)
	}
	if manager.HasPosition("PRSO") {
		t.Fatal("expected PRSO position to be fully closed")
	}
}
