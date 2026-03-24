package strategy

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

func openPosition(pm *portfolio.Manager, symbol string, side string, price, stopPrice float64, ts time.Time) {
	brokerSide := domain.SideBuy
	if domain.IsShort(side) {
		brokerSide = domain.SideSell
	}
	pm.OpenPosition(domain.ExecutionReport{
		Symbol:       symbol,
		Side:         brokerSide,
		Intent:       domain.IntentOpen,
		PositionSide: side,
		Price:        price,
		Quantity:     100,
		StopPrice:    stopPrice,
		RiskPerShare: 1.0,
		EntryATR:     1.0,
		SetupType:    "breakout",
		Playbook:     "breakout",
		FilledAt:     ts,
	})
}

// TestStopLossExitUsesLimitOrder verifies stop-loss exits use limit orders (not market).
// The execution engine handles fill aggressiveness via widening slippage retries.
func TestStopLossExitUsesLimitOrder(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false

	rt := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, rt)

	loc := markethours.Location()
	ts := time.Date(2026, 3, 23, 10, 30, 0, 0, loc) // Monday 10:30 AM ET

	openPosition(pm, "STOP", domain.DirectionLong, 50.0, 48.0, ts)

	// Tick below stop price
	tick := domain.Tick{
		Symbol:    "STOP",
		Price:     47.50,
		BarOpen:   49.0,
		BarHigh:   49.5,
		BarLow:    47.0,
		Open:      50.0,
		HighOfDay: 51.0,
		Volume:    100000,
		Timestamp: ts.Add(5 * time.Minute),
	}

	signal, ok := s.EvaluateExit(tick)
	if !ok {
		t.Fatal("expected stop-loss exit signal")
	}
	if signal.Reason != "stop-loss" {
		t.Fatalf("expected reason stop-loss, got %q", signal.Reason)
	}
	if signal.OrderType == "market" {
		t.Error("stop-loss exit should not use market order — widening slippage handles fill aggressiveness")
	}
}

// TestStopLossFallbackExitUsesLimitOrder verifies fallback stops use limit orders.
func TestStopLossFallbackExitUsesLimitOrder(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false

	rt := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, rt)

	loc := markethours.Location()
	ts := time.Date(2026, 3, 23, 10, 30, 0, 0, loc)

	// Open position with zero stop (triggers fallback)
	pm.OpenPosition(domain.ExecutionReport{
		Symbol:       "FALLBACK",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        50.0,
		Quantity:     100,
		StopPrice:    0, // no stop set
		RiskPerShare: 0,
		EntryATR:     0,
		SetupType:    "breakout",
		Playbook:     "breakout",
		FilledAt:     ts,
	})

	// Tick well below entry (should trigger fallback stop)
	tick := domain.Tick{
		Symbol:    "FALLBACK",
		Price:     47.0, // 6% below entry, well below 2% fallback
		BarOpen:   49.0,
		BarHigh:   49.5,
		BarLow:    46.5,
		Open:      50.0,
		HighOfDay: 51.0,
		Volume:    100000,
		Timestamp: ts.Add(5 * time.Minute),
	}

	signal, ok := s.EvaluateExit(tick)
	if !ok {
		t.Fatal("expected stop-loss-fallback exit signal")
	}
	if signal.Reason != "stop-loss-fallback" {
		t.Fatalf("expected reason stop-loss-fallback, got %q", signal.Reason)
	}
	if signal.OrderType == "market" {
		t.Error("stop-loss-fallback exit should not use market order — widening slippage handles fill aggressiveness")
	}
}

