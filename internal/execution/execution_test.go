package execution

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

// testBroker is a configurable mock for execution tests.
// It cancels orders on the wrong attempt (triggering a fast retry) and fills on the target attempt.
type testBroker struct {
	mu            sync.Mutex
	submitOrders  []domain.OrderRequest
	fillOnAttempt int           // 1-based; fill immediately on this attempt. 0 = never fill.
	cancelAfter   time.Duration // how long until non-fill attempts self-cancel (simulates timeout/cancel)
}

func (m *testBroker) SubmitOrder(_ context.Context, order domain.OrderRequest) (string, error) {
	m.mu.Lock()
	m.submitOrders = append(m.submitOrders, order)
	count := len(m.submitOrders)
	m.mu.Unlock()
	return fmt.Sprintf("order-%d", count), nil
}

func (m *testBroker) PollOrderStatus(_ context.Context, orderID string) (string, float64, error) {
	m.mu.Lock()
	attempt := len(m.submitOrders)
	m.mu.Unlock()

	if m.fillOnAttempt > 0 && attempt == m.fillOnAttempt {
		return "filled", 50.0, nil
	}
	// Non-fill attempts: return "canceled" to simulate a fast timeout+cancel cycle
	// (the real broker.CancelOrder makes the next poll return "canceled")
	return "canceled", 0, nil
}

func (m *testBroker) CancelOrder(_ context.Context, _ string) error {
	return nil
}

func (m *testBroker) IsShortable(_ string) bool {
	return true
}

func (m *testBroker) getSubmitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.submitOrders)
}

func (m *testBroker) getSubmittedOrders() []domain.OrderRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.OrderRequest, len(m.submitOrders))
	copy(out, m.submitOrders)
	return out
}

// fillBroker fills on the target attempt, returns canceled on others.
// This triggers fast retries since "canceled" causes submitAndPoll to return false immediately.
type fillBroker struct {
	mu            sync.Mutex
	submitOrders  []domain.OrderRequest
	fillOnAttempt int // 1-based attempt
	pollCount     int32
}

func (m *fillBroker) SubmitOrder(_ context.Context, order domain.OrderRequest) (string, error) {
	m.mu.Lock()
	m.submitOrders = append(m.submitOrders, order)
	count := len(m.submitOrders)
	m.mu.Unlock()
	return fmt.Sprintf("order-%d", count), nil
}

func (m *fillBroker) PollOrderStatus(_ context.Context, orderID string) (string, float64, error) {
	atomic.AddInt32(&m.pollCount, 1)
	m.mu.Lock()
	attempt := len(m.submitOrders)
	m.mu.Unlock()

	if attempt == m.fillOnAttempt {
		return "filled", 50.0, nil
	}
	// Return "canceled" so submitAndPoll exits quickly for non-target attempts
	return "canceled", 0, nil
}

func (m *fillBroker) CancelOrder(_ context.Context, _ string) error {
	return nil
}

func (m *fillBroker) IsShortable(_ string) bool {
	return true
}

func (m *fillBroker) getSubmitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.submitOrders)
}

func (m *fillBroker) getSubmittedOrders() []domain.OrderRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.OrderRequest, len(m.submitOrders))
	copy(out, m.submitOrders)
	return out
}

// neverFillBroker returns "canceled" immediately, simulating timeout+cancel for every attempt.
type neverFillBroker struct {
	mu           sync.Mutex
	submitOrders []domain.OrderRequest
}

func (m *neverFillBroker) SubmitOrder(_ context.Context, order domain.OrderRequest) (string, error) {
	m.mu.Lock()
	m.submitOrders = append(m.submitOrders, order)
	count := len(m.submitOrders)
	m.mu.Unlock()
	return fmt.Sprintf("order-%d", count), nil
}

func (m *neverFillBroker) PollOrderStatus(_ context.Context, _ string) (string, float64, error) {
	return "canceled", 0, nil
}

func (m *neverFillBroker) CancelOrder(_ context.Context, _ string) error {
	return nil
}

func (m *neverFillBroker) IsShortable(_ string) bool {
	return true
}

func (m *neverFillBroker) getSubmitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.submitOrders)
}

func (m *neverFillBroker) getSubmittedOrders() []domain.OrderRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.OrderRequest, len(m.submitOrders))
	copy(out, m.submitOrders)
	return out
}

