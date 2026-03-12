package risk

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func TestRiskBlocksTradingWhenPaused(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	runtimeState.Pause()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)

	_, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "APVO",
		Side:      "buy",
		Price:     4,
		Quantity:  100,
		Timestamp: time.Now().UTC(),
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
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "APVO",
		Side:     "buy",
		Price:    4,
		Quantity: 100,
		FilledAt: time.Now().UTC().Add(-time.Minute),
	})
	engine := NewEngine(cfg, book, runtimeState)

	request, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "APVO",
		Side:      "sell",
		Price:     3.9,
		Quantity:  100,
		Reason:    "operator-close-all",
		Timestamp: time.Now().UTC(),
	})
	if !approved {
		t.Fatalf("expected sell order to be approved, got %s", reason)
	}
	if request.Side != "sell" {
		t.Fatalf("unexpected request: %+v", request)
	}
}
