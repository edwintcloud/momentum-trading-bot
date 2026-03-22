package strategy

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/sector"
)

// Strategy implements breakout entries and managed exits.
type Strategy struct {
	config              config.TradingConfig
	portfolio           *portfolio.Manager
	runtime             *runtime.State
	riskEngine          *risk.Engine
	volEstimator        *risk.VolatilityEstimator
	lastEntryAt         map[string]time.Time
	lastExitAt          map[string]time.Time
	symbolStates        map[string]symbolTradeState
	reallocationTargets map[string]bool
}

type symbolTradeState struct {
	dayKey       string
	entrySignals int
	lossExits    int
	lastLossAt   time.Time
	entrySides   map[string]bool
}

// NewStrategy creates a strategy instance.
// Optional variadic args: riskEngine *risk.Engine, volEstimator *risk.VolatilityEstimator
func NewStrategy(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State, opts ...interface{}) *Strategy {
	s := &Strategy{
		config:              cfg,
		portfolio:           portfolioManager,
		runtime:             runtimeState,
		lastEntryAt:         make(map[string]time.Time),
		lastExitAt:          make(map[string]time.Time),
		symbolStates:        make(map[string]symbolTradeState),
		reallocationTargets: make(map[string]bool),
	}
	for _, opt := range opts {
		switch v := opt.(type) {
		case *risk.Engine:
			s.riskEngine = v
		case *risk.VolatilityEstimator:
			s.volEstimator = v
		}
	}
	return s
}

// UpdateConfig replaces the strategy's trading config.
func (s *Strategy) UpdateConfig(cfg config.TradingConfig) {
	s.config = cfg
}

// Start listens for candidates and ticks, generating both entry and exit signals.
func (s *Strategy) Start(ctx context.Context, candidates <-chan domain.Candidate, ticks <-chan domain.Tick, out chan<- domain.TradeSignal) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case candidate, ok := <-candidates:
			if !ok {
				return fmt.Errorf("strategy candidates channel closed")
			}
			signal, shouldEmit := s.evaluateCandidate(candidate)
			if !shouldEmit {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- signal:
			}
		case tick, ok := <-ticks:
			if !ok {
				return fmt.Errorf("strategy ticks channel closed")
			}
			signal, shouldEmit := s.evaluateExit(tick)
			if !shouldEmit {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- signal:
			}
		}
	}
}

// EvaluateCandidate applies entry rules (exported for backtesting).
func (s *Strategy) EvaluateCandidate(candidate domain.Candidate) (domain.TradeSignal, bool) {
	return s.evaluateCandidate(candidate)
}

// EvaluateExit applies exit rules (exported for backtesting).
func (s *Strategy) EvaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	return s.evaluateExit(tick)
}

// EvaluateExitDetailed applies exit rules and returns the reason.
func (s *Strategy) EvaluateExitDetailed(tick domain.Tick) (domain.TradeSignal, bool, string) {
	signal, ok := s.evaluateExit(tick)
	return signal, ok, signal.Reason
}

// CandidateDecision captures the full decision context for a candidate evaluation.
type CandidateDecision struct {
	Signal                 domain.TradeSignal
	Emit                   bool
	Reason                 string
	PredictedReturnPct     float64
	RequiredReturnPct      float64
	AllowedDistanceHighPct float64
	StrongSqueeze          bool
}

// EvaluateCandidateDecision returns a rich decision object for a candidate.
func (s *Strategy) EvaluateCandidateDecision(candidate domain.Candidate) CandidateDecision {
	signal, ok := s.evaluateCandidate(candidate)
	reason := "no-signal"
	if ok {
		reason = signal.Reason
	} else if candidate.Score < s.config.MinEntryScore {
		reason = "low-score"
	} else if s.runtime.IsPaused() || s.runtime.IsEmergencyStopped() {
		reason = "system-paused"
	} else if _, exists := s.portfolio.GetPosition(candidate.Symbol); exists {
		reason = "existing-position"
	}
	return CandidateDecision{
		Signal: signal,
		Emit:   ok,
		Reason: reason,
	}
}

