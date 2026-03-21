package strategy

import (
	"context"
	"fmt"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
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

// CandidateDecision captures the strategy's entry decision and supporting metrics.
type CandidateDecision struct {
	Signal                 domain.TradeSignal
	Emit                   bool
	Reason                 string
	AllowedDistanceHighPct float64
	StrongSqueeze          bool
}

type symbolTradeState struct {
	dayKey       string
	entrySignals int
	lossExits    int
	lastLossAt   time.Time
	entrySides   map[string]bool // sides already entered ("long", "short")
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

// EvaluateCandidate applies the entry rules to a scanner candidate.
func (s *Strategy) EvaluateCandidate(candidate domain.Candidate) (domain.TradeSignal, bool) {
	decision := s.evaluateCandidateDecision(candidate)
	return decision.Signal, decision.Emit
}

// EvaluateCandidateDetailed applies the entry rules and returns the block reason when rejected.
func (s *Strategy) EvaluateCandidateDetailed(candidate domain.Candidate) (domain.TradeSignal, bool, string) {
	decision := s.evaluateCandidateDecision(candidate)
	return decision.Signal, decision.Emit, decision.Reason
}

// EvaluateCandidateDecision applies the entry rules and returns supporting metrics.
func (s *Strategy) EvaluateCandidateDecision(candidate domain.Candidate) CandidateDecision {
	return s.evaluateCandidateDecision(candidate)
}

func (s *Strategy) evaluateOpportunitySwap(candidate domain.Candidate) bool {
	positions := s.portfolio.Positions()
	if len(positions) == 0 {
		return false
	}
	decisionAt := decisionTime(candidate.Timestamp)

	var weakestSymbol string
	weakestR := 999.0
	longestHold := time.Duration(0)

	for _, p := range positions {
		timingPosition := s.timingPosition(p, decisionAt)
		holdingTime := decisionAt.Sub(timingPosition.OpenedAt)
		if holdingTime < 5*time.Minute {
			continue // Give new positions time to breathe
		}

		currentR := currentRMultiple(p, p.LastPrice)
		// We only swap if the position is not already crushing it
		if currentR < 0.5 {
			// Prioritize swapping losers or the ones held longest with no progress
			score := currentR - (holdingTime.Minutes() * 0.05)
			if score < weakestR {
				weakestR = score
				weakestSymbol = p.Symbol
				longestHold = holdingTime
			}
		}
	}

	if weakestSymbol != "" {
		s.reallocationTargets[weakestSymbol] = true
		s.runtime.RecordLog("info", "strategy", fmt.Sprintf("flagged %s for reallocation swap (held %.0f m, currentR %.2f) to capture %s (score %.2f)", weakestSymbol, longestHold.Minutes(), currentRMultiple(s.portfolio.Positions()[0], s.portfolio.Positions()[0].LastPrice), candidate.Symbol, candidate.Score))
		return true
	}

	return false
}

// EvaluateExit applies the managed exit rules to a market tick.
func (s *Strategy) EvaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	signal, ok, _ := s.evaluateExitDetailed(tick)
	return signal, ok
}

// EvaluateExitDetailed applies the exit rules and returns the decision reason.
func (s *Strategy) EvaluateExitDetailed(tick domain.Tick) (domain.TradeSignal, bool, string) {
	return s.evaluateExitDetailed(tick)
}

func (s *Strategy) evaluateCandidate(candidate domain.Candidate) (domain.TradeSignal, bool) {
	decision := s.evaluateCandidateDecision(candidate)
	return decision.Signal, decision.Emit
}

func (s *Strategy) evaluateCandidateDecision(candidate domain.Candidate) CandidateDecision {
	candidate = s.normalizeCandidate(candidate)
	decisionAt := decisionTime(candidate.Timestamp)
	candidate.Direction = domain.NormalizeDirection(candidate.Direction)
	strongSqueeze := s.isStrongSqueeze(candidate)
	allowedDistance := s.allowedBreakoutSlack(candidate)
	if !markethours.IsTradableSessionAt(decisionAt) {
		return CandidateDecision{Reason: "outside-session", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if s.isLateSession(decisionAt) {
		return CandidateDecision{Reason: "late-session-momentum-decay", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if blockReason := s.runtime.EntryBlockReasonAt(decisionAt); blockReason != "" {
		return CandidateDecision{Reason: blockReason, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if s.portfolio.HasPosition(candidate.Symbol) {
		return CandidateDecision{Reason: "has-position", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if lastEntry, exists := s.lastEntryAt[candidate.Symbol]; exists {
		if decisionAt.Sub(lastEntry) < time.Duration(s.config.EntryCooldownSec)*time.Second {
			return CandidateDecision{Reason: "entry-cooldown", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
		}
	}
	symbolState := s.symbolState(candidate.Symbol, decisionAt)
	if symbolState.entrySignals >= 2 {
		return CandidateDecision{Reason: "symbol-daily-cap", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if symbolState.lossExits > 0 {
		return CandidateDecision{Reason: "symbol-loss-lockout", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if symbolState.entrySides[candidate.Direction] {
		return CandidateDecision{Reason: "symbol-side-already-traded", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if ok, reason := s.passesEntryQuality(candidate); !ok {
		return CandidateDecision{Reason: reason, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if domain.IsLong(candidate.Direction) && candidate.BreakoutPct < -allowedDistance {
		return CandidateDecision{Reason: "below-breakout-zone", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	plan, ok, reason := buildEntryPlan(s.config, candidate)
	if !ok {
		return CandidateDecision{Reason: reason, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}

	effectiveCapital := s.portfolio.EffectiveCapital()
	availableCash := s.portfolio.AvailableCash()
	maxExposure := effectiveCapital * s.config.MaxExposurePct
	if s.portfolio.OpenPositionCount() >= s.config.MaxOpenPositions || s.portfolio.Exposure() >= maxExposure || availableCash < candidate.Price {
		if domain.IsLong(candidate.Direction) && candidate.Score >= 16.0 {
			if s.evaluateOpportunitySwap(candidate) {
				return CandidateDecision{Reason: "reallocation-swap-pending", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
			}
		}
		return CandidateDecision{Reason: "max-capacity-reached", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}

	quantity := int64(0)
	riskAmount := effectiveCapital * s.config.RiskPerTradePct
	if plan.RiskPerShare > 0 {
		quantity = int64(riskAmount / plan.RiskPerShare)
	}
	quantity = int64(float64(quantity) * s.positionSizeMultiplier(candidate))
	maxQuantityByCapacity := int64(availableCash / candidate.Price)
	if domain.IsShort(candidate.Direction) {
		availableShortExposure := (effectiveCapital * s.config.MaxShortExposurePct) - s.portfolio.ShortExposure()
		if availableShortExposure < 0 {
			availableShortExposure = 0
		}
		maxQuantityByExposure := int64(availableShortExposure / candidate.Price)
		if maxQuantityByExposure < maxQuantityByCapacity || maxQuantityByCapacity == 0 {
			maxQuantityByCapacity = maxQuantityByExposure
		}
	}
	if maxQuantityByCapacity < quantity {
		quantity = maxQuantityByCapacity
	}
	if quantity < 1 {
		return CandidateDecision{Reason: "max-capacity-reached", AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	s.lastEntryAt[candidate.Symbol] = decisionAt
	symbolState.entrySignals++
	if symbolState.entrySides == nil {
		symbolState.entrySides = make(map[string]bool)
	}
	symbolState.entrySides[candidate.Direction] = true
	s.symbolStates[candidate.Symbol] = symbolState

	signal := domain.TradeSignal{
		Symbol:       candidate.Symbol,
		Side:         domain.OpenBrokerSide(candidate.Direction),
		Intent:       domain.IntentOpen,
		PositionSide: candidate.Direction,
		Price:        candidate.Price,
		Quantity:     quantity,
		StopPrice:    plan.StopPrice,
		RiskPerShare: plan.RiskPerShare,
		EntryATR:     plan.EntryATR,
		SetupType:    plan.SetupType,
		Reason:       entryReason(candidate.Direction, candidate.SetupType),
		Confidence:   1.0,
		MarketRegime: candidate.MarketRegime,
		RegimeConfidence: candidate.RegimeConfidence,
		Playbook:     candidate.Playbook,
		Timestamp:    decisionAt,
	}

	if s.runtime.Recorder() != nil {
		s.runtime.Recorder().RecordIndicatorState(domain.IndicatorSnapshot{
			Symbol:     candidate.Symbol,
			Timestamp:  decisionAt,
			SignalType: "entry",
			Reason:     "entry-signal",
			Indicators: map[string]float64{
				"price":                 candidate.Price,
				"open":                  candidate.Open,
				"gapPercent":            candidate.GapPercent,
				"relativeVolume":        candidate.RelativeVolume,
				"preMarketVolume":       float64(candidate.PreMarketVolume),
				"volume":                float64(candidate.Volume),
				"highOfDay":             candidate.HighOfDay,
				"priceVsOpenPct":        candidate.PriceVsOpenPct,
				"distanceFromHighPct":   candidate.DistanceFromHighPct,
				"oneMinuteReturnPct":    candidate.OneMinuteReturnPct,
				"threeMinuteReturnPct":  candidate.ThreeMinuteReturnPct,
				"volumeRate":            candidate.VolumeRate,
				"volumeLeaderPct":       candidate.VolumeLeaderPct,
				"minutesSinceOpen":      candidate.MinutesSinceOpen,
				"atr":                   candidate.ATR,
				"atrPct":                candidate.ATRPct,
				"vwap":                  candidate.VWAP,
				"priceVsVwapPct":        candidate.PriceVsVWAPPct,
				"breakoutPct":           candidate.BreakoutPct,
				"consolidationRangePct": candidate.ConsolidationRangePct,
				"pullbackDepthPct":      candidate.PullbackDepthPct,
				"closeOffHighPct":       candidate.CloseOffHighPct,
				"setupHigh":             candidate.SetupHigh,
				"setupLow":              candidate.SetupLow,
				"score":                 candidate.Score,
				"rsiMASlope":            candidate.RSIMASlope,
				"fiveMinRange":          candidate.FiveMinRange,
				"priceVsEMA9Pct":        candidate.PriceVsEMA9Pct,
				"emaFast":               candidate.EMAFast,
				"emaSlow":               candidate.EMASlow,
				"directionShort":        boolIndicator(domain.IsShort(candidate.Direction)),
			},
		})
	}

	return CandidateDecision{
		Signal:                 signal,
		Emit:                   true,
		Reason:                 "entry-signal",
		AllowedDistanceHighPct: allowedDistance,
		StrongSqueeze:          strongSqueeze,
	}
}

func (s *Strategy) evaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	signal, ok, _ := s.evaluateExitDetailed(tick)
	return signal, ok
}

func (s *Strategy) evaluateExitDetailed(tick domain.Tick) (domain.TradeSignal, bool, string) {
	decisionAt := decisionTime(tick.Timestamp)
	if !markethours.IsTradableSessionAt(decisionAt) {
		return domain.TradeSignal{}, false, "outside-session"
	}
	position, exists := s.portfolio.Position(tick.Symbol)
	if !exists {
		return domain.TradeSignal{}, false, "no-position"
	}
	position = s.timingPosition(position, decisionAt)
	if lastExit, seen := s.lastExitAt[tick.Symbol]; seen {
		if decisionAt.Sub(lastExit) < time.Duration(s.config.ExitCooldownSec)*time.Second {
			return domain.TradeSignal{}, false, "exit-cooldown"
		}
	}
	if domain.IsShort(position.Side) {
		return s.evaluateShortExit(position, tick, decisionAt)
	}
	return s.evaluateLongExit(position, tick, decisionAt)
}

func (s *Strategy) evaluateLongExit(position domain.Position, tick domain.Tick, decisionAt time.Time) (domain.TradeSignal, bool, string) {
	tradeCfg := s.tradeConfigForPosition(position)
	highWatermark := maxPrice(position.HighestPrice, tick.BarHigh, tick.Price)
	previousStop, previousReason := protectiveStop(tradeCfg, position, position.HighestPrice, firstPositive(position.LastPrice, position.AvgPrice), decisionAt)
	if previousStop <= 0 {
		previousStop, previousReason = protectiveStop(tradeCfg, position, highWatermark, firstPositive(position.LastPrice, tick.Price), decisionAt)
	}
	barOpen := firstPositive(tick.BarOpen, tick.Price)
	barLow := firstPositive(tick.BarLow, tick.Price)
	barClose := firstPositive(tick.Price, barOpen)
	peakReturn := peakRMultiple(position, highWatermark)
	holdingTime := decisionAt.Sub(position.OpenedAt)
	sameDayHold := sameTradingDay(position.OpenedAt, decisionAt)

	reason := ""
	localTime := decisionAt.In(markethours.Location())
	minutes := localTime.Hour()*60 + localTime.Minute()

	switch {
	case minutes >= 15*60+55:
		reason = "end-of-day-liquidation"
		tick.Price = barClose
	case s.reallocationTargets[position.Symbol]:
		delete(s.reallocationTargets, position.Symbol)
		reason = "opportunity-reallocation"
		tick.Price = barOpen
		if tick.Price == 0 {
			tick.Price = barClose
		}
	case barOpen > 0 && previousStop > 0 && barOpen <= previousStop:
		reason = previousReason
		tick.Price = barOpen
	case sameDayHold &&
		holdingTime >= time.Duration(tradeCfg.BreakoutFailureWindowMin)*time.Minute &&
		peakReturn < 1.0 &&
		barLow > 0 &&
		barLow <= failedBreakoutPrice(tradeCfg, position):
		reason = "failed-breakout"
		tick.Price = failedBreakoutPrice(tradeCfg, position)
	case sameDayHold &&
		holdingTime >= time.Duration(tradeCfg.StagnationWindowMin)*time.Minute &&
		peakReturn < tradeCfg.StagnationMinPeakPct:
		reason = "stagnation-time-stop"
		tick.Price = barClose
	case func() bool {
		stopPrice, stopReason := protectiveStop(tradeCfg, position, highWatermark, barClose, decisionAt)
		if stopPrice <= 0 || barLow <= 0 || barLow > stopPrice {
			return false
		}
		reason = stopReason
		tick.Price = stopPrice
		return true
	}():
	default:
		return domain.TradeSignal{}, false, "hold"
	}

	s.lastExitAt[tick.Symbol] = decisionAt
	if reason == "stop-loss" || reason == "failed-breakout" {
		state := s.symbolState(tick.Symbol, decisionAt)
		state.lossExits++
		state.lastLossAt = decisionAt
		s.symbolStates[tick.Symbol] = state
	}

	if s.runtime.Recorder() != nil {
		s.runtime.Recorder().RecordIndicatorState(domain.IndicatorSnapshot{
			Symbol:     tick.Symbol,
			Timestamp:  decisionAt,
			SignalType: "exit",
			Reason:     reason,
			Indicators: map[string]float64{
				"tickPrice":            tick.Price,
				"tickBarOpen":          tick.BarOpen,
				"tickBarHigh":          tick.BarHigh,
				"tickBarLow":           tick.BarLow,
				"tickVolume":           float64(tick.Volume),
				"positionQuantity":     float64(position.Quantity),
				"positionAvgPrice":     position.AvgPrice,
				"positionLastPrice":    position.LastPrice,
				"positionHighestPrice": position.HighestPrice,
				"positionRisk":         position.RiskPerShare,
				"positionATR":          position.EntryATR,
				"highWatermark":        highWatermark,
				"previousStop":         previousStop,
				"peakReturn":           peakReturn,
				"holdingTimeMin":       holdingTime.Minutes(),
			},
		})
	}

	return domain.TradeSignal{
		Symbol:       tick.Symbol,
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        tick.Price,
		Quantity:     position.Quantity,
		StopPrice:    position.StopPrice,
		RiskPerShare: position.RiskPerShare,
		EntryATR:     position.EntryATR,
		SetupType:    position.SetupType,
		Reason:       reason,
		Confidence:   1,
		MarketRegime: position.MarketRegime,
		RegimeConfidence: position.RegimeConfidence,
		Playbook:     position.Playbook,
		Timestamp:    decisionAt,
	}, true, reason
}

func (s *Strategy) evaluateShortExit(position domain.Position, tick domain.Tick, decisionAt time.Time) (domain.TradeSignal, bool, string) {
	tradeCfg := s.tradeConfigForPosition(position)
	lowWatermark := minPrice(position.LowestPrice, tick.BarLow, tick.Price)
	previousStop, previousReason := protectiveStop(tradeCfg, position, position.LowestPrice, firstPositive(position.LastPrice, position.AvgPrice), decisionAt)
	if previousStop <= 0 {
		previousStop, previousReason = protectiveStop(tradeCfg, position, lowWatermark, firstPositive(position.LastPrice, tick.Price), decisionAt)
	}
	barOpen := firstPositive(tick.BarOpen, tick.Price)
	barHigh := firstPositive(tick.BarHigh, tick.Price)
	barClose := firstPositive(tick.Price, barOpen)
	peakReturn := peakRMultiple(position, lowWatermark)
	holdingTime := decisionAt.Sub(position.OpenedAt)
	sameDayHold := sameTradingDay(position.OpenedAt, decisionAt)

	reason := ""
	localTime := decisionAt.In(markethours.Location())
	minutes := localTime.Hour()*60 + localTime.Minute()

	switch {
	case minutes >= 15*60+55:
		reason = "end-of-day-liquidation"
		tick.Price = barClose
	case barOpen > 0 && previousStop > 0 && barOpen >= previousStop:
		reason = previousReason
		tick.Price = barOpen
	case sameDayHold &&
		holdingTime >= time.Duration(tradeCfg.BreakoutFailureWindowMin)*time.Minute &&
		peakReturn < 1.0 &&
		barHigh > 0 &&
		barHigh >= failedBreakoutPrice(tradeCfg, position):
		reason = "failed-breakdown"
		tick.Price = failedBreakoutPrice(tradeCfg, position)
	case sameDayHold &&
		holdingTime >= time.Duration(tradeCfg.StagnationWindowMin)*time.Minute &&
		peakReturn < tradeCfg.StagnationMinPeakPct:
		reason = "stagnation-time-stop"
		tick.Price = barClose
	case func() bool {
		stopPrice, stopReason := protectiveStop(tradeCfg, position, lowWatermark, barClose, decisionAt)
		if stopPrice <= 0 || barHigh <= 0 || barHigh < stopPrice {
			return false
		}
		reason = stopReason
		tick.Price = stopPrice
		return true
	}():
	default:
		return domain.TradeSignal{}, false, "hold"
	}

	s.lastExitAt[tick.Symbol] = decisionAt
	if reason == "stop-loss" || reason == "failed-breakdown" {
		state := s.symbolState(tick.Symbol, decisionAt)
		state.lossExits++
		state.lastLossAt = decisionAt
		s.symbolStates[tick.Symbol] = state
	}

	if s.runtime.Recorder() != nil {
		s.runtime.Recorder().RecordIndicatorState(domain.IndicatorSnapshot{
			Symbol:     tick.Symbol,
			Timestamp:  decisionAt,
			SignalType: "exit",
			Reason:     reason,
			Indicators: map[string]float64{
				"tickPrice":           tick.Price,
				"tickBarOpen":         tick.BarOpen,
				"tickBarHigh":         tick.BarHigh,
				"tickBarLow":          tick.BarLow,
				"tickVolume":          float64(tick.Volume),
				"positionQuantity":    float64(position.Quantity),
				"positionAvgPrice":    position.AvgPrice,
				"positionLastPrice":   position.LastPrice,
				"positionLowestPrice": position.LowestPrice,
				"positionRisk":        position.RiskPerShare,
				"positionATR":         position.EntryATR,
				"lowWatermark":        lowWatermark,
				"previousStop":        previousStop,
				"peakReturn":          peakReturn,
				"holdingTimeMin":      holdingTime.Minutes(),
				"directionShort":      1,
			},
		})
	}

	return domain.TradeSignal{
		Symbol:       tick.Symbol,
		Side:         domain.SideBuy,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionShort,
		Price:        tick.Price,
		Quantity:     position.Quantity,
		StopPrice:    position.StopPrice,
		RiskPerShare: position.RiskPerShare,
		EntryATR:     position.EntryATR,
		SetupType:    position.SetupType,
		Reason:       reason,
		Confidence:   1,
		MarketRegime: position.MarketRegime,
		RegimeConfidence: position.RegimeConfidence,
		Playbook:     position.Playbook,
		Timestamp:    decisionAt,
	}, true, reason
}

func (s *Strategy) timingPosition(position domain.Position, at time.Time) domain.Position {
	if !position.BrokerSeeded {
		return position
	}
	position.OpenedAt = tradingDayStart(at)
	return position
}

func (s *Strategy) normalizeCandidate(candidate domain.Candidate) domain.Candidate {
	candidate.Direction = domain.NormalizeDirection(candidate.Direction)
	if candidate.Price <= 0 {
		return candidate
	}
	if candidate.ATR <= 0 {
		atrPct := candidate.ATRPct
		if atrPct <= 0 {
			atrPct = 4.0
			if candidate.PriceVsOpenPct > 20 {
				atrPct = 6.0
			}
			if candidate.Price < 3 {
				atrPct += 1.0
			}
		}
		candidate.ATRPct = atrPct
		candidate.ATR = roundPrice(candidate.Price * (atrPct / 100))
	}
	if domain.IsShort(candidate.Direction) {
		if candidate.SetupHigh <= 0 {
			candidate.SetupHigh = maxPrice(candidate.HighOfDay, candidate.Price+candidate.ATR)
		}
		if candidate.SetupLow <= 0 {
			candidate.SetupLow = candidate.Price
		}
		if candidate.BreakoutPct == 0 && candidate.SetupLow > 0 {
			candidate.BreakoutPct = ((candidate.Price - candidate.SetupLow) / candidate.SetupLow) * 100
		}
	} else {
		if candidate.SetupHigh <= 0 {
			if candidate.HighOfDay > 0 {
				candidate.SetupHigh = candidate.HighOfDay
			} else {
				candidate.SetupHigh = candidate.Price
			}
		}
		if candidate.SetupLow <= 0 {
			candidate.SetupLow = candidate.Price - candidate.ATR
			if candidate.Open > 0 && candidate.Open < candidate.SetupLow {
				candidate.SetupLow = candidate.Open
			}
		}
		if candidate.BreakoutPct == 0 && candidate.SetupHigh > 0 {
			candidate.BreakoutPct = ((candidate.Price - candidate.SetupHigh) / candidate.SetupHigh) * 100
		}
	}
	if candidate.PriceVsVWAPPct == 0 {
		candidate.PriceVsVWAPPct = candidate.OneMinuteReturnPct
		if candidate.PriceVsVWAPPct == 0 {
			candidate.PriceVsVWAPPct = candidate.ThreeMinuteReturnPct * 0.5
		}
	}
	if candidate.CloseOffHighPct == 0 {
		switch {
		case candidate.DistanceFromHighPct <= 0.40:
			candidate.CloseOffHighPct = 20
		case candidate.DistanceFromHighPct <= 2.00:
			candidate.CloseOffHighPct = 35
		default:
			candidate.CloseOffHighPct = 55
		}
	}
	if candidate.SetupType == "" && candidate.Volume == 0 {
		if domain.IsShort(candidate.Direction) {
			if candidate.PriceVsVWAPPct <= s.config.ShortVWAPBreakMinPct && candidate.ThreeMinuteReturnPct < 0 {
				candidate.SetupType = "parabolic-failed-reclaim-short"
			}
		} else {
			switch {
			case candidate.PriceVsVWAPPct >= -0.10 && candidate.BreakoutPct >= -0.20:
				candidate.SetupType = "consolidation-breakout"
			case candidate.PriceVsVWAPPct >= -0.10 && candidate.ThreeMinuteReturnPct > 0:
				candidate.SetupType = "vwap-reclaim"
			}
		}
	}
	if candidate.LeaderRank <= 0 && candidate.Volume == 0 {
		candidate.LeaderRank = 1
	}
	return candidate
}

func decisionTime(timestamp time.Time) time.Time {
	if timestamp.IsZero() {
		return time.Now()
	}
	return timestamp
}

func tradingDayStart(at time.Time) time.Time {
	if at.IsZero() {
		at = time.Now()
	}
	local := at.In(markethours.Location())
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, markethours.Location())
}

func (s *Strategy) passesEntryQuality(candidate domain.Candidate) (bool, string) {
	strongSqueeze := s.isStrongSqueeze(candidate)
	if domain.IsShort(candidate.Direction) {
		return s.passesShortEntryQuality(candidate)
	}
	// === Cameron-style momentum long rules ===
	// Must be above VWAP — hard requirement, no exceptions
	if candidate.PriceVsVWAPPct < 0 {
		return false, "below-vwap"
	}
	// Must be above 9 EMA (momentum is intact)
	if candidate.PriceVsEMA9Pct < -0.5 {
		return false, "below-ema9"
	}
	// Not chasing — price should be near 9 EMA, not extended far above it
	if candidate.PriceVsEMA9Pct > 4.0 && !strongSqueeze {
		return false, "extended-above-ema9"
	}
	if candidate.PriceVsEMA9Pct > 6.0 {
		return false, "extended-above-ema9"
	}
	// Must have real activity in the last 5 bars
	if candidate.FiveMinRange < 0.5 {
		return false, "range-too-narrow"
	}
	// Cameron: stock must be meaningfully up on the day — don't buy weak gappers
	if candidate.PriceVsOpenPct < 3.0 {
		return false, "weak-intraday-move"
	}
	if candidate.Price < s.config.MinPrice {
		return false, "low-price"
	}
	volumeLeaderPct := s.volumeLeaderPct(candidate)
	minLeaderPct := 0.12
	maxLeaderRank := 5
	if s.isPremarket(candidate.Timestamp) || s.isOpeningSession(candidate.Timestamp) {
		minLeaderPct = 0.18
		maxLeaderRank = 3
	}
	if strongSqueeze {
		minLeaderPct -= 0.03
		maxLeaderRank += 1
	}
	if minLeaderPct < 0.08 {
		minLeaderPct = 0.08
	}
	if candidate.Score < s.config.MinEntryScore && !(strongSqueeze && candidate.Score >= s.config.MinEntryScore-1.5) {
		return false, "low-score"
	}
	// Longs need higher conviction than the minimum baseline
	if candidate.Score < s.config.MinEntryScore+1 && !strongSqueeze {
		return false, "long-needs-conviction"
	}
	if !s.isPremarket(candidate.Timestamp) && sessionMinutesSinceOpen(candidate.Timestamp) > 90 && !strongSqueeze {
		return false, "post-open-chop"
	}
	if s.isOpeningSession(candidate.Timestamp) &&
		candidate.RelativeVolume >= 40 &&
		candidate.PriceVsOpenPct >= 12 &&
		candidate.OneMinuteReturnPct >= 2.5 &&
		candidate.DistanceFromHighPct <= 1.0 {
		return false, "opening-parabolic"
	}
	if s.isParabolicEntry(candidate) {
		return false, "parabolic-spike"
	}
	if candidate.Volume > 0 {
		dollarVolume := entryDollarVolume(candidate)
		if s.isEarlyPremarket(candidate.Timestamp) && dollarVolume < 4_000_000 {
			return false, "thin-premarket"
		}
		if !s.isPremarket(candidate.Timestamp) && dollarVolume < 10_000_000 {
			return false, "thin-session"
		}
	}
	if s.leaderRank(candidate) > maxLeaderRank {
		return false, "secondary-volume"
	}
	if volumeLeaderPct < minLeaderPct {
		return false, "secondary-volume"
	}
	if candidate.SetupType == "" {
		return false, "no-setup"
	}
	if s.isContinuationProfile() {
		if candidate.MinutesSinceOpen < 8 {
			return false, "awaiting-continuation-window"
		}
		if candidate.SetupType == "opening-range-breakout" && candidate.MinutesSinceOpen < 12 {
			return false, "awaiting-continuation-window"
		}
	}
	if !s.hasTimingConfirmation(candidate, strongSqueeze) {
		return false, "no-renewed-volume"
	}
	// Hard caps that apply even for squeeze entries — extreme extension is never safe.
	if candidate.PriceVsVWAPPct > 10.0 {
		return false, "vwap-extension"
	}
	if candidate.DistanceFromHighPct > 8.0 {
		return false, "distance-from-high"
	}
	if candidate.BreakoutPct > 3.0 {
		return false, "chasing-extended-breakout"
	}
	if candidate.PriceVsVWAPPct > 8.0 && !strongSqueeze {
		return false, "vwap-extension"
	}
	if candidate.DistanceFromHighPct > 6.0 && !strongSqueeze {
		return false, "distance-from-high"
	}
	if candidate.PriceVsOpenPct > maxFloat(s.config.MaxPriceVsOpenPct, candidate.ATRPct*6.5) &&
		candidate.BreakoutPct < -0.10 &&
		candidate.PriceVsVWAPPct < 0.50 &&
		candidate.CloseOffHighPct > 35 &&
		!strongSqueeze {
		return false, "too-extended-from-open"
	}
	if candidate.OneMinuteReturnPct < -0.35 && candidate.ThreeMinuteReturnPct < s.config.MinThreeMinuteReturnPct {
		return false, "weak-one-minute-return"
	}
	if candidate.ThreeMinuteReturnPct < -0.20 && candidate.VolumeRate < s.config.MinVolumeRate {
		return false, "weak-three-minute-return"
	}
	if candidate.OneMinuteReturnPct < s.config.MinOneMinuteReturnPct &&
		candidate.ThreeMinuteReturnPct < s.config.MinThreeMinuteReturnPct &&
		candidate.VolumeRate < s.config.MinVolumeRate &&
		candidate.BreakoutPct < -0.20 {
		return false, "weak-follow-through"
	}
	if candidate.VolumeRate < s.config.MinVolumeRate &&
		candidate.RelativeVolume < s.config.MinRelativeVolume+1 &&
		candidate.Score < s.config.MinEntryScore+2 {
		return false, "weak-volume-rate"
	}
	if candidate.CloseOffHighPct > 30 {
		return false, "weak-close"
	}
	if candidate.SetupType == "opening-range-breakout" && candidate.BreakoutPct < 0.10 && !strongSqueeze {
		return false, "weak-breakout-commit"
	}
	if candidate.ATRPct <= 0 {
		return false, "missing-atr"
	}
	return true, ""
}

func (s *Strategy) passesShortEntryQuality(candidate domain.Candidate) (bool, string) {
	if !s.config.EnableShorts {
		return false, "shorts-disabled"
	}
	if candidate.Price < s.config.MinPrice {
		return false, "low-price"
	}
	if candidate.SetupType != "parabolic-failed-reclaim-short" {
		return false, "no-short-setup"
	}
	if candidate.Score < s.config.ShortMinEntryScore {
		return false, "low-score"
	}
	shortVWAPLimit := s.config.ShortVWAPBreakMinPct
	if candidate.SetupType == "parabolic-failed-reclaim-short" {
		shortVWAPLimit = 2.0
	}
	if candidate.PriceVsVWAPPct > shortVWAPLimit {
		return false, "above-vwap"
	}
	if candidate.CloseOffHighPct < 55 {
		return false, "not-closing-weak"
	}
	if candidate.OneMinuteReturnPct > -maxFloat(0.25, s.config.MinOneMinuteReturnPct*0.50) {
		return false, "weak-one-minute-selloff"
	}
	if candidate.ThreeMinuteReturnPct > -maxFloat(0.50, s.config.MinThreeMinuteReturnPct*0.75) {
		return false, "weak-three-minute-selloff"
	}
	if candidate.BreakoutPct > -0.05 {
		return false, "not-below-breakdown-trigger"
	}
	// Indicator confluence: block shorts when momentum is still sharply rising
	if candidate.RSIMASlope >= 3 {
		return false, "rsi-slope-still-rising"
	}
	// Activity filter: stock must be showing meaningful range
	if candidate.FiveMinRange < 0.5 {
		return false, "range-too-narrow"
	}
	// Pre-market shorts need extreme conditions
	if s.isBeforeNineAM(candidate.Timestamp) {
		if s.isBeforeSevenAM(candidate.Timestamp) {
			return false, "short-overnight-block"
		}
	}
	// Post-open shorts need a convincing breakdown
	if s.isAfterNineThirtyAM(candidate.Timestamp) &&
		candidate.DistanceFromHighPct < 50 &&
		candidate.BreakoutPct > -4.5 &&
		candidate.ThreeMinuteReturnPct > -10 {
		return false, "short-needs-stronger-breakdown"
	}
	// Require minimum volatility for shorts
	if candidate.ATRPct < 3.0 {
		return false, "short-needs-volatility"
	}
	if candidate.Open <= 0 || candidate.HighOfDay <= 0 {
		return false, "missing-peak-extension"
	}
	peakExtensionPct := ((candidate.HighOfDay - candidate.Open) / candidate.Open) * 100
	if peakExtensionPct < s.config.ShortPeakExtensionMinPct {
		return false, "insufficient-peak-extension"
	}

	if candidate.Volume > 0 {
		dollarVolume := entryDollarVolume(candidate)
		if s.isEarlyPremarket(candidate.Timestamp) && dollarVolume < 4_000_000 {
			return false, "thin-premarket"
		}
		if !s.isPremarket(candidate.Timestamp) && dollarVolume < 10_000_000 {
			return false, "thin-session"
		}
	}
	if s.leaderRank(candidate) > 3 {
		return false, "secondary-volume"
	}
	if s.volumeLeaderPct(candidate) < 0.18 {
		return false, "secondary-volume"
	}
	return true, ""
}

func (s *Strategy) isStrongSqueeze(candidate domain.Candidate) bool {
	if domain.IsShort(candidate.Direction) {
		return false
	}
	scoreThreshold := s.config.MinEntryScore + 5
	relativeVolumeThreshold := s.config.MinRelativeVolume + 1.5
	threeMinuteThreshold := s.config.MinThreeMinuteReturnPct + 0.40
	volumeRateThreshold := s.config.MinVolumeRate + 0.15
	volumeLeaderThreshold := 0.18
	maxLeaderRank := 3

	if s.isPremarket(candidate.Timestamp) {
		scoreThreshold += 3
		relativeVolumeThreshold += 2.0
		threeMinuteThreshold += 0.40
		volumeRateThreshold += 0.15
		volumeLeaderThreshold = 0.30
		maxLeaderRank = 2
	}
	if s.isOpeningSession(candidate.Timestamp) {
		scoreThreshold += 1.5
		relativeVolumeThreshold += 1.0
		threeMinuteThreshold += 0.20
		volumeRateThreshold += 0.10
		volumeLeaderThreshold = 0.25
		maxLeaderRank = 2
	}

	if s.isParabolicEntry(candidate) {
		return false
	}

	return candidate.Score >= scoreThreshold &&
		candidate.RelativeVolume >= relativeVolumeThreshold &&
		candidate.ThreeMinuteReturnPct >= threeMinuteThreshold &&
		candidate.VolumeRate >= volumeRateThreshold &&
		s.volumeLeaderPct(candidate) >= volumeLeaderThreshold &&
		s.leaderRank(candidate) <= maxLeaderRank &&
		candidate.SetupType != ""
}

func (s *Strategy) hasTimingConfirmation(candidate domain.Candidate, strongSqueeze bool) bool {
	minVolumeRate := maxFloat(s.config.MinVolumeRate, 1.10)
	if strongSqueeze {
		minVolumeRate = maxFloat(0.95, s.config.MinVolumeRate)
	}
	if s.isContinuationProfile() && candidate.MinutesSinceOpen >= 10 {
		minVolumeRate = maxFloat(1.0, minVolumeRate-0.10)
	}

	switch candidate.SetupType {
	case "consolidation-breakout", "opening-range-breakout":
		if candidate.VolumeRate < maxFloat(minVolumeRate, 1.15) {
			return false
		}
		if candidate.OneMinuteReturnPct < 0.10 && candidate.BreakoutPct < 0 {
			return false
		}
		if candidate.CloseOffHighPct > 35 {
			return false
		}
		return true
	case "higher-low-reclaim", "vwap-reclaim":
		if candidate.VolumeRate < minVolumeRate {
			return false
		}
		if s.isContinuationProfile() && candidate.MinutesSinceOpen >= 10 {
			if candidate.PriceVsVWAPPct < 0 {
				return false
			}
			if candidate.OneMinuteReturnPct < -0.05 && candidate.BreakoutPct < -0.20 {
				return false
			}
			return true
		}
		if candidate.OneMinuteReturnPct < 0.05 && candidate.PriceVsVWAPPct < 0 && candidate.BreakoutPct < -0.10 {
			return false
		}
		if candidate.PriceVsVWAPPct < -0.20 {
			return false
		}
		return true
	default:
		return candidate.VolumeRate >= minVolumeRate
	}
}

func (s *Strategy) symbolState(symbol string, at time.Time) symbolTradeState {
	state := s.symbolStates[symbol]
	dayKey := tradingDayKey(at)
	if state.dayKey == dayKey {
		return state
	}
	return symbolTradeState{dayKey: dayKey}
}

func (s *Strategy) allowedBreakoutSlack(candidate domain.Candidate) float64 {
	if domain.IsShort(candidate.Direction) {
		return 0
	}
	allowance := maxFloat(candidate.ATRPct*0.65, 0.35)
	if candidate.SetupType == "vwap-reclaim" {
		allowance += maxFloat(candidate.ATRPct*0.35, 0.20)
	}
	if candidate.SetupType == "consolidation-breakout" {
		allowance += 0.10
	}
	if s.volumeLeaderPct(candidate) >= 0.65 {
		allowance += 0.10
	}
	if s.isContinuationProfile() && candidate.MinutesSinceOpen >= 10 {
		allowance += 0.15
	}
	if allowance > 1.85 {
		allowance = 1.85
	}
	return allowance
}

func (s *Strategy) isPremarket(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(markethours.Location())
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= 4*60 && minutes < 9*60+30
}

func (s *Strategy) isEarlyPremarket(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(markethours.Location())
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= 4*60 && minutes < 7*60
}

func (s *Strategy) isOpeningSession(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(markethours.Location())
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= 9*60+30 && minutes < 9*60+45
}

func (s *Strategy) isLateSession(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(markethours.Location())
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= 15*60+30
}

func (s *Strategy) isBeforeNineAM(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(markethours.Location())
	minutes := local.Hour()*60 + local.Minute()
	return minutes < 9*60
}

func (s *Strategy) isBeforeSevenAM(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(markethours.Location())
	minutes := local.Hour()*60 + local.Minute()
	return minutes < 7*60
}

func (s *Strategy) isAfterNineThirtyAM(at time.Time) bool {
	if at.IsZero() {
		return false
	}
	local := at.In(markethours.Location())
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= 9*60+30
}

func (s *Strategy) isParabolicEntry(candidate domain.Candidate) bool {
	if candidate.OneMinuteReturnPct >= 12 || candidate.ThreeMinuteReturnPct >= 20 {
		return true
	}
	if s.isEarlyPremarket(candidate.Timestamp) && (candidate.OneMinuteReturnPct >= 5 || candidate.ThreeMinuteReturnPct >= 8) {
		return true
	}
	if s.isPremarket(candidate.Timestamp) &&
		candidate.RelativeVolume >= 200 &&
		candidate.ThreeMinuteReturnPct >= 4.5 &&
		candidate.PriceVsOpenPct >= 18 {
		return true
	}
	if s.isOpeningSession(candidate.Timestamp) &&
		candidate.RelativeVolume >= 75 &&
		candidate.ThreeMinuteReturnPct >= 4 &&
		candidate.PriceVsOpenPct >= 15 {
		return true
	}
	return candidate.RelativeVolume >= 100 &&
		candidate.OneMinuteReturnPct >= 4 &&
		candidate.BreakoutPct >= -0.10
}

func (s *Strategy) positionSizeMultiplier(candidate domain.Candidate) float64 {
	multiplier := 1.0
	volumeLeaderPct := s.volumeLeaderPct(candidate)
	if s.isPremarket(candidate.Timestamp) {
		multiplier *= 0.70
	}
	if s.isOpeningSession(candidate.Timestamp) {
		multiplier *= 0.80
	}
	if candidate.Price < 3 {
		multiplier *= 0.90
	}
	if candidate.RelativeVolume >= 100 && candidate.PriceVsOpenPct >= 20 {
		multiplier *= 0.80
	}
	if volumeLeaderPct < 0.55 {
		multiplier *= 0.75
	}
	if s.leaderRank(candidate) > 2 {
		multiplier *= 0.80
	}
	if candidate.VolumeLeaderPct >= 0.90 && !s.isPremarket(candidate.Timestamp) {
		multiplier *= 1.05
	}
	if candidate.SetupType == "vwap-reclaim" {
		multiplier *= 0.85
	}
	if candidate.SetupType == "higher-low-reclaim" {
		multiplier *= 0.95
	}
	if s.isContinuationProfile() {
		multiplier *= 0.90
	}
	// Shorts get reduced sizing
	if domain.IsShort(candidate.Direction) {
		multiplier *= 0.55
	}
	// Longs get moderately reduced sizing until the strategy proves out
	if domain.IsLong(candidate.Direction) {
		multiplier *= 0.80
	}
	if multiplier < 0.55 {
		multiplier = 0.55
	}
	return multiplier
}

func (s *Strategy) tradeConfigForPosition(position domain.Position) config.TradingConfig {
	return s.config
}

func (s *Strategy) isContinuationProfile() bool {
	return s.config.StrategyProfileName == string(config.StrategyProfileContinuation)
}

func (s *Strategy) volumeLeaderPct(candidate domain.Candidate) float64 {
	if candidate.VolumeLeaderPct <= 0 && candidate.Volume == 0 {
		return 1
	}
	return candidate.VolumeLeaderPct
}

func (s *Strategy) leaderRank(candidate domain.Candidate) int {
	if candidate.LeaderRank <= 0 && candidate.Volume == 0 {
		return 1
	}
	if candidate.LeaderRank <= 0 {
		return 999
	}
	return candidate.LeaderRank
}

func sameTradingDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return tradingDayKey(a) == tradingDayKey(b)
}

func entryDollarVolume(candidate domain.Candidate) float64 {
	return candidate.Price * float64(candidate.Volume)
}

func maxFloat(values ...float64) float64 {
	maximum := 0.0
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func maxPrice(values ...float64) float64 {
	return maxFloat(values...)
}

func minPrice(values ...float64) float64 {
	minimum := 0.0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if minimum == 0 || value < minimum {
			minimum = value
		}
	}
	return minimum
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func tradingDayKey(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.In(markethours.Location()).Format("2006-01-02")
}

func sessionMinutesSinceOpen(at time.Time) float64 {
	if at.IsZero() {
		return 0
	}
	local := at.In(markethours.Location())
	open := time.Date(local.Year(), local.Month(), local.Day(), 9, 30, 0, 0, local.Location())
	return maxFloat(0, local.Sub(open).Minutes())
}

func entryReason(direction string, setupType string) string {
	if domain.IsShort(direction) {
		return "parabolic-failed-reclaim-short-entry"
	}
	return "ml-breakout-entry"
}

func boolIndicator(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
