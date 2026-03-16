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
	config       config.TradingConfig
	portfolio    *portfolio.Manager
	runtime      *runtime.State
	seedModel    LinearModel
	entryModel   LinearModel
	lastEntryAt  map[string]time.Time
	lastExitAt   map[string]time.Time
	symbolStates map[string]symbolTradeState
}

// CandidateDecision captures the strategy's entry decision and supporting metrics.
type CandidateDecision struct {
	Signal                 domain.TradeSignal
	Emit                   bool
	Reason                 string
	PredictedReturnPct     float64
	RequiredReturnPct      float64
	AllowedDistanceHighPct float64
	StrongSqueeze          bool
}

type symbolTradeState struct {
	dayKey       string
	entrySignals int
	lossExits    int
	lastLossAt   time.Time
}

// NewStrategy creates a strategy instance.
func NewStrategy(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State) *Strategy {
	seedModel := DefaultEntryModel()
	entryModel := seedModel
	if cfg.EntryModelPath != "" {
		loaded, err := LoadLinearModel(cfg.EntryModelPath)
		if err != nil {
			runtimeState.RecordLog("warn", "strategy", "could not load entry model from "+cfg.EntryModelPath+": "+err.Error())
		} else {
			entryModel = loaded
			runtimeState.RecordLog("info", "strategy", "loaded entry model "+entryModel.Name)
		}
	}
	return &Strategy{
		config:       cfg,
		portfolio:    portfolioManager,
		runtime:      runtimeState,
		seedModel:    seedModel,
		entryModel:   entryModel,
		lastEntryAt:  make(map[string]time.Time),
		lastExitAt:   make(map[string]time.Time),
		symbolStates: make(map[string]symbolTradeState),
	}
}