func (s *Strategy) evaluateCandidate(c domain.Candidate) (domain.TradeSignal, bool) {
	now := c.Timestamp
	if !markethours.IsMarketOpen(now) {
		return domain.TradeSignal{}, false
	}

	// Check if paused or emergency stopped
	if s.runtime.IsPaused() || s.runtime.IsEmergencyStopped() {
		return domain.TradeSignal{}, false
	}

	// Cooldown check
	if last, ok := s.lastEntryAt[c.Symbol]; ok {
		if now.Sub(last) < time.Duration(s.config.EntryCooldownSec)*time.Second {
			return domain.TradeSignal{}, false
		}
	}

	// Already have a position in this symbol
	if _, exists := s.portfolio.GetPosition(c.Symbol); exists {
		return domain.TradeSignal{}, false
	}

	// Day state
	dayKey := markethours.TradingDay(now)
	state := s.getSymbolState(c.Symbol, dayKey)

	// Check if already entered this side today
	if state.entrySides[c.Direction] {
		return domain.TradeSignal{}, false
	}

	// Too many losses on this symbol today
	if state.lossExits >= 2 && now.Sub(state.lastLossAt) < 30*time.Minute {
		return domain.TradeSignal{}, false
	}

	// Score threshold already checked in scanner, but double-check
	minScore := s.config.MinEntryScore
	if c.Direction == domain.DirectionShort {
		minScore = s.config.ShortMinEntryScore
	}
	if c.Score < minScore {
		return domain.TradeSignal{}, false
	}

	// Regime gating
	if s.config.RegimeGatingEnabled {
		switch c.MarketRegime {
		case domain.RegimeBearish:
			if c.Direction == domain.DirectionLong {
				return domain.TradeSignal{}, false
			}
		case domain.RegimeBullish:
			if c.Direction == domain.DirectionShort {
				return domain.TradeSignal{}, false
			}
		case domain.RegimeMixed:
			boosted := minScore * s.config.RegimeMixedScoreBoost
			if c.Score < boosted {
				return domain.TradeSignal{}, false
			}
		case domain.RegimeNeutral:
			boosted := minScore * s.config.RegimeNeutralScoreBoost
			if c.Score < boosted {
				return domain.TradeSignal{}, false
			}
		}
	}

	// Compute position sizing
	riskPerShare := s.computeRiskPerShare(c)
	if riskPerShare <= 0 {
		return domain.TradeSignal{}, false
	}

	currentEquity := s.portfolio.CurrentEquity()
	if currentEquity <= 0 {
		currentEquity = s.config.StartingCapital
	}

	// Phase 2 Change 5: Kelly Criterion sizing
	riskPct := s.config.RiskPerTradePct
	if s.config.KellySizingEnabled {
		winRate, wlRatio, tradeCount := s.portfolio.RollingTradeStats(s.config.KellyWindowSize)
		if tradeCount >= s.config.KellyMinTrades {
			kellyF := KellyFraction(winRate, wlRatio)
			fractionalKelly := kellyF * s.config.KellyFraction
			if fractionalKelly > s.config.MaxKellyRiskPct {
				fractionalKelly = s.config.MaxKellyRiskPct
			}
			if fractionalKelly > 0 {
				riskPct = fractionalKelly
			}
		}
	}

	riskBudget := currentEquity * riskPct

	// Scale position size by confidence (Phase 1 Change 7)
	if s.config.ConfidenceSizingEnabled {
		confidence := c.Score / 8.0
		if confidence > 1.0 {
			confidence = 1.0
		}
		floor := s.config.ConfidenceSizingFloor
		sizeMultiplier := floor + (1.0-floor)*confidence
		riskBudget *= sizeMultiplier
	}

	// Phase 2 Change 2: Graduated daily loss sizing factor
	if s.riskEngine != nil {
		dailyLossFactor := s.riskEngine.DailyLossSizingFactor()
		if dailyLossFactor <= 0 {
			return domain.TradeSignal{}, false
		}
		if dailyLossFactor < 1.0 {
			s.runtime.RecordLog("info", "strategy", fmt.Sprintf("daily loss response: sizing reduced to %.0f%%", dailyLossFactor*100))
		}
		riskBudget *= dailyLossFactor
	}

	// Phase 2 Change 7: Drawdown-based risk reduction
	if s.riskEngine != nil {
		drawdownFactor := s.riskEngine.DrawdownSizingFactor()
		if drawdownFactor <= 0 {
			return domain.TradeSignal{}, false
		}
		riskBudget *= drawdownFactor
	}

	// Update HWM tracking
	s.portfolio.UpdateEquityTracking()

	quantity := int64(math.Floor(riskBudget / riskPerShare))
	if quantity <= 0 {
		return domain.TradeSignal{}, false
	}

	// Phase 2 Change 6: Volatility-based position sizing cap
	if s.config.VolTargetSizingEnabled && s.volEstimator != nil {
		stockVol := s.volEstimator.GetVolatility(c.Symbol)
		if stockVol > 0 {
			targetDollarVol := currentEquity * s.config.TargetVolPerPosition
			positionDollar := targetDollarVol / stockVol
			volBasedQty := int64(positionDollar / c.Price)
			if volBasedQty > 0 && volBasedQty < quantity {
				quantity = volBasedQty
			}
		}
	}

	var stopPrice float64
	if domain.IsLong(c.Direction) {
		stopPrice = c.Price - riskPerShare
	} else {
		stopPrice = c.Price + riskPerShare
	}

	// Resolve sector for the candidate
	candidateSector := c.Sector
	if candidateSector == "" {
		candidateSector = sector.SectorForSymbol(c.Symbol)
	}

	signal := domain.TradeSignal{
		Symbol:           c.Symbol,
		Side:             domain.OpenBrokerSide(c.Direction),
		Intent:           domain.IntentOpen,
		PositionSide:     c.Direction,
		Price:            c.Price,
		Quantity:         quantity,
		StopPrice:        stopPrice,
		RiskPerShare:     riskPerShare,
		EntryATR:         c.ATR,
		SetupType:        c.SetupType,
		Reason:           fmt.Sprintf("scanner score=%.1f setup=%s", c.Score, c.SetupType),
		Confidence:       c.Score / 8.0,
		MarketRegime:     c.MarketRegime,
		RegimeConfidence: c.RegimeConfidence,
		Playbook:         c.Playbook,
		Sector:           candidateSector,
		Timestamp:        now,
	}

	s.lastEntryAt[c.Symbol] = now
	state.entrySignals++
	state.entrySides[c.Direction] = true
	s.symbolStates[c.Symbol] = state

	return signal, true
}

