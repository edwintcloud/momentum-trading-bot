package risk

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func testConfig() config.TradingConfig {
	return config.TradingConfig{
		StartingCapital:           100_000,
		EnableShorts:              false,
		RiskPerTradePct:           0.01,
		DailyLossLimitPct:         0.03,
		MaxTradesPerDay:           8,
		MaxOpenPositions:          4,
		MaxExposurePct:            0.30,
		MaxShortOpenPositions:     1,
		MaxShortExposurePct:       0.15,
		ShortMinEntryScore:        20,
		EntryStopATRMultiplier:    1.00,
		ShortStopATRMultiplier:    1.25,
		MaxRiskATRMultiplier:      4.00,
		LimitOrderSlippageDollars: 0.10,
	}
}

type stubShortableChecker map[string]bool

func (s stubShortableChecker) IsShortable(symbol string) bool {
	return s[symbol]
}

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
	cfg := testConfig()
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
	cfg := testConfig()
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
	cfg := testConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(93809.87, 100000)
	book.SyncBrokerCash(93809.87)
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

func TestRiskUsesCashValueForMaxExposure(t *testing.T) {
	cfg := testConfig()
	cfg.LimitOrderSlippageDollars = 0.05
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50000)
	book.SyncBrokerCash(50000)
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
	cfg := testConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50_000, 50_000)
	book.SyncBrokerCash(50_000)
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

func TestRiskTrimsEntriesToAvailableCash(t *testing.T) {
	cfg := testConfig()
	cfg.MaxExposurePct = 1.0
	cfg.LimitOrderSlippageDollars = 0.05
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50_000, 50_000)
	book.SyncBrokerCash(5_000)
	at := inSessionSignalTime()
	book.SeedPosition(domain.Position{
		Symbol:      "SEEDED",
		Quantity:    4_500,
		AvgPrice:    10,
		LastPrice:   10,
		MarketValue: 45_000,
		OpenedAt:    at.Add(-time.Hour),
		UpdatedAt:   at.Add(-time.Hour),
	})
	engine := NewEngine(cfg, book, runtimeState)

	request, approved, reason := engine.Evaluate(testBuySignal("APVO", 10, 2000, at))
	if !approved {
		t.Fatalf("expected order to be trimmed to remaining cash, got %s", reason)
	}
	if request.Quantity != 498 {
		t.Fatalf("expected quantity capped by $5,000 available cash, got %d", request.Quantity)
	}
}

func TestRiskBlocksOrdersOutsideTradableSession(t *testing.T) {
	cfg := testConfig()
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
	cfg := testConfig()
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

func TestRiskMaxTradesCountsApprovedEntriesBeforeFills(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTradesPerDay = 1
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState)
	at := inSessionSignalTime()

	_, approved, reason := engine.Evaluate(testBuySignal("TZA", 7.33, 100, at))
	if !approved {
		t.Fatalf("expected first entry approval, got %s", reason)
	}

	_, approved, reason = engine.Evaluate(testBuySignal("UVIX", 18.20, 100, at.Add(2*time.Second)))
	if approved {
		t.Fatal("expected second approved entry to be blocked before fills")
	}
	if reason != "max-trades" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestRiskUsesAdaptiveBufferForLowPricedBuy(t *testing.T) {
	cfg := testConfig()
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
	cfg := testConfig()
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
	cfg := testConfig()
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

func TestRiskBlocksShortEntriesWhenSymbolIsNotShortable(t *testing.T) {
	cfg := testConfig()
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	engine := NewEngine(cfg, book, runtimeState, stubShortableChecker{"IBIO": false})
	at := inSessionSignalTime()

	_, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:       "IBIO",
		Side:         domain.SideSell,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionShort,
		Price:        8.40,
		Quantity:     100,
		StopPrice:    8.95,
		RiskPerShare: 0.55,
		EntryATR:     0.32,
		SetupType:    "parabolic-failed-reclaim-short",
		Reason:       "short-test",
		Timestamp:    at,
	})
	if approved {
		t.Fatal("expected non-shortable symbol to be blocked")
	}
	if reason != "symbol-not-shortable" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestRiskApprovesShortEntriesWithinDedicatedCaps(t *testing.T) {
	cfg := testConfig()
	cfg.EnableShorts = true
	cfg.MaxShortOpenPositions = 2
	cfg.MaxShortExposurePct = 0.20
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerCash(50_000)
	engine := NewEngine(cfg, book, runtimeState, stubShortableChecker{"IBIO": true})
	at := inSessionSignalTime()

	request, approved, reason := engine.Evaluate(domain.TradeSignal{
		Symbol:       "IBIO",
		Side:         domain.SideSell,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionShort,
		Price:        8.40,
		Quantity:     1000,
		StopPrice:    8.95,
		RiskPerShare: 0.55,
		EntryATR:     0.32,
		SetupType:    "parabolic-failed-reclaim-short",
		Reason:       "short-test",
		Timestamp:    at,
	})
	if !approved {
		t.Fatalf("expected short entry approval, got %s", reason)
	}
	if request.Side != domain.SideSell || request.Intent != domain.IntentOpen || request.PositionSide != domain.DirectionShort {
		t.Fatalf("unexpected short request: %+v", request)
	}
}
