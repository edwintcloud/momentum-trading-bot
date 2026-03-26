package risk

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/execution"
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
	mu                 sync.Mutex
	config             config.TradingConfig
	portfolio          *portfolio.Manager
	runtime            *runtime.State
	shortable          ShortabilityChecker
	dayKey             string
	approved           int
	lastMinuteKey      string
	minuteApproved     int
	CorrelationTracker *CorrelationTracker
	VaRCalc            *VaRCalculator
	GARCHForecaster    *GARCHForecaster
	RiskBudget         *RiskBudgetManager
}

// NewEngine creates a new risk engine.
func NewEngine(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State, shortable ...ShortabilityChecker) *Engine {
	var checker ShortabilityChecker
	if len(shortable) > 0 {
		checker = shortable[0]
	}
	e := &Engine{
		config:             cfg,
		portfolio:          portfolioManager,
		runtime:            runtimeState,
		shortable:          checker,
		dayKey:             "",
		CorrelationTracker: NewCorrelationTracker(cfg.CorrelationWindowSize),
	}
	if cfg.VaREnabled {
		e.VaRCalc = NewVaRCalculator(cfg.VaRConfidenceLevel, cfg.VaRMethod, 390)
	}
	if cfg.GARCHEnabled {
		e.GARCHForecaster = NewGARCHForecaster(cfg.GARCHAlpha, cfg.GARCHBeta, cfg.GARCHLongRunVar)
	}
	if cfg.DynamicRiskBudgetEnabled {
		e.RiskBudget = NewRiskBudgetManager(cfg.TargetVolAnnualized, cfg.DailyRiskBudgetPct)
	}
	return e
}

// UpdateConfig replaces the trading config (thread-safe for hot-reload).
func (e *Engine) UpdateConfig(cfg config.TradingConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.config = cfg
}

