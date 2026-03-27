package strategy

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/sector"
)

// TimeWindow classifies the current market session period.
type TimeWindow int

const (
	TimeWindowOpen    TimeWindow = iota // first 30 minutes
	TimeWindowMorning                   // 30 min to 2 hours after open
	TimeWindowMidDay                    // 2 hours to 1 hour before close
	TimeWindowClose                     // final hour
)

// TimeWindowConfig holds per-window adjustments.
type TimeWindowConfig struct {
	ScoreThresholdMultiplier float64
	RiskMultiplier           float64
	ProfitTargetMultiplier   float64
}

var defaultTimeWindowConfigs = map[TimeWindow]TimeWindowConfig{
	TimeWindowOpen:    {ScoreThresholdMultiplier: 1.0, RiskMultiplier: 1.3, ProfitTargetMultiplier: 1.0},
	TimeWindowMorning: {ScoreThresholdMultiplier: 1.0, RiskMultiplier: 1.0, ProfitTargetMultiplier: 1.0},
	TimeWindowMidDay:  {ScoreThresholdMultiplier: 1.15, RiskMultiplier: 0.85, ProfitTargetMultiplier: 0.8},
	TimeWindowClose:   {ScoreThresholdMultiplier: 1.3, RiskMultiplier: 0.7, ProfitTargetMultiplier: 0.6},
}

// currentTimeWindow classifies the market session period using ET.
func currentTimeWindow(ts time.Time) TimeWindow {
	loc := markethours.Location()
	et := ts.In(loc)
	open := markethours.MarketOpen(ts)
	openET := open.In(loc)
	minutesSinceOpen := et.Sub(openET).Minutes()
	closeET := markethours.MarketClose(ts).In(loc)
	minutesToClose := closeET.Sub(et).Minutes()

	switch {
	case minutesSinceOpen < 30:
		return TimeWindowOpen
	case minutesToClose <= 60:
		return TimeWindowClose
	case minutesSinceOpen < 120:
		return TimeWindowMorning
	default:
		return TimeWindowMidDay
	}
}

// Strategy implements breakout entries and managed exits.
type Strategy struct {
	config              config.TradingConfig
	mu                  sync.Mutex
	portfolio           *portfolio.Manager
	runtime             *runtime.State
	riskEngine          *risk.Engine
	volEstimator        *risk.VolatilityEstimator
	lastEntryAt         map[string]time.Time
	lastExitAt          map[string]time.Time
	symbolStates        map[string]symbolTradeState
	tapePressure        tapePressureState
	reallocationTargets map[string]bool
	recentPrices        map[string][]float64 // for Bollinger Band exit on mean-reversion
}

type symbolTradeState struct {
	dayKey                    string
	entrySignals              int
	lossExits                 int
	lastLossAt                time.Time
	dangerousShortFadeExits   int
	lastDangerousShortFadeAt  time.Time
	lastDangerousShortFadeVWAP float64
	lastDangerousShortFadeDist float64
	profitableShortExits      int
	lastProfitableShortExitAt time.Time
	lastProfitableShortSetup  string
	lastLongImpulseHigh       float64
	lastLongSetup             string
	lastLongEntryAt           time.Time
}

type tapePressureState struct {
	dayKey     string
	lastUpdate time.Time
	bull       float64
	bear       float64
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
		recentPrices:        make(map[string][]float64),
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = cfg
}

// getConfig returns a snapshot of the current config safe for concurrent use.
func (s *Strategy) getConfig() config.TradingConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
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
			signal, shouldEmit, reason := s.evaluateCandidate(candidate)
			if !shouldEmit {
				if reason != "" && reason != "market-closed" && reason != "system-paused" {
					s.runtime.RecordLog("debug", "strategy", fmt.Sprintf("candidate rejected: %s reason=%s", candidate.Symbol, reason))
				}
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
	signal, shouldEmit, _ := s.evaluateCandidate(candidate)
	return signal, shouldEmit
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
	cfg := s.getConfig()
	signal, ok, signalReason := s.evaluateCandidate(candidate)
	reason := "no-signal"
	if ok {
		reason = signal.Reason
	} else if !markethours.IsTradableSessionAt(candidate.Timestamp) {
		reason = "market-closed"
	} else if s.runtime.IsPaused() || s.runtime.IsEmergencyStopped() {
		reason = "system-paused"
	} else if _, exists := s.portfolio.GetPosition(candidate.Symbol); exists {
		reason = "existing-position"
	} else if cfg.RegimeGatingEnabled {
		if (candidate.MarketRegime == domain.RegimeBearish && domain.IsLong(candidate.Direction)) ||
			(candidate.MarketRegime == domain.RegimeBullish && domain.IsShort(candidate.Direction)) {
			reason = "regime-gated"
		}
	}
	if reason == "no-signal" {
		// Check cooldown
		if last, exists := s.lastEntryAt[candidate.Symbol]; exists {
			if candidate.Timestamp.Sub(last) < time.Duration(cfg.EntryCooldownSec)*time.Second {
				reason = "cooldown"
			}
		}
	}
	if reason == "no-signal" {
		// Check loss cooldown
		dayKey := markethours.TradingDay(candidate.Timestamp)
		state := s.getSymbolState(candidate.Symbol, dayKey)
		if state.lossExits >= 2 && candidate.Timestamp.Sub(state.lastLossAt) < 30*time.Minute {
			reason = "loss-cooldown"
		}
	}
	if reason == "no-signal" {
		reason = signalReason
	}
	return CandidateDecision{
		Signal: signal,
		Emit:   ok,
		Reason: reason,
	}
}