func TestExitOrderRetry_FillsOnFifthAttempt(t *testing.T) {
	broker := &fillBroker{fillOnAttempt: 5}
	rt := runtime.NewState()
	engine := NewEngine(broker, rt, nil)
	fills := make(chan domain.ExecutionReport, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	order := domain.OrderRequest{
		Symbol:       "TEST",
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        50.0,
		Quantity:     100,
	}

	engine.executeOrder(ctx, order, fills)

	submitted := broker.getSubmittedOrders()
	if len(submitted) != 5 {
		t.Fatalf("expected 5 submit calls, got %d", len(submitted))
	}

	// Verify slippage multipliers on each attempt
	// Attempt 1: no slippage multiplier (0 or 1)
	if submitted[0].SlippageMultiplier > 1 {
		t.Errorf("attempt 1: expected no slippage multiplier, got %.0f", submitted[0].SlippageMultiplier)
	}

	// Attempt 2: 3x slippage
	if submitted[1].SlippageMultiplier != 3.0 {
		t.Errorf("attempt 2: expected 3x slippage, got %.0f", submitted[1].SlippageMultiplier)
	}

	// Attempt 3: 5x slippage
	if submitted[2].SlippageMultiplier != 5.0 {
		t.Errorf("attempt 3: expected 5x slippage, got %.0f", submitted[2].SlippageMultiplier)
	}

	// Attempt 4: 8x slippage
	if submitted[3].SlippageMultiplier != 8.0 {
		t.Errorf("attempt 4: expected 8x slippage, got %.0f", submitted[3].SlippageMultiplier)
	}

	// Attempt 5: 12x slippage
	if submitted[4].SlippageMultiplier != 12.0 {
		t.Errorf("attempt 5: expected 12x slippage, got %.0f", submitted[4].SlippageMultiplier)
	}

	// No attempt should use market order type
	for i, sub := range submitted {
		if sub.OrderType == "market" {
			t.Errorf("attempt %d: should never use market order type, got %q", i+1, sub.OrderType)
		}
	}

	// Should have received a fill
	select {
	case report := <-fills:
		if report.Symbol != "TEST" {
			t.Errorf("expected fill for TEST, got %s", report.Symbol)
		}
	default:
		t.Error("expected fill report but channel was empty")
	}
}

func TestEntryOrderNoRetry(t *testing.T) {
	broker := &neverFillBroker{}
	rt := runtime.NewState()
	engine := NewEngine(broker, rt, nil)
	fills := make(chan domain.ExecutionReport, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	order := domain.OrderRequest{
		Symbol:       "TEST",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        50.0,
		Quantity:     100,
	}

	engine.executeOrder(ctx, order, fills)

	// Entry orders should only submit once — no retries
	if count := broker.getSubmitCount(); count != 1 {
		t.Fatalf("expected 1 submit call for entry order, got %d", count)
	}
}

func TestExitOrderRetry_PartialIntent(t *testing.T) {
	broker := &neverFillBroker{}
	rt := runtime.NewState()
	engine := NewEngine(broker, rt, nil)
	fills := make(chan domain.ExecutionReport, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	order := domain.OrderRequest{
		Symbol:       "TEST",
		Side:         domain.SideSell,
		Intent:       domain.IntentPartial,
		PositionSide: domain.DirectionLong,
		Price:        50.0,
		Quantity:     50,
	}

	engine.executeOrder(ctx, order, fills)

	// Partial exits should retry like full closes
	if count := broker.getSubmitCount(); count != defaultExitMaxAttempts {
		t.Fatalf("expected %d submit calls for partial exit, got %d", defaultExitMaxAttempts, count)
	}

	// Last submit should have 12x slippage (attempt 5), not market order
	submitted := broker.getSubmittedOrders()
	lastOrder := submitted[len(submitted)-1]
	if lastOrder.SlippageMultiplier != 12.0 {
		t.Errorf("expected final attempt to have 12x slippage, got %.0f", lastOrder.SlippageMultiplier)
	}
	if lastOrder.OrderType == "market" {
		t.Error("expected final attempt to NOT use market order type")
	}
}

func TestExitOrderRetry_AllAttemptsFail(t *testing.T) {
	broker := &neverFillBroker{}
	rt := runtime.NewState()
	engine := NewEngine(broker, rt, nil)
	fills := make(chan domain.ExecutionReport, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	order := domain.OrderRequest{
		Symbol:       "TEST",
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        50.0,
		Quantity:     100,
	}

	engine.executeOrder(ctx, order, fills)

	// Should have tried all 5 attempts
	if count := broker.getSubmitCount(); count != defaultExitMaxAttempts {
		t.Fatalf("expected %d submit calls, got %d", defaultExitMaxAttempts, count)
	}

	// No fill should be received
	select {
	case <-fills:
		t.Error("expected no fill report when all attempts fail")
	default:
		// expected
	}
}

func TestSlippageForAttempt(t *testing.T) {
	cases := []struct {
		attempt  int
		expected float64
	}{
		{2, 3.0},
		{3, 5.0},
		{4, 8.0},
		{5, 12.0},
		{6, 18.0}, // default: attempt * 3
	}
	for _, tc := range cases {
		got := slippageForAttempt(tc.attempt)
		if got != tc.expected {
			t.Errorf("slippageForAttempt(%d) = %.1f, want %.1f", tc.attempt, got, tc.expected)
		}
	}
}

func TestExitOrderRetry_NeverUsesMarketType(t *testing.T) {
	broker := &neverFillBroker{}
	rt := runtime.NewState()
	engine := NewEngine(broker, rt, nil)
	fills := make(chan domain.ExecutionReport, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test with an order that has OrderType pre-set to "market" — should still not matter
	order := domain.OrderRequest{
		Symbol:       "TEST",
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        50.0,
		Quantity:     100,
		OrderType:    "market", // pre-set — execution engine should not care
	}

	engine.executeOrder(ctx, order, fills)

	// Verify widening slippage was applied on retries
	submitted := broker.getSubmittedOrders()
	if len(submitted) != defaultExitMaxAttempts {
		t.Fatalf("expected %d attempts, got %d", defaultExitMaxAttempts, len(submitted))
	}

	// Verify slippage progresses correctly even when OrderType was set to "market"
	if submitted[1].SlippageMultiplier != 3.0 {
		t.Errorf("attempt 2: expected 3x slippage, got %.0f", submitted[1].SlippageMultiplier)
	}
	if submitted[4].SlippageMultiplier != 12.0 {
		t.Errorf("attempt 5: expected 12x slippage, got %.0f", submitted[4].SlippageMultiplier)
	}
}