// getConfig returns a snapshot of the current config.
func (e *Engine) getConfig() config.TradingConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config
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
			order, approved, reason := e.Evaluate(signal)
			if !approved {
				e.runtime.RecordLog("info", "risk", fmt.Sprintf("signal for %s risk blocked: %s", signal.Symbol, reason))
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
// Returns the approved order, whether it was approved, and the rejection reason.
func (e *Engine) Evaluate(signal domain.TradeSignal) (domain.OrderRequest, bool, string) {
	cfg := e.getConfig()
	// Preserve original closing intent before inferIntent might override it
	originalIntent := signal.Intent

	// Infer intent
	pos, posExists := e.portfolio.GetPosition(signal.Symbol)
	signal = e.inferIntent(signal, pos, posExists)

	// If the original signal was a close/partial, always allow the exit
	if domain.IsClosingIntent(originalIntent) || domain.IsClosingIntent(signal.Intent) {
		if e.portfolio.HasPendingClose(signal.Symbol) {
			return domain.OrderRequest{}, false, "pending-close"
		}
		return e.toOrderRequest(signal), true, ""
	}

	if e.portfolio.HasPendingOrder(signal.Symbol) {
		return domain.OrderRequest{}, false, "pending-order"
	}

	// Gate checks for opening trades
	if e.runtime.IsPaused() || e.runtime.IsEmergencyStopped() {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s %s: system paused/stopped", signal.Side, signal.Symbol))
		return domain.OrderRequest{}, false, "system-paused"
	}

	if !markethours.IsTradableSessionAt(signal.Timestamp) {
		return domain.OrderRequest{}, false, "market-closed"
	}

	// Position limit
	positions := e.portfolio.GetPositions()
	if len(positions) >= cfg.MaxOpenPositions {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: max positions reached (%d)", signal.Symbol, cfg.MaxOpenPositions))
		return domain.OrderRequest{}, false, "max-positions"
	}

	// Daily trade limit
	e.resetDayIfNeeded(signal.Timestamp)
	if e.approved >= cfg.MaxTradesPerDay {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: max daily trades reached (%d)", signal.Symbol, cfg.MaxTradesPerDay))
		return domain.OrderRequest{}, false, "max-daily-trades"
	}

	// Per-minute entry throttle
	minuteKey := signal.Timestamp.In(markethours.Location()).Format("2006-01-02T15:04")
	if minuteKey != e.lastMinuteKey {
		e.lastMinuteKey = minuteKey
		e.minuteApproved = 0
	}
	if e.minuteApproved >= cfg.MaxEntriesPerMinute {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: max entries per minute reached (%d)", signal.Symbol, cfg.MaxEntriesPerMinute))
		return domain.OrderRequest{}, false, "max-entries-per-minute"
	}

	// Exposure limit
	totalExposure, longExposure, shortExposure := e.portfolio.Exposure()
	proposedValue := signal.Price * float64(signal.Quantity)
	currentEquity := e.portfolio.CurrentEquity()
	if currentEquity <= 0 {
		currentEquity = cfg.StartingCapital
	}
	maxExposure := currentEquity * cfg.MaxExposurePct
	if totalExposure+proposedValue > maxExposure {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: exposure limit (%.0f + %.0f > %.0f)", signal.Symbol, totalExposure, proposedValue, maxExposure))
		return domain.OrderRequest{}, false, "exposure-limit"
	}

	// Short-specific limits
	if domain.IsShort(signal.PositionSide) {
		shortCount := 0
		for _, p := range positions {
			if domain.IsShort(p.Side) {
				shortCount++
			}
		}
		if shortCount >= cfg.MaxShortOpenPositions {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s short: max short positions reached", signal.Symbol))
			return domain.OrderRequest{}, false, "max-short-positions"
		}
		maxShortExposure := currentEquity * cfg.MaxShortExposurePct
		if shortExposure+proposedValue > maxShortExposure {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s short: short exposure limit", signal.Symbol))
			return domain.OrderRequest{}, false, "short-exposure-limit"
		}
		if e.shortable != nil && !e.shortable.IsShortable(signal.Symbol) {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s short: not shortable", signal.Symbol))
			return domain.OrderRequest{}, false, "not-shortable"
		}
	}

	// Daily loss limit (kept for backward compat; graduated response is in DailyLossSizingFactor)
	snapshot := e.portfolio.StatusSnapshot()
	dailyLossLimit := currentEquity * cfg.DailyLossLimitPct
	if math.Abs(snapshot.DayPnL) >= dailyLossLimit && snapshot.DayPnL < 0 {
		e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: daily loss limit reached (%.2f)", signal.Symbol, snapshot.DayPnL))
		return domain.OrderRequest{}, false, "daily-loss-limit"
	}

	// VaR limit check: halt new entries if intraday VaR exceeds daily budget
	if cfg.VaREnabled && e.VaRCalc != nil {
		if e.VaRCalc.ExceedsDailyLimit(currentEquity, cfg.VaRDailyLimitPct) {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: intraday VaR exceeds daily limit (%.2f%%)", signal.Symbol, cfg.VaRDailyLimitPct*100))
			return domain.OrderRequest{}, false, "var-limit-exceeded"
		}
	}

	// Phase 2 Change 1: Portfolio heat gate
	if cfg.PortfolioHeatEnabled {
		currentHeat := e.portfolio.PortfolioHeat()
		proposedRisk := signal.RiskPerShare * float64(signal.Quantity)
		proposedHeatPct := (currentHeat + proposedRisk) / currentEquity

		if proposedHeatPct > cfg.MaxPortfolioHeatPct {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: portfolio heat would exceed maximum: %.1f%% > %.1f%%",
				signal.Symbol, proposedHeatPct*100, cfg.MaxPortfolioHeatPct*100))
			return domain.OrderRequest{}, false, "portfolio-heat-limit"
		}

		currentHeatPct := currentHeat / currentEquity
		if currentHeatPct > cfg.PortfolioHeatAlertPct {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("portfolio heat elevated: %.1f%%", currentHeatPct*100))
		}
	}

	// Phase 2 Change 3: Sector concentration gate
	// Skip for unknown/empty sectors — small-cap momentum stocks are rarely in the
	// hardcoded sector map, so they all resolve to "unknown" and would saturate the
	// single "unknown" bucket, blocking all subsequent entries.
	if cfg.SectorConcentrationEnabled && signal.Sector != "" && signal.Sector != "unknown" {
		exposures := e.sectorExposures(positions)
		existing := exposures[signal.Sector]

		if existing.positionCount >= cfg.MaxPositionsPerSector {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: sector concentration: %s already has %d positions (max %d)",
				signal.Symbol, signal.Sector, existing.positionCount, cfg.MaxPositionsPerSector))
			return domain.OrderRequest{}, false, "sector-concentration"
		}

		proposedSectorPct := (existing.notionalValue + proposedValue) / currentEquity
		if proposedSectorPct > cfg.MaxSectorExposurePct {
			e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: sector exposure for %s would reach %.1f%% (max %.1f%%)",
				signal.Symbol, signal.Sector, proposedSectorPct*100, cfg.MaxSectorExposurePct*100))
			return domain.OrderRequest{}, false, "sector-exposure-limit"
		}
	}

	// Phase 2 Change 4: Correlation-aware position approval
	if cfg.CorrelationCheckEnabled && e.CorrelationTracker != nil {
		existingSymbols := e.portfolio.OpenSymbols()
		if len(existingSymbols) > 0 {
			avgCorr := e.CorrelationTracker.AvgPortfolioCorrelation(existingSymbols, signal.Symbol)
			if avgCorr > cfg.MaxAvgCorrelation {
				e.runtime.RecordLog("warn", "risk", fmt.Sprintf("blocked %s: correlation too high: avg %.2f with portfolio (max %.2f)",
					signal.Symbol, avgCorr, cfg.MaxAvgCorrelation))
				return domain.OrderRequest{}, false, "correlation-limit"
			}
		}
	}

	// Phase 5: Market impact model — cap position size based on estimated impact
	if cfg.ImpactModelEnabled && signal.Price > 0 && signal.Quantity > 0 {
		adv := signal.AvgDailyVolume
		if adv <= 0 {
			// Conservative fallback when ADV is unknown
			adv = float64(signal.Quantity) * 100
		}
		impactParams := execution.DefaultImpactParams(adv, cfg.DefaultVolatility)
		impact := execution.EstimateImpact(int(signal.Quantity), signal.Price, impactParams)
		if impact > cfg.MaxAcceptableImpactPct {
			maxQty := execution.FindMaxQtyWithinImpact(signal.Price, impactParams, cfg.MaxAcceptableImpactPct)
			if maxQty <= 0 {
				return domain.OrderRequest{}, false, "market-impact-limit"
			}
			signal.Quantity = int64(maxQty)
		}
	}

	// Position size cap (re-derive proposedValue after impact model may have reduced quantity)
	proposedValue = signal.Price * float64(signal.Quantity)
	maxPositionValue := currentEquity * cfg.MaxExposurePct / float64(cfg.MaxOpenPositions)
	if proposedValue > maxPositionValue {
		newQty := int64(math.Floor(maxPositionValue / signal.Price))
		if newQty <= 0 {
			return domain.OrderRequest{}, false, "position-size-cap"
		}
		signal.Quantity = newQty
	}

	// Dynamic risk budget position cap
	if cfg.DynamicRiskBudgetEnabled && e.RiskBudget != nil && signal.Price > 0 {
		intradayVol := e.RiskBudget.IntradayRealizedVol(30)
		if intradayVol > 0 {
			remainingBars := markethours.RemainingMinutes(signal.Timestamp)
			maxQty := e.RiskBudget.MaxPositionFromBudget(currentEquity, remainingBars, 390, intradayVol, signal.Price)
			if maxQty > 0 && maxQty < signal.Quantity {
				signal.Quantity = maxQty
			}
		}
	}

	e.approved++
	e.minuteApproved++
	_ = longExposure // used in exposure calc
	return e.toOrderRequest(signal), true, ""
}

