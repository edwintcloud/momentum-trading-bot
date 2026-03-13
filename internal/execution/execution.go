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
		case order, ok := <-in:
			if !ok {
				return fmt.Errorf("execution input channel closed")
			}
			err := e.fill(ctx, order, portfolioManager)
			if err != nil {
				e.runtime.RecordLog("error", "execution", err.Error())
				continue
			}
		}
	}
}

func (e *Engine) fill(ctx context.Context, order domain.OrderRequest, portfolioManager *portfolio.Manager) error {
	if order.Side == "sell" {
		adjustedOrder, _, err := e.reconcileSellOrder(ctx, order, portfolioManager)
		if err != nil {
			return err
		}
		order = adjustedOrder
	}

	submissionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	submitted, err := e.client.SubmitOrder(submissionCtx, order)
	if err != nil {
		if order.Side == "sell" && alpaca.IsInsufficientQuantityError(err) {
			if availableQty, ok := alpaca.AvailableQuantityFromError(err); ok {
				adjustedOrder, changed, adjustErr := e.applyAvailableSellQuantity(order, availableQty, portfolioManager)
				if adjustErr == nil && changed {
					submissionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					submitted, err = e.client.SubmitOrder(submissionCtx, adjustedOrder)
					if err == nil {
						order = adjustedOrder
					}
				}
			}
		}
		if err != nil && order.Side == "sell" && alpaca.IsInsufficientQuantityError(err) {
			adjustedOrder, changed, reconcileErr := e.reconcileSellOrder(ctx, order, portfolioManager)
			if reconcileErr == nil && changed {
				submissionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				submitted, err = e.client.SubmitOrder(submissionCtx, adjustedOrder)
				if err == nil {
					order = adjustedOrder
				}
			}
		}
	}
	if err != nil {
		return fmt.Errorf("submit order %s %s failed: %w", order.Side, order.Symbol, err)
	}

	deadline := time.Now().Add(time.Duration(e.config.OrderFillTimeoutSec) * time.Second)
	appliedQty := int64(0)
	for time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(ctx, 10*time.Second)
		current, pollErr := e.client.GetOrder(pollCtx, submitted.ID)
		pollCancel()
		if pollErr != nil {
			return fmt.Errorf("poll order %s failed: %w", submitted.ID, pollErr)
		}

		filledQty, filledPrice := filledOrderState(current, order)
		if filledQty > appliedQty {
			deltaQty := filledQty - appliedQty
			portfolioManager.ApplyExecution(domain.ExecutionReport{
				Symbol:        order.Symbol,
				Side:          order.Side,
				Price:         filledPrice,
				Quantity:      deltaQty,
				Reason:        order.Reason,
				BrokerOrderID: current.ID,
				BrokerStatus:  current.Status,
				FilledAt:      time.Now().UTC(),
			})
			appliedQty = filledQty
			e.runtime.RecordLog("info", "execution", fmt.Sprintf("%s %s filled %d/%d via Alpaca (%s)", order.Side, order.Symbol, appliedQty, order.Quantity, current.Status))
		}

		if current.Status == "filled" {
			return nil
		}

		if current.Status == "partially_filled" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(e.config.OrderPollIntervalSec) * time.Second):
			}
			continue
		}

		if current.Status == "canceled" || current.Status == "rejected" || current.Status == "expired" {
			if appliedQty > 0 {
				return fmt.Errorf("order %s %s ended with status %s after partial fill of %d shares", order.Side, order.Symbol, current.Status, appliedQty)
			}
			return fmt.Errorf("order %s %s ended with status %s", order.Side, order.Symbol, current.Status)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(e.config.OrderPollIntervalSec) * time.Second):
		}
	}

	if appliedQty > 0 {
		return fmt.Errorf("order fill timeout for %s %s after partial fill of %d shares", order.Side, order.Symbol, appliedQty)
	}
	return fmt.Errorf("order fill timeout for %s %s", order.Side, order.Symbol)
}

func filledOrderState(current alpaca.Order, order domain.OrderRequest) (int64, float64) {
	filledQty, err := strconv.ParseInt(current.FilledQty, 10, 64)
	if err != nil {
		filledQty = 0
	}
	if current.Status == "filled" && filledQty == 0 {
		filledQty = order.Quantity
	}
	filledPrice, err := strconv.ParseFloat(current.FilledAvgPrice, 64)
	if err != nil || filledPrice == 0 {
		filledPrice = order.Price
	}
	return filledQty, filledPrice
}

func (e *Engine) applyAvailableSellQuantity(order domain.OrderRequest, availableQty int64, portfolioManager *portfolio.Manager) (domain.OrderRequest, bool, error) {
	portfolioManager.SyncPositionQuantity(order.Symbol, availableQty)
	if availableQty <= 0 {
		return domain.OrderRequest{}, false, fmt.Errorf("no broker shares available to sell for %s", order.Symbol)
	}
	if availableQty >= order.Quantity {
		return order, false, nil
	}
	adjusted := order
	adjusted.Quantity = availableQty
	e.runtime.RecordLog("warn", "execution", fmt.Sprintf("clamped sell %s quantity from %d to %d based on Alpaca rejection payload", order.Symbol, order.Quantity, availableQty))
	return adjusted, true, nil
}

func (e *Engine) reconcileSellOrder(ctx context.Context, order domain.OrderRequest, portfolioManager *portfolio.Manager) (domain.OrderRequest, bool, error) {
	brokerQty, err := e.brokerPositionQuantity(ctx, order.Symbol)
	if err != nil {
		return domain.OrderRequest{}, false, fmt.Errorf("reconcile sell quantity for %s failed: %w", order.Symbol, err)
	}
	portfolioManager.SyncPositionQuantity(order.Symbol, brokerQty)
	if brokerQty <= 0 {
		return domain.OrderRequest{}, false, fmt.Errorf("no broker shares available to sell for %s", order.Symbol)
	}
	if brokerQty >= order.Quantity {
		return order, false, nil
	}
	adjusted := order
	adjusted.Quantity = brokerQty
	e.runtime.RecordLog("warn", "execution", fmt.Sprintf("clamped sell %s quantity from %d to %d based on broker position", order.Symbol, order.Quantity, brokerQty))
	return adjusted, true, nil
}

func (e *Engine) brokerPositionQuantity(ctx context.Context, symbol string) (int64, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	position, exists, err := e.client.GetPosition(lookupCtx, symbol)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	return parseShareQuantity(position.Qty)
}

func parseShareQuantity(value string) (int64, error) {
	return alpaca.ParseShareQuantity(value)
}