// SetEntryModel swaps the active entry model at runtime.
func (s *Strategy) SetEntryModel(model LinearModel) {
	s.entryModel = model
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
	decisionAt := decisionTime(candidate.Timestamp)
	strongSqueeze := s.isStrongSqueeze(candidate)
	allowedDistance := s.allowedDistanceFromHigh(candidate)
	predictedReturn := s.predictEntryReturn(candidate, strongSqueeze)
	requiredReturn := s.requiredPredictedReturn(candidate)
	if !markethours.IsTradableSessionAt(decisionAt) {
		return CandidateDecision{Reason: "outside-session", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if blockReason := s.runtime.EntryBlockReasonAt(decisionAt); blockReason != "" {
		return CandidateDecision{Reason: blockReason, PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if s.portfolio.HasPosition(candidate.Symbol) {
		return CandidateDecision{Reason: "has-position", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if lastEntry, exists := s.lastEntryAt[candidate.Symbol]; exists {
		if decisionAt.Sub(lastEntry) < time.Duration(s.config.EntryCooldownSec)*time.Second {
			return CandidateDecision{Reason: "entry-cooldown", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
		}
	}
	symbolState := s.symbolState(candidate.Symbol, decisionAt)
	if symbolState.entrySignals >= 2 {
		return CandidateDecision{Reason: "symbol-daily-cap", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if symbolState.lossExits > 0 && decisionAt.Sub(symbolState.lastLossAt) < 30*time.Minute {
		return CandidateDecision{Reason: "post-loss-cooldown", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if ok, reason := s.passesEntryQuality(candidate); !ok {
		return CandidateDecision{Reason: reason, PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if candidate.DistanceFromHighPct > allowedDistance {
		return CandidateDecision{Reason: "below-breakout-zone", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if s.config.EntryModelEnabled && predictedReturn < requiredReturn {
		return CandidateDecision{Reason: "model-threshold", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}

	quantity := int64(0)
	riskAmount := s.portfolio.EffectiveCapital() * s.config.RiskPerTradePct
	stopDistance := candidate.Price * s.config.StopLossPct
	if stopDistance > 0 {
		quantity = int64(riskAmount / stopDistance)
	}
	quantity = int64(float64(quantity) * s.positionSizeMultiplier(candidate))
	if quantity < 1 {
		quantity = 1
	}
	s.lastEntryAt[candidate.Symbol] = decisionAt
	symbolState.entrySignals++
	s.symbolStates[candidate.Symbol] = symbolState

	signal := domain.TradeSignal{
		Symbol:     candidate.Symbol,
		Side:       "buy",
		Price:      candidate.Price,
		Quantity:   quantity,
		Reason:     "ml-breakout-entry",
		Confidence: predictedReturn,
		Timestamp:  decisionAt,
	}
	return CandidateDecision{
		Signal:                 signal,
		Emit:                   true,
		Reason:                 "entry-signal",
		PredictedReturnPct:     predictedReturn,
		RequiredReturnPct:      requiredReturn,
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
	if lastExit, seen := s.lastExitAt[tick.Symbol]; seen {
		if decisionAt.Sub(lastExit) < time.Duration(s.config.ExitCooldownSec)*time.Second {
			return domain.TradeSignal{}, false, "exit-cooldown"
		}
	}

	stopPrice := position.AvgPrice * (1 - s.config.StopLossPct)
	trailingStop := position.HighestPrice * (1 - s.config.TrailingStopPct)
	trailingActivationPrice := position.AvgPrice * (1 + s.config.TrailingStopActivationPct)
	currentReturn := priceReturn(position.AvgPrice, tick.Price)
	peakReturn := priceReturn(position.AvgPrice, position.HighestPrice)
	holdingTime := decisionAt.Sub(position.OpenedAt)
	sameDayHold := sameTradingDay(position.OpenedAt, decisionAt)

	reason := ""
	switch {
	case tick.Price <= stopPrice:
		reason = "stop-loss"
	case sameDayHold &&
		holdingTime >= time.Duration(s.config.BreakoutFailureWindowMin)*time.Minute &&
		peakReturn < s.config.BreakEvenActivationPct &&
		currentReturn <= -s.config.BreakoutFailureLossPct:
		reason = "failed-breakout"
	case peakReturn >= s.config.BreakEvenActivationPct && currentReturn <= s.config.BreakEvenFloorPct:
		reason = "break-even-stop"
	case sameDayHold &&
		holdingTime >= time.Duration(s.config.StagnationWindowMin)*time.Minute &&
		peakReturn < s.config.StagnationMinPeakPct &&
		currentReturn <= 0:
		reason = "time-stop"
	case position.HighestPrice >= trailingActivationPrice && tick.Price <= trailingStop:
		reason = "trailing-stop"
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
	return domain.TradeSignal{
		Symbol:     tick.Symbol,
		Side:       "sell",
		Price:      tick.Price,
		Quantity:   position.Quantity,
		Reason:     reason,
		Confidence: 1,
		Timestamp:  decisionAt,
	}, true, reason
}

func decisionTime(timestamp time.Time) time.Time {
	if timestamp.IsZero() {
		return time.Now().UTC()
	}
	return timestamp.UTC()
}

func (s *Strategy) requiredPredictedReturn(candidate domain.Candidate) float64 {
	threshold := s.config.EntryModelMinPredictedReturnPct
	strongSqueeze := s.isStrongSqueeze(candidate)
	volumeLeaderPct := s.volumeLeaderPct(candidate)
	if volumeLeaderPct >= 0.85 {
		threshold -= 0.20
	} else if volumeLeaderPct < 0.45 {
		threshold += 0.20
	}
	if s.isPremarket(candidate.Timestamp) && !strongSqueeze {
		threshold += 0.20
	}
	if s.isOpeningSession(candidate.Timestamp) {
		threshold += 0.35
	}
	if candidate.MinutesSinceOpen > 180 && !strongSqueeze {
		threshold += 0.30
	} else if candidate.MinutesSinceOpen > 90 && !strongSqueeze {
		threshold += 0.15
	}
	if strongSqueeze {
		threshold -= 0.80
	}
	if candidate.Score >= s.config.MinEntryScore+4 {
		threshold -= 0.35
	}
	if candidate.RelativeVolume >= s.config.MinRelativeVolume+2 {
		threshold -= 0.20
	}
	if candidate.Score < s.config.MinEntryScore+2 {
		threshold += 0.25
	}
	if candidate.VolumeRate < s.config.MinVolumeRate+0.25 {
		threshold += 0.15
	}
	if candidate.OneMinuteReturnPct < 0 {
		threshold += 0.20
	}
	if candidate.PriceVsOpenPct > s.config.MaxPriceVsOpenPct-2 && !strongSqueeze {
		threshold += 0.20
	}
	if candidate.DistanceFromHighPct <= 1.25 &&
		candidate.ThreeMinuteReturnPct >= s.config.MinThreeMinuteReturnPct+0.25 &&
		candidate.VolumeRate >= s.config.MinVolumeRate {
		threshold -= 0.20
	}
	minThreshold := s.config.EntryModelMinPredictedReturnPct * 0.30
	if strongSqueeze {
		minThreshold = 0.15
	}
	if strongSqueeze && s.isPremarket(candidate.Timestamp) {
		minThreshold = 0.90
	}
	if strongSqueeze && s.isOpeningSession(candidate.Timestamp) {
		minThreshold = 0.85
	}
	if strongSqueeze && threshold < 0.15 {
		threshold = 0.0
	}
	if threshold < minThreshold {
		threshold = minThreshold
	}
	return threshold
}

func (s *Strategy) passesEntryQuality(candidate domain.Candidate) (bool, string) {
	strongSqueeze := s.isStrongSqueeze(candidate)
	volumeLeaderPct := s.volumeLeaderPct(candidate)
	if candidate.Score < s.config.MinEntryScore && !(strongSqueeze && candidate.Score >= s.config.MinEntryScore-1) {
		return false, "low-score"
	}
	if volumeLeaderPct < 0.35 &&
		candidate.RelativeVolume < s.config.MinRelativeVolume+6 &&
		candidate.Score < s.config.MinEntryScore+8 {
		return false, "secondary-volume"
	}
	if (s.isPremarket(candidate.Timestamp) || s.isOpeningSession(candidate.Timestamp)) &&
		volumeLeaderPct < 0.55 &&
		candidate.RelativeVolume < s.config.MinRelativeVolume+8 {
		return false, "secondary-volume"
	}
	if s.isEarlyPremarket(candidate.Timestamp) && entryDollarVolume(candidate) < 2_000_000 {
		return false, "thin-premarket"
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
	if candidate.PriceVsOpenPct > s.config.MaxPriceVsOpenPct &&
		candidate.DistanceFromHighPct > 0.90 &&
		candidate.OneMinuteReturnPct < s.config.MinOneMinuteReturnPct &&
		candidate.ThreeMinuteReturnPct < s.config.MinThreeMinuteReturnPct+0.20 &&
		candidate.VolumeRate < s.config.MinVolumeRate+0.10 &&
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
		candidate.DistanceFromHighPct > 0.35 {
		return false, "weak-follow-through"
	}
	if candidate.VolumeRate < s.config.MinVolumeRate &&
		candidate.RelativeVolume < s.config.MinRelativeVolume+1 &&
		candidate.Score < s.config.MinEntryScore+2 {
		return false, "weak-volume-rate"
	}
	return true, ""
}

func (s *Strategy) isStrongSqueeze(candidate domain.Candidate) bool {
	scoreThreshold := s.config.MinEntryScore + 4
	relativeVolumeThreshold := s.config.MinRelativeVolume + 1.5
	threeMinuteThreshold := s.config.MinThreeMinuteReturnPct + 0.40
	volumeRateThreshold := s.config.MinVolumeRate + 0.15
	volumeLeaderThreshold := 0.35

	if s.isPremarket(candidate.Timestamp) {
		scoreThreshold += 3
		relativeVolumeThreshold += 2.0
		threeMinuteThreshold += 0.40
		volumeRateThreshold += 0.15
		volumeLeaderThreshold = 0.50
	}
	if s.isOpeningSession(candidate.Timestamp) {
		scoreThreshold += 1.5
		relativeVolumeThreshold += 1.0
		threeMinuteThreshold += 0.20
		volumeRateThreshold += 0.10
		volumeLeaderThreshold = 0.45
	}

	if s.isParabolicEntry(candidate) {
		return false
	}

	return candidate.Score >= scoreThreshold &&
		candidate.RelativeVolume >= relativeVolumeThreshold &&
		candidate.ThreeMinuteReturnPct >= threeMinuteThreshold &&
		candidate.VolumeRate >= volumeRateThreshold &&
		s.volumeLeaderPct(candidate) >= volumeLeaderThreshold
}

func (s *Strategy) predictEntryReturn(candidate domain.Candidate, strongSqueeze bool) float64 {
	activePrediction := s.entryModel.Predict(candidate)
	if s.entryModel.Name == s.seedModel.Name {
		return activePrediction
	}
	seedPrediction := s.seedModel.Predict(candidate)
	blended := (activePrediction * 0.70) + (seedPrediction * 0.30)
	if strongSqueeze && seedPrediction > 0 && blended < 0 {
		return 0
	}
	return blended
}

func (s *Strategy) symbolState(symbol string, at time.Time) symbolTradeState {
	state := s.symbolStates[symbol]
	dayKey := tradingDayKey(at)
	if state.dayKey == dayKey {
		return state
	}
	return symbolTradeState{dayKey: dayKey}
}

func (s *Strategy) allowedDistanceFromHigh(candidate domain.Candidate) float64 {
	allowance := 0.80
	if candidate.RelativeVolume >= s.config.MinRelativeVolume+1 {
		allowance += 0.35
	}
	if candidate.RelativeVolume >= s.config.MinRelativeVolume+3 {
		allowance += 0.45
	}
	if candidate.ThreeMinuteReturnPct >= s.config.MinThreeMinuteReturnPct+0.25 {
		allowance += 0.45
	}
	if candidate.VolumeRate >= s.config.MinVolumeRate+0.10 {
		allowance += 0.30
	}
	if s.volumeLeaderPct(candidate) >= 0.85 {
		allowance += 0.25
	}
	if candidate.MinutesSinceOpen >= 90 {
		allowance += 0.35
	}
	if s.isStrongSqueeze(candidate) {
		allowance += 0.60
	}
	if allowance > 3.25 {
		allowance = 3.25
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
		candidate.DistanceFromHighPct <= 1.0
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
		multiplier *= 0.80
	}
	if candidate.RelativeVolume >= 100 && candidate.PriceVsOpenPct >= 20 {
		multiplier *= 0.80
	}
	if volumeLeaderPct < 0.55 {
		multiplier *= 0.75
	}
	if candidate.VolumeLeaderPct >= 0.90 && !s.isPremarket(candidate.Timestamp) {
		multiplier *= 1.05
	}
	if multiplier < 0.40 {
		multiplier = 0.40
	}
	return multiplier
}

func (s *Strategy) volumeLeaderPct(candidate domain.Candidate) float64 {
	if candidate.VolumeLeaderPct <= 0 && candidate.Volume == 0 {
		return 1
	}
	return candidate.VolumeLeaderPct
}

func sameTradingDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return tradingDayKey(a) == tradingDayKey(b)
}

func priceReturn(entryPrice, currentPrice float64) float64 {
	if entryPrice <= 0 {
		return 0
	}
	return (currentPrice - entryPrice) / entryPrice
}

func entryDollarVolume(candidate domain.Candidate) float64 {
	return candidate.Price * float64(candidate.Volume)
}

func tradingDayKey(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.In(markethours.Location()).Format("2006-01-02")
}