func (s *Strategy) evaluateCandidate(c domain.Candidate) (domain.TradeSignal, bool, string) {
	cfg := s.getConfig()
	now := c.Timestamp
	tape := s.peekTapePressure(now)
	defer s.recordTapePressure(c)
	if !markethours.IsTradableSessionAt(now) {
		return domain.TradeSignal{}, false, "market-closed"
	}

	// Block new entries within 15 minutes of extended-hours session end (8:00 PM ET).
	sessionEnd := markethours.SessionClose(now)
	if now.After(sessionEnd.Add(-15 * time.Minute)) {
		return domain.TradeSignal{}, false, "session-closing"
	}

	// Check if paused or emergency stopped
	if s.runtime.IsPaused() || s.runtime.IsEmergencyStopped() {
		return domain.TradeSignal{}, false, "system-paused"
	}

	// Cooldown check
	if last, ok := s.lastEntryAt[c.Symbol]; ok {
		if now.Sub(last) < time.Duration(cfg.EntryCooldownSec)*time.Second {
			return domain.TradeSignal{}, false, "cooldown"
		}
	}

	// Already have a position in this symbol
	if _, exists := s.portfolio.GetPosition(c.Symbol); exists {
		return domain.TradeSignal{}, false, "existing-position"
	}
	if s.portfolio.HasPendingOrder(c.Symbol) {
		return domain.TradeSignal{}, false, "pending-order"
	}
	if reachedDailyProfitLock(s.portfolio, cfg) {
		return domain.TradeSignal{}, false, "daily-profit-lock"
	}

	// Day state
	dayKey := markethours.TradingDay(now)
	state := s.getSymbolState(c.Symbol, dayKey)

	// Block re-entry on any ticker that had a losing trade today
	if cfg.BlockLosingTickerReentry && s.portfolio.SymbolHadLossToday(c.Symbol) {
		return domain.TradeSignal{}, false, "losing-ticker-blocked"
	}

	// Too many losses on this symbol today
	if state.lossExits >= 2 && now.Sub(state.lastLossAt) < 30*time.Minute {
		return domain.TradeSignal{}, false, "loss-cooldown"
	}
	if domain.IsLong(c.Direction) {
		if shouldBlockLongForBearPressure(c, tape) {
			return domain.TradeSignal{}, false, "bear-pressure-long-block"
		}
		if reason, blocked := rejectRepeatedLongImpulse(c, state); blocked {
			return domain.TradeSignal{}, false, reason
		}
		if reason, blocked := rejectLongAfterProfitableShort(c, state); blocked {
			return domain.TradeSignal{}, false, reason
		}
		if reason, blocked := rejectWeakLongStockSelection(c); blocked {
			return domain.TradeSignal{}, false, reason
		}
		if reason, blocked := rejectWeakLongBreakout(c, cfg); blocked {
			return domain.TradeSignal{}, false, reason
		}
	} else {
		if reason, blocked := rejectRepeatedDangerousShortFade(c, state); blocked {
			return domain.TradeSignal{}, false, reason
		}
		if reason, blocked := rejectWeakShortMomentum(c, cfg); blocked {
			return domain.TradeSignal{}, false, reason
		}
	}

	// Candidate-quality gate: only take higher-conviction momentum setups.
	scoreThreshold := cfg.MinEntryScore
	if domain.IsShort(c.Direction) {
		scoreThreshold = cfg.ShortMinEntryScore
	}
	if scoreThreshold > 0 && c.Score < scoreThreshold {
		return domain.TradeSignal{}, false, "low-score"
	}

	// Generic pullbacks need a meaningful intraday trend behind them unless they
	// are clearly reclaiming near the session high with leader-level participation.
	if domain.IsLong(c.Direction) && c.SetupType == "pullback" &&
		c.IntradayReturnPct < math.Max(cfg.MinGapPercent*1.5, 5.0) &&
		!isLeaderOpenDrivePullback(c) {
		return domain.TradeSignal{}, false, "weak-intraday-trend"
	}
	// --- Hard indicator filters (rules-based entry) ---

	// MACD histogram: must confirm direction
	if domain.IsLong(c.Direction) && c.MACDHistogram <= 0 {
		return domain.TradeSignal{}, false, "macd-filter"
	}
	if domain.IsShort(c.Direction) && c.MACDHistogram >= 0 {
		return domain.TradeSignal{}, false, "macd-filter"
	}

	// VWAP: price must be above VWAP for longs, below for shorts
	if c.VWAP > 0 {
		if domain.IsLong(c.Direction) && c.Price < c.VWAP {
			return domain.TradeSignal{}, false, "vwap-filter"
		}
		if domain.IsShort(c.Direction) && c.Price > c.VWAP {
			return domain.TradeSignal{}, false, "vwap-filter"
		}
	}

	// EMA9: price must be above EMA9 for longs, below for shorts
	if domain.IsLong(c.Direction) && c.PriceVsEMA9Pct < 0 {
		return domain.TradeSignal{}, false, "ema9-filter"
	}
	if domain.IsShort(c.Direction) && c.PriceVsEMA9Pct > 0 {
		return domain.TradeSignal{}, false, "ema9-filter"
	}

	// Volume rate must exceed minimum
	if c.VolumeRate < cfg.MinVolumeRate {
		return domain.TradeSignal{}, false, "low-volume"
	}

	// Use the aligned 5-minute candle return for long-side momentum confirmation.
	if domain.IsLong(c.Direction) && c.FiveMinuteReturnPct < cfg.MinThreeMinuteReturnPct {
		return domain.TradeSignal{}, false, "no-confirmation"
	}

	// Regime gating: hard reject on clear directional mismatch
	if cfg.RegimeGatingEnabled {
		switch c.MarketRegime {
		case domain.RegimeBearish:
			if domain.IsLong(c.Direction) {
				return domain.TradeSignal{}, false, "regime-gated"
			}
		case domain.RegimeBullish:
			if domain.IsShort(c.Direction) {
				return domain.TradeSignal{}, false, "regime-gated"
			}
		}
	}

	// Compute position sizing
	riskPerShare := s.computeRiskPerShare(c)
	if riskPerShare <= 0 {
		return domain.TradeSignal{}, false, "invalid-risk"
	}

	// Risk/Reward pre-check: reject trades where reward < MinRiskRewardRatio × risk
	if cfg.MinRiskRewardRatio > 0 && riskPerShare > 0 {
		var estimatedReward float64
		if domain.IsLong(c.Direction) {
			estimatedReward = c.HighOfDay - c.Price
			if estimatedReward <= 0 || c.SetupType == "hod_breakout" || c.SetupType == "orb_breakout" {
				estimatedReward = c.ATR * 2.0
			}
		} else {
			// For shorts, use distance to setup low (breakdown target) or ATR fallback
			estimatedReward = c.Price - c.SetupLow
			if estimatedReward <= 0 {
				estimatedReward = c.ATR * 2.0
			}
		}
		rewardRiskRatio := estimatedReward / riskPerShare
		if rewardRiskRatio < cfg.MinRiskRewardRatio {
			return domain.TradeSignal{}, false, "poor-risk-reward"
		}
	}

	currentEquity := s.portfolio.CurrentEquity()
	if currentEquity <= 0 {
		currentEquity = cfg.StartingCapital
	}

	// Phase 2 Change 5: Kelly Criterion sizing
	riskPct := cfg.RiskPerTradePct
	if cfg.KellySizingEnabled {
		winRate, wlRatio, tradeCount := s.portfolio.RollingTradeStats(cfg.KellyWindowSize)
		if tradeCount >= cfg.KellyMinTrades {
			kellyF := KellyFraction(winRate, wlRatio)
			fractionalKelly := kellyF * cfg.KellyFraction
			if fractionalKelly > cfg.MaxKellyRiskPct {
				fractionalKelly = cfg.MaxKellyRiskPct
			}
			if fractionalKelly > 0 {
				riskPct = fractionalKelly
			}
		}
	}

	riskBudget := currentEquity * riskPct

	// Graduated daily loss sizing factor
	if s.riskEngine != nil {
		dailyLossFactor := s.riskEngine.DailyLossSizingFactor()
		if dailyLossFactor <= 0 {
			return domain.TradeSignal{}, false, "daily-loss-cooldown"
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
			return domain.TradeSignal{}, false, "drawdown-cooldown"
		}
		riskBudget *= drawdownFactor
	}
	riskBudget *= entryRiskMultiplier(c)
	riskBudget *= longBearPressureRiskMultiplier(c, tape)

	// Update HWM tracking
	s.portfolio.UpdateEquityTracking()

	quantity := int64(math.Floor(riskBudget / riskPerShare))
	if quantity <= 0 {
		return domain.TradeSignal{}, false, "position-too-small"
	}

	// Volatility-based position sizing cap
	if cfg.VolTargetSizingEnabled && s.volEstimator != nil {
		stockVol := s.volEstimator.GetVolatility(c.Symbol)
		if stockVol > 0 {
			targetDollarVol := currentEquity * cfg.TargetVolPerPosition
			positionDollar := targetDollarVol / stockVol
			volBasedQty := int64(positionDollar / c.Price)
			if volBasedQty > 0 && volBasedQty < quantity {
				quantity = volBasedQty
			}
		}
	}

	entryPrice := c.Price
	if domain.IsLong(c.Direction) {
		if isExceptionalSqueezeBreakout(c) {
			entryPrice = exceptionalSqueezeEntryLimit(c.Price, c.ATR)
		} else {
			switch c.SetupType {
			case "orb_reclaim":
				entryPrice = aggressiveEntryLimit(c.Price, c.ATR, 0.003, 0.15)
			case "orb_breakout":
				entryPrice = aggressiveEntryLimit(c.Price, c.ATR, 0.002, 0.10)
			case "hod_breakout":
				if c.Score >= 9.0 &&
					c.OneMinuteReturnPct >= 2.0 &&
					c.ThreeMinuteReturnPct >= 5.0 &&
					c.RelativeVolume >= 12.0 &&
					c.PriceVsVWAPPct <= 10.0 &&
					c.DistanceFromHighPct <= 0.6 {
					entryPrice = aggressiveEntryLimit(c.Price, c.ATR, 0.006, 0.25)
				}
			}
		}
	}

	var stopPrice float64
	if domain.IsLong(c.Direction) {
		stopPrice = entryPrice - riskPerShare
	} else {
		stopPrice = entryPrice + riskPerShare
	}

	// Resolve sector for the candidate
	candidateSector := c.Sector
	if candidateSector == "" {
		candidateSector = sector.SectorForSymbol(c.Symbol)
	}

	// Position size floor: enforce minimum notional position
	if cfg.MinPositionNotionalPct > 0 && quantity > 0 && c.Price > 0 {
		minNotional := currentEquity * cfg.MinPositionNotionalPct
		minQty := int64(math.Floor(minNotional / c.Price))
		minQty = max(minQty, 0)
		if quantity < minQty {
			quantity = minQty
		}
	}

	playbook := c.Playbook
	if domain.IsLong(c.Direction) {
		switch {
		case isExceptionalSqueezeBreakout(c):
			playbook = "continuation"
		case c.SetupType == "hod_breakout" && isExplosiveLeaderReExpansionBreakout(c):
			playbook = "pullback"
		case c.SetupType == "pullback" && isLeaderOpenDrivePullback(c):
			playbook = "continuation"
		}
	}

	signal := domain.TradeSignal{
		Symbol:           c.Symbol,
		Side:             domain.OpenBrokerSide(c.Direction),
		Intent:           domain.IntentOpen,
		PositionSide:     c.Direction,
		Price:            entryPrice,
		Quantity:         quantity,
		StopPrice:        stopPrice,
		RiskPerShare:     riskPerShare,
		EntryATR:         c.ATR,
		SetupType:        c.SetupType,
		Reason:           fmt.Sprintf("setup=%s", c.SetupType),
		Confidence:       math.Min(1.0, c.Score/5.0),
		MarketRegime:     c.MarketRegime,
		RegimeConfidence: c.RegimeConfidence,
		Playbook:         playbook,
		Sector:           candidateSector,
		AvgDailyVolume:   float64(c.PrevDayVolume),
		LeaderRank:       c.LeaderRank,
		VolumeLeaderPct:  c.VolumeLeaderPct,
		StockSelectScore: c.StockSelectionScore,
		PriceVsVWAPPct:   c.PriceVsVWAPPct,
		DistanceHighPct:  c.DistanceFromHighPct,
		Timestamp:        now,
	}

	s.lastEntryAt[c.Symbol] = now
	state.entrySignals++
	if domain.IsLong(c.Direction) && isHODContinuationSetup(c.SetupType) {
		state.lastLongEntryAt = now
		state.lastLongSetup = c.SetupType
		if c.HighOfDay > state.lastLongImpulseHigh {
			state.lastLongImpulseHigh = c.HighOfDay
		}
	}
	s.symbolStates[c.Symbol] = state

	return signal, true, ""
}

