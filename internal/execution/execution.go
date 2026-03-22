package execution

import (
	"context"
	"fmt"
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case order, ok := <-in:
			if !ok {
				return fmt.Errorf("execution order channel closed")
			}
			go e.executeOrder(ctx, order, fills)
		}
	}
}

func (e *Engine) executeOrder(ctx context.Context, order domain.OrderRequest, fills chan<- domain.ExecutionReport) {
	e.runtime.RecordLog("info", "execution",
		fmt.Sprintf("submitting %s %s %s qty=%d price=%.2f",
			order.Intent, order.PositionSide, order.Symbol, order.Quantity, order.Price))

	orderID, err := e.broker.SubmitOrder(ctx, order)
	if err != nil {
		e.runtime.RecordLog("error", "execution",
			fmt.Sprintf("submit failed %s %s: %v", order.Symbol, order.Side, err))
		return
	}

	// Poll for fill with timeout
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			e.runtime.RecordLog("warn", "execution",
				fmt.Sprintf("order timeout %s %s orderID=%s — cancelling", order.Symbol, order.Side, orderID))
			cancelCtx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
			if err := e.broker.CancelOrder(cancelCtx, orderID); err != nil {
				e.runtime.RecordLog("error", "execution",
					fmt.Sprintf("cancel failed %s orderID=%s: %v", order.Symbol, orderID, err))
			}
			cancelFn()
			return
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
				return

			case "rejected", "canceled", "expired":
				e.runtime.RecordLog("warn", "execution",
					fmt.Sprintf("order %s %s %s: %s", status, order.Symbol, order.Side, orderID))
				return
			}
		}
	}
}
