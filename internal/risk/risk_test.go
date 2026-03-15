package risk

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func inSessionSignalTime() time.Time {
	return time.Date(2026, 3, 13, 14, 0, 0, 0, time.UTC)
}

func TestRiskBlocksTradingWhenPaused(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	runtimeState.Pause()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	_, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "APVO",
		Side:      "buy",
		Price:     4,
		Quantity:  100,
		Timestamp: at,
	})
	if approved {
		t.Fatal("expected paused trading to block new orders")
	}
	if reason != "trading-paused" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestRiskAllowsExitEvenWhenPaused(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	runtimeState.Pause()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionSignalTime()
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "APVO",
		Side:     "buy",
		Price:    4,
		Quantity: 100,
		FilledAt: at.Add(-time.Minute),
	})
	engine := NewEngine(cfg, book, runtimeState)

	request, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "APVO",
		Side:      "sell",
		Price:     3.9,
		Quantity:  100,
		Reason:    "operator-close-all",
		Timestamp: at,
	})
	if !approved {
		t.Fatalf("expected sell order to be approved, got %s", reason)
	}
	if request.Side != "sell" {
		t.Fatalf("unexpected request: %+v", request)
	}
}

func TestRiskBlocksEntriesWhenBrokerDayPnLExceedsLossLimit(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(93809.87, 100000)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	_, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "EONR",
		Side:      "buy",
		Price:     3.25,
		Quantity:  100,
		Timestamp: at,
	})
	if approved {
		t.Fatal("expected broker day loss to block new entries")
	}
	if reason != "daily-loss-limit" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestRiskUsesEffectiveCapitalForMaxExposure(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50000)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	request, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "EONR",
		Side:      "buy",
		Price:     10,
		Quantity:  2000,
		Timestamp: at,
	})
	if !approved {
		t.Fatalf("expected quantity to be trimmed to fit max exposure, got %s", reason)
	}
	if request.Quantity != 1500 {
		t.Fatalf("expected trimmed quantity of 1500, got %d", request.Quantity)
	}
}

func TestRiskBlocksEntriesWhenNoExposureRemains(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50_000, 50_000)
	at := inSessionSignalTime()
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "EONR",
		Side:     "buy",
		Price:    10,
		Quantity: 1500,
		FilledAt: at.Add(-time.Minute),
	})
	engine := NewEngine(cfg, book, runtimeState)

	_, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "APVO",
		Side:      "buy",
		Price:     10,
		Quantity:  1,
		Timestamp: at,
	})
	if approved {
		t.Fatal("expected max exposure block when no exposure remains")
	}
	if reason != "max-exposure" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestRiskBlocksOrdersOutsideTradableSession(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)

	_, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "APVO",
		Side:      "buy",
		Price:     4,
		Quantity:  100,
		Timestamp: time.Date(2026, 3, 13, 6, 30, 0, 0, time.UTC),
	})
	if approved {
		t.Fatal("expected outside-session order to be blocked")
	}
	if reason != "outside-session" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}
