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
	"github.com/edwintcloud/momentum-trading-bot/internal/ml"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/sector"
	"github.com/edwintcloud/momentum-trading-bot/internal/signals"
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
	scorer              ml.Scorer
	driftDetector       *ml.DriftDetector
	lastEntryAt         map[string]time.Time
	lastExitAt          map[string]time.Time
	symbolStates        map[string]symbolTradeState
	reallocationTargets map[string]bool
	recentPrices        map[string][]float64 // for Bollinger Band exit on mean-reversion
	signalAggregator    *signals.Aggregator
	recentSignals       map[string][]signals.Signal // latest alpha signals per symbol
}

type symbolTradeState struct {
	dayKey       string
	entrySignals int
	lossExits    int
	lastLossAt   time.Time
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
		recentSignals:       make(map[string][]signals.Signal),
	}
	for _, opt := range opts {
		switch v := opt.(type) {
		case *risk.Engine:
			s.riskEngine = v
		case *risk.VolatilityEstimator:
			s.volEstimator = v
		case *signals.Aggregator:
			s.signalAggregator = v
		case ml.Scorer:
			s.scorer = v
		case *ml.DriftDetector:
			s.driftDetector = v
		}
	}
	if s.signalAggregator == nil {
		s.signalAggregator = BuildSignalAggregator(cfg)
	}
	return s
}