func (s *Strategy) evaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	pos, exists := s.portfolio.GetPosition(tick.Symbol)
	if !exists {
		return domain.TradeSignal{}, false
	}

	now := tick.Timestamp

	// Cooldown
	if last, ok := s.lastExitAt[tick.Symbol]; ok {
		if now.Sub(last) < time.Duration(s.config.ExitCooldownSec)*time.Second {
			return domain.TradeSignal{}, false
		}
	}

	s.portfolio.UpdatePrice(tick.Symbol, tick.Price)

	// Check exit conditions in priority order
	reason, shouldExit := s.checkExitConditions(pos, tick)
	if !shouldExit {
		// Update trailing stop
		s.updateTrailingStop(pos, tick)
		return domain.TradeSignal{}, false
	}

	signal := domain.TradeSignal{
		Symbol:       tick.Symbol,
		Side:         domain.CloseBrokerSide(pos.Side),
		Intent:       domain.IntentClose,
		PositionSide: pos.Side,
		Price:        tick.Price,
		Quantity:     pos.Quantity,
		Reason:       reason,
		Timestamp:    now,
	}

	s.lastExitAt[tick.Symbol] = now

	// Track losses
	pnl := (tick.Price - pos.AvgPrice) * float64(pos.Quantity)
	if domain.IsShort(pos.Side) {
		pnl = -pnl
	}
	if pnl < 0 {
		dayKey := markethours.TradingDay(now)
		state := s.getSymbolState(tick.Symbol, dayKey)
		state.lossExits++
		state.lastLossAt = now
		s.symbolStates[tick.Symbol] = state
	}

	return signal, true
}

func (s *Strategy) getPlaybookExitConfig(playbook string) config.PlaybookExitConfig {
	switch playbook {
	case "breakout":
		return s.config.PlaybookExits.Breakout
	case "pullback":
		return s.config.PlaybookExits.Pullback
	case "continuation":
		return s.config.PlaybookExits.Continuation
	case "reversal":
		return s.config.PlaybookExits.Reversal
	default:
		return s.config.PlaybookExits.Breakout
	}
}

func (s *Strategy) checkExitConditions(pos domain.Position, tick domain.Tick) (string, bool) {
	r := s.currentR(pos, tick.Price)
	exitCfg := s.getPlaybookExitConfig(pos.Playbook)

	// Hard stop
	if domain.IsLong(pos.Side) && tick.Price <= pos.StopPrice {
		return "stop-loss", true
	}
	if domain.IsShort(pos.Side) && tick.Price >= pos.StopPrice {
		return "stop-loss", true
	}

	// Profit target (playbook-specific)
	if r >= exitCfg.ProfitTargetR {
		return "profit-target", true
	}

	// Failed breakout cut (playbook-specific)
	if r <= exitCfg.FailedBreakoutCutR {
		holdMinutes := tick.Timestamp.Sub(pos.OpenedAt).Minutes()
		if holdMinutes < float64(exitCfg.BreakoutFailureWindowMin) {
			return "failed-breakout", true
		}
	}

	// End of day — close 15 minutes before market close
	closeTime := markethours.MarketClose(tick.Timestamp)
	if tick.Timestamp.After(closeTime.Add(-15 * time.Minute)) {
		return "end-of-day", true
	}

	// Stagnation check (Change 8 fix: use peakR directly, not pct/100)
	holdMinutes := tick.Timestamp.Sub(pos.OpenedAt).Minutes()
	peakR := s.peakR(pos)
	if holdMinutes > float64(exitCfg.StagnationWindowMin) && peakR < exitCfg.StagnationMinPeakR {
		return "stagnation", true
	}

	return "", false
}

