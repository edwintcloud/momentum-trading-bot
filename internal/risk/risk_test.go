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

func testBuySignal(symbol string, price float64, quantity int64, at time.Time) domain.TradeSignal {
	riskPerShare := price * 0.05
	return domain.TradeSignal{
		Symbol:       symbol,
		Side:         "buy",
		Price:        price,
		Quantity:     quantity,
		StopPrice:    price - riskPerShare,
		RiskPerShare: riskPerShare,
		EntryATR:     price * 0.03,
		SetupType:    "consolidation-breakout",
		Timestamp:    at,
	}
}

func testBuyExecution(symbol string, price float64, quantity int64, at time.Time) domain.ExecutionReport {
	riskPerShare := price * 0.05
	return domain.ExecutionReport{
		Symbol:       symbol,
		Side:         "buy",
		Price:        price,
		Quantity:     quantity,
		StopPrice:    price - riskPerShare,
		RiskPerShare: riskPerShare,
		EntryATR:     price * 0.03,
		SetupType:    "consolidation-breakout",
		FilledAt:     at,
	}
}

func TestRiskBlocksTradingWhenPaused(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	runtimeState.Pause()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	_, approved, reason := engine.Evaluate(testBuySignal("APVO", 4, 100, at))
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
	book.ApplyExecution(testBuyExecution("APVO", 4, 100, at.Add(-time.Minute)))
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

	_, approved, reason := engine.Evaluate(testBuySignal("EONR", 3.25, 100, at))
	if approved {
		t.Fatal("expected broker day loss to block new entries")
	}
	if reason != "daily-loss-limit" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestRiskUsesEffectiveCapitalForMaxExposure(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.LimitOrderSlippageDollars = 0.05
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50000)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	request, approved, reason := engine.Evaluate(testBuySignal("EONR", 10, 2000, at))
	if !approved {
		t.Fatalf("expected quantity to be trimmed to fit max exposure, got %s", reason)
	}
	if request.Quantity != 1494 {
		t.Fatalf("expected trimmed quantity of 1494 using buffered entry price, got %d", request.Quantity)
	}
}

func TestRiskBlocksEntriesWhenNoExposureRemains(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50_000, 50_000)
	at := inSessionSignalTime()
	book.ApplyExecution(testBuyExecution("EONR", 10, 1500, at.Add(-time.Minute)))
	engine := NewEngine(cfg, book, runtimeState)

	_, approved, reason := engine.Evaluate(testBuySignal("APVO", 10, 1, at))
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

	_, approved, reason := engine.Evaluate(testBuySignal("APVO", 4, 100, time.Date(2026, 3, 13, 6, 30, 0, 0, time.UTC)))
	if approved {
		t.Fatal("expected outside-session order to be blocked")
	}
	if reason != "outside-session" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestRiskMaxTradesCountsEntriesNotExits(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MaxTradesPerDay = 1
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	book.ApplyExecution(testBuyExecution("APVO", 4, 100, at.Add(-2*time.Minute)))

	_, approved, reason := engine.Evaluate(testBuySignal("EONR", 3.25, 100, at))
	if approved {
		t.Fatal("expected second entry of the day to be blocked")
	}
	if reason != "max-trades" {
		t.Fatalf("unexpected block reason: %s", reason)
	}

	request, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "APVO",
		Side:      "sell",
		Price:     4.1,
		Quantity:  100,
		Reason:    "test-exit",
		Timestamp: at,
	})
	if !approved {
		t.Fatalf("expected exit to remain allowed after entry cap, got %s", reason)
	}
	if request.Side != "sell" || request.Quantity != 100 {
		t.Fatalf("unexpected exit request: %+v", request)
	}
}

func TestRiskUsesAdaptiveBufferForLowPricedBuy(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.LimitOrderSlippageDollars = 0.05
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	request, approved, reason := engine.Evaluate(testBuySignal("BIAF", 1.88, 500, at))
	if !approved {
		t.Fatalf("expected low-priced buy to be approved, got %s", reason)
	}
	if request.Price != 1.89 {
		t.Fatalf("expected adaptive buy limit of 1.89, got %.2f", request.Price)
	}
}

func TestRiskUsesAdaptiveBufferForLowPricedSell(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.LimitOrderSlippageDollars = 0.05
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionSignalTime()
	book.ApplyExecution(testBuyExecution("BIAF", 1.88, 100, at.Add(-time.Minute)))
	engine := NewEngine(cfg, book, runtimeState)

	request, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:    "BIAF",
		Side:      "sell",
		Price:     1.76,
		Quantity:  100,
		Reason:    "break-even-stop",
		Timestamp: at,
	})
	if !approved {
		t.Fatalf("expected low-priced sell to be approved, got %s", reason)
	}
	if request.Price != 1.75 {
		t.Fatalf("expected adaptive sell limit of 1.75, got %.2f", request.Price)
	}
}

func TestRiskCapsAdaptiveBufferForHigherPricedNames(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.LimitOrderSlippageDollars = 0.05
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	request, approved, reason := engine.Evaluate(testBuySignal("KBDU", 27.04, 100, at))
	if !approved {
		t.Fatalf("expected higher-priced buy to be approved, got %s", reason)
	}
	if request.Price != 27.09 {
		t.Fatalf("expected capped buy limit of 27.09, got %.2f", request.Price)
	}
}
