package strategy

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func TestStrategyCreatesEntrySignal(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:         "HUMA",
		Price:          4.20,
		HighOfDay:      4.21,
		GapPercent:     21,
		RelativeVolume: 6.4,
		Score:          22,
		Timestamp:      time.Now().UTC(),
	})
	if !ok {
		t.Fatal("expected strategy to emit entry signal")
	}
	if signal.Side != "buy" {
		t.Fatalf("unexpected side: %s", signal.Side)
	}
	if signal.Quantity <= 0 {
		t.Fatal("expected positive quantity")
	}
}

func TestStrategyCreatesExitSignalOnStopLoss(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "RKLB",
		Side:     "buy",
		Price:    10,
		Quantity: 100,
		FilledAt: time.Now().UTC().Add(-time.Minute),
	})
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     9.40,
		HighOfDay: 10.50,
		Timestamp: time.Now().UTC(),
	})
	if !ok {
		t.Fatal("expected stop-loss exit")
	}
	if signal.Side != "sell" || signal.Reason != "stop-loss" {
		t.Fatalf("unexpected exit signal: %+v", signal)
	}
}

func TestStrategyUsesEffectiveCapitalForSizing(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50500)
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:         "HUMA",
		Price:          10.00,
		HighOfDay:      10.05,
		GapPercent:     21,
		RelativeVolume: 6.4,
		Score:          22,
		Timestamp:      time.Now().UTC(),
	})
	if !ok {
		t.Fatal("expected strategy to emit entry signal")
	}
	if signal.Quantity != 1000 {
		t.Fatalf("expected quantity 1000 using broker equity sizing, got %d", signal.Quantity)
	}
}
