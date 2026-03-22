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
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

// Strategy implements breakout entries and managed exits.
type Strategy struct {
	config              config.TradingConfig
	portfolio           *portfolio.Manager
	runtime             *runtime.State
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
func NewStrategy(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State) *Strategy {
	return &Strategy{
		config:              cfg,
		portfolio:           portfolioManager,
		runtime:             runtimeState,
		lastEntryAt:         make(map[string]time.Time),
		lastExitAt:          make(map[string]time.Time),
		symbolStates:        make(map[string]symbolTradeState),
		reallocationTargets: make(map[string]bool),
	}
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

	// Compute position sizing
	riskPerShare := s.computeRiskPerShare(c)
	if riskPerShare <= 0 {
		return domain.TradeSignal{}, false
	}

	riskBudget := s.config.StartingCapital * s.config.RiskPerTradePct
	quantity := int64(math.Floor(riskBudget / riskPerShare))
	if quantity <= 0 {
		return domain.TradeSignal{}, false
	}

	var stopPrice float64
	if domain.IsLong(c.Direction) {
		stopPrice = c.Price - riskPerShare
	} else {
		stopPrice = c.Price + riskPerShare
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
		Confidence:       c.Score / 6.0,
		MarketRegime:     c.MarketRegime,
		RegimeConfidence: c.RegimeConfidence,
		Playbook:         c.Playbook,
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

func (s *Strategy) checkExitConditions(pos domain.Position, tick domain.Tick) (string, bool) {
	r := s.currentR(pos, tick.Price)

	// Hard stop
	if domain.IsLong(pos.Side) && tick.Price <= pos.StopPrice {
		return "stop-loss", true
	}
	if domain.IsShort(pos.Side) && tick.Price >= pos.StopPrice {
		return "stop-loss", true
	}

	// Profit target
	if r >= s.config.ProfitTargetR {
		return "profit-target", true
	}

	// Failed breakout cut
	if r <= s.config.FailedBreakoutCutR {
		holdMinutes := time.Since(pos.OpenedAt).Minutes()
		if holdMinutes < float64(s.config.BreakoutFailureWindowMin) {
			return "failed-breakout", true
		}
	}

	// End of day — close 15 minutes before market close
	closeTime := markethours.MarketClose(tick.Timestamp)
	if tick.Timestamp.After(closeTime.Add(-15 * time.Minute)) {
		return "end-of-day", true
	}

	// Stagnation check
	holdMinutes := tick.Timestamp.Sub(pos.OpenedAt).Minutes()
	if holdMinutes > float64(s.config.StagnationWindowMin) && r < s.config.StagnationMinPeakPct/100 {
		return "stagnation", true
	}

	return "", false
}

func (s *Strategy) updateTrailingStop(pos domain.Position, tick domain.Tick) {
	r := s.currentR(pos, tick.Price)

	var newStop float64
	if domain.IsLong(pos.Side) {
		// Break-even stop
		if r >= s.config.BreakEvenMinR && pos.StopPrice < pos.AvgPrice {
			newStop = pos.AvgPrice + pos.EntryATR*0.1
		}

		// Trailing stop activation
		if r >= s.config.TrailActivationR {
			trailStop := tick.Price - pos.EntryATR*s.config.TrailATRMultiplier
			if trailStop > pos.StopPrice {
				newStop = trailStop
			}
		}

		// Tight trail
		if r >= s.config.TightTrailTriggerR {
			tightStop := tick.Price - pos.EntryATR*s.config.TightTrailATRMultiplier
			if tightStop > pos.StopPrice {
				newStop = tightStop
			}
		}
	} else {
		// Short trailing stops (mirrored)
		if r >= s.config.BreakEvenMinR && pos.StopPrice > pos.AvgPrice {
			newStop = pos.AvgPrice - pos.EntryATR*0.1
		}
		if r >= s.config.TrailActivationR {
			trailStop := tick.Price + pos.EntryATR*s.config.TrailATRMultiplier
			if trailStop < pos.StopPrice || pos.StopPrice == 0 {
				newStop = trailStop
			}
		}
		if r >= s.config.TightTrailTriggerR {
			tightStop := tick.Price + pos.EntryATR*s.config.TightTrailATRMultiplier
			if tightStop < pos.StopPrice || pos.StopPrice == 0 {
				newStop = tightStop
			}
		}
	}

	if newStop > 0 {
		s.portfolio.UpdateStopPrice(pos.Symbol, newStop)
	}
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
