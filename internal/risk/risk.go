package risk

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

// Engine enforces position sizing and loss controls before execution.
type Engine struct {
	config    config.TradingConfig
	portfolio *portfolio.Manager
	runtime   *runtime.State
}

// NewEngine creates a new risk engine.
func NewEngine(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State) *Engine {
	return &Engine{config: cfg, portfolio: portfolioManager, runtime: runtimeState}
}

// Start receives trade signals and applies risk checks.
func (r *Engine) Start(ctx context.Context, in <-chan domain.TradeSignal, out chan<- domain.OrderRequest) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case signal, ok := <-in:
			if !ok {
				return fmt.Errorf("risk input channel closed")
			}
			request, approved, reason := r.Evaluate(signal)
			if !approved {
				r.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s %s: %s", signal.Side, signal.Symbol, reason))
				if reason == "daily-loss-limit" {
					r.runtime.TriggerDailyLossStop(signal.Timestamp)
				}
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- request:
			}
		}
	}
}

// Evaluate validates a signal and returns an execution request when allowed.
func (r *Engine) Evaluate(signal domain.TradeSignal) (domain.OrderRequest, bool, string) {
	orderTime := signal.Timestamp
	if orderTime.IsZero() {
		orderTime = time.Now().UTC()
	}
	if !markethours.IsTradableSessionAt(orderTime) {
		return domain.OrderRequest{}, false, "outside-session"
	}
	if signal.Quantity <= 0 {
		return domain.OrderRequest{}, false, "invalid-quantity"
	}
	if signal.Side == "sell" {
		position, exists := r.portfolio.Position(signal.Symbol)
		if !exists || position.Quantity == 0 {
			return domain.OrderRequest{}, false, "no-position"
		}
		quantity := signal.Quantity
		if quantity > position.Quantity {
			quantity = position.Quantity
		}
		limitPrice := r.limitPrice(signal.Price, signal.Side)
		return domain.OrderRequest{Symbol: signal.Symbol, Side: signal.Side, Price: limitPrice, Quantity: quantity, Reason: signal.Reason, Timestamp: orderTime}, true, "approved"
	}

	if blockReason := r.runtime.EntryBlockReasonAt(orderTime); blockReason != "" {
		return domain.OrderRequest{}, false, blockReason
	}
	if r.portfolio.EntriesToday() >= r.config.MaxTradesPerDay {
		return domain.OrderRequest{}, false, "max-trades"
	}
	if r.portfolio.OpenPositionCount() >= r.config.MaxOpenPositions {
		return domain.OrderRequest{}, false, "max-open-positions"
	}
	effectiveCapital := r.portfolio.EffectiveCapital()
	if r.portfolio.DayPnL() <= -(effectiveCapital * r.config.DailyLossLimitPct) {
		return domain.OrderRequest{}, false, "daily-loss-limit"
	}
	maxExposure := effectiveCapital * r.config.MaxExposurePct
	availableExposure := maxExposure - r.portfolio.Exposure()
	if availableExposure <= 0 {
		return domain.OrderRequest{}, false, "max-exposure"
	}
	limitPrice := r.limitPrice(signal.Price, signal.Side)
	quantity := signal.Quantity
	maxQuantityByExposure := int64(availableExposure / limitPrice)
	if maxQuantityByExposure < quantity {
		quantity = maxQuantityByExposure
	}
	if quantity < 1 {
		return domain.OrderRequest{}, false, "max-exposure"
	}
	return domain.OrderRequest{Symbol: signal.Symbol, Side: signal.Side, Price: limitPrice, Quantity: quantity, Reason: signal.Reason, Timestamp: orderTime}, true, "approved"
}

func (r *Engine) limitPrice(price float64, side string) float64 {
	if price <= 0 {
		return 0
	}
	buffer := adaptiveLimitBuffer(price, r.config.LimitOrderSlippageDollars)
	if side == "sell" {
		return roundPrice(math.Max(0.01, price-buffer))
	}
	return roundPrice(price + buffer)
}

func adaptiveLimitBuffer(price, maxBuffer float64) float64 {
	if price <= 0 || maxBuffer <= 0 {
		return 0
	}
	buffer := price * 0.004
	if buffer < 0.01 {
		buffer = 0.01
	}
	if buffer > maxBuffer {
		buffer = maxBuffer
	}
	return roundPrice(buffer)
}

func roundPrice(price float64) float64 {
	return math.Round(price*100) / 100
}