// DailyLossSizingFactor returns a multiplicative sizing factor based on graduated daily loss tiers.
// Change 2: 0-1% loss: 1.0, 1% loss: 0.5, 1.5% loss: 0.25, 2%+ loss: 0.0
func (e *Engine) DailyLossSizingFactor() float64 {
	cfg := e.getConfig()
	equity := e.portfolio.CurrentEquity()
	if equity <= 0 {
		return 1.0
	}
	dayPnL := e.portfolio.DayPnL()
	if dayPnL >= 0 {
		return 1.0
	}

	lossPct := math.Abs(dayPnL) / equity

	switch {
	case lossPct >= cfg.DailyLossHaltPct:
		return 0.0
	case lossPct >= cfg.DailyLossSeverePct:
		return 0.25
	case lossPct >= cfg.DailyLossModeratePct:
		return 0.50
	default:
		return 1.0
	}
}

// DrawdownSizingFactor returns a multiplicative sizing factor based on drawdown from HWM.
// Change 7: linear scale from 1.0 (no DD) to 0.0 (at MaxAcceptableDrawdown).
func (e *Engine) DrawdownSizingFactor() float64 {
	cfg := e.getConfig()
	if !cfg.DrawdownRiskEnabled {
		return 1.0
	}
	dd := e.portfolio.CurrentDrawdown()
	if dd <= 0 {
		return 1.0
	}
	if cfg.MaxAcceptableDrawdown <= 0 {
		return 1.0
	}
	factor := math.Max(0, 1.0-dd/cfg.MaxAcceptableDrawdown)
	return factor
}

// sectorExposure tracks per-sector position count and notional value.
type sectorExposure struct {
	positionCount int
	notionalValue float64
}

// sectorExposures computes per-sector exposure from the given positions.
func (e *Engine) sectorExposures(positions []domain.Position) map[string]sectorExposure {
	exposures := make(map[string]sectorExposure)
	for _, pos := range positions {
		sector := pos.Sector
		if sector == "" {
			sector = "unknown"
		}
		exp := exposures[sector]
		exp.positionCount++
		exp.notionalValue += math.Abs(pos.MarketValue)
		exposures[sector] = exp
	}
	return exposures
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
		OrderType:        signal.OrderType,
		MarketRegime:     signal.MarketRegime,
		RegimeConfidence: signal.RegimeConfidence,
		Playbook:         signal.Playbook,
		Sector:           signal.Sector,
		AvgDailyVolume:   signal.AvgDailyVolume,
		Timestamp:        signal.Timestamp,
	}
}

func (e *Engine) resetDayIfNeeded(at time.Time) {
	today := markethours.TradingDay(at)
	if today != e.dayKey {
		e.dayKey = today
		e.approved = 0
	}
}
