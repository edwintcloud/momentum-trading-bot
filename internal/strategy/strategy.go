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
		lastEntryAt:         make(map[string]time.Time),
		lastExitAt:          make(map[string]time.Time),
		symbolStates:        make(map[string]symbolTradeState),
		reallocationTargets: make(map[string]bool),
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
		holdingTime := decisionAt.Sub(p.OpenedAt)
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
	strongSqueeze := s.isStrongSqueeze(candidate)
	allowedDistance := s.allowedBreakoutSlack(candidate)
	predictedReturn := s.predictEntryReturn(candidate, strongSqueeze)
	requiredReturn := s.requiredPredictedReturn(candidate)
	if !markethours.IsTradableSessionAt(decisionAt) {
		return CandidateDecision{Reason: "outside-session", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if s.isLateSession(decisionAt) {
		return CandidateDecision{Reason: "late-session-momentum-decay", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
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
	if symbolState.entrySignals >= 5 {
		return CandidateDecision{Reason: "symbol-daily-cap", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if ok, reason := s.passesEntryQuality(candidate); !ok {
		return CandidateDecision{Reason: reason, PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if candidate.BreakoutPct < -allowedDistance {
		return CandidateDecision{Reason: "below-breakout-zone", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	if s.config.EntryModelEnabled && predictedReturn < requiredReturn {
		return CandidateDecision{Reason: "model-threshold", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}
	plan, ok, reason := buildEntryPlan(candidate, s.config)
	if !ok {
		return CandidateDecision{Reason: reason, PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}

	maxExposure := s.portfolio.EffectiveCapital() * s.config.MaxExposurePct
	if s.portfolio.OpenPositionCount() >= s.config.MaxOpenPositions || s.portfolio.Exposure() >= maxExposure {
		if candidate.Score >= 16.0 {
			if s.evaluateOpportunitySwap(candidate) {
				return CandidateDecision{Reason: "reallocation-swap-pending", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
			}
		}
		return CandidateDecision{Reason: "max-capacity-reached", PredictedReturnPct: predictedReturn, RequiredReturnPct: requiredReturn, AllowedDistanceHighPct: allowedDistance, StrongSqueeze: strongSqueeze}
	}

	quantity := int64(0)
	riskAmount := s.portfolio.EffectiveCapital() * s.config.RiskPerTradePct
	if plan.RiskPerShare > 0 {
		quantity = int64(riskAmount / plan.RiskPerShare)
	}
	quantity = int64(float64(quantity) * s.positionSizeMultiplier(candidate))
	
	maxExposurePct := s.config.MaxExposurePct
	if maxExposurePct <= 0 {
		maxExposurePct = 0.25
	}
	openPos := s.config.MaxOpenPositions
	if openPos <= 0 {
		openPos = 4
	}
	maxAllowedValue := s.portfolio.EffectiveCapital() * (maxExposurePct / float64(openPos))
	
	if float64(quantity)*candidate.Price > maxAllowedValue {
		quantity = int64(maxAllowedValue / candidate.Price)
		s.runtime.RecordLog("info", "strategy", fmt.Sprintf("clipped entry size for %s to portfolio limit of %d shares ($%.0f)", candidate.Symbol, quantity, maxAllowedValue))
	}
	
	if quantity < 1 {
		quantity = 1
	}
	s.lastEntryAt[candidate.Symbol] = decisionAt
	symbolState.entrySignals++
	s.symbolStates[candidate.Symbol] = symbolState

	signal := domain.TradeSignal{
		Symbol:       candidate.Symbol,
		Side:         "buy",
		Price:        candidate.Price,
		Quantity:     quantity,
		StopPrice:    plan.StopPrice,
		RiskPerShare: plan.RiskPerShare,
		EntryATR:     plan.EntryATR,
		SetupType:    plan.SetupType,
		Reason:       "ml-breakout-entry",
		Confidence:   predictedReturn,
		Timestamp:    decisionAt,
	}

	if s.runtime.Recorder() != nil {
		s.runtime.Recorder().RecordIndicatorState(domain.IndicatorSnapshot{
			Symbol:     candidate.Symbol,
			Timestamp:  decisionAt,
			SignalType: "entry",
			Reason:     "entry-signal",
			Indicators: map[string]float64{
				"price":                   candidate.Price,
				"open":                    candidate.Open,
				"gapPercent":              candidate.GapPercent,
				"relativeVolume":          candidate.RelativeVolume,
				"preMarketVolume":         float64(candidate.PreMarketVolume),
				"volume":                  float64(candidate.Volume),
				"highOfDay":               candidate.HighOfDay,
				"priceVsOpenPct":          candidate.PriceVsOpenPct,
				"distanceFromHighPct":     candidate.DistanceFromHighPct,
				"oneMinuteReturnPct":      candidate.OneMinuteReturnPct,
				"threeMinuteReturnPct":    candidate.ThreeMinuteReturnPct,
				"volumeRate":              candidate.VolumeRate,
				"volumeLeaderPct":         candidate.VolumeLeaderPct,
				"minutesSinceOpen":        candidate.MinutesSinceOpen,
				"atr":                     candidate.ATR,
				"atrPct":                  candidate.ATRPct,
				"vwap":                    candidate.VWAP,
				"priceVsVwapPct":          candidate.PriceVsVWAPPct,
				"breakoutPct":             candidate.BreakoutPct,
				"consolidationRangePct":   candidate.ConsolidationRangePct,
				"pullbackDepthPct":        candidate.PullbackDepthPct,
				"closeOffHighPct":         candidate.CloseOffHighPct,
				"setupHigh":               candidate.SetupHigh,
				"setupLow":                candidate.SetupLow,
				"score":                   candidate.Score,
				"predictedReturn":         predictedReturn,
				"requiredReturn":          requiredReturn,
			},
		})
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

	highWatermark := maxPrice(position.HighestPrice, tick.BarHigh, tick.Price)
	previousStop, previousReason := protectiveStop(position, position.HighestPrice, firstPositive(position.LastPrice, position.AvgPrice), decisionAt)
	if previousStop <= 0 {
		previousStop, previousReason = protectiveStop(position, highWatermark, firstPositive(position.LastPrice, tick.Price), decisionAt)
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
		fmt.Printf("DEBUG STRATEGY: %s opportunity-reallocation barOpen=%.2f holdingTime=%.2f peakReturn=%.2f\n", position.Symbol, tick.Price, holdingTime.Minutes(), peakReturn)
	case barOpen > 0 && previousStop > 0 && barOpen <= previousStop:
		reason = previousReason
		tick.Price = barOpen
		fmt.Printf("DEBUG STRATEGY: %s open-stop previousStop=%.2f barOpen=%.2f\n", position.Symbol, previousStop, barOpen)
	case sameDayHold &&
		holdingTime >= time.Duration(s.config.BreakoutFailureWindowMin)*time.Minute &&
		peakReturn < 1.0 &&
		barLow > 0 &&
		barLow <= failedBreakoutPrice(position):
		reason = "failed-breakout"
		tick.Price = failedBreakoutPrice(position)
		fmt.Printf("DEBUG STRATEGY: %s failed-breakout fbp=%.2f barLow=%.2f holdingTime=%.2f peakReturn=%.2f\n", position.Symbol, tick.Price, barLow, holdingTime.Minutes(), peakReturn)
	case sameDayHold &&
		holdingTime >= time.Duration(s.config.StagnationWindowMin)*time.Minute &&
		peakReturn < s.config.StagnationMinPeakPct:
		reason = "stagnation-time-stop"
		tick.Price = barClose
		fmt.Printf("DEBUG STRATEGY: %s stagnation-time-stop barClose=%.2f holdingTime=%.2f peakReturn=%.2f\n", position.Symbol, tick.Price, holdingTime.Minutes(), peakReturn)
	case func() bool {
		stopPrice, stopReason := protectiveStop(position, highWatermark, barClose, decisionAt)
		if stopPrice <= 0 || barLow <= 0 || barLow > stopPrice {
			return false
		}
		reason = stopReason
		tick.Price = stopPrice
		fmt.Printf("DEBUG STRATEGY: %s %s stopPrice=%.2f barLow=%.2f peakReturn=%.2f\n", position.Symbol, reason, stopPrice, barLow, peakReturn)
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
				"tickPrice":           tick.Price,
				"tickBarOpen":         tick.BarOpen,
				"tickBarHigh":         tick.BarHigh,
				"tickBarLow":          tick.BarLow,
				"tickVolume":          float64(tick.Volume),
				"positionQuantity":    float64(position.Quantity),
				"positionAvgPrice":    position.AvgPrice,
				"positionLastPrice":    position.LastPrice,
				"positionHighestPrice": position.HighestPrice,
				"positionRisk":        position.RiskPerShare,
				"positionATR":         position.EntryATR,
				"highWatermark":       highWatermark,
				"previousStop":        previousStop,
				"peakReturn":          peakReturn,
				"holdingTimeMin":      holdingTime.Minutes(),
			},
		})
	}

	return domain.TradeSignal{
		Symbol:       tick.Symbol,
		Side:         "sell",
		Price:        tick.Price,
		Quantity:     position.Quantity,
		StopPrice:    position.StopPrice,
		RiskPerShare: position.RiskPerShare,
		EntryATR:     position.EntryATR,
		SetupType:    position.SetupType,
		Reason:       reason,
		Confidence:   1,
		Timestamp:    decisionAt,
	}, true, reason
}

func (s *Strategy) normalizeCandidate(candidate domain.Candidate) domain.Candidate {
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
	if candidate.SetupType == "" {
		switch {
		case candidate.PriceVsVWAPPct >= -0.10 && candidate.BreakoutPct >= -0.20:
			candidate.SetupType = "consolidation-breakout"
		case candidate.PriceVsVWAPPct >= -0.10 && candidate.ThreeMinuteReturnPct > 0:
			candidate.SetupType = "vwap-reclaim"
		}
	}
	if candidate.LeaderRank <= 0 {
		candidate.LeaderRank = 1
	}
	return candidate
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

	// Setup-type discounts.
	if candidate.SetupType == "consolidation-breakout" {
		threshold -= 0.25
	}
	if candidate.SetupType == "higher-low-reclaim" {
		threshold -= 0.15
	}
	if candidate.SetupType == "vwap-reclaim" {
		threshold -= 0.10
	}

	// Session penalties (keep only the strongest 3).
	if s.isOpeningSession(candidate.Timestamp) {
		threshold += 0.20
	}
	if volumeLeaderPct < 0.20 {
		threshold += 0.15
	}

	// Quality discounts.
	if strongSqueeze {
		threshold -= 0.80
	}
	if volumeLeaderPct >= 0.85 {
		threshold -= 0.20
	}
	if candidate.Score >= s.config.MinEntryScore+4 {
		threshold -= 0.25
	}
	if candidate.BreakoutPct >= -0.15 &&
		candidate.ThreeMinuteReturnPct >= s.config.MinThreeMinuteReturnPct+0.15 &&
		candidate.VolumeRate >= s.config.MinVolumeRate {
		threshold -= 0.20
	}

	minThreshold := s.config.EntryModelMinPredictedReturnPct
	if candidate.VolumeRate >= 5.0 && !s.isPremarket(candidate.Timestamp) {
		minThreshold = maxFloat(minThreshold-0.05, 0.10)
	}
	if strongSqueeze {
		minThreshold = maxFloat(minThreshold, 0.20)
	}
	if strongSqueeze && s.isPremarket(candidate.Timestamp) {
		minThreshold = 0.60
	}
	if strongSqueeze && s.isOpeningSession(candidate.Timestamp) {
		minThreshold = 0.45
	}
	if threshold < minThreshold {
		threshold = minThreshold
	}
	return threshold
}

var knownLeveragedETFs = map[string]bool{
	"UVIX": true, "UVXY": true, "SQQQ": true, "TQQQ": true, "SOXL": true, "SOXS": true,
	"SPXL": true, "SPXS": true, "UPRO": true, "SPXU": true, "UDOW": true, "SDOW": true,
	"TNA":  true, "TZA":  true, "FAS":  true, "FAZ":  true, "NUGT": true, "DUST": true,
	"JNUG": true, "JDST": true, "UGL":  true, "GLL":  true, "AGQ":  true, "ZSL":  true,
	"BOIL": true, "KOLD": true, "UCO":  true, "SCO":  true, "YINN": true, "YANG": true,
	"CWEB": true, "KORU": true, "EURL": true, "EDC":  true, "EDZ":  true, "INDL": true,
	"LBJ":  true, "GUSH": true, "DRIP": true, "NRGU": true, "NRGD": true, "UAMY": true,
	"BITX": true, "BITI": true, "MSTU": true, "TSLL": true, "TSLQ": true, "CONL": true,
	"GDXD": true, "GDXU": true, "AAPU": true, "AAPD": true, "AMZU": true, "AMZD": true,
	"NVDL": true, "NVDD": true, "NVDU": true, "MSFU": true, "MSFD": true, "GOOU": true,
	"GOOD": true, "COINU": true, "COIND": true, "DPST": true, "LABU": true, "LABD": true,
	"WTIU": true, "MSTZ": true, "SUPX": true, "DXD": true,
}

func isLeveragedETF(symbol string) bool {
	return knownLeveragedETFs[symbol]
}

func (s *Strategy) passesEntryQuality(candidate domain.Candidate) (bool, string) {
	if candidate.Price < s.config.MinPrice {
		return false, "low-price"
	}
	if isLeveragedETF(candidate.Symbol) {
		return false, "leveraged-etf"
	}
	if candidate.MinutesSinceOpen > float64(s.config.MaxEntryMinutesSinceOpen) {
		return false, "late-session-entry"
	}
	if s.config.MaxPriceVsVWAPPct > 0 && candidate.PriceVsVWAPPct > s.config.MaxPriceVsVWAPPct {
		return false, "vwap-overextended"
	}

	strongSqueeze := s.isStrongSqueeze(candidate)
	volumeLeaderPct := s.volumeLeaderPct(candidate)
	minLeaderPct := 0.01
	maxLeaderRank := 50
	if strongSqueeze {
		minLeaderPct -= 0.008 // Floor of 0.002 for strong squeezes
		maxLeaderRank += 2
	}
	if candidate.Score < s.config.MinEntryScore && !(strongSqueeze && candidate.Score >= s.config.MinEntryScore-1.5) {
		return false, "low-score"
	}
	if s.isEarlyPremarket(candidate.Timestamp) {
		return false, "early-premarket-banned"
	}
	if s.config.MaxOneMinuteReturnPct > 0 && candidate.OneMinuteReturnPct > s.config.MaxOneMinuteReturnPct {
		return false, "one-minute-flash-spike"
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
	if s.leaderRank(candidate) > maxLeaderRank {
		return false, "secondary-volume"
	}
	if volumeLeaderPct < minLeaderPct {
		return false, "secondary-volume"
	}
	if candidate.SetupType == "" {
		return false, "no-setup"
	}
	if !s.hasTimingConfirmation(candidate, strongSqueeze) {
		return false, "no-renewed-volume"
	}
	if candidate.PriceVsVWAPPct < 0.25 {
		return false, "below-vwap"
	}
	// Hard caps that apply even for squeeze entries — extreme extension is never safe.
	if candidate.PriceVsVWAPPct > 16.0 {
		return false, "vwap-extension"
	}
	if candidate.DistanceFromHighPct > 12.0 {
		return false, "distance-from-high"
	}
	if candidate.PriceVsVWAPPct > 12.0 && !strongSqueeze {
		return false, "vwap-extension"
	}
	if candidate.DistanceFromHighPct > 4.5 && !strongSqueeze {
		return false, "distance-from-high"
	}
	if candidate.BreakoutPct > 2.0 && !strongSqueeze {
		return false, "breakout-too-extended"
	}
	if candidate.PriceVsOpenPct > maxFloat(s.config.MaxPriceVsOpenPct, candidate.ATRPct*6.5) &&
		candidate.BreakoutPct < -0.10 &&
		candidate.PriceVsVWAPPct < 0.50 &&
		candidate.CloseOffHighPct > 35 &&
		!strongSqueeze {
		return false, "too-extended-from-open"
	}
	if candidate.FifteenMinuteReturnPct < s.config.MinFifteenMinuteReturnPct && candidate.OneMinuteReturnPct < -0.35 && candidate.ThreeMinuteReturnPct < s.config.MinThreeMinuteReturnPct {
		return false, "weak-one-minute-return"
	}
	if candidate.FifteenMinuteReturnPct < s.config.MinFifteenMinuteReturnPct && candidate.ThreeMinuteReturnPct < -0.20 && candidate.VolumeRate < s.config.MinVolumeRate {
		return false, "weak-three-minute-return"
	}

	if candidate.CloseOffHighPct > 38 {
		return false, "weak-close"
	}
	if candidate.ATRPct <= 0 {
		return false, "missing-atr"
	}
	return true, ""
}

func (s *Strategy) isStrongSqueeze(candidate domain.Candidate) bool {
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
	minVolumeRate := s.config.MinVolumeRate
	if strongSqueeze {
		minVolumeRate -= 0.20
	}

	switch candidate.SetupType {
	case "consolidation-breakout", "opening-range-breakout":
		if candidate.VolumeRate < minVolumeRate {
			return false
		}
		return true
	case "higher-low-reclaim", "vwap-reclaim":
		if candidate.VolumeRate < minVolumeRate {
			return false
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

func (s *Strategy) predictEntryReturn(candidate domain.Candidate, strongSqueeze bool) float64 {
	activePrediction := s.entryModel.Predict(candidate)
	if s.entryModel.Name == s.seedModel.Name {
		return activePrediction
	}
	seedPrediction := s.seedModel.Predict(candidate)
	blended := (activePrediction * 0.70) + (seedPrediction * 0.30)
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

func (s *Strategy) allowedBreakoutSlack(candidate domain.Candidate) float64 {
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

func (s *Strategy) isParabolicEntry(candidate domain.Candidate) bool {
	if candidate.FifteenMinuteReturnPct < 15 && (candidate.OneMinuteReturnPct >= 12 || candidate.ThreeMinuteReturnPct >= 20) {
		return true
	}
	if s.isEarlyPremarket(candidate.Timestamp) && candidate.FifteenMinuteReturnPct < 6 && (candidate.OneMinuteReturnPct >= 5 || candidate.ThreeMinuteReturnPct >= 8) {
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
	if multiplier < 0.55 {
		multiplier = 0.55
	}
	return multiplier
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
