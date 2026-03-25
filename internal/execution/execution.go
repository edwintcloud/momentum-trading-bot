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
	IsShortable(symbol string) bool
}

// Engine submits approved orders to the broker and polls for fills.
type Engine struct {
	broker   BrokerClient
	runtime  *runtime.State
	recorder domain.EventRecorder
}

// NewEngine creates an execution engine.
func NewEngine(broker BrokerClient, runtimeState *runtime.State, recorder domain.EventRecorder) *Engine {
	return &Engine{
		broker:   broker,
		runtime:  runtimeState,
		recorder: recorder,
	}
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
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.executeOrder(ctx, order, fills)
			}()
		}
	}
}

const defaultExitMaxAttempts = 5

func (e *Engine) executeOrder(ctx context.Context, order domain.OrderRequest, fills chan<- domain.ExecutionReport) {
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
			return
		}

		if !isExit {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	e.runtime.RecordLog("error", "execution",
		fmt.Sprintf("FAILED to exit %s %s after %d attempts", order.Symbol, order.Side, maxAttempts))
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

	pollTimeout := 30 * time.Second
	pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
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
					FilledAt:         time.Now(),
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
					FilledAt:         time.Now(),
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