// BuildSignalAggregator creates a signal aggregator from config.
func BuildSignalAggregator(cfg config.TradingConfig) *signals.Aggregator {
	var sources []signals.SignalSource

	sources = append(sources, signals.NewOFI(signals.OFIConfig{
		Enabled:           cfg.OFIEnabled,
		WindowBars:        cfg.OFIWindowBars,
		ThresholdSigma:    cfg.OFIThresholdSigma,
		PersistenceMinBar: cfg.OFIPersistenceMin,
	}))

	sources = append(sources, signals.NewVPIN(signals.VPINConfig{
		Enabled:         cfg.VPINEnabled,
		BucketDivisor:   cfg.VPINBucketDivisor,
		LookbackBuckets: cfg.VPINLookbackBuckets,
		HighThreshold:   cfg.VPINHighThreshold,
		LowThreshold:    cfg.VPINLowThreshold,
	}))

	sources = append(sources, signals.NewOBVDivergence(signals.OBVConfig{
		Enabled:      cfg.OBVDivergenceEnabled,
		LookbackBars: cfg.OBVLookbackBars,
	}))

	sources = append(sources, signals.NewDollarBarBuilder(signals.DollarBarConfig{
		Enabled:   cfg.DollarBarsEnabled,
		Threshold: cfg.DollarBarThreshold,
	}))

	sources = append(sources, signals.NewVolumeBarBuilder(signals.VolumeBarConfig{
		Enabled:   cfg.VolumeBarsEnabled,
		Threshold: cfg.VolumeBarThreshold,
	}))

	sources = append(sources, signals.NewORB(signals.ORBConfig{
		Enabled:          cfg.ORBEnabled,
		WindowMinutes:    cfg.ORBWindowMinutes,
		BufferPct:        cfg.ORBBufferPct,
		VolumeMultiplier: cfg.ORBVolumeMultiplier,
		MaxGapPct:        cfg.ORBMaxGapPct,
		TargetMultiplier: cfg.ORBTargetMultiplier,
	}))

	return signals.NewAggregator(sources...)
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
				if reason != "" && reason != "market-closed"  && reason != "system-paused" {
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
	} else if candidate.Score < cfg.MinEntryScore {
		reason = "low-score"
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
	if reason == "no-signal" && cfg.EntryDeadlineMinutesAfterOpen > 0 {
		minutesSinceOpen := markethours.MinutesSinceOpen(candidate.Timestamp)
		if minutesSinceOpen > float64(cfg.EntryDeadlineMinutesAfterOpen) {
			reason = "past-entry-deadline"
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
	if !markethours.IsTradableSessionAt(now) {
		return domain.TradeSignal{}, false, "market-closed"
	}

	// Entry deadline: block entries after N minutes from open
	if cfg.EntryDeadlineMinutesAfterOpen > 0 {
		minutesSinceOpen := markethours.MinutesSinceOpen(now)
		if minutesSinceOpen > float64(cfg.EntryDeadlineMinutesAfterOpen) {
			return domain.TradeSignal{}, false, "past-entry-deadline"
		}
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

	// Score threshold already checked in scanner, but double-check
	minScore := cfg.MinEntryScore
	if c.Direction == domain.DirectionShort {
		minScore = cfg.ShortMinEntryScore
	}

	// Phase 3 Change 3: Time-of-day adaptive score threshold
	tw := currentTimeWindow(now)
	if cfg.TimeOfDayEnabled {
		twCfg := defaultTimeWindowConfigs[tw]
		multiplier := twCfg.ScoreThresholdMultiplier
		// Allow configurable midday multiplier override
		if tw == TimeWindowMidDay && cfg.MidDayScoreMultiplier > 0 {
			multiplier = cfg.MidDayScoreMultiplier
		}
		minScore *= multiplier
	}

	if c.Score < minScore {
		return domain.TradeSignal{}, false, "low-score"
	}

	// Regime gating
	if cfg.RegimeGatingEnabled {
		switch c.MarketRegime {
		case domain.RegimeBearish:
			if c.Direction == domain.DirectionLong {
				return domain.TradeSignal{}, false, "regime-gated"
			}
		case domain.RegimeBullish:
			if c.Direction == domain.DirectionShort {
				return domain.TradeSignal{}, false, "regime-gated"
			}
		case domain.RegimeMixed:
			boosted := minScore * cfg.RegimeMixedScoreBoost
			if c.Score < boosted {
				return domain.TradeSignal{}, false, "regime-gated"
			}
		case domain.RegimeNeutral:
			boosted := minScore * cfg.RegimeNeutralScoreBoost
			if c.Score < boosted {
				return domain.TradeSignal{}, false, "regime-gated"
			}
		}
	}

	// Alpha signals: feed candidate bar and compute signal agreement
	alphaConfidenceBoost := 0.0
	if s.signalAggregator != nil {
		bar := signals.Bar{
			Open:      c.Open,
			High:      c.HighOfDay,
			Low:       c.Price, // approximate low from current price
			Close:     c.Price,
			Volume:    c.Volume,
			Timestamp: c.Timestamp,
		}
		sigs := s.signalAggregator.OnBar(c.Symbol, bar)
		if len(sigs) > 0 {
			s.recentSignals[c.Symbol] = sigs
		}
		// Check cached signals for directional agreement
		for _, sig := range s.recentSignals[c.Symbol] {
			if (sig.Direction == signals.DirectionLong && domain.IsLong(c.Direction)) ||
				(sig.Direction == signals.DirectionShort && domain.IsShort(c.Direction)) {
				alphaConfidenceBoost += sig.Strength * 0.1 // up to +0.1 per agreeing signal
			}
		}
	}

	// Compute position sizing
	riskPerShare := s.computeRiskPerShare(c)
	if riskPerShare <= 0 {
		return domain.TradeSignal{}, false, "invalid-risk"
	}

	// Phase 3 Change 3: Time-of-day risk multiplier (wider stops at open, tighter at close)
	if cfg.TimeOfDayEnabled {
		twCfg := defaultTimeWindowConfigs[tw]
		riskPerShare *= twCfg.RiskMultiplier
	}

	// Risk/Reward pre-check: reject trades where reward < MinRiskRewardRatio × risk
	if cfg.MinRiskRewardRatio > 0 && riskPerShare > 0 {
		var estimatedReward float64
		if domain.IsLong(c.Direction) {
			estimatedReward = c.HighOfDay - c.Price
			if estimatedReward <= 0 || c.SetupType == "hod_breakout" {
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

	// Scale position size by confidence (Phase 1 Change 7)
	if cfg.ConfidenceSizingEnabled {
		confidence := c.Score / 8.0
		if confidence > 1.0 {
			confidence = 1.0
		}
		floor := cfg.ConfidenceSizingFloor
		sizeMultiplier := floor + (1.0-floor)*confidence
		riskBudget *= sizeMultiplier
	}

	// Phase 2 Change 2: Graduated daily loss sizing factor
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

	// Update HWM tracking
	s.portfolio.UpdateEquityTracking()

	quantity := int64(math.Floor(riskBudget / riskPerShare))
	if quantity <= 0 {
		return domain.TradeSignal{}, false, "position-too-small"
	}

	// Phase 2 Change 6: Volatility-based position sizing cap
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

	if c.VolumeRate < 30000 {
		return domain.TradeSignal{}, false, "low-volume"
	}

	if c.Direction == "long" && c.ThreeMinuteReturnPct < cfg.MinThreeMinuteReturnPct {
		return domain.TradeSignal{}, false, "no-confirmation"
	}

	// ML Scoring gate: skip trade if ML score below threshold
	if cfg.MLScoringEnabled && s.scorer != nil && s.scorer.Enabled() {
		features := ml.ScorerFeatures{
			RelativeVolume:     c.RelativeVolume,
			GapPercent:         c.GapPercent,
			VolumeRate:         c.VolumeRate,
			OneMinuteReturn:    c.OneMinuteReturnPct,
			ThreeMinuteReturn:  c.ThreeMinuteReturnPct,
			BreakoutPct:        c.BreakoutPct,
			PriceVsVWAPPct:     c.PriceVsVWAPPct,
			RSI:                c.RSI,
			RSIMASlope:         c.RSIMASlope,
			ATR:                c.ATR,
			ConsolidationRange: c.ConsolidationRangePct,
			PullbackDepth:      c.PullbackDepthPct,
			RegimeProb:         c.RegimeConfidence,
			VolumeLeaderPct:    c.VolumeLeaderPct,
			MACDHistogram:      c.MACDHistogram,
			Direction:          c.Direction,
		}
		// Normalize MACD histogram to percentage of price for consistent scoring
		if c.Price > 0 {
			features.MACDHistogram = c.MACDHistogram / c.Price * 100
		}
		if c.EMASlow > 0 {
			features.EMAAlignment = (c.EMAFast - c.EMASlow) / c.EMASlow
		}
		localTime := now.In(markethours.Location())
		features.TimeOfDay = float64(localTime.Hour()*60+localTime.Minute()-9*60-30) / 390.0

		mlScore, err := s.scorer.Score(features)
		if err == nil {
			// Apply drift confidence reduction if detector available
			if cfg.ConceptDriftEnabled && s.driftDetector != nil {
				// Performance-based drift: reduces score when rolling Sharpe decays
				if s.driftDetector.CheckPerformanceDrift(cfg.SharpeDecayThreshold) {
					mlScore *= 0.5
				}
			}
			if mlScore < cfg.MLScoringThreshold {
				return domain.TradeSignal{}, false, "ml-score-gated"
			}
			// Scale position by ML score using MLScoreWeight to control blend
			w := cfg.MLScoreWeight
			mlSizeMultiplier := (1.0 - w) + 2.0*w*mlScore
			if mlSizeMultiplier > 1.5 {
				mlSizeMultiplier = 1.5
			}
			if mlSizeMultiplier < 0.5 {
				mlSizeMultiplier = 0.5
			}
			quantity = int64(math.Floor(float64(quantity) * mlSizeMultiplier))
			if quantity <= 0 {
				return domain.TradeSignal{}, false, "ml-score-position-too-small"
			}
		}
	}

	// Meta-label confidence gating: skip trade if confidence too low
	if cfg.MetaLabelEnabled {
		metaProb := c.Score / 8.0 // use rule-based score as proxy probability
		if metaProb > 1.0 {
			metaProb = 1.0
		}
		quantity = int64(ml.MetaLabelSizing(metaProb, int(quantity), cfg.MetaLabelConfidenceThreshold))
		if quantity <= 0 {
			return domain.TradeSignal{}, false, "meta-label-gated"
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

	// Position size floor: enforce minimum notional position
	if cfg.MinPositionNotionalPct > 0 && quantity > 0 && c.Price > 0 {
		minNotional := currentEquity * cfg.MinPositionNotionalPct
		minQty := int64(math.Floor(minNotional / c.Price))
		minQty = max(minQty, 0)
		if quantity < minQty {
			quantity = minQty
		}
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
		Confidence:       math.Min(c.Score/8.0+alphaConfidenceBoost, 1.0),
		MarketRegime:     c.MarketRegime,
		RegimeConfidence: c.RegimeConfidence,
		Playbook:         c.Playbook,
		Sector:           candidateSector,
		AvgDailyVolume:   float64(c.PrevDayVolume),
		Timestamp:        now,
	}

	s.lastEntryAt[c.Symbol] = now
	state.entrySignals++
	s.symbolStates[c.Symbol] = state

	return signal, true, ""
}

func (s *Strategy) evaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	cfg := s.getConfig()
	pos, exists := s.portfolio.GetPosition(tick.Symbol)
	if !exists {
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

	// Feed tick into signal aggregator for alpha signal computation
	if s.signalAggregator != nil {
		bar := signals.Bar{
			Open:      tick.BarOpen,
			High:      tick.BarHigh,
			Low:       tick.BarLow,
			Close:     tick.Price,
			Volume:    tick.Volume,
			Timestamp: tick.Timestamp,
		}
		sigs := s.signalAggregator.OnBar(tick.Symbol, bar)
		if len(sigs) > 0 {
			s.recentSignals[tick.Symbol] = sigs
		}
	}

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
		if pnl < 0 {
			dayKey := markethours.TradingDay(now)
			state := s.getSymbolState(tick.Symbol, dayKey)
			state.lossExits++
			state.lastLossAt = now
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

func (s *Strategy) checkExitConditions(pos domain.Position, tick domain.Tick) (string, bool) {
	cfg := s.getConfig()
	r := s.currentR(pos, tick.Price)
	exitCfg := s.getPlaybookExitConfig(pos.Playbook)

	// Safety: if stop price is zero (shouldn't happen but defensive), compute one
	if pos.StopPrice == 0 {
		fallbackRisk := pos.AvgPrice * cfg.EntryATRPercentFallback / 100.0
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
	// Phase 3 Change 3: Time-of-day profit target adjustment
	if cfg.TimeOfDayEnabled {
		tw := currentTimeWindow(tick.Timestamp)
		twCfg := defaultTimeWindowConfigs[tw]
		profitTargetR *= twCfg.ProfitTargetMultiplier
	}
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
	return c.Price * cfg.EntryATRPercentFallback / 100
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