func (s *Strategy) evaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	cfg := s.getConfig()
	pos, exists := s.portfolio.GetPosition(tick.Symbol)
	if !exists {
		return domain.TradeSignal{}, false
	}
	if s.portfolio.HasPendingClose(tick.Symbol) {
		return domain.TradeSignal{}, false
	}

	now := tick.Timestamp

	// Force close tiny positions (leftover from partial fills)
	if pos.Quantity > 0 && pos.OriginalQuantity > 0 {
		if pos.Quantity <= int64(math.Max(1, float64(pos.OriginalQuantity)*0.05)) || pos.Quantity < 5 {
			return domain.TradeSignal{
				Symbol:       tick.Symbol,
				Side:         domain.CloseBrokerSide(pos.Side),
				Intent:       domain.IntentClose,
				PositionSide: pos.Side,
				Price:        tick.Price,
				Quantity:     pos.Quantity,
				Reason:       "cleanup-remainder",
				Timestamp:    now,
			}, true
		}
	}

	// Cooldown
	if last, ok := s.lastExitAt[tick.Symbol]; ok {
		if now.Sub(last) < time.Duration(cfg.ExitCooldownSec)*time.Second {
			return domain.TradeSignal{}, false
		}
	}

	s.portfolio.UpdatePrice(tick.Symbol, tick.Price)

	// Track recent prices for mean-reversion BB exit
	if cfg.MeanReversionEnabled {
		prices := s.recentPrices[tick.Symbol]
		prices = append(prices, tick.Price)
		maxLen := cfg.BollingerPeriod + 10
		if len(prices) > maxLen {
			prices = prices[len(prices)-maxLen:]
		}
		s.recentPrices[tick.Symbol] = prices
	}

	// Check exit conditions in priority order
	reason, shouldExit := s.checkExitConditions(pos, tick)
	if !shouldExit {
		// Update trailing stop
		s.updateTrailingStop(pos, tick)
		return domain.TradeSignal{}, false
	}

	// Phase 3 Change 4: Partial exit — reduce quantity instead of full close
	exitQty := pos.Quantity
	exitIntent := domain.IntentClose
	if reason == "partial-1" || reason == "partial-2" {
		var partialQty int64
		if reason == "partial-1" {
			partialQty = int64(math.Floor(float64(pos.OriginalQuantity) * cfg.PartialTrigger1Pct))
		} else {
			// Partial-2: exit all remaining shares (avoids tiny leftover remainders)
			partialQty = pos.Quantity
		}
		if partialQty > pos.Quantity {
			partialQty = pos.Quantity
		}
		// If selling this partial would leave a tiny remainder, close the whole position
		remainingAfterPartial := pos.Quantity - partialQty
		if remainingAfterPartial > 0 && remainingAfterPartial <= int64(math.Max(1, float64(pos.OriginalQuantity)*0.05)) {
			partialQty = pos.Quantity
		}
		if partialQty > 0 {
			exitQty = partialQty
			if partialQty < pos.Quantity {
				exitIntent = "partial"
			}
			// If partialQty == pos.Quantity, it's a full close with partial reason
		}
	}

	// Use actual market price for exit orders. In live trading, the order must be
	// priced at or through the current market to fill — clamping to the stop price
	// produces a limit order on the wrong side of the market when price gaps through,
	// causing the order to sit unfilled while losses grow.
	exitPrice := tick.Price

	signal := domain.TradeSignal{
		Symbol:       tick.Symbol,
		Side:         domain.CloseBrokerSide(pos.Side),
		Intent:       exitIntent,
		PositionSide: pos.Side,
		Price:        exitPrice,
		Quantity:     exitQty,
		Reason:       reason,
		Timestamp:    now,
	}

	s.lastExitAt[tick.Symbol] = now

	// Track losses (only for full close)
	if exitIntent == domain.IntentClose {
		pnl := (tick.Price - pos.AvgPrice) * float64(exitQty)
		if domain.IsShort(pos.Side) {
			pnl = -pnl
		}
		dayKey := markethours.TradingDay(now)
		state := s.getSymbolState(tick.Symbol, dayKey)
		if pnl < 0 {
			state.lossExits++
			state.lastLossAt = now
			if isDangerousFailedShortFade(pos, reason) {
				state.dangerousShortFadeExits++
				state.lastDangerousShortFadeAt = now
				state.lastDangerousShortFadeVWAP = pos.PriceVsVWAPPct
				state.lastDangerousShortFadeDist = pos.DistanceHighPct
			}
			s.symbolStates[tick.Symbol] = state
		} else if pnl > 0 && domain.IsShort(pos.Side) {
			state.profitableShortExits++
			state.lastProfitableShortExitAt = now
			state.lastProfitableShortSetup = pos.SetupType
			s.symbolStates[tick.Symbol] = state
		}
	}

	return signal, true
}

func (s *Strategy) getPlaybookExitConfig(playbook string) config.PlaybookExitConfig {
	cfg := s.getConfig()
	switch playbook {
	case "breakout":
		return cfg.PlaybookExits.Breakout
	case "pullback":
		return cfg.PlaybookExits.Pullback
	case "continuation":
		return cfg.PlaybookExits.Continuation
	case "reversal":
		return cfg.PlaybookExits.Reversal
	default:
		return cfg.PlaybookExits.Breakout
	}
}

func (s *Strategy) peekTapePressure(now time.Time) tapePressureState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return decayTapePressureState(s.tapePressure, now)
}

func (s *Strategy) recordTapePressure(c domain.Candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := decayTapePressureState(s.tapePressure, c.Timestamp)
	pressure := candidateDirectionalPressure(c)
	if domain.IsLong(c.Direction) {
		state.bull += pressure
	} else if domain.IsShort(c.Direction) {
		state.bear += pressure
	}
	s.tapePressure = state
}

func decayTapePressureState(state tapePressureState, now time.Time) tapePressureState {
	dayKey := markethours.TradingDay(now)
	if state.dayKey != dayKey {
		return tapePressureState{dayKey: dayKey, lastUpdate: now}
	}
	if state.lastUpdate.IsZero() {
		state.lastUpdate = now
		return state
	}
	elapsed := now.Sub(state.lastUpdate)
	if elapsed <= 0 {
		return state
	}
	decay := math.Exp(-elapsed.Minutes() / 45.0)
	state.bull *= decay
	state.bear *= decay
	state.lastUpdate = now
	return state
}

func candidateDirectionalPressure(c domain.Candidate) float64 {
	base := math.Max(c.StockSelectionScore, 0) + math.Max(c.Score-3.0, 0)*0.35
	if domain.IsLong(c.Direction) {
		base += math.Max(c.ThreeMinuteReturnPct, 0) * 0.12
		base += math.Max(c.FiveMinuteReturnPct, 0) * 0.18
		if c.LeaderRank > 0 && c.LeaderRank <= 2 {
			base += 0.35
		}
		if c.SetupType == "hod_breakout" || c.SetupType == "orb_breakout" {
			base += 0.25
		}
	} else if domain.IsShort(c.Direction) {
		base += math.Max(-c.OneMinuteReturnPct, 0) * 0.12
		base += math.Max(-c.ThreeMinuteReturnPct, 0) * 0.15
		base += math.Max(c.DistanceFromHighPct, 0) * 0.03
		if c.SetupType == "parabolic-failed-reclaim-short" {
			base += 0.45
		}
	}
	if base < 0.25 {
		return 0.25
	}
	return base
}

func shouldBlockLongForBearPressure(c domain.Candidate, tape tapePressureState) bool {
	if !domain.IsLong(c.Direction) {
		return false
	}
	if tape.bear < 4.0 {
		return false
	}
	if tape.bear < tape.bull*1.25 && tape.bear-tape.bull < 1.5 {
		return false
	}
	if isBearPressureLongException(c) {
		return false
	}
	switch c.SetupType {
	case "hod_pullback":
		return true
	case "hod_breakout":
		return !isBearPressureHODBreakoutException(c)
	case "orb_breakout":
		return !isBearPressureORBBreakoutException(c)
	case "orb_reclaim":
		return !isBearPressureORBReclaimException(c)
	case "pullback":
		return !isBearPressurePullbackException(c)
	case "breakout":
		return true
	default:
		return c.Score < 9.4 || c.StockSelectionScore < 4.6
	}
}

func isBearPressureLongException(c domain.Candidate) bool {
	if c.StockSelectionScore >= 4.8 &&
		c.Score >= 9.2 &&
		c.RelativeVolume >= 10.0 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 &&
		(c.SetupType == "orb_reclaim" || c.SetupType == "orb_breakout" || c.SetupType == "hod_breakout") {
		return true
	}
	if c.SetupType == "pullback" && isLeaderOpenDrivePullback(c) &&
		c.StockSelectionScore >= 4.4 &&
		c.Score >= 8.6 {
		return true
	}
	if c.SetupType == "hod_breakout" && isExplosiveLeaderReExpansionBreakout(c) &&
		c.StockSelectionScore >= 4.2 &&
		c.Score >= 8.8 {
		return true
	}
	return false
}

func isBearPressurePullbackException(c domain.Candidate) bool {
	return c.SetupType == "pullback" &&
		isLeaderOpenDrivePullback(c) &&
		c.StockSelectionScore >= 4.5 &&
		c.Score >= 8.8 &&
		c.RelativeVolume >= 18.0 &&
		c.PriceVsVWAPPct <= 6.5
}

