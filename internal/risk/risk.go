package risk

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

// ShortabilityChecker reports whether a symbol may be shorted.
type ShortabilityChecker interface {
	IsShortable(symbol string) bool
}

// Engine enforces position sizing and loss controls before execution.
type Engine struct {
	config    config.TradingConfig
	portfolio *portfolio.Manager
	runtime   *runtime.State
	shortable ShortabilityChecker
	dayKey    string
	approved  int
}

// NewEngine creates a new risk engine.
func NewEngine(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State, shortable ...ShortabilityChecker) *Engine {
	var checker ShortabilityChecker
	if len(shortable) > 0 {
		checker = shortable[0]
	}
	return &Engine{
		config:    cfg,
		portfolio: portfolioManager,
		runtime:   runtimeState,
		shortable: checker,
		dayKey:    markethours.TradingDay(time.Now()),
	}
}

// Start processes trade signals and emits approved order requests.
func (e *Engine) Start(ctx context.Context, in <-chan domain.TradeSignal, out chan<- domain.OrderRequest) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case signal, ok := <-in:
			if !ok {
				return fmt.Errorf("risk signal channel closed")
			}
			order, approved := e.Evaluate(signal)
			if !approved {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- order:
			}
		}
	}
}

// Evaluate checks a trade signal against all risk gates.
func (e *Engine) Evaluate(signal domain.TradeSignal) (domain.OrderRequest, bool) {
	// Infer intent
	pos, posExists := e.portfolio.GetPosition(signal.Symbol)
	signal = e.inferIntent(signal, pos, posExists)

	// Closing trades always pass risk
	if domain.IsClosingIntent(signal.Intent) {
		return e.toOrderRequest(signal), true
	}

	// Gate checks for opening trades
	if e.runtime.IsPaused() || e.runtime.IsEmergencyStopped() {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s %s: system paused/stopped", signal.Side, signal.Symbol))
		return domain.OrderRequest{}, false
	}

	if !markethours.IsMarketOpen(signal.Timestamp) {
		return domain.OrderRequest{}, false
	}

	// Position limit
	positions := e.portfolio.GetPositions()
	if len(positions) >= e.config.MaxOpenPositions {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: max positions reached (%d)", signal.Symbol, e.config.MaxOpenPositions))
		return domain.OrderRequest{}, false
	}

	// Daily trade limit
	e.resetDayIfNeeded()
	if e.approved >= e.config.MaxTradesPerDay {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: max daily trades reached (%d)", signal.Symbol, e.config.MaxTradesPerDay))
		return domain.OrderRequest{}, false
	}

	// Exposure limit
	totalExposure, longExposure, shortExposure := e.portfolio.Exposure()
	proposedValue := signal.Price * float64(signal.Quantity)
	maxExposure := e.config.StartingCapital * e.config.MaxExposurePct
	if totalExposure+proposedValue > maxExposure {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: exposure limit (%.0f + %.0f > %.0f)", signal.Symbol, totalExposure, proposedValue, maxExposure))
		return domain.OrderRequest{}, false
	}

	// Short-specific limits
	if domain.IsShort(signal.PositionSide) {
		shortCount := 0
		for _, p := range positions {
			if domain.IsShort(p.Side) {
				shortCount++
			}
		}
		if shortCount >= e.config.MaxShortOpenPositions {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s short: max short positions reached", signal.Symbol))
			return domain.OrderRequest{}, false
		}
		maxShortExposure := e.config.StartingCapital * e.config.MaxShortExposurePct
		if shortExposure+proposedValue > maxShortExposure {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s short: short exposure limit", signal.Symbol))
			return domain.OrderRequest{}, false
		}
		if e.shortable != nil && !e.shortable.IsShortable(signal.Symbol) {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s short: not shortable", signal.Symbol))
			return domain.OrderRequest{}, false
		}
	}

	// Daily loss limit
	snapshot := e.portfolio.StatusSnapshot()
	dailyLossLimit := e.config.StartingCapital * e.config.DailyLossLimitPct
	if math.Abs(snapshot.DayPnL) >= dailyLossLimit && snapshot.DayPnL < 0 {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: daily loss limit reached (%.2f)", signal.Symbol, snapshot.DayPnL))
		return domain.OrderRequest{}, false
	}

	// Position size cap
	maxPositionValue := e.config.StartingCapital * e.config.MaxExposurePct / float64(e.config.MaxOpenPositions)
	if proposedValue > maxPositionValue {
		newQty := int64(math.Floor(maxPositionValue / signal.Price))
		if newQty <= 0 {
			return domain.OrderRequest{}, false
		}
		signal.Quantity = newQty
	}

	e.approved++
	_ = longExposure // used in exposure calc
	return e.toOrderRequest(signal), true
}

func (e *Engine) inferIntent(signal domain.TradeSignal, pos domain.Position, exists bool) domain.TradeSignal {
	signal.Side = domain.NormalizeSide(signal.Side)
	if exists && signal.PositionSide == "" {
		signal.PositionSide = pos.Side
	}
	signal.PositionSide = domain.NormalizeDirection(signal.PositionSide)
	if signal.Intent != "" {
		signal.Intent = domain.NormalizeIntent(signal.Intent)
		return signal
	}
	switch {
	case exists && signal.Side == domain.CloseBrokerSide(pos.Side):
		signal.Intent = domain.IntentClose
		signal.PositionSide = pos.Side
	case exists && signal.Side == domain.OpenBrokerSide(pos.Side):
		signal.Intent = domain.IntentOpen
		signal.PositionSide = pos.Side
	case signal.Side == domain.SideSell:
		signal.Intent = domain.IntentOpen
		signal.PositionSide = domain.DirectionShort
	default:
		signal.Intent = domain.IntentOpen
		signal.PositionSide = domain.DirectionLong
	}
	return signal
}

func (e *Engine) toOrderRequest(signal domain.TradeSignal) domain.OrderRequest {
	return domain.OrderRequest{
		Symbol:           signal.Symbol,
		Side:             signal.Side,
		Intent:           signal.Intent,
		PositionSide:     signal.PositionSide,
		Price:            signal.Price,
		Quantity:         signal.Quantity,
		StopPrice:        signal.StopPrice,
		RiskPerShare:     signal.RiskPerShare,
		EntryATR:         signal.EntryATR,
		SetupType:        signal.SetupType,
		Reason:           signal.Reason,
		MarketRegime:     signal.MarketRegime,
		RegimeConfidence: signal.RegimeConfidence,
		Playbook:         signal.Playbook,
		Timestamp:        signal.Timestamp,
	}
}

func (e *Engine) resetDayIfNeeded() {
	today := markethours.TradingDay(time.Now())
	if today != e.dayKey {
		e.dayKey = today
		e.approved = 0
	}
}
