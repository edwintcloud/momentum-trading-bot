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

func TestExitOrderRetry_FillsOnThirdAttempt(t *testing.T) {
	broker := &fillBroker{fillOnAttempt: 3}
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
	if len(submitted) != 3 {
		t.Fatalf("expected 3 submit calls, got %d", len(submitted))
	}

	// Final attempt should be market order
	if submitted[2].OrderType != "market" {
		t.Errorf("expected final attempt to be market order, got %q", submitted[2].OrderType)
	}

	// First two attempts should not be market
	if submitted[0].OrderType == "market" {
		t.Error("expected first attempt to not be market order")
	}
	if submitted[1].OrderType == "market" {
		t.Error("expected second attempt to not be market order")
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

	// Last submit should be market order
	submitted := broker.getSubmittedOrders()
	if submitted[len(submitted)-1].OrderType != "market" {
		t.Errorf("expected final attempt to be market order, got %q", submitted[len(submitted)-1].OrderType)
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

	// Should have tried all 3 attempts
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

func TestOrderTypeLabel(t *testing.T) {
	if got := orderTypeLabel("market"); got != "market" {
		t.Errorf("orderTypeLabel(market) = %q, want market", got)
	}
	if got := orderTypeLabel(""); got != "limit" {
		t.Errorf("orderTypeLabel('') = %q, want limit", got)
	}
	if got := orderTypeLabel("limit"); got != "limit" {
		t.Errorf("orderTypeLabel(limit) = %q, want limit", got)
	}
}
