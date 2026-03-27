package execution

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

// BrokerClient defines the interface for order execution.
type BrokerClient interface {
	SubmitOrder(ctx context.Context, order domain.OrderRequest) (string, error)
	PollOrderStatus(ctx context.Context, orderID string) (string, float64, error)
	CancelOrder(ctx context.Context, orderID string) error
	IsEasyToBorrow(symbol string) bool
}

// Engine submits approved orders to the broker and polls for fills.
type Engine struct {
	broker         BrokerClient
	runtime        *runtime.State
	recorder       domain.EventRecorder
	pollInterval   time.Duration
	pollTimeout    time.Duration
	nowFunc        func() time.Time
	synchronous    bool
	mu             sync.Mutex
	activeBySymbol map[string]domain.OrderRequest
	onAccepted     func(domain.OrderRequest)
	onFailed       func(domain.OrderRequest)
}

// EngineOption configures optional Engine behavior.
type EngineOption func(*Engine)

// WithPollInterval sets the polling interval for order status checks.
// Default is 500ms. Use a smaller value (e.g. 1ms) for paper/backtest brokers.
func WithPollInterval(d time.Duration) EngineOption {
	return func(e *Engine) { e.pollInterval = d }
}

// WithPollTimeout sets the maximum time to wait for an order fill before cancelling.
// Default is 30s.
func WithPollTimeout(d time.Duration) EngineOption {
	return func(e *Engine) { e.pollTimeout = d }
}

// WithNowFunc overrides the clock used for FilledAt timestamps.
// Default is time.Now. Use this for backtest simulated time.
func WithNowFunc(fn func() time.Time) EngineOption {
	return func(e *Engine) { e.nowFunc = fn }
}

// WithSynchronous forces the execution engine to process orders serially.
// Useful for deterministic backtests.
func WithSynchronous(enabled bool) EngineOption {
	return func(e *Engine) { e.synchronous = enabled }
}

// WithOrderCallbacks installs lifecycle callbacks for accepted and failed orders.
func WithOrderCallbacks(onAccepted func(domain.OrderRequest), onFailed func(domain.OrderRequest)) EngineOption {
	return func(e *Engine) {
		e.onAccepted = onAccepted
		e.onFailed = onFailed
	}
}

// NewEngine creates an execution engine.
func NewEngine(broker BrokerClient, runtimeState *runtime.State, recorder domain.EventRecorder, opts ...EngineOption) *Engine {
	e := &Engine{
		broker:         broker,
		runtime:        runtimeState,
		recorder:       recorder,
		pollInterval:   500 * time.Millisecond,
		pollTimeout:    30 * time.Second,
		nowFunc:        time.Now,
		activeBySymbol: make(map[string]domain.OrderRequest),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Start processes order requests from the pipeline.
func (e *Engine) Start(ctx context.Context, in <-chan domain.OrderRequest, fills chan<- domain.ExecutionReport) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case order, ok := <-in:
			if !ok {
				return fmt.Errorf("execution order channel closed")
			}
			if !e.tryBeginOrder(order) {
				continue
			}
			if e.onAccepted != nil {
				e.onAccepted(order)
			}
			if e.synchronous {
				filled := e.executeOrder(ctx, order, fills)
				if !filled && e.onFailed != nil {
					e.onFailed(order)
				}
				e.finishOrder(order.Symbol)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				filled := e.executeOrder(ctx, order, fills)
				if !filled && e.onFailed != nil {
					e.onFailed(order)
				}
				defer e.finishOrder(order.Symbol)
			}()
		}
	}
}

const defaultExitMaxAttempts = 5