func isBearPressureHODBreakoutException(c domain.Candidate) bool {
	if c.SetupType != "hod_breakout" {
		return false
	}
	if c.StockSelectionScore < 4.15 || c.Score < 8.9 {
		return false
	}
	if c.RelativeVolume < 9.0 || c.ThreeMinuteReturnPct < 2.4 || c.FiveMinuteReturnPct < 1.4 {
		return false
	}
	if c.PriceVsVWAPPct < 0.8 || c.PriceVsVWAPPct > 7.0 {
		return false
	}
	if c.DistanceFromHighPct > 0.35 {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 2 && c.VolumeLeaderPct < 35.0 {
		return false
	}
	return c.OneMinuteReturnPct >= 1.0 || c.BreakoutPct >= 0.4
}

func isBearPressureORBBreakoutException(c domain.Candidate) bool {
	if c.SetupType != "orb_breakout" {
		return false
	}
	if c.StockSelectionScore < 4.0 || c.Score < 8.8 {
		return false
	}
	if c.BreakoutPct < 1.0 || c.OneMinuteReturnPct < 1.2 || c.ThreeMinuteReturnPct < 2.0 {
		return false
	}
	if c.PriceVsVWAPPct < 0.4 || c.PriceVsVWAPPct > 5.0 {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 2 && c.VolumeLeaderPct < 35.0 {
		return false
	}
	return true
}

func isBearPressureORBReclaimException(c domain.Candidate) bool {
	if c.SetupType != "orb_reclaim" {
		return false
	}
	if c.StockSelectionScore < 4.0 || c.Score < 8.7 {
		return false
	}
	if c.BreakoutPct < 0.8 || c.ThreeMinuteReturnPct < 1.8 || c.FiveMinuteReturnPct < 1.0 {
		return false
	}
	if c.PriceVsVWAPPct < 0.5 || c.PriceVsVWAPPct > 5.5 {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 2 && c.VolumeLeaderPct < 45.0 {
		return false
	}
	return true
}

func (s *Strategy) checkExitConditions(pos domain.Position, tick domain.Tick) (string, bool) {
	cfg := s.getConfig()
	r := s.currentR(pos, tick.Price)
	exitCfg := s.getPlaybookExitConfig(pos.Playbook)

	// Safety: if stop price is zero (shouldn't happen but defensive), compute one
	if pos.StopPrice == 0 {
		fallbackRisk := pos.AvgPrice * normalizedFallbackRiskPct(cfg) / 100.0
		if fallbackRisk <= 0 {
			fallbackRisk = pos.AvgPrice * 0.02
		}
		if domain.IsLong(pos.Side) {
			computedStop := pos.AvgPrice - fallbackRisk
			if tick.Price <= computedStop {
				return "stop-loss-fallback", true
			}
		} else {
			computedStop := pos.AvgPrice + fallbackRisk
			if tick.Price >= computedStop {
				return "stop-loss-fallback", true
			}
		}
	}

	// Hard stop
	if domain.IsLong(pos.Side) && tick.Price <= pos.StopPrice {
		return "stop-loss", true
	}
	if domain.IsShort(pos.Side) && tick.Price >= pos.StopPrice {
		return "stop-loss", true
	}

	// Phase 3 Change 4: Partial exit framework
	if cfg.PartialExitsEnabled && pos.OriginalQuantity > 0 && pos.Quantity > 0 {
		partialReason, partialExit := s.checkPartialExit(pos, r)
		if partialExit {
			return partialReason, true
		}
	}

	// Phase 3 Change 6: Mean-reversion exit at BB middle
	if cfg.MeanReversionEnabled && isMeanReversionSetup(pos.SetupType) {
		prices := s.recentPrices[pos.Symbol]
		if len(prices) >= cfg.BollingerPeriod {
			_, bbMiddle, _ := scanner.ComputeBollingerBandsFromPrices(prices, cfg.BollingerPeriod, cfg.BollingerK)
			if bbMiddle > 0 {
				if domain.IsLong(pos.Side) && tick.Price >= bbMiddle {
					return "mean-reversion-target", true
				}
				if domain.IsShort(pos.Side) && tick.Price <= bbMiddle {
					return "mean-reversion-target", true
				}
			}
		}
	}

	// Profit target (playbook-specific)
	profitTargetR := exitCfg.ProfitTargetR
	if r >= profitTargetR {
		return "profit-target", true
	}

	// Failed breakout cut (playbook-specific)
	if r <= exitCfg.FailedBreakoutCutR {
		holdMinutes := tick.Timestamp.Sub(pos.OpenedAt).Minutes()
		if holdMinutes < float64(exitCfg.BreakoutFailureWindowMin) {
			return "failed-breakout", true
		}
	}

	// Strong HOD pullback winners should not round-trip after an extreme squeeze
	// extension. Once the trade has produced a large gain, protect it on the
	// first meaningful recoil from the post-entry peak.
	if domain.IsLong(pos.Side) &&
		pos.SetupType == "hod_pullback" &&
		r >= 2.0 &&
		pos.HighestPrice > 0 &&
		pos.EntryATR > 0 &&
		tick.Price <= pos.HighestPrice-pos.EntryATR*0.35 {
		return "momentum-fade", true
	}

	// End of day — close 5 minutes before extended-hours session end (7:55 PM ET)
	sessionEnd := markethours.SessionClose(tick.Timestamp)
	if tick.Timestamp.After(sessionEnd.Add(-5 * time.Minute)) {
		return "end-of-day", true
	}

	// Stagnation check (Change 8 fix: use peakR directly, not pct/100)
	holdMinutes := tick.Timestamp.Sub(pos.OpenedAt).Minutes()
	peakR := s.peakR(pos)
	if holdMinutes > float64(exitCfg.StagnationWindowMin) && peakR < exitCfg.StagnationMinPeakR {
		return "stagnation", true
	}

	// Momentum day trades should not sit for hours without proving themselves.
	if holdMinutes > 60 && peakR < 0.5 && r < 0.2 {
		return "momentum-faded", true
	}
	if holdMinutes > 180 && peakR < 1.0 {
		return "stale-position", true
	}

	return "", false
}

// checkPartialExit determines if a partial exit should be taken.
func (s *Strategy) checkPartialExit(pos domain.Position, r float64) (string, bool) {
	cfg := s.getConfig()
	// Partial 1: at trigger1R, exit trigger1Pct of original
	if pos.PartialsExecuted == 0 && r >= cfg.PartialTrigger1R {
		exitQty := int64(math.Floor(float64(pos.OriginalQuantity) * cfg.PartialTrigger1Pct))
		if exitQty > 0 && exitQty < pos.Quantity {
			return "partial-1", true
		}
	}
	// Partial 2: at trigger2R, exit trigger2Pct of original
	if pos.PartialsExecuted == 1 && r >= cfg.PartialTrigger2R {
		exitQty := int64(math.Floor(float64(pos.OriginalQuantity) * cfg.PartialTrigger2Pct))
		if exitQty > 0 && exitQty <= pos.Quantity {
			return "partial-2", true
		}
	}
	return "", false
}

func isMeanReversionSetup(setupType string) bool {
	return setupType == "mean_reversion_long" || setupType == "mean_reversion_short"
}

// volRegimeTrailFactor returns a multiplier for trail distances based on volatility regime.
func (s *Strategy) volRegimeTrailFactor(pos domain.Position) float64 {
	cfg := s.getConfig()
	if !cfg.AdaptiveTrailEnabled {
		return 1.0
	}
	switch pos.MarketRegime {
	case domain.RegimeBullish:
		if domain.IsLong(pos.Side) {
			return 1.2 // let winners run in favorable regime
		}
		return 0.8 // tighter stops for shorts in bullish
	case domain.RegimeBearish:
		if domain.IsShort(pos.Side) {
			return 1.2
		}
		return 0.8
	case domain.RegimeMixed:
		return 0.9 // slightly tighter in mixed
	default:
		return 1.0
	}
}

func (s *Strategy) updateTrailingStop(pos domain.Position, tick domain.Tick) {
	cfg := s.getConfig()
	r := s.currentR(pos, tick.Price)
	exitCfg := s.getPlaybookExitConfig(pos.Playbook)

	// Phase 3 Change 5: Adaptive trailing stop multiplier
	volFactor := s.volRegimeTrailFactor(pos)

	var newStop float64
	if domain.IsLong(pos.Side) {
		if isExceptionalSqueezePosition(pos) {
			if r >= cfg.BreakEvenMinR && pos.StopPrice < pos.AvgPrice {
				newStop = pos.AvgPrice + pos.EntryATR*0.1
			}
			if pos.EntryATR > 0 && pos.HighestPrice > 0 {
				peakR := s.peakR(pos)
				if peakR >= 2.0 {
					squeezeStop := exceptionalSqueezePeakStop(pos.HighestPrice, pos.EntryATR, peakR)
					if squeezeStop > pos.StopPrice && squeezeStop > newStop {
						newStop = squeezeStop
					}
				}
			}
		} else {
			// Break-even stop
			if r >= cfg.BreakEvenMinR && pos.StopPrice < pos.AvgPrice {
				newStop = pos.AvgPrice + pos.EntryATR*0.1
			}

			// Trailing stop activation (playbook-specific, volatility-adjusted)
			if r >= exitCfg.TrailActivationR {
				trailStop := tick.Price - pos.EntryATR*exitCfg.TrailATRMultiplier*volFactor
				if trailStop > pos.StopPrice {
					newStop = trailStop
				}
			}

			// Tight trail (playbook-specific, volatility-adjusted)
			if r >= exitCfg.TightTrailTriggerR {
				tightStop := tick.Price - pos.EntryATR*exitCfg.TightTrailATRMultiplier*volFactor
				if tightStop > pos.StopPrice {
					newStop = tightStop
				}
			}
		}
	} else {
		// Short trailing stops (mirrored)
		if r >= cfg.BreakEvenMinR && pos.StopPrice > pos.AvgPrice {
			newStop = pos.AvgPrice - pos.EntryATR*0.1
		}
		if r >= exitCfg.TrailActivationR {
			trailStop := tick.Price + pos.EntryATR*exitCfg.TrailATRMultiplier*volFactor
			if trailStop < pos.StopPrice || pos.StopPrice == 0 {
				newStop = trailStop
			}
		}
		if r >= exitCfg.TightTrailTriggerR {
			tightStop := tick.Price + pos.EntryATR*exitCfg.TightTrailATRMultiplier*volFactor
			if tightStop < pos.StopPrice || pos.StopPrice == 0 {
				newStop = tightStop
			}
		}
	}

	// Phase 3 Change 4: Move stop to break-even after partial exit
	if cfg.MoveStopAfterPartial && pos.PartialsExecuted > 0 {
		if domain.IsLong(pos.Side) && newStop < pos.AvgPrice {
			newStop = pos.AvgPrice + pos.EntryATR*0.1
		}
		if domain.IsShort(pos.Side) && (newStop > pos.AvgPrice || newStop == 0) {
			newStop = pos.AvgPrice - pos.EntryATR*0.1
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
	cfg := s.getConfig()
	if c.ATR > 0 {
		risk := c.ATR * cfg.EntryStopATRMultiplier
		maxRisk := c.ATR * cfg.MaxRiskATRMultiplier
		if risk > maxRisk {
			risk = maxRisk
		}
		return risk
	}
	return c.Price * normalizedFallbackRiskPct(cfg) / 100
}

func normalizedFallbackRiskPct(cfg config.TradingConfig) float64 {
	riskPct := cfg.EntryATRPercentFallback
	switch {
	case riskPct <= 0:
		return 2.0
	case riskPct < 0.5:
		// Guard against decimal-vs-percent mistakes like 0.05 meaning 0.05%.
		return 2.0
	default:
		return riskPct
	}
}

func entryRiskMultiplier(c domain.Candidate) float64 {
	if domain.IsShort(c.Direction) {
		switch c.SetupType {
		case "parabolic-failed-reclaim-short":
			if c.Price < 8.0 &&
				c.RelativeVolume >= 200.0 &&
				c.LeaderRank > 0 &&
				c.LeaderRank <= 2 &&
				c.PriceVsVWAPPct > -9.0 {
				return 0.40
			}
			if c.GapPercent > 3.0 &&
				c.PriceVsOpenPct > 0.85 &&
				c.DistanceFromHighPct < 28.0 &&
				c.RelativeVolume >= 30.0 &&
				c.PriceVsVWAPPct > -12.0 &&
				c.StockSelectionScore < 4.0 {
				return 0.55
			}
			if c.DistanceFromHighPct >= 25.0 &&
				c.PriceVsVWAPPct <= -10.0 &&
				c.OneMinuteReturnPct <= -3.0 &&
				c.ThreeMinuteReturnPct <= -4.0 &&
				c.RelativeVolume >= 8.0 {
				return 1.45
			}
			if c.DistanceFromHighPct >= 18.0 &&
				c.PriceVsVWAPPct <= -6.0 &&
				c.ThreeMinuteReturnPct <= -2.5 &&
				c.FiveMinuteReturnPct <= -1.5 {
				return 1.20
			}
		case "breakdown":
			if c.DistanceFromHighPct >= 15.0 &&
				c.PriceVsVWAPPct <= -4.0 &&
				c.ThreeMinuteReturnPct <= -2.0 {
				return 1.10
			}
		}
		return 1.0
	}
	switch c.SetupType {
	case "orb_reclaim":
		if c.StockSelectionScore < 3.5 {
			return 0.45
		}
		if c.GapPercent < 1.0 || c.StockSelectionScore < 3.9 {
			return 0.65
		}
		return 1.50
	case "orb_breakout":
		if c.GapPercent <= 0 {
			return 0.25
		}
		if c.GapPercent < 1.5 &&
			c.StockSelectionScore < 3.9 {
			return 0.30
		}
		if c.PriceVsVWAPPct > 6.5 &&
			c.StockSelectionScore < 4.1 {
			return 0.35
		}
		if c.StockSelectionScore < 3.6 {
			return 0.45
		}
		if c.GapPercent < 2.0 || c.StockSelectionScore < 4.0 ||
			(c.LeaderRank > 2 && c.VolumeLeaderPct < 30.0) {
			return 0.60
		}
		return 1.35
	case "hod_breakout":
		if c.GapPercent <= 0 {
			return 0.25
		}
		if c.GapPercent < 1.0 &&
			c.StockSelectionScore < 4.1 {
			return 0.30
		}
		if c.StockSelectionScore < 3.8 &&
			c.PriceVsVWAPPct > 5.0 {
			return 0.30
		}
		if c.PriceVsVWAPPct > 8.0 &&
			c.ThreeMinuteReturnPct < 3.5 {
			return 0.35
		}
		if c.StockSelectionScore < 3.5 {
			return 0.40
		}
		if c.GapPercent < 2.0 || c.StockSelectionScore < 4.0 ||
			(c.LeaderRank > 2 && c.VolumeLeaderPct < 30.0) {
			return 0.50
		}
		if c.Score >= 9.0 &&
			c.OneMinuteReturnPct >= 2.0 &&
			c.ThreeMinuteReturnPct >= 5.0 &&
			c.RelativeVolume >= 12.0 &&
			c.LeaderRank > 0 &&
			c.LeaderRank <= 2 &&
			c.PriceVsVWAPPct >= 1.0 &&
			c.PriceVsVWAPPct <= 10.0 &&
			c.DistanceFromHighPct <= 0.6 {
			return 1.10
		}
		if c.Score >= 8.5 &&
			c.OneMinuteReturnPct >= 5.0 &&
			c.ThreeMinuteReturnPct >= 7.0 &&
			c.RelativeVolume >= 25.0 &&
			c.VolumeLeaderPct >= 25.0 &&
			c.PriceVsVWAPPct >= 1.0 &&
			c.PriceVsVWAPPct <= 8.0 &&
			c.DistanceFromHighPct <= 0.6 {
			return 0.95
		}
		if c.BreakoutPct >= 0.10 &&
			c.RelativeVolume >= 6 &&
			(c.VolumeLeaderPct >= 20 || (c.LeaderRank > 0 && c.LeaderRank <= 3)) {
			return 0.75
		}
		return 0.55
	case "breakout":
		if c.BreakoutPct >= 0.15 && c.RelativeVolume >= 5 {
			return 1.00
		}
		return 0.80
	case "hod_pullback":
		if c.GapPercent <= 0 {
			return 0.30
		}
		if isStructuredHODPullback(c) {
			if c.LeaderRank > 0 &&
				c.LeaderRank <= 2 &&
				c.RelativeVolume >= 10.0 &&
				c.PriceVsVWAPPct >= 1.5 &&
				c.PriceVsVWAPPct <= 10.5 &&
				c.CloseOffHighPct >= 0.8 {
				return 0.85
			}
			return 0.70
		}
		if isExplosiveHODPullback(c) {
			return 0.35
		}
		return 0.25
	case "pullback":
		if isLeaderOpenDrivePullback(c) {
			return 0.80
		}
		return 0.55
	}
	return 1.0
}

func longBearPressureRiskMultiplier(c domain.Candidate, tape tapePressureState) float64 {
	if !domain.IsLong(c.Direction) {
		return 1.0
	}
	if tape.bear < 3.0 {
		return 1.0
	}
	if tape.bear < tape.bull*1.1 && tape.bear-tape.bull < 1.0 {
		return 1.0
	}
	if isBearPressureLongException(c) {
		return 1.0
	}

	mult := 1.0
	switch c.SetupType {
	case "hod_pullback":
		mult *= 0.45
	case "hod_breakout":
		mult *= 0.55
	case "orb_breakout":
		mult *= 0.50
	case "orb_reclaim":
		mult *= 0.60
	case "pullback":
		if !isLeaderOpenDrivePullback(c) {
			mult *= 0.65
		}
	}
	if c.SetupType != "pullback" && c.GapPercent <= 0 {
		mult *= 0.65
	}
	if c.StockSelectionScore < 3.9 {
		mult *= 0.75
	}
	if c.LeaderRank > 2 && c.VolumeLeaderPct < 30.0 {
		mult *= 0.80
	}
	if mult < 0.20 {
		return 0.20
	}
	return mult
}

func aggressiveEntryLimit(price, atr, pctBuffer, atrFraction float64) float64 {
	if price <= 0 {
		return price
	}
	buffer := price * pctBuffer
	if atr > 0 {
		atrBuffer := atr * atrFraction
		if atrBuffer > 0 && (buffer == 0 || atrBuffer < buffer) {
			buffer = atrBuffer
		}
	}
	if buffer <= 0 {
		return price
	}
	return price + buffer
}

func exceptionalSqueezeEntryLimit(price, atr float64) float64 {
	if price <= 0 {
		return price
	}
	limit := price * 1.15
	if atr > 0 {
		atrLimit := price + atr*1.25
		if atrLimit > limit {
			limit = atrLimit
		}
	}
	return limit
}

func reachedDailyProfitLock(pm *portfolio.Manager, cfg config.TradingConfig) bool {
	if pm == nil || cfg.DailyProfitLockPct <= 0 || cfg.StartingCapital <= 0 {
		return false
	}
	pm.RefreshDayIfNeeded()
	dayNetPnL := pm.DayPnL() + pm.UnrealizedPnL()
	dayROIPct := (dayNetPnL / cfg.StartingCapital) * 100.0
	return dayROIPct >= cfg.DailyProfitLockPct
}

func isExceptionalSqueezePosition(pos domain.Position) bool {
	return domain.IsLong(pos.Side) &&
		pos.SetupType == "hod_breakout" &&
		pos.Playbook == "continuation"
}

func exceptionalSqueezeTrailATRMultiplier(peakR float64) float64 {
	switch {
	case peakR >= 12.0:
		return 8.0
	case peakR >= 4.0:
		return 6.0
	default:
		return 5.0
	}
}

func exceptionalSqueezePeakStop(highestPrice, entryATR, peakR float64) float64 {
	if highestPrice <= 0 || entryATR <= 0 {
		return 0
	}
	stop := highestPrice - entryATR*exceptionalSqueezeTrailATRMultiplier(peakR)
	if peakR >= 12.0 {
		percentStop := highestPrice * 0.775
		if percentStop < stop {
			stop = percentStop
		}
	}
	return stop
}

func isLeaderOpenDrivePullback(c domain.Candidate) bool {
	if !domain.IsLong(c.Direction) || c.SetupType != "pullback" {
		return false
	}
	if c.RelativeVolume < 20 {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 2 && c.VolumeLeaderPct < 40 {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 2 && c.VolumeLeaderPct < 60 {
		return false
	}
	if c.DistanceFromHighPct > 1.8 {
		return false
	}
	if c.PriceVsVWAPPct < 0.1 {
		return false
	}
	if c.PriceVsVWAPPct > 8.0 {
		return false
	}
	if c.ThreeMinuteReturnPct < 1.25 {
		return false
	}
	if c.FiveMinuteReturnPct < 0.5 {
		return false
	}
	if c.ConsolidationRangePct > 9.0 {
		return false
	}
	if c.PullbackDepthPct < 8.0 {
		return false
	}
	if c.CloseOffHighPct < 0.75 {
		return false
	}
	if c.PullbackDepthPct > 50 {
		return false
	}
	return true
}

func rejectWeakLongStockSelection(c domain.Candidate) (string, bool) {
	if !domain.IsLong(c.Direction) {
		return "", false
	}
	if isLeaderOpenDrivePullback(c) || isExceptionalSqueezeBreakout(c) {
		return "", false
	}

	minSelectionScore := 2.9
	switch c.SetupType {
	case "pullback":
		minSelectionScore = 4.25
	case "hod_breakout", "hod_pullback":
		minSelectionScore = 3.25
	case "orb_breakout", "orb_reclaim":
		minSelectionScore = 3.0
	}
	if c.StockSelectionScore < minSelectionScore {
		return "stock-selection-filter", true
	}

	if c.SetupType == "pullback" &&
		c.StockSelectionScore < 4.0 &&
		c.RelativeVolume < 8 &&
		c.IntradayReturnPct < 8 {
		return "stock-selection-filter", true
	}

	if c.SetupType == "pullback" &&
		!isLeaderOpenDrivePullback(c) &&
		c.PriceVsVWAPPct > 12.0 &&
		(c.LeaderRank == 0 || c.LeaderRank > 3 || c.VolumeLeaderPct < 50) {
		return "pullback-late-extension", true
	}

	if c.SetupType == "pullback" &&
		!isLeaderOpenDrivePullback(c) &&
		c.ConsolidationRangePct >= 8.0 &&
		c.PullbackDepthPct < math.Max(4.0, c.ConsolidationRangePct*0.65) {
		return "pullback-reclaim-filter", true
	}

	if c.SetupType == "pullback" &&
		!isLeaderOpenDrivePullback(c) &&
		c.PriceVsVWAPPct > 14.0 &&
		c.ConsolidationRangePct > 14.0 &&
		c.PullbackDepthPct < 10.0 {
		return "pullback-late-extension", true
	}
	if (c.SetupType == "pullback" || c.SetupType == "hod_pullback") &&
		c.GapPercent < 1.0 &&
		c.ATRPct >= 6.0 &&
		c.StockSelectionScore < 3.9 &&
		c.LeaderRank > 1 &&
		c.VolumeLeaderPct < 40.0 {
		return "stock-selection-filter", true
	}

	if c.SetupType == "orb_reclaim" &&
		c.LeaderRank > 3 &&
		c.VolumeLeaderPct < 8 {
		return "stock-selection-filter", true
	}

	if c.SetupType == "orb_reclaim" &&
		c.ThreeMinuteReturnPct < 0.9 &&
		c.BreakoutPct < 0.6 {
		return "stock-selection-filter", true
	}

	if c.SetupType == "orb_breakout" &&
		c.LeaderRank > 1 &&
		c.VolumeLeaderPct < 5 &&
		c.BreakoutPct < 0.6 {
		return "stock-selection-filter", true
	}

	if c.SetupType == "orb_breakout" &&
		c.PriceVsVWAPPct > 18.0 &&
		c.BreakoutPct < 0.25 &&
		c.ConsolidationRangePct > 12.0 {
		return "stock-selection-filter", true
	}
	if c.SetupType == "orb_breakout" &&
		c.PriceVsVWAPPct > 10.0 &&
		c.ATRPct >= 7.0 &&
		c.ThreeMinuteReturnPct >= 4.0 &&
		c.FiveMinuteReturnPct >= 4.0 {
		return "late-extension-chase", true
	}
	if c.SetupType == "orb_breakout" &&
		c.PriceVsVWAPPct > 8.0 &&
		c.DistanceFromHighPct > 1.0 &&
		c.OneMinuteReturnPct >= 1.0 &&
		c.ThreeMinuteReturnPct >= 4.0 &&
		c.ATRPct >= 6.0 {
		return "late-extension-chase", true
	}

	if c.LeaderRank > 0 &&
		c.LeaderRank > 5 &&
		c.StockSelectionScore < 4.25 &&
		c.VolumeLeaderPct < 20 {
		return "stock-selection-filter", true
	}

	return "", false
}

func rejectWeakShortMomentum(c domain.Candidate, cfg config.TradingConfig) (string, bool) {
	if !domain.IsShort(c.Direction) {
		return "", false
	}

	hardFlush := c.BreakoutPct <= -4.0 || c.OneMinuteReturnPct <= -5.0 || c.ThreeMinuteReturnPct <= -6.0

	minSelectionScore := 2.5
	switch c.SetupType {
	case "parabolic-failed-reclaim-short":
		minSelectionScore = 2.8
	case "breakdown":
		minSelectionScore = 2.6
	}
	if c.StockSelectionScore < minSelectionScore && !hardFlush {
		return "short-selection-filter", true
	}

	minScore := math.Max(cfg.ShortMinEntryScore, 3.2)
	if c.Score < minScore && !hardFlush {
		return "short-selection-filter", true
	}

	if c.SetupType == "parabolic-failed-reclaim-short" {
		if c.Price < 4.0 &&
			c.RelativeVolume >= 100.0 &&
			c.ATRPct >= 12.0 &&
			c.DistanceFromHighPct < 40.0 &&
			c.PriceVsVWAPPct > -12.0 &&
			c.BreakoutPct > -4.0 {
			return "cheap-short-squeeze-fade", true
		}
		if c.Price < 10.0 &&
			c.GapPercent > 5.0 &&
			c.RelativeVolume >= 80.0 &&
			c.PriceVsOpenPct > 0.90 &&
			c.DistanceFromHighPct < 30.0 &&
			c.PriceVsVWAPPct > -9.0 &&
			c.FiveMinuteReturnPct > -6.5 &&
			!hardFlush {
			return "cheap-gap-squeeze-short", true
		}
		if c.LeaderRank > 0 &&
			c.LeaderRank <= 2 &&
			c.RelativeVolume >= 5.0 &&
			c.DistanceFromHighPct < 25.0 &&
			c.PriceVsVWAPPct > -10.0 &&
			c.ThreeMinuteReturnPct > -5.0 &&
			!hardFlush {
			return "short-strong-leader", true
		}
		if c.DistanceFromHighPct >= 40.0 &&
			c.PriceVsVWAPPct <= -20.0 &&
			c.BreakoutPct <= -8.0 &&
			c.OneMinuteReturnPct <= -8.0 {
			return "late-flush-chase", true
		}
	}

	minVWAPBreakPct := math.Max(cfg.ShortVWAPBreakMinPct, 1.0)
	if c.PriceVsVWAPPct > -minVWAPBreakPct && !hardFlush {
		return "short-vwap-break", true
	}
	if c.OneMinuteReturnPct > -0.75 {
		return "short-no-impulse", true
	}
	if c.ThreeMinuteReturnPct > -1.5 {
		return "short-no-impulse", true
	}
	if c.FiveMinuteReturnPct > -0.75 {
		return "short-no-confirmation", true
	}
	if c.DistanceFromHighPct < math.Max(cfg.ShortPeakExtensionMinPct*0.60, 6.0) && !hardFlush {
		return "short-fade-too-early", true
	}
	if c.RelativeVolume < math.Max(cfg.MinRelativeVolume, 3.5) {
		return "short-selection-filter", true
	}
	if c.LeaderRank > 0 &&
		c.LeaderRank <= 2 &&
		c.DistanceFromHighPct < 15.0 &&
		c.PriceVsVWAPPct > -4.0 {
		return "short-strong-leader", true
	}

	return "", false
}

func rejectRepeatedDangerousShortFade(c domain.Candidate, state symbolTradeState) (string, bool) {
	if !domain.IsShort(c.Direction) || c.SetupType != "parabolic-failed-reclaim-short" {
		return "", false
	}
	if state.dangerousShortFadeExits <= 0 || state.lastDangerousShortFadeAt.IsZero() {
		return "", false
	}
	since := c.Timestamp.Sub(state.lastDangerousShortFadeAt)
	if since < 0 || since > 2*time.Hour {
		return "", false
	}

	stillStrongLeader := (c.LeaderRank > 0 && c.LeaderRank <= 2) || c.VolumeLeaderPct >= 25.0
	if !stillStrongLeader {
		return "", false
	}

	// Allow a retry only if the new candidate is materially more extended than the
	// failed one, which indicates the squeeze has truly rolled over rather than
	// simply bouncing after the first failed fade.
	moreExtended := c.PriceVsVWAPPct <= state.lastDangerousShortFadeVWAP-4.0 &&
		c.DistanceFromHighPct >= state.lastDangerousShortFadeDist+10.0
	if moreExtended {
		return "", false
	}

	hardFlush := c.BreakoutPct <= -4.0 || c.OneMinuteReturnPct <= -5.0 || c.ThreeMinuteReturnPct <= -6.0
	if hardFlush &&
		c.PriceVsVWAPPct <= -12.0 &&
		c.DistanceFromHighPct >= 35.0 &&
		c.FiveMinuteReturnPct <= -2.0 {
		return "", false
	}

	return "repeat-short-fade-failure", true
}

func isDangerousFailedShortFade(pos domain.Position, exitReason string) bool {
	if !domain.IsShort(pos.Side) || pos.SetupType != "parabolic-failed-reclaim-short" {
		return false
	}

	leaderishEntry := (pos.LeaderRank > 0 && pos.LeaderRank <= 2) || pos.VolumeLeaderPct >= 25.0
	if !leaderishEntry {
		return false
	}

	mfeR, maeR := computePositionExcursion(pos)
	switch exitReason {
	case "stop-loss":
		return maeR >= 1.0 && mfeR < 1.0
	case "failed-breakout":
		return mfeR < 0.5
	case "stagnation":
		return mfeR < 0.15 &&
			pos.PriceVsVWAPPct > -12.0 &&
			pos.DistanceHighPct < 35.0
	default:
		return false
	}
}

func computePositionExcursion(pos domain.Position) (mfeR, maeR float64) {
	if pos.RiskPerShare <= 0 || pos.AvgPrice <= 0 {
		return 0, 0
	}
	if domain.IsShort(pos.Side) {
		if pos.LowestPrice > 0 {
			mfeR = (pos.AvgPrice - pos.LowestPrice) / pos.RiskPerShare
		}
		if pos.HighestPrice > 0 {
			maeR = (pos.HighestPrice - pos.AvgPrice) / pos.RiskPerShare
		}
		return max(mfeR, 0), max(maeR, 0)
	}
	if pos.HighestPrice > 0 {
		mfeR = (pos.HighestPrice - pos.AvgPrice) / pos.RiskPerShare
	}
	if pos.LowestPrice > 0 {
		maeR = (pos.AvgPrice - pos.LowestPrice) / pos.RiskPerShare
	}
	return max(mfeR, 0), max(maeR, 0)
}

func rejectRepeatedLongImpulse(c domain.Candidate, state symbolTradeState) (string, bool) {
	if !isHODContinuationSetup(c.SetupType) || state.lastLongEntryAt.IsZero() {
		return "", false
	}

	requiredExtensionPct := 0.75
	if c.ATRPct > 0 {
		atrBased := c.ATRPct * 0.35
		if atrBased < 0.4 {
			atrBased = 0.4
		}
		if atrBased > 2.0 {
			atrBased = 2.0
		}
		if atrBased > requiredExtensionPct {
			requiredExtensionPct = atrBased
		}
	}

	if state.lastLongImpulseHigh > 0 && c.HighOfDay > 0 {
		extensionPct := ((c.HighOfDay - state.lastLongImpulseHigh) / state.lastLongImpulseHigh) * 100
		if extensionPct >= requiredExtensionPct {
			return "", false
		}
	}

	if c.SetupType == "hod_pullback" {
		if c.OneMinuteReturnPct >= 1.0 &&
			c.ThreeMinuteReturnPct >= 2.0 &&
			c.FiveMinuteReturnPct >= 1.2 &&
			c.DistanceFromHighPct <= 1.0 &&
			c.PriceVsVWAPPct >= 1.5 {
			return "", false
		}
	}

	return "await-new-impulse", true
}

func rejectLongAfterProfitableShort(c domain.Candidate, state symbolTradeState) (string, bool) {
	if !domain.IsLong(c.Direction) || state.lastProfitableShortExitAt.IsZero() {
		return "", false
	}
	since := c.Timestamp.Sub(state.lastProfitableShortExitAt)
	if since < 0 || since > 8*time.Hour {
		return "", false
	}
	if isElitePostShortFadeLong(c) {
		return "", false
	}

	strictFailedSqueeze := state.lastProfitableShortSetup == "parabolic-failed-reclaim-short"
	switch c.SetupType {
	case "pullback", "hod_pullback":
		if strictFailedSqueeze &&
			(c.PriceVsVWAPPct > 0.5 ||
				c.DistanceFromHighPct > 0.6 ||
				c.ThreeMinuteReturnPct < 3.0 ||
				c.FiveMinuteReturnPct < 2.0) {
			return "post-short-fade-long-block", true
		}
		if c.PriceVsVWAPPct > 4.0 &&
			(c.LeaderRank == 0 || c.LeaderRank > 1 || c.VolumeLeaderPct < 70.0) {
			return "post-short-fade-long-block", true
		}
	case "orb_breakout", "hod_breakout":
		if strictFailedSqueeze &&
			(c.GapPercent <= 1.0 ||
				c.PriceVsVWAPPct > 2.5 ||
				c.DistanceFromHighPct > 0.25 ||
				c.VolumeLeaderPct < 70.0) {
			return "post-short-fade-long-block", true
		}
		if c.GapPercent <= 0 &&
			c.PriceVsVWAPPct > 4.0 &&
			(c.LeaderRank == 0 || c.LeaderRank > 2 || c.VolumeLeaderPct < 60.0) {
			return "post-short-fade-long-block", true
		}
	default:
		if strictFailedSqueeze &&
			c.PriceVsVWAPPct > 6.0 &&
			c.StockSelectionScore < 4.7 {
			return "post-short-fade-long-block", true
		}
	}

	return "", false
}

func isElitePostShortFadeLong(c domain.Candidate) bool {
	if !domain.IsLong(c.Direction) {
		return false
	}
	if c.StockSelectionScore < 4.8 || c.Score < 9.2 || c.RelativeVolume < 30.0 {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 1 && c.VolumeLeaderPct < 80.0 {
		return false
	}

	switch c.SetupType {
	case "pullback":
		return isLeaderOpenDrivePullback(c) &&
			c.PriceVsVWAPPct <= 4.0 &&
			c.ThreeMinuteReturnPct >= 3.0 &&
			c.FiveMinuteReturnPct >= 2.0
	case "hod_pullback":
		return isStructuredHODPullback(c) &&
			c.DistanceFromHighPct <= 1.0 &&
			c.PriceVsVWAPPct <= 5.0
	case "orb_breakout", "hod_breakout":
		return c.GapPercent >= 5.0 &&
			c.PriceVsVWAPPct <= 4.0 &&
			c.BreakoutPct >= 1.0 &&
			c.OneMinuteReturnPct >= 1.5 &&
			c.ThreeMinuteReturnPct >= 4.0
	default:
		return false
	}
}

func rejectWeakLongBreakout(c domain.Candidate, cfg config.TradingConfig) (string, bool) {
	if !domain.IsLong(c.Direction) {
		return "", false
	}
	if isExceptionalSqueezeBreakout(c) {
		return "", false
	}

	switch c.SetupType {
	case "hod_pullback":
		return rejectWeakHODPullback(c, cfg)
	case "hod_breakout":
		// Continue into the breakout-specific quality checks below.
	case "orb_breakout":
		if c.Price < 6.0 &&
			c.LeaderRank > 2 &&
			c.VolumeLeaderPct < 35.0 &&
			c.RelativeVolume < 12.0 {
			return "low-leader-cheap-breakout", true
		}
		if c.GapPercent < 0 &&
			c.PriceVsVWAPPct > 6.0 &&
			c.RelativeVolume < 80.0 &&
			(c.LeaderRank == 0 || c.LeaderRank > 2 || c.VolumeLeaderPct < 60.0) {
			return "negative-gap-breakout-chase", true
		}
		return "", false
	default:
		return "", false
	}

	minScore := math.Max(cfg.MinEntryScore+0.8, 4.6)
	if c.Score < minScore {
		return "hod-breakout-quality", true
	}
	if c.RelativeVolume < math.Max(cfg.HODMomoMinRelativeVolume, 5.0) {
		return "hod-breakout-quality", true
	}
	leaderLimit := cfg.MaxVolumeLeaders
	if leaderLimit <= 0 || leaderLimit > 4 {
		leaderLimit = 4
	}
	if c.LeaderRank > 0 && c.LeaderRank > leaderLimit && c.VolumeLeaderPct < 20 {
		return "hod-breakout-quality", true
	}
	if c.LeaderRank > 1 &&
		c.VolumeLeaderPct < 5 &&
		c.StockSelectionScore < 3.7 {
		return "hod-breakout-quality", true
	}
	if c.LeaderRank > 1 &&
		c.VolumeLeaderPct < 15 &&
		c.ThreeMinuteReturnPct < 3.0 {
		return "hod-breakout-quality", true
	}
	if c.Price < 6.0 &&
		c.LeaderRank > 2 &&
		c.VolumeLeaderPct < 35.0 &&
		c.RelativeVolume < 12.0 {
		return "low-leader-cheap-breakout", true
	}
	if c.SetupType == "hod_breakout" &&
		c.VolumeRate < 10000 &&
		c.RelativeVolume < 20.0 {
		return "hod-breakout-quality", true
	}
	if c.SetupType == "hod_breakout" &&
		c.RelativeVolume < 6.5 &&
		c.FiveMinuteReturnPct < 1.6 &&
		c.ThreeMinuteReturnPct < 5.0 &&
		c.PriceVsVWAPPct > 5.0 {
		return "hod-breakout-quality", true
	}
	if c.DistanceFromHighPct > 0.6 {
		return "hod-breakout-quality", true
	}
	if c.OneMinuteReturnPct < math.Max(cfg.MinOneMinuteReturnPct, 0.35) {
		return "hod-breakout-quality", true
	}
	if c.ThreeMinuteReturnPct < math.Max(cfg.MinThreeMinuteReturnPct, 1.0) {
		return "hod-breakout-quality", true
	}
	if c.FiveMinuteReturnPct < math.Max(cfg.MinThreeMinuteReturnPct, 1.1) {
		return "hod-breakout-quality", true
	}
	if c.PriceVsVWAPPct < 0.8 {
		return "hod-breakout-quality", true
	}
	if c.PullbackDepthPct > 0 &&
		c.PullbackDepthPct < 2.5 &&
		c.ConsolidationRangePct > 10.0 &&
		c.PriceVsVWAPPct > 8.0 {
		return "hod-breakout-quality", true
	}
	if c.OneMinuteReturnPct > math.Max(3.5, c.ThreeMinuteReturnPct*1.25) &&
		c.ThreeMinuteReturnPct < 3.0 &&
		c.PriceVsVWAPPct > 6.0 &&
		c.ConsolidationRangePct < 7.0 {
		return "hod-breakout-quality", true
	}
	if c.PriceVsVWAPPct > 9.0 &&
		c.ATRPct >= 4.0 &&
		c.FiveMinuteReturnPct >= 5.0 &&
		(c.OneMinuteReturnPct >= 4.0 || c.ThreeMinuteReturnPct >= 8.0) {
		return "late-extension-chase", true
	}
	if c.PriceVsVWAPPct > 11.0 &&
		c.RelativeVolume < 12.0 &&
		c.FiveMinuteReturnPct >= 8.0 &&
		c.ThreeMinuteReturnPct >= 8.0 {
		return "late-extension-chase", true
	}
	if c.PriceVsVWAPPct > 15.0 &&
		c.FiveMinuteReturnPct >= 4.0 &&
		c.OneMinuteReturnPct >= 2.5 {
		return "late-extension-chase", true
	}
	if c.PriceVsVWAPPct > 18.0 &&
		c.OneMinuteReturnPct >= 5.5 &&
		c.ThreeMinuteReturnPct >= 10.0 {
		return "late-extension-chase", true
	}
	if c.GapPercent < 0 &&
		c.PriceVsVWAPPct > 6.0 &&
		c.RelativeVolume < 80.0 &&
		(c.LeaderRank == 0 || c.LeaderRank > 2 || c.VolumeLeaderPct < 60.0) {
		return "negative-gap-breakout-chase", true
	}
	if c.PriceVsVWAPPct > 25.0 &&
		c.ConsolidationRangePct > 10.0 &&
		c.RelativeVolume < 40.0 {
		return "late-extension-chase", true
	}

	lateChaseVWAPCap := 12.0
	if c.ATRPct > 0 {
		atrCap := c.ATRPct * 1.25
		if atrCap > lateChaseVWAPCap && atrCap < 18.0 {
			lateChaseVWAPCap = atrCap
		}
	}
	if c.PriceVsVWAPPct > lateChaseVWAPCap && c.BreakoutPct < 0.35 {
		if !isExplosiveLeaderReExpansionBreakout(c) {
			return "late-extension-chase", true
		}
	}

	freshBreakout := c.BreakoutPct >= 0.15
	if !freshBreakout && c.DistanceFromHighPct <= 0.15 && c.OneMinuteReturnPct >= math.Max(cfg.MinOneMinuteReturnPct, 0.6) {
		freshBreakout = true
	}
	if !freshBreakout {
		if c.ThreeMinuteReturnPct < 2.0 ||
			c.FiveMinuteReturnPct < 1.5 ||
			c.PriceVsVWAPPct > 12.0 ||
			(c.VolumeLeaderPct < 15 && (c.LeaderRank == 0 || c.LeaderRank > 3)) {
			return "await-breakout-expansion", true
		}
	}

	return "", false
}

func isExceptionalSqueezeBreakout(c domain.Candidate) bool {
	if !domain.IsLong(c.Direction) {
		return false
	}
	switch c.SetupType {
	case "hod_breakout", "orb_breakout":
	default:
		return false
	}
	if c.Score < 7.5 || c.RelativeVolume < 10.0 {
		return false
	}
	if c.IntradayReturnPct < 60.0 {
		return false
	}
	if c.OneMinuteReturnPct < 5.0 || c.FiveMinuteReturnPct < 4.0 {
		return false
	}
	if c.PriceVsVWAPPct < 18.0 || c.PriceVsVWAPPct > 40.0 {
		return false
	}
	if c.DistanceFromHighPct > 0.5 {
		return false
	}
	if c.ConsolidationRangePct < 10.0 {
		return false
	}
	// Keep this carve-out for fresh ignition bars on thin squeeze names, not
	// already-crowded multi-bar vertical runs that should still face the normal
	// HOD breakout quality filters.
	if c.ThreeMinuteReturnPct > 8.0 && c.FiveMinuteReturnPct > 12.0 {
		return false
	}
	if c.RelativeVolume > 40.0 && c.VolumeRate > 15000 {
		return false
	}
	return true
}

func rejectWeakHODPullback(c domain.Candidate, cfg config.TradingConfig) (string, bool) {
	minScore := math.Max(cfg.MinEntryScore+1.0, 4.8)
	if c.Score < minScore {
		return "pullback-reclaim-filter", true
	}
	if c.RelativeVolume < math.Max(cfg.HODMomoMinRelativeVolume, 5.0) {
		return "pullback-reclaim-filter", true
	}
	if c.LeaderRank > 1 &&
		c.VolumeLeaderPct < 20.0 &&
		c.DistanceFromHighPct > 2.0 &&
		c.PriceVsVWAPPct > 5.0 {
		return "pullback-reclaim-filter", true
	}
	if c.DistanceFromHighPct > allowedHODPullbackDistance(c) {
		return "pullback-reclaim-filter", true
	}
	if c.OneMinuteReturnPct < math.Max(cfg.MinOneMinuteReturnPct, 0.35) {
		return "pullback-reclaim-filter", true
	}
	if c.ThreeMinuteReturnPct < math.Max(cfg.MinThreeMinuteReturnPct, 0.85) {
		return "pullback-reclaim-filter", true
	}
	if c.FiveMinuteReturnPct < math.Max(cfg.MinThreeMinuteReturnPct, 1.2) {
		return "pullback-reclaim-filter", true
	}
	if c.PriceVsVWAPPct < 1.0 {
		return "pullback-reclaim-filter", true
	}

	structuredReclaim := isStructuredHODPullback(c)
	explosiveReclaim := isExplosiveHODPullback(c)

	lateChaseVWAPCap := structuredHODPullbackVWAPCap(c)
	if explosiveReclaim {
		lateChaseVWAPCap = math.Max(lateChaseVWAPCap, explosiveHODPullbackVWAPCap(c))
	}
	if c.PriceVsVWAPPct > lateChaseVWAPCap && !explosiveReclaim {
		return "pullback-late-extension", true
	}
	if explosiveReclaim && c.PriceVsVWAPPct > lateChaseVWAPCap+4.0 {
		return "pullback-late-extension", true
	}
	if c.DistanceFromHighPct > 3.25 &&
		c.PriceVsVWAPPct > 3.5 &&
		c.CloseOffHighPct < 0.75 &&
		c.PullbackDepthPct < 15.0 {
		return "pullback-reclaim-filter", true
	}
	if c.DistanceFromHighPct > 4.75 &&
		c.PriceVsVWAPPct > 9.0 &&
		c.OneMinuteReturnPct > 4.0 &&
		c.PullbackDepthPct < 15.0 {
		return "pullback-late-extension", true
	}

	if !structuredReclaim && !explosiveReclaim {
		return "pullback-reclaim-filter", true
	}

	return "", false
}

func allowedHODPullbackDistance(c domain.Candidate) float64 {
	cap := 2.2
	if c.IntradayReturnPct >= 8 &&
		c.RelativeVolume >= 8 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 &&
		cap < 4.25 {
		cap = 4.25
	}
	if c.IntradayReturnPct >= 15 &&
		c.RelativeVolume >= 15 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 &&
		cap < 6.5 {
		cap = 6.5
	}
	return cap
}

func isStructuredHODPullback(c domain.Candidate) bool {
	distCap := 1.8
	rangeCap := 3.2
	closeOffHighCap := 1.15
	minPullbackDepth := 18.0
	if c.IntradayReturnPct >= 8 &&
		c.RelativeVolume >= 8 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 {
		distCap = 4.25
		rangeCap = 8.5
		closeOffHighCap = 2.5
	}
	if c.IntradayReturnPct >= 15 &&
		c.RelativeVolume >= 15 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 {
		distCap = 6.5
		minPullbackDepth = 8.0
	}
	if c.DistanceFromHighPct > distCap {
		return false
	}
	if c.ConsolidationRangePct < 0.35 || c.ConsolidationRangePct > rangeCap {
		return false
	}
	if c.PullbackDepthPct < minPullbackDepth || c.PullbackDepthPct > 82 {
		return false
	}
	if c.CloseOffHighPct > closeOffHighCap {
		return false
	}
	if c.OneMinuteReturnPct < 0.5 {
		return false
	}
	if c.ThreeMinuteReturnPct < 1.2 {
		return false
	}
	if c.FiveMinuteReturnPct < 1.5 {
		return false
	}
	priceVsVWAPCap := structuredHODPullbackVWAPCap(c)
	if c.PriceVsVWAPPct > priceVsVWAPCap {
		return false
	}
	return true
}

func isExplosiveHODPullback(c domain.Candidate) bool {
	if c.OneMinuteReturnPct < 3.0 ||
		c.ThreeMinuteReturnPct < 6.0 ||
		c.FiveMinuteReturnPct < 4.0 {
		return false
	}
	if c.RelativeVolume < 8.0 {
		return false
	}
	if c.DistanceFromHighPct > 1.2 {
		return false
	}
	if c.CloseOffHighPct > 0.9 {
		return false
	}
	priceVsVWAPCap := explosiveHODPullbackVWAPCap(c)
	if c.PriceVsVWAPPct > priceVsVWAPCap {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 2 && c.VolumeLeaderPct < 15 {
		return false
	}
	if c.PullbackDepthPct > 85 {
		return false
	}
	return true
}

func structuredHODPullbackVWAPCap(c domain.Candidate) float64 {
	cap := 8.0
	if c.ATRPct > 0 {
		dynamicCap := c.ATRPct * 1.8
		if dynamicCap > cap && dynamicCap < 18.0 {
			cap = dynamicCap
		}
	}
	if c.IntradayReturnPct >= 8 &&
		c.RelativeVolume >= 8 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 {
		intradayCap := c.IntradayReturnPct
		if intradayCap > cap && intradayCap < 22.0 {
			cap = intradayCap
		}
	}
	if c.IntradayReturnPct >= 8 &&
		c.RelativeVolume >= 8 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 &&
		cap < 9.0 {
		cap = 9.0
	}
	return cap
}

func explosiveHODPullbackVWAPCap(c domain.Candidate) float64 {
	cap := 12.0
	if c.ATRPct > 0 {
		dynamicCap := c.ATRPct * 3.25
		if dynamicCap > cap && dynamicCap < 28.0 {
			cap = dynamicCap
		}
	}
	if c.IntradayReturnPct >= 20 &&
		c.RelativeVolume >= 10 &&
		c.LeaderRank > 0 &&
		c.LeaderRank <= 2 &&
		cap < 20.0 {
		cap = 20.0
	}
	return cap
}

func isExplosiveLeaderReExpansionBreakout(c domain.Candidate) bool {
	if c.SetupType != "hod_breakout" {
		return false
	}
	if c.RelativeVolume < 20 {
		return false
	}
	if c.IntradayReturnPct < 20 {
		return false
	}
	if c.OneMinuteReturnPct < 4.0 || c.ThreeMinuteReturnPct < 7.0 || c.FiveMinuteReturnPct < 7.0 {
		return false
	}
	if c.LeaderRank > 0 && c.LeaderRank > 2 && c.VolumeLeaderPct < 30 {
		return false
	}
	return true
}

func isHODContinuationSetup(setupType string) bool {
	switch setupType {
	case "hod_breakout", "hod_pullback":
		return true
	default:
		return false
	}
}

func (s *Strategy) getSymbolState(symbol, dayKey string) symbolTradeState {
	state, ok := s.symbolStates[symbol]
	if !ok || state.dayKey != dayKey {
		state = symbolTradeState{
			dayKey: dayKey,
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