// TestEndOfDayExitUsesLimitOrder verifies end-of-day exits use limit orders.
func TestEndOfDayExitUsesLimitOrder(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false
	cfg.PartialExitsEnabled = false

	rt := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, rt)

	loc := markethours.Location()
	entryTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	openPosition(pm, "EOD", domain.DirectionLong, 50.0, 48.0, entryTime)

	// Tick at 3:46 PM — after the 15-minute-before-close cutoff
	eodTime := time.Date(2026, 3, 23, 15, 46, 0, 0, loc)
	tick := domain.Tick{
		Symbol:    "EOD",
		Price:     51.0,
		BarOpen:   50.5,
		BarHigh:   51.5,
		BarLow:    50.0,
		Open:      50.0,
		HighOfDay: 51.5,
		Volume:    100000,
		Timestamp: eodTime,
	}

	signal, ok := s.EvaluateExit(tick)
	if !ok {
		t.Fatal("expected end-of-day exit signal")
	}
	if signal.Reason != "end-of-day" {
		t.Fatalf("expected reason end-of-day, got %q", signal.Reason)
	}
	if signal.OrderType == "market" {
		t.Error("end-of-day exit should not use market order — widening slippage handles fill aggressiveness")
	}
}

// TestFailedBreakoutExitUsesLimitOrder verifies failed breakout exits use limit orders.
func TestFailedBreakoutExitUsesLimitOrder(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false

	rt := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, rt)

	loc := markethours.Location()
	entryTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)

	openPosition(pm, "FAIL", domain.DirectionLong, 50.0, 48.0, entryTime)

	// Tick shortly after entry with price at negative R (failed breakout)
	failTime := entryTime.Add(2 * time.Minute) // within breakout failure window
	tick := domain.Tick{
		Symbol:    "FAIL",
		Price:     49.40, // below -0.5R threshold
		BarOpen:   49.8,
		BarHigh:   50.0,
		BarLow:    49.3,
		Open:      50.0,
		HighOfDay: 50.5,
		Volume:    100000,
		Timestamp: failTime,
	}

	signal, ok := s.EvaluateExit(tick)
	if !ok {
		t.Fatal("expected failed-breakout exit signal")
	}
	if signal.Reason != "failed-breakout" {
		t.Fatalf("expected reason failed-breakout, got %q", signal.Reason)
	}
	if signal.OrderType == "market" {
		t.Error("failed-breakout exit should not use market order — widening slippage handles fill aggressiveness")
	}
}

func TestProfitTargetExitUsesLimitOrder(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false
	cfg.PartialExitsEnabled = false

	rt := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, rt)

	loc := markethours.Location()
	entryTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)

	openPosition(pm, "PROFIT", domain.DirectionLong, 50.0, 48.0, entryTime)

	// Tick at profit target (4R for breakout playbook, riskPerShare=1.0)
	tick := domain.Tick{
		Symbol:    "PROFIT",
		Price:     54.10, // > 4R above entry
		BarOpen:   53.5,
		BarHigh:   54.2,
		BarLow:    53.0,
		Open:      50.0,
		HighOfDay: 54.2,
		Volume:    100000,
		Timestamp: entryTime.Add(30 * time.Minute),
	}

	signal, ok := s.EvaluateExit(tick)
	if !ok {
		t.Fatal("expected profit-target exit signal")
	}
	if signal.Reason != "profit-target" {
		t.Fatalf("expected reason profit-target, got %q", signal.Reason)
	}
	// Profit targets should NOT be market orders
	if signal.OrderType == "market" {
		t.Error("profit-target exit should not use market order")
	}
}

// TestNoExitSignalSetsMarketOrder verifies that no exit signal ever sets OrderType to "market".
func TestNoExitSignalSetsMarketOrder(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false

	rt := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, rt)

	loc := markethours.Location()
	ts := time.Date(2026, 3, 23, 10, 30, 0, 0, loc)

	openPosition(pm, "NOMARKET", domain.DirectionLong, 50.0, 48.0, ts)

	// Trigger stop-loss
	tick := domain.Tick{
		Symbol:    "NOMARKET",
		Price:     47.50,
		BarOpen:   49.0,
		BarHigh:   49.5,
		BarLow:    47.0,
		Open:      50.0,
		HighOfDay: 51.0,
		Volume:    100000,
		Timestamp: ts.Add(5 * time.Minute),
	}

	signal, ok := s.EvaluateExit(tick)
	if !ok {
		t.Fatal("expected exit signal")
	}
	if signal.OrderType == "market" {
		t.Errorf("exit signal for %q should never set OrderType to market (got %q)", signal.Reason, signal.OrderType)
	}
}