func (e *Engine) executeOrder(ctx context.Context, order domain.OrderRequest, fills chan<- domain.ExecutionReport) bool {
	isExit := domain.IsClosingIntent(order.Intent) || domain.IsPartialIntent(order.Intent)
	maxAttempts := 1
	if isExit {
		maxAttempts = defaultExitMaxAttempts
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Widen slippage on each retry attempt
		if attempt > 1 && isExit {
			order.SlippageMultiplier = slippageForAttempt(attempt)
			e.runtime.RecordLog("warn", "execution",
				fmt.Sprintf("retrying %s %s exit with %.0fx slippage (attempt %d/%d)",
					order.Symbol, order.Side, order.SlippageMultiplier, attempt, maxAttempts))
		}

		filled := e.submitAndPoll(ctx, order, fills, attempt)
		if filled {
			return true
		}

		if !isExit {
			return false
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}

	e.runtime.RecordLog("error", "execution",
		fmt.Sprintf("FAILED to exit %s %s after %d attempts", order.Symbol, order.Side, maxAttempts))
	return false
}

func slippageForAttempt(attempt int) float64 {
	switch attempt {
	case 2:
		return 3.0
	case 3:
		return 5.0
	case 4:
		return 8.0
	case 5:
		return 12.0
	default:
		return float64(attempt) * 3.0
	}
}

func (e *Engine) submitAndPoll(ctx context.Context, order domain.OrderRequest, fills chan<- domain.ExecutionReport, attempt int) bool {
	slippageLabel := ""
	if order.SlippageMultiplier > 1 {
		slippageLabel = fmt.Sprintf(" slippage=%.0fx", order.SlippageMultiplier)
	}
	e.runtime.RecordLog("info", "execution",
		fmt.Sprintf("submitting %s %s %s qty=%d price=%.2f type=limit%s (attempt %d)",
			order.Intent, order.PositionSide, order.Symbol, order.Quantity, order.Price,
			slippageLabel, attempt))

	orderID, err := e.broker.SubmitOrder(ctx, order)
	if err != nil {
		e.runtime.RecordLog("error", "execution",
			fmt.Sprintf("submit failed %s %s: %v", order.Symbol, order.Side, err))
		return false
	}

	pollCtx, cancel := context.WithTimeout(ctx, e.pollTimeout)
	defer cancel()

	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			e.runtime.RecordLog("warn", "execution",
				fmt.Sprintf("order timeout %s %s orderID=%s — cancelling", order.Symbol, order.Side, orderID))
			cancelCtx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			e.broker.CancelOrder(cancelCtx, orderID)
			cancelFn()

			// Final status check: the order may have filled between the last poll and the cancel.
			finalCtx, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
			finalStatus, finalPrice, finalErr := e.broker.PollOrderStatus(finalCtx, orderID)
			finalCancel()
			if finalErr == nil && finalStatus == "filled" {
				e.runtime.RecordLog("info", "execution",
					fmt.Sprintf("order %s %s filled during cancel race — processing fill", order.Symbol, order.Side))
				report := domain.ExecutionReport{
					Symbol:           order.Symbol,
					Side:             order.Side,
					Intent:           order.Intent,
					PositionSide:     order.PositionSide,
					Price:            finalPrice,
					Quantity:         order.Quantity,
					StopPrice:        order.StopPrice,
					RiskPerShare:     order.RiskPerShare,
					EntryATR:         order.EntryATR,
					SetupType:        order.SetupType,
					Reason:           order.Reason,
					MarketRegime:     order.MarketRegime,
					RegimeConfidence: order.RegimeConfidence,
					Playbook:         order.Playbook,
					BrokerOrderID:    orderID,
					BrokerStatus:     finalStatus,
					FilledAt:         e.nowFunc(),
				}
				if e.recorder != nil {
					e.recorder.RecordExecution(report)
				}
				select {
				case fills <- report:
				case <-ctx.Done():
				}
				return true
			}
			return false
		case <-ticker.C:
			status, fillPrice, err := e.broker.PollOrderStatus(pollCtx, orderID)
			if err != nil {
				continue
			}

			switch status {
			case "filled":
				report := domain.ExecutionReport{
					Symbol:           order.Symbol,
					Side:             order.Side,
					Intent:           order.Intent,
					PositionSide:     order.PositionSide,
					Price:            fillPrice,
					Quantity:         order.Quantity,
					StopPrice:        order.StopPrice,
					RiskPerShare:     order.RiskPerShare,
					EntryATR:         order.EntryATR,
					SetupType:        order.SetupType,
					Reason:           order.Reason,
					MarketRegime:     order.MarketRegime,
					RegimeConfidence: order.RegimeConfidence,
					Playbook:         order.Playbook,
					BrokerOrderID:    orderID,
					BrokerStatus:     status,
					FilledAt:         e.nowFunc(),
				}
				if e.recorder != nil {
					e.recorder.RecordExecution(report)
				}
				select {
				case fills <- report:
				case <-ctx.Done():
				}
				return true

			case "rejected", "canceled", "expired":
				e.runtime.RecordLog("warn", "execution",
					fmt.Sprintf("order %s %s %s: %s", status, order.Symbol, order.Side, orderID))
				return false
			}
		}
	}
}

func (e *Engine) tryBeginOrder(order domain.OrderRequest) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.activeBySymbol[order.Symbol]; ok {
		e.runtime.RecordLog("warn", "execution",
			fmt.Sprintf("skipping duplicate order for %s intent=%s side=%s while %s/%s is active",
				order.Symbol, order.Intent, order.Side, existing.Intent, existing.Side))
		return false
	}
	e.activeBySymbol[order.Symbol] = order
	return true
}

func (e *Engine) finishOrder(symbol string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.activeBySymbol, symbol)
}
