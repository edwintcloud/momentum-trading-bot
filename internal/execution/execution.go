package execution

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

// Engine sends approved orders to Alpaca and waits for fill confirmation.
type Engine struct {
	client  *alpaca.Client
	config  config.AlpacaConfig
	runtime *runtime.State
}

// NewEngine creates a live execution engine.
func NewEngine(client *alpaca.Client, cfg config.AlpacaConfig, runtimeState *runtime.State) *Engine {
	return &Engine{client: client, config: cfg, runtime: runtimeState}
}

// Start listens for orders and applies confirmed fills to the portfolio.
func (e *Engine) Start(ctx context.Context, in <-chan domain.OrderRequest, portfolioManager *portfolio.Manager) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case order := <-in:
			report, err := e.fill(ctx, order)
			if err != nil {
				e.runtime.RecordLog("error", "execution", err.Error())
				continue
			}
			portfolioManager.ApplyExecution(report)
			e.runtime.RecordLog("info", "execution", report.Side+" "+report.Symbol+" filled via Alpaca")
		}
	}
}

func (e *Engine) fill(ctx context.Context, order domain.OrderRequest) (domain.ExecutionReport, error) {
	submissionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	submitted, err := e.client.SubmitOrder(submissionCtx, order)
	if err != nil {
		return domain.ExecutionReport{}, fmt.Errorf("submit order %s %s failed: %w", order.Side, order.Symbol, err)
	}

	deadline := time.Now().Add(time.Duration(e.config.OrderFillTimeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(ctx, 10*time.Second)
		current, pollErr := e.client.GetOrder(pollCtx, submitted.ID)
		pollCancel()
		if pollErr != nil {
			return domain.ExecutionReport{}, fmt.Errorf("poll order %s failed: %w", submitted.ID, pollErr)
		}

		if current.Status == "filled" || current.Status == "partially_filled" {
			filledQty, err := strconv.ParseInt(current.FilledQty, 10, 64)
			if err != nil || filledQty == 0 {
				filledQty = order.Quantity
			}
			filledPrice, err := strconv.ParseFloat(current.FilledAvgPrice, 64)
			if err != nil || filledPrice == 0 {
				filledPrice = order.Price
			}
			return domain.ExecutionReport{
				Symbol:        order.Symbol,
				Side:          order.Side,
				Price:         filledPrice,
				Quantity:      filledQty,
				Reason:        order.Reason,
				BrokerOrderID: current.ID,
				BrokerStatus:  current.Status,
				FilledAt:      time.Now().UTC(),
			}, nil
		}

		if current.Status == "canceled" || current.Status == "rejected" || current.Status == "expired" {
			return domain.ExecutionReport{}, fmt.Errorf("order %s %s ended with status %s", order.Side, order.Symbol, current.Status)
		}

		select {
		case <-ctx.Done():
			return domain.ExecutionReport{}, ctx.Err()
		case <-time.After(time.Duration(e.config.OrderPollIntervalSec) * time.Second):
		}
	}

	return domain.ExecutionReport{}, fmt.Errorf("order fill timeout for %s %s", order.Side, order.Symbol)
}
