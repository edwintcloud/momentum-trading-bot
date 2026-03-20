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
	shortable ShortabilityChecker
	dayKey    string
	approved  int
}

// ShortabilityChecker reports whether a symbol may be shorted.
type ShortabilityChecker interface {
	IsShortable(symbol string) bool
}

func inferSignalIntent(signal domain.TradeSignal, position domain.Position, exists bool) domain.TradeSignal {
	signal.Side = domain.NormalizeSide(signal.Side)
	if exists && signal.PositionSide == "" {
		signal.PositionSide = position.Side
	}
	signal.PositionSide = domain.NormalizeDirection(signal.PositionSide)
	if signal.Intent != "" {
		signal.Intent = domain.NormalizeIntent(signal.Intent)
		return signal
	}
	switch {
	case exists && signal.Side == domain.CloseBrokerSide(position.Side):
		signal.Intent = domain.IntentClose
		signal.PositionSide = position.Side
	case exists && signal.Side == domain.OpenBrokerSide(position.Side):
		signal.Intent = domain.IntentOpen
		signal.PositionSide = position.Side
	case signal.Side == domain.SideSell:
		signal.Intent = domain.IntentOpen
		signal.PositionSide = domain.DirectionShort
	default:
		signal.Intent = domain.IntentOpen
		signal.PositionSide = domain.DirectionLong
	}
	return signal
}

// NewEngine creates a new risk engine.
func NewEngine(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State, shortable ...ShortabilityChecker) *Engine {
	var checker ShortabilityChecker
	if len(shortable) > 0 {
		checker = shortable[0]
	}
	return &Engine{config: cfg, portfolio: portfolioManager, runtime: runtimeState, shortable: checker}
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
	position, exists := r.portfolio.Position(signal.Symbol)
	signal = inferSignalIntent(signal, position, exists)
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
	if domain.IsClosingIntent(signal.Intent) {
		if !exists || position.Quantity == 0 {
			return domain.OrderRequest{}, false, "no-position"
		}
		if domain.NormalizeDirection(position.Side) != signal.PositionSide {
			return domain.OrderRequest{}, false, "position-side-mismatch"
		}
		quantity := signal.Quantity
		if quantity > position.Quantity {
			quantity = position.Quantity
		}
		limitPrice := r.limitPrice(signal.Price, signal.Side)
		return domain.OrderRequest{
			Symbol:       signal.Symbol,
			Side:         signal.Side,
			Intent:       signal.Intent,
			PositionSide: signal.PositionSide,
			Price:        limitPrice,
			Quantity:     quantity,
			StopPrice:    signal.StopPrice,
			RiskPerShare: signal.RiskPerShare,
			EntryATR:     signal.EntryATR,
			SetupType:    signal.SetupType,
			Reason:       signal.Reason,
			MarketRegime: signal.MarketRegime,
			RegimeConfidence: signal.RegimeConfidence,
			Playbook:     signal.Playbook,
			Timestamp:    orderTime,
		}, true, "approved"
	}

	if blockReason := r.runtime.EntryBlockReasonAt(orderTime); blockReason != "" {
		return domain.OrderRequest{}, false, blockReason
	}
	r.rollDay(orderTime)
	if filledEntries := r.portfolio.EntriesToday(); filledEntries > r.approved {
		r.approved = filledEntries
	}
	if domain.IsShort(signal.PositionSide) {
		if !r.config.EnableShorts {
			return domain.OrderRequest{}, false, "shorts-disabled"
		}
		if r.shortable != nil && !r.shortable.IsShortable(signal.Symbol) {
			return domain.OrderRequest{}, false, "symbol-not-shortable"
		}
	}
	if signal.StopPrice <= 0 || signal.RiskPerShare <= 0 {
		return domain.OrderRequest{}, false, "missing-stop"
	}
	if r.approved >= r.config.MaxTradesPerDay {
		return domain.OrderRequest{}, false, "max-trades"
	}
	if r.portfolio.OpenPositionCount() >= r.config.MaxOpenPositions {
		return domain.OrderRequest{}, false, "max-open-positions"
	}
	if domain.IsShort(signal.PositionSide) && r.portfolio.PositionCountBySide(domain.DirectionShort) >= r.config.MaxShortOpenPositions {
		return domain.OrderRequest{}, false, "max-short-open-positions"
	}
	effectiveCapital := r.portfolio.EffectiveCapital()
	if r.portfolio.DayPnL() <= -(effectiveCapital * r.config.DailyLossLimitPct) {
		return domain.OrderRequest{}, false, "daily-loss-limit"
	}
	maxExposure := effectiveCapital * r.config.MaxExposurePct
	availableExposure := maxExposure - r.portfolio.Exposure()
	if domain.IsShort(signal.PositionSide) {
		maxExposure = effectiveCapital * r.config.MaxShortExposurePct
		availableExposure = maxExposure - r.portfolio.ShortExposure()
	}
	if availableExposure <= 0 {
		return domain.OrderRequest{}, false, "max-exposure"
	}
	availableCash := r.portfolio.AvailableCash()
	if availableCash <= 0 {
		return domain.OrderRequest{}, false, "max-exposure"
	}
	limitPrice := r.limitPrice(signal.Price, signal.Side)
	quantity := signal.Quantity
	availableNotional := math.Min(availableExposure, availableCash)
	maxQuantityByExposure := int64(availableNotional / limitPrice)
	if maxQuantityByExposure < quantity {
		quantity = maxQuantityByExposure
	}
	if quantity < 1 {
		return domain.OrderRequest{}, false, "max-exposure"
	}
	// Reject trivially small positions that result from nearly-full
	// exposure. A position worth less than 0.5% of capital is not
	// worth the slippage and execution risk.
	notional := float64(quantity) * limitPrice
	if notional < effectiveCapital*0.005 {
		return domain.OrderRequest{}, false, "position-too-small"
	}
	request := domain.OrderRequest{
		Symbol:       signal.Symbol,
		Side:         signal.Side,
		Intent:       signal.Intent,
		PositionSide: signal.PositionSide,
		Price:        limitPrice,
		Quantity:     quantity,
		StopPrice:    signal.StopPrice,
		RiskPerShare: signal.RiskPerShare,
		EntryATR:     signal.EntryATR,
		SetupType:    signal.SetupType,
		Reason:       signal.Reason,
		MarketRegime: signal.MarketRegime,
		RegimeConfidence: signal.RegimeConfidence,
		Playbook:     signal.Playbook,
		Timestamp:    orderTime,
	}
	r.approved++
	return request, true, "approved"
}

func (r *Engine) rollDay(at time.Time) {
	day := at.In(markethours.Location()).Format("2006-01-02")
	if r.dayKey == day {
		return
	}
	r.dayKey = day
	r.approved = 0
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
