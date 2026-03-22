package execution

import (
	"math"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func adaptiveLimitCfg() config.TradingConfig {
	return config.TradingConfig{
		AdaptiveLimitEnabled:          true,
		AdaptiveLimitToleranceBps:     5.0,
		AdaptiveLimitWidenStepBps:     0.5,
		AdaptiveLimitWidenIntervalSec: 5,
		AdaptiveLimitMaxSlippageBps:   20.0,
	}
}

func TestNewAdaptiveLimitState_BuyPrice(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()

	state := NewAdaptiveLimitState("AAPL", "buy", 100.0, now, cfg)

	// Buy limit = 100 * (1 + 5/10000) = 100.05
	expected := 100.0 * (1.0 + 5.0/10000.0)
	if math.Abs(state.InitialLimit-expected) > 0.0001 {
		t.Errorf("InitialLimit = %f, want %f", state.InitialLimit, expected)
	}
	if state.CurrentLimit != state.InitialLimit {
		t.Error("CurrentLimit should equal InitialLimit initially")
	}
}

func TestNewAdaptiveLimitState_SellPrice(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()

	state := NewAdaptiveLimitState("AAPL", "sell", 100.0, now, cfg)

	// Sell limit = 100 * (1 - 5/10000) = 99.95
	expected := 100.0 * (1.0 - 5.0/10000.0)
	if math.Abs(state.InitialLimit-expected) > 0.0001 {
		t.Errorf("InitialLimit = %f, want %f", state.InitialLimit, expected)
	}
}

func TestAdaptiveLimitState_HoldBeforeInterval(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()
	state := NewAdaptiveLimitState("AAPL", "buy", 100.0, now, cfg)

	// Check at 3 seconds — less than 5 second interval.
	action, _ := state.Check(now.Add(3 * time.Second))
	if action != AdaptiveLimitHold {
		t.Errorf("expected Hold, got %d", action)
	}
}

func TestAdaptiveLimitState_WidenAfterInterval(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()
	state := NewAdaptiveLimitState("AAPL", "buy", 100.0, now, cfg)

	initialLimit := state.CurrentLimit

	// Check at 5 seconds — should widen.
	action, newPrice := state.Check(now.Add(5 * time.Second))
	if action != AdaptiveLimitWiden {
		t.Errorf("expected Widen, got %d", action)
	}
	if newPrice <= initialLimit {
		t.Errorf("widened price %f should be > initial %f for buy", newPrice, initialLimit)
	}
	if state.WideningSteps != 1 {
		t.Errorf("WideningSteps = %d, want 1", state.WideningSteps)
	}
}

func TestAdaptiveLimitState_SellWiden(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()
	state := NewAdaptiveLimitState("AAPL", "sell", 100.0, now, cfg)

	initialLimit := state.CurrentLimit

	// Check at 5 seconds — should widen downward for sell.
	action, newPrice := state.Check(now.Add(5 * time.Second))
	if action != AdaptiveLimitWiden {
		t.Errorf("expected Widen, got %d", action)
	}
	if newPrice >= initialLimit {
		t.Errorf("widened sell price %f should be < initial %f", newPrice, initialLimit)
	}
}

func TestAdaptiveLimitState_CancelOnMaxSlippage(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()
	cfg.AdaptiveLimitWidenStepBps = 5.0 // aggressive widening
	cfg.AdaptiveLimitMaxSlippageBps = 10.0

	state := NewAdaptiveLimitState("AAPL", "buy", 100.0, now, cfg)

	// Widen several times until cancelled.
	cursor := now
	var lastAction AdaptiveLimitAction
	for i := 0; i < 100; i++ {
		cursor = cursor.Add(5 * time.Second)
		lastAction, _ = state.Check(cursor)
		if lastAction == AdaptiveLimitCancel {
			break
		}
	}

	if lastAction != AdaptiveLimitCancel {
		t.Error("expected cancel after exceeding max slippage")
	}
}

func TestAdaptiveLimitState_ProgressiveWidening(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()
	state := NewAdaptiveLimitState("AAPL", "buy", 100.0, now, cfg)

	var prices []float64
	cursor := now
	for i := 0; i < 5; i++ {
		cursor = cursor.Add(5 * time.Second)
		action, price := state.Check(cursor)
		if action == AdaptiveLimitCancel {
			break
		}
		if action == AdaptiveLimitWiden {
			prices = append(prices, price)
		}
	}

	// Each successive price should be higher (for buy).
	for i := 1; i < len(prices); i++ {
		if prices[i] <= prices[i-1] {
			t.Errorf("price %d (%f) should be > price %d (%f)", i, prices[i], i-1, prices[i-1])
		}
	}
}

func TestAdaptiveLimitState_SlippageBps(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := adaptiveLimitCfg()
	state := NewAdaptiveLimitState("AAPL", "buy", 100.0, now, cfg)

	slippage := state.SlippageBps()
	// Initial slippage should be ~5 bps (the tolerance).
	if math.Abs(slippage-5.0) > 0.01 {
		t.Errorf("initial SlippageBps = %f, want ~5.0", slippage)
	}
}