func (s *Strategy) updateTrailingStop(pos domain.Position, tick domain.Tick) {
	r := s.currentR(pos, tick.Price)
	exitCfg := s.getPlaybookExitConfig(pos.Playbook)

	var newStop float64
	if domain.IsLong(pos.Side) {
		// Break-even stop
		if r >= s.config.BreakEvenMinR && pos.StopPrice < pos.AvgPrice {
			newStop = pos.AvgPrice + pos.EntryATR*0.1
		}

		// Trailing stop activation (playbook-specific)
		if r >= exitCfg.TrailActivationR {
			trailStop := tick.Price - pos.EntryATR*exitCfg.TrailATRMultiplier
			if trailStop > pos.StopPrice {
				newStop = trailStop
			}
		}

		// Tight trail (playbook-specific)
		if r >= exitCfg.TightTrailTriggerR {
			tightStop := tick.Price - pos.EntryATR*exitCfg.TightTrailATRMultiplier
			if tightStop > pos.StopPrice {
				newStop = tightStop
			}
		}
	} else {
		// Short trailing stops (mirrored)
		if r >= s.config.BreakEvenMinR && pos.StopPrice > pos.AvgPrice {
			newStop = pos.AvgPrice - pos.EntryATR*0.1
		}
		if r >= exitCfg.TrailActivationR {
			trailStop := tick.Price + pos.EntryATR*exitCfg.TrailATRMultiplier
			if trailStop < pos.StopPrice || pos.StopPrice == 0 {
				newStop = trailStop
			}
		}
		if r >= exitCfg.TightTrailTriggerR {
			tightStop := tick.Price + pos.EntryATR*exitCfg.TightTrailATRMultiplier
			if tightStop < pos.StopPrice || pos.StopPrice == 0 {
				newStop = tightStop
			}
		}
	}

	if newStop > 0 {
		s.portfolio.UpdateStopPrice(pos.Symbol, newStop)
	}
}

// peakR computes the peak R-multiple reached during the position's life.
func (s *Strategy) peakR(pos domain.Position) float64 {
	if pos.RiskPerShare <= 0 {
		return 0
	}
	if domain.IsLong(pos.Side) {
		return (pos.HighestPrice - pos.AvgPrice) / pos.RiskPerShare
	}
	return (pos.AvgPrice - pos.LowestPrice) / pos.RiskPerShare
}

func (s *Strategy) currentR(pos domain.Position, price float64) float64 {
	if pos.RiskPerShare <= 0 {
		return 0
	}
	if domain.IsLong(pos.Side) {
		return (price - pos.AvgPrice) / pos.RiskPerShare
	}
	return (pos.AvgPrice - price) / pos.RiskPerShare
}

func (s *Strategy) computeRiskPerShare(c domain.Candidate) float64 {
	if c.ATR > 0 {
		risk := c.ATR * s.config.EntryStopATRMultiplier
		maxRisk := c.ATR * s.config.MaxRiskATRMultiplier
		if risk > maxRisk {
			risk = maxRisk
		}
		return risk
	}
	return c.Price * s.config.EntryATRPercentFallback / 100
}

func (s *Strategy) getSymbolState(symbol, dayKey string) symbolTradeState {
	state, ok := s.symbolStates[symbol]
	if !ok || state.dayKey != dayKey {
		state = symbolTradeState{
			dayKey:     dayKey,
			entrySides: make(map[string]bool),
		}
	}
	return state
}

// KellyFraction computes the optimal Kelly bet fraction.
// f* = (b*p - q) / b where p=win rate, q=loss rate, b=avg win/avg loss
func KellyFraction(winRate, avgWinLossRatio float64) float64 {
	if avgWinLossRatio <= 0 || winRate <= 0 || winRate >= 1 {
		return 0
	}
	b := avgWinLossRatio
	p := winRate
	q := 1 - p
	kelly := (b*p - q) / b
	if kelly < 0 {
		return 0
	}
	return kelly
}
