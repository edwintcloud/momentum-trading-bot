package pipeline

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/execution"
	"github.com/edwintcloud/momentum-trading-bot/internal/market"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/ml"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/regime"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/strategy"
)

// Config holds all the components needed to wire a trading pipeline.
type Config struct {
	TradingCfg   config.TradingConfig
	Runtime      *runtime.State
	Portfolio    *portfolio.Manager
	Normalizer   *market.Normalizer
	Scanner      *scanner.Scanner
	Strategy     *strategy.Strategy
	RiskEngine   *risk.Engine
	VolEstimator *risk.VolatilityEstimator
	Broker       execution.BrokerClient
	Recorder     domain.EventRecorder // optional
	Scorer       ml.Scorer            // optional shadow-mode scorer

	// RegimeTracker is optional; if set the pipeline updates it on every tick.
	RegimeTracker *regime.Tracker

	// OnTick is called for every normalized tick before fan-out.
	// Use this to feed PaperBroker.UpdateBar or attach custom hooks.
	OnTick func(domain.Tick, domain.Bar)

	// FloatLookup returns the float for a symbol (0 = unknown).
	FloatLookup func(symbol string) int64

	// EngineOptions are passed to the execution.Engine constructor.
	EngineOptions []execution.EngineOption

	// TickFilter, if set, controls which ticks reach scanner/strategy.
	// Return true to process, false to skip. Skipped ticks still update
	// portfolio prices, vol estimator, and correlation tracker.
	TickFilter func(domain.Tick) bool

	// Diagnostic callbacks (optional). When any of these are set, the
	// pipeline uses inline evaluate loops instead of delegating to
	// component Start() methods. This gives full diagnostic access.
	OnScanResult    func(tick domain.Tick, candidate domain.Candidate, passed bool, reason string)
	OnEntryDecision func(candidate domain.Candidate, decision strategy.CandidateDecision)
	OnExitCheck     func(tick domain.Tick, signal domain.TradeSignal, shouldExit bool, reason string)
	OnRiskDecision  func(signal domain.TradeSignal, order domain.OrderRequest, approved bool, reason string)

	// OnTickFanOut is called after portfolio price updates in the fan-out stage.
	OnTickFanOut func(domain.Tick)

	// OnCandidateEvaluation is invoked whenever a candidate evaluation row is recorded.
	OnCandidateEvaluation func(domain.CandidateEvaluation)

	// CandidateEvaluationSource labels candidate evaluation rows emitted from this
	// pipeline instance, for example "live" or "backtest".
	CandidateEvaluationSource string

	// Deterministic forces blocking handoffs and ordered diagnostic processing.
	// Use this for backtests where reproducibility matters more than throughput.
	Deterministic bool
}

// Pipeline is a reusable channel-based trading pipeline shared
// between live trading, backtests, and optimizer runs.
type Pipeline struct {
	cfg             Config
	barCh           chan domain.Bar
	closeAllCh      chan domain.OrderRequest
	fillCh          chan domain.ExecutionReport
	wg              sync.WaitGroup
	mlMu            sync.Mutex
	mlDayKey        string
	mlDayScores     []float64
	mlBarTime       time.Time
	mlBarScores     []float64
	mlAdvisoryVetos int
	mlDrift         map[string]*ml.DriftDetector
	mlPerfDrift     *ml.DriftDetector
	mlDriftState    map[string]bool
}

type deterministicPendingOrder struct {
	orderID string
	order   domain.OrderRequest
}

type signalEnvelope struct {
	signal        domain.TradeSignal
	candidateEval *domain.CandidateEvaluation
}

// New creates a pipeline but does not start it.
func New(cfg Config) *Pipeline {
	p := &Pipeline{
		cfg:          cfg,
		barCh:        make(chan domain.Bar, 1024),
		closeAllCh:   make(chan domain.OrderRequest, 64),
		fillCh:       make(chan domain.ExecutionReport, 64),
		mlDrift:      make(map[string]*ml.DriftDetector),
		mlDriftState: make(map[string]bool),
	}
	p.initMLDrift()
	return p
}

// BarCh returns the channel callers use to feed bars into the pipeline.
func (p *Pipeline) BarCh() chan<- domain.Bar {
	return p.barCh
}

// CloseAllCh returns the channel for injecting close-all order requests.
func (p *Pipeline) CloseAllCh() chan<- domain.OrderRequest {
	return p.closeAllCh
}

// Close signals the pipeline that no more bars or close-all orders will
// arrive. Call Wait() afterwards to block until everything drains.
func (p *Pipeline) Close() {
	close(p.closeAllCh)
	close(p.barCh)
}

func (p *Pipeline) hasDiagnostics() bool {
	return p.cfg.OnScanResult != nil ||
		p.cfg.OnEntryDecision != nil ||
		p.cfg.OnExitCheck != nil ||
		p.cfg.OnRiskDecision != nil
}

// Start wires all pipeline stages and launches goroutines.
// It returns immediately; call Close() then Wait() to drain.
func (p *Pipeline) Start(ctx context.Context) {
	diagnostics := p.hasDiagnostics()
	if p.cfg.Deterministic {
		p.startDeterministic(ctx, diagnostics)
		return
	}
	tickCh := make(chan domain.Tick, 1024)
	candidateCh := make(chan domain.Candidate, 256)
	signalCh := make(chan signalEnvelope, 64)
	orderCh := make(chan domain.OrderRequest, 64)

	scannerTicks := make(chan domain.Tick, 1024)
	strategyTicks := make(chan domain.Tick, 1024)

	// Stage 1: Bar → Tick normalizer
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(tickCh)
		for bar := range p.barCh {
			tick := p.cfg.Normalizer.Normalize(bar)
			if p.cfg.FloatLookup != nil {
				tick.Float = p.cfg.FloatLookup(tick.Symbol)
			}
			if p.cfg.OnTick != nil {
				p.cfg.OnTick(tick, bar)
			}
			if !p.sendTick(ctx, tickCh, tick) {
				return
			}
		}
	}()

	// Stage 2: Tick fan-out
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(scannerTicks)
		defer close(strategyTicks)
		for tick := range tickCh {
			if p.cfg.RegimeTracker != nil && p.cfg.RegimeTracker.IsBenchmark(tick.Symbol) {
				p.cfg.RegimeTracker.UpdateTick(tick)
			}
			p.cfg.Portfolio.MarkPriceAt(tick.Symbol, tick.BarHigh, tick.Timestamp)
			p.cfg.Portfolio.MarkPriceAt(tick.Symbol, tick.BarLow, tick.Timestamp)
			p.cfg.Portfolio.MarkPriceAt(tick.Symbol, tick.Price, tick.Timestamp)
			p.cfg.VolEstimator.UpdatePrice(tick.Symbol, tick.Price)
			p.cfg.RiskEngine.CorrelationTracker.UpdatePrice(tick.Symbol, tick.Price)
			if p.cfg.OnTickFanOut != nil {
				p.cfg.OnTickFanOut(tick)
			}

			if p.cfg.TickFilter != nil && !p.cfg.TickFilter(tick) {
				continue
			}

			if diagnostics {
				if !p.sendTick(ctx, scannerTicks, tick) {
					return
				}
				continue
			}
			if !p.sendTick(ctx, scannerTicks, tick) {
				return
			}
			if !p.sendTick(ctx, strategyTicks, tick) {
				return
			}
		}
	}()

	// Order channel coordination: both risk and close-all write to orderCh.
	// A coordinator closes orderCh only after both writers finish.
	var orderWriters sync.WaitGroup

	if diagnostics {
		p.startDiagnosticStages(ctx, scannerTicks, strategyTicks, candidateCh, signalCh, orderCh, &orderWriters)
	} else {
		p.startProductionStages(ctx, scannerTicks, strategyTicks, candidateCh, signalCh, orderCh, &orderWriters)
	}

	// Close-all bridge
	orderWriters.Add(1)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer orderWriters.Done()
		for order := range p.closeAllCh {
			select {
			case orderCh <- order:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Order channel closer
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		orderWriters.Wait()
		close(orderCh)
	}()

	// Execution engine
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(p.fillCh)
		engineOpts := append([]execution.EngineOption{}, p.cfg.EngineOptions...)
		engineOpts = append(engineOpts, execution.WithOrderCallbacks(
			p.cfg.Portfolio.MarkPendingOrder,
			func(order domain.OrderRequest) {
				p.cfg.Portfolio.ClearPendingOrder(order.Symbol)
			},
		))
		execEngine := execution.NewEngine(p.cfg.Broker, p.cfg.Runtime, p.cfg.Recorder, engineOpts...)
		if err := execEngine.Start(ctx, orderCh, p.fillCh); err != nil {
			log.Printf("pipeline/execution: %v", err)
		}
	}()

	// Fill consumer
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for fill := range p.fillCh {
			p.cfg.Portfolio.ApplyExecution(fill)
			if p.cfg.Recorder != nil {
				p.cfg.Recorder.RecordExecution(fill)
			}
		}
	}()
}

func (p *Pipeline) sendTick(ctx context.Context, ch chan domain.Tick, tick domain.Tick) bool {
	select {
	case ch <- tick:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *Pipeline) initMLDrift() {
	if !p.cfg.TradingCfg.ConceptDriftEnabled {
		return
	}
	artifactScorer, ok := p.cfg.Scorer.(*ml.ArtifactScorer)
	if !ok || artifactScorer == nil {
		return
	}
	for _, side := range []string{"long", "short"} {
		dist := artifactScorer.TrainingProbabilityDistribution(side)
		if len(dist) == 0 {
			continue
		}
		p.mlDrift[side] = ml.NewDriftDetector(dist, 0.95)
	}
	p.mlPerfDrift = ml.NewDriftDetector(nil, 0.95)
	if p.cfg.Portfolio != nil {
		p.cfg.Portfolio.SetDriftDetector(p.mlPerfDrift)
	}
}

func (p *Pipeline) mlPSIThreshold() float64 {
	threshold := p.cfg.TradingCfg.PSIThreshold
	if threshold <= 0 {
		return 0.2
	}
	return threshold
}

func (p *Pipeline) mlSharpeThreshold() float64 {
	return p.cfg.TradingCfg.SharpeDecayThreshold
}

type mlDriftSnapshot struct {
	Enabled              bool
	Active               bool
	PSI                  float64
	PSIThreshold         float64
	ProbabilitySamples   int
	RollingSharpe        float64
	SharpeThreshold      float64
	ConfidenceMultiplier float64
	PerformanceFallback  bool
}

func (p *Pipeline) updateMLDrift(side string, probability float64) mlDriftSnapshot {
	side = strings.ToLower(strings.TrimSpace(side))
	snapshot := mlDriftSnapshot{}
	if !p.cfg.TradingCfg.ConceptDriftEnabled {
		return snapshot
	}

	p.mlMu.Lock()
	defer p.mlMu.Unlock()

	detector := p.mlDrift[side]
	if detector == nil {
		return snapshot
	}
	detector.RecordProbability(probability)
	snapshot = p.currentMLDriftSnapshotLocked(side)
	p.logMLDriftTransitionLocked(side, snapshot)
	return snapshot
}

func (p *Pipeline) currentMLDriftSnapshot(side string) mlDriftSnapshot {
	if !p.cfg.TradingCfg.ConceptDriftEnabled {
		return mlDriftSnapshot{}
	}
	p.mlMu.Lock()
	defer p.mlMu.Unlock()
	return p.currentMLDriftSnapshotLocked(strings.ToLower(strings.TrimSpace(side)))
}

func (p *Pipeline) currentMLDriftSnapshotLocked(side string) mlDriftSnapshot {
	snapshot := mlDriftSnapshot{
		Enabled:              p.cfg.TradingCfg.ConceptDriftEnabled,
		PSIThreshold:         p.mlPSIThreshold(),
		SharpeThreshold:      p.mlSharpeThreshold(),
		ConfidenceMultiplier: 1.0,
	}
	detector := p.mlDrift[side]
	if detector != nil {
		snapshot.ProbabilitySamples = detector.ProbabilitySampleCount()
		if snapshot.ProbabilitySamples >= 10 {
			liveDist := detector.LiveProbabilityDistribution()
			snapshot.PSI, _ = detector.CheckPSI(liveDist, snapshot.PSIThreshold)
			if snapshot.PSI > snapshot.PSIThreshold {
				snapshot.Active = true
				snapshot.ConfidenceMultiplier = ml.ConfidenceMultiplier(snapshot.PSI, snapshot.PSIThreshold)
			}
		}
	}
	if p.mlPerfDrift != nil {
		snapshot.RollingSharpe = p.mlPerfDrift.RollingSharpe()
		if p.mlPerfDrift.CheckPerformanceDrift(snapshot.SharpeThreshold) {
			snapshot.Active = true
			snapshot.PerformanceFallback = true
			snapshot.ConfidenceMultiplier = 0
		}
	}
	return snapshot
}

func (p *Pipeline) logMLDriftTransitionLocked(side string, snapshot mlDriftSnapshot) {
	if p.cfg.Runtime == nil {
		return
	}
	active := snapshot.Active
	if prev, ok := p.mlDriftState[side]; ok && prev == active {
		return
	}
	p.mlDriftState[side] = active
	if active {
		p.cfg.Runtime.RecordLog(
			"warn",
			"ml-drift",
			fmt.Sprintf(
				"side=%s psi=%.4f psi_threshold=%.4f sharpe=%.4f sharpe_threshold=%.4f fallback=%t",
				side,
				snapshot.PSI,
				snapshot.PSIThreshold,
				snapshot.RollingSharpe,
				snapshot.SharpeThreshold,
				snapshot.PerformanceFallback,
			),
		)
		return
	}
	p.cfg.Runtime.RecordLog("info", "ml-drift", fmt.Sprintf("side=%s cleared", side))
}

func (p *Pipeline) candidateEvaluationSource() string {
	if p.cfg.CandidateEvaluationSource != "" {
		return p.cfg.CandidateEvaluationSource
	}
	if p.cfg.Deterministic {
		return "backtest"
	}
	return "live"
}

func (p *Pipeline) buildCandidateEvaluation(candidate domain.Candidate, decision strategy.CandidateDecision) domain.CandidateEvaluation {
	candidateEval := domain.CandidateEvaluation{
		RecordedAt:             candidate.Timestamp,
		Source:                 p.candidateEvaluationSource(),
		Candidate:              candidate,
		StrategyEvaluated:      true,
		StrategyEmitted:        decision.Emit,
		StrategyReason:         decision.Reason,
		PredictedReturnPct:     decision.PredictedReturnPct,
		RequiredReturnPct:      decision.RequiredReturnPct,
		AllowedDistanceHighPct: decision.AllowedDistanceHighPct,
		StrongSqueeze:          decision.StrongSqueeze,
		Signal:                 decision.Signal,
	}
	p.applyMLShadow(candidate, &candidateEval)
	return candidateEval
}

func (p *Pipeline) applyMLShadow(candidate domain.Candidate, candidateEval *domain.CandidateEvaluation) {
	if candidateEval == nil || p.cfg.Scorer == nil || !p.cfg.Scorer.Enabled() || !p.cfg.TradingCfg.MLScoringEnabled {
		return
	}

	features := ml.FeaturesFromCandidate(candidate)
	probability, err := p.cfg.Scorer.Score(features)
	if err != nil {
		return
	}
	candidateEval.MLScored = true
	candidateEval.MLProbability = probability
	candidateEval.MLThreshold = p.cfg.TradingCfg.MLScoringThreshold
	candidateEval.MLModelSide = features.Direction
	if artifactScorer, ok := p.cfg.Scorer.(*ml.ArtifactScorer); ok {
		candidateEval.MLModelPath = artifactScorer.ModelPath()
	}
	candidateEval.MLShadowDecision, candidateEval.MLShadowSizeMultiplier = p.shadowDecision(probability)
	candidateEval.MLShadowVeto = candidateEval.MLShadowDecision == "veto"
	candidateEval.MLShadowUpsize = candidateEval.MLShadowDecision == "upsize"
	candidateEval.MLDayRankSoFar, candidateEval.MLBarRankSoFar = p.recordMLScore(candidate.Timestamp, probability)
	p.applyMLDriftSnapshot(candidateEval, p.updateMLDrift(features.Direction, probability))
}

func (p *Pipeline) applyMLAdvisory(signal domain.TradeSignal, candidateEval *domain.CandidateEvaluation) (domain.TradeSignal, bool) {
	if candidateEval == nil || !candidateEval.MLScored || !p.cfg.TradingCfg.MLAdvisoryEnabled || !domain.IsOpeningIntent(signal.Intent) {
		return signal, true
	}

	cfg := p.cfg.TradingCfg
	candidateEval.MLAdvisoryEnabled = true
	candidateEval.MLAdvisoryDecision = "keep"
	candidateEval.MLAdvisorySizeMultiplier = 1.0
	candidateEval.MLAdvisoryOriginalQuantity = signal.Quantity
	candidateEval.MLAdvisoryAdjustedQuantity = signal.Quantity
	driftSnapshot := p.currentMLDriftSnapshot(candidateEval.MLModelSide)
	p.applyMLDriftSnapshot(candidateEval, driftSnapshot)

	if signal.Quantity <= 0 {
		candidateEval.Signal = signal
		return signal, true
	}

	if driftSnapshot.PerformanceFallback {
		candidateEval.MLAdvisoryDecision = "drift-fallback"
		candidateEval.Signal = signal
		return signal, true
	}

	minProb := cfg.MLAdvisoryMinProb
	if minProb <= 0 {
		minProb = cfg.MLScoringThreshold
	}
	if minProb <= 0 {
		minProb = 0.5
	}

	if cfg.MLAdvisoryVetoEnabled && candidateEval.MLProbability < minProb && p.consumeMLAdvisoryVeto(signal.Timestamp) {
		candidateEval.MLAdvisoryApplied = true
		candidateEval.MLAdvisoryDecision = "veto"
		candidateEval.MLAdvisoryVeto = true
		candidateEval.MLAdvisorySizeMultiplier = 0
		candidateEval.MLAdvisoryAdjustedQuantity = 0
		signal.Quantity = 0
		candidateEval.Signal = signal
		return signal, false
	}

	upsizeThreshold := cfg.MLAdvisoryUpsizeThreshold
	if upsizeThreshold <= 0 {
		upsizeThreshold = math.Max(minProb+0.10, 0.75)
	}
	if cfg.MLAdvisoryUpsizeEnabled && candidateEval.MLProbability >= upsizeThreshold {
		upsizeMultiplier := p.mlAdvisoryUpsizeMultiplier(driftSnapshot)
		adjusted, applied := applyQuantityMultiplier(signal, upsizeMultiplier, 1.10)
		if applied {
			candidateEval.MLAdvisoryApplied = true
			candidateEval.MLAdvisoryDecision = "upsize"
			candidateEval.MLAdvisorySizeMultiplier = float64(adjusted.Quantity) / float64(signal.Quantity)
			candidateEval.MLAdvisoryAdjustedQuantity = adjusted.Quantity
			candidateEval.Signal = adjusted
			return adjusted, true
		}
	}

	downsizeThreshold := p.mlAdvisoryDownsizeThreshold(signal, minProb)
	if cfg.MLAdvisoryDownsizeEnabled && candidateEval.MLProbability < downsizeThreshold && !p.protectMLAdvisoryDownsize(candidateEval) {
		downsizeMultiplier := p.mlAdvisoryDownsizeMultiplier(driftSnapshot)
		adjusted, applied := applyQuantityMultiplier(signal, downsizeMultiplier, 0.75)
		if applied {
			candidateEval.MLAdvisoryApplied = true
			candidateEval.MLAdvisoryDecision = "downsize"
			candidateEval.MLAdvisorySizeMultiplier = float64(adjusted.Quantity) / float64(signal.Quantity)
			candidateEval.MLAdvisoryAdjustedQuantity = adjusted.Quantity
			candidateEval.Signal = adjusted
			return adjusted, true
		}
	}

	candidateEval.Signal = signal
	return signal, true
}

func (p *Pipeline) protectMLAdvisoryDownsize(candidateEval *domain.CandidateEvaluation) bool {
	if candidateEval == nil {
		return false
	}
	cfg := p.cfg.TradingCfg
	if p.protectEliteShortDownsize(candidateEval) {
		return true
	}
	if !strings.EqualFold(candidateEval.Signal.Side, "buy") {
		return false
	}
	if cfg.MLAdvisoryProtectTopDayRank > 0 && candidateEval.MLDayRankSoFar > 0 && candidateEval.MLDayRankSoFar <= cfg.MLAdvisoryProtectTopDayRank {
		return true
	}
	if cfg.MLAdvisoryProtectTopBarRank > 0 && candidateEval.MLBarRankSoFar > 0 && candidateEval.MLBarRankSoFar <= cfg.MLAdvisoryProtectTopBarRank {
		return true
	}
	return false
}

func (p *Pipeline) applyMLDriftSnapshot(candidateEval *domain.CandidateEvaluation, snapshot mlDriftSnapshot) {
	if candidateEval == nil {
		return
	}
	candidateEval.MLDriftEnabled = snapshot.Enabled
	candidateEval.MLDriftActive = snapshot.Active
	candidateEval.MLDriftPSI = snapshot.PSI
	candidateEval.MLDriftPSIThreshold = snapshot.PSIThreshold
	candidateEval.MLDriftProbabilitySamples = snapshot.ProbabilitySamples
	candidateEval.MLDriftRollingSharpe = snapshot.RollingSharpe
	candidateEval.MLDriftSharpeThreshold = snapshot.SharpeThreshold
	candidateEval.MLDriftConfidenceMultiplier = snapshot.ConfidenceMultiplier
	candidateEval.MLDriftPerformanceFallback = snapshot.PerformanceFallback
}

func (p *Pipeline) mlAdvisoryUpsizeMultiplier(snapshot mlDriftSnapshot) float64 {
	return blendTowardNeutral(p.cfg.TradingCfg.MLAdvisoryUpsizeMultiplier, 1.10, snapshot.ConfidenceMultiplier)
}

func (p *Pipeline) mlAdvisoryDownsizeMultiplier(snapshot mlDriftSnapshot) float64 {
	return blendTowardNeutral(p.cfg.TradingCfg.MLAdvisoryDownsizeMultiplier, 0.75, snapshot.ConfidenceMultiplier)
}

func (p *Pipeline) mlAdvisoryDownsizeThreshold(signal domain.TradeSignal, minProb float64) float64 {
	cfg := p.cfg.TradingCfg
	defaultThreshold := cfg.MLAdvisoryDownsizeThreshold
	if defaultThreshold <= 0 {
		defaultThreshold = math.Max(minProb, 0.60)
	}
	if strings.EqualFold(signal.Side, "sell") {
		if cfg.MLAdvisoryShortDownsizeThreshold > 0 {
			return cfg.MLAdvisoryShortDownsizeThreshold
		}
		return defaultThreshold
	}
	if cfg.MLAdvisoryLongDownsizeThreshold > 0 {
		return cfg.MLAdvisoryLongDownsizeThreshold
	}
	return defaultThreshold
}

func (p *Pipeline) protectEliteShortDownsize(candidateEval *domain.CandidateEvaluation) bool {
	if candidateEval == nil || !strings.EqualFold(candidateEval.Signal.Side, "sell") {
		return false
	}
	minProb := p.cfg.TradingCfg.MLAdvisoryProtectEliteShortMinProb
	if minProb <= 0 {
		minProb = 0.20
	}
	c := candidateEval.Candidate
	if candidateEval.MLProbability < minProb {
		return false
	}
	if c.LeaderRank <= 0 || c.LeaderRank > 2 {
		return false
	}
	if c.VolumeLeaderPct < 25 {
		return false
	}
	if c.Score < 6.0 {
		return false
	}
	return true
}

func (p *Pipeline) shadowDecision(probability float64) (string, float64) {
	threshold := p.cfg.TradingCfg.MLScoringThreshold
	if threshold <= 0 {
		threshold = 0.5
	}
	switch {
	case probability < threshold:
		return "veto", 0
	case probability >= threshold+0.10:
		return "upsize", 1.25
	case probability >= threshold:
		return "keep", 1.0
	default:
		return "neutral", 1.0
	}
}

func (p *Pipeline) recordMLScore(ts time.Time, probability float64) (int, int) {
	p.mlMu.Lock()
	defer p.mlMu.Unlock()

	p.resetMLDayStateLocked(ts)
	if p.mlBarTime.IsZero() || !p.mlBarTime.Equal(ts) {
		p.mlBarTime = ts
		p.mlBarScores = p.mlBarScores[:0]
	}

	p.mlDayScores = append(p.mlDayScores, probability)
	p.mlBarScores = append(p.mlBarScores, probability)
	return scoreRank(p.mlDayScores, probability), scoreRank(p.mlBarScores, probability)
}

func (p *Pipeline) resetMLDayStateLocked(ts time.Time) {
	dayKey := markethours.TradingDay(ts)
	if p.mlDayKey == dayKey {
		return
	}
	p.mlDayKey = dayKey
	p.mlDayScores = p.mlDayScores[:0]
	p.mlBarTime = time.Time{}
	p.mlBarScores = p.mlBarScores[:0]
	p.mlAdvisoryVetos = 0
}

func (p *Pipeline) consumeMLAdvisoryVeto(ts time.Time) bool {
	p.mlMu.Lock()
	defer p.mlMu.Unlock()

	p.resetMLDayStateLocked(ts)
	maxVetos := p.cfg.TradingCfg.MLAdvisoryMaxVetosPerDay
	if maxVetos > 0 && p.mlAdvisoryVetos >= maxVetos {
		return false
	}
	p.mlAdvisoryVetos++
	return true
}

func applyQuantityMultiplier(signal domain.TradeSignal, multiplier float64, fallback float64) (domain.TradeSignal, bool) {
	if signal.Quantity <= 0 {
		return signal, false
	}
	if multiplier <= 0 {
		multiplier = fallback
	}
	adjustedQty := int64(math.Round(float64(signal.Quantity) * multiplier))
	if adjustedQty < 1 {
		adjustedQty = 1
	}
	if adjustedQty == signal.Quantity {
		return signal, false
	}
	signal.Quantity = adjustedQty
	return signal, true
}

func blendTowardNeutral(multiplier float64, fallback float64, confidence float64) float64 {
	if multiplier <= 0 {
		multiplier = fallback
	}
	if confidence >= 1 {
		return multiplier
	}
	if confidence <= 0 {
		return 1.0
	}
	return 1.0 + (multiplier-1.0)*confidence
}

func scoreRank(scores []float64, probability float64) int {
	rank := 1
	for _, score := range scores {
		if score > probability {
			rank++
		}
	}
	return rank
}

func (p *Pipeline) recordCandidateEvaluation(candidateEval domain.CandidateEvaluation) {
	if p.cfg.OnCandidateEvaluation != nil {
		p.cfg.OnCandidateEvaluation(candidateEval)
	}
	if p.cfg.Recorder == nil {
		return
	}
	p.cfg.Recorder.RecordCandidateEvaluation(candidateEval)
}

func (p *Pipeline) recordCandidateEvaluationPtr(candidateEval *domain.CandidateEvaluation) {
	if candidateEval == nil {
		return
	}
	p.recordCandidateEvaluation(*candidateEval)
}

func (p *Pipeline) startDeterministic(ctx context.Context, diagnostics bool) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		pendingEntries := make([]deterministicPendingOrder, 0, 16)

		barCh := p.barCh
		closeAllCh := p.closeAllCh

		for barCh != nil || closeAllCh != nil {
			select {
			case <-ctx.Done():
				return
			case order, ok := <-closeAllCh:
				if !ok {
					closeAllCh = nil
					continue
				}
				if !p.processDeterministicOrder(ctx, order, time.Time{}, &pendingEntries) {
					return
				}
			case bar, ok := <-barCh:
				if !ok {
					barCh = nil
					continue
				}
				if !p.processDeterministicBar(ctx, bar, diagnostics, &pendingEntries) {
					return
				}
			}
		}
	}()
}

func (p *Pipeline) processDeterministicBar(
	ctx context.Context,
	bar domain.Bar,
	diagnostics bool,
	pendingEntries *[]deterministicPendingOrder,
) bool {
	tick := p.cfg.Normalizer.Normalize(bar)
	if p.cfg.FloatLookup != nil {
		tick.Float = p.cfg.FloatLookup(tick.Symbol)
	}
	if p.cfg.OnTick != nil {
		p.cfg.OnTick(tick, bar)
	}
	if !p.resolveDeterministicEntries(ctx, tick.Timestamp, pendingEntries) {
		return false
	}

	if p.cfg.RegimeTracker != nil && p.cfg.RegimeTracker.IsBenchmark(tick.Symbol) {
		p.cfg.RegimeTracker.UpdateTick(tick)
	}
	p.cfg.Portfolio.MarkPriceAt(tick.Symbol, tick.BarHigh, tick.Timestamp)
	p.cfg.Portfolio.MarkPriceAt(tick.Symbol, tick.BarLow, tick.Timestamp)
	p.cfg.Portfolio.MarkPriceAt(tick.Symbol, tick.Price, tick.Timestamp)
	p.cfg.VolEstimator.UpdatePrice(tick.Symbol, tick.Price)
	p.cfg.RiskEngine.CorrelationTracker.UpdatePrice(tick.Symbol, tick.Price)
	if p.cfg.OnTickFanOut != nil {
		p.cfg.OnTickFanOut(tick)
	}

	if p.cfg.TickFilter != nil && !p.cfg.TickFilter(tick) {
		return true
	}

	if diagnostics {
		candidate, ok, reason := p.cfg.Scanner.EvaluateTickDetailed(tick)
		if p.cfg.OnScanResult != nil {
			p.cfg.OnScanResult(tick, candidate, ok, reason)
		}
		if ok {
			p.cfg.Runtime.AddCandidate(candidate)
			decision := p.cfg.Strategy.EvaluateCandidateDecision(candidate)
			candidateEval := p.buildCandidateEvaluation(candidate, decision)
			if p.cfg.OnEntryDecision != nil {
				p.cfg.OnEntryDecision(candidate, decision)
			}
			if decision.Emit {
				if !p.processDeterministicSignal(ctx, decision.Signal, tick.Timestamp, pendingEntries, &candidateEval) {
					return false
				}
			} else {
				p.recordCandidateEvaluation(candidateEval)
			}
		}

		signal, shouldExit, exitReason := p.cfg.Strategy.EvaluateExitDetailed(tick)
		if p.cfg.OnExitCheck != nil {
			p.cfg.OnExitCheck(tick, signal, shouldExit, exitReason)
		}
		if shouldExit && !p.processDeterministicSignal(ctx, signal, tick.Timestamp, pendingEntries, nil) {
			return false
		}
		return true
	}

	candidate, ok := p.cfg.Scanner.Evaluate(tick)
	if ok {
		p.cfg.Runtime.AddCandidate(candidate)
		decision := p.cfg.Strategy.EvaluateCandidateDecision(candidate)
		candidateEval := p.buildCandidateEvaluation(candidate, decision)
		if decision.Emit {
			if !p.processDeterministicSignal(ctx, decision.Signal, tick.Timestamp, pendingEntries, &candidateEval) {
				return false
			}
		} else {
			p.recordCandidateEvaluation(candidateEval)
		}
	}

	signal, shouldExit := p.cfg.Strategy.EvaluateExit(tick)
	if shouldExit && !p.processDeterministicSignal(ctx, signal, tick.Timestamp, pendingEntries, nil) {
		return false
	}

	return true
}

func (p *Pipeline) processDeterministicSignal(
	ctx context.Context,
	signal domain.TradeSignal,
	fillTime time.Time,
	pendingEntries *[]deterministicPendingOrder,
	candidateEval *domain.CandidateEvaluation,
) bool {
	adjustedSignal, allowed := p.applyMLAdvisory(signal, candidateEval)
	if candidateEval != nil && !allowed {
		p.recordCandidateEvaluationPtr(candidateEval)
		return true
	}
	signal = adjustedSignal
	order, approved, reason := p.cfg.RiskEngine.Evaluate(signal)
	if p.cfg.OnRiskDecision != nil {
		p.cfg.OnRiskDecision(signal, order, approved, reason)
	}
	if candidateEval != nil {
		candidateEval.RiskEvaluated = true
		candidateEval.RiskApproved = approved
		candidateEval.RiskReason = reason
		candidateEval.Order = order
		p.recordCandidateEvaluationPtr(candidateEval)
	}
	if !approved {
		return true
	}
	return p.processDeterministicOrder(ctx, order, fillTime, pendingEntries)
}

func (p *Pipeline) processDeterministicOrder(
	ctx context.Context,
	order domain.OrderRequest,
	fillTime time.Time,
	pendingEntries *[]deterministicPendingOrder,
) bool {
	p.cfg.Portfolio.MarkPendingOrder(order)
	orderID, err := p.cfg.Broker.SubmitOrder(ctx, order)
	if err != nil {
		p.cfg.Portfolio.ClearPendingOrder(order.Symbol)
		log.Printf("pipeline/execution: submit failed for %s %s: %v", order.Symbol, order.Side, err)
		return true
	}

	if domain.IsOpeningIntent(order.Intent) {
		*pendingEntries = append(*pendingEntries, deterministicPendingOrder{
			orderID: orderID,
			order:   order,
		})
		return true
	}

	fill, filled := p.pollDeterministicOrder(ctx, order, orderID, fillTime)
	if !filled {
		p.cfg.Portfolio.ClearPendingOrder(order.Symbol)
		return true
	}
	p.cfg.Portfolio.ApplyExecution(fill)
	if p.cfg.Recorder != nil {
		p.cfg.Recorder.RecordExecution(fill)
	}
	return true
}

func (p *Pipeline) resolveDeterministicEntries(
	ctx context.Context,
	fillTime time.Time,
	pendingEntries *[]deterministicPendingOrder,
) bool {
	if len(*pendingEntries) == 0 {
		return true
	}

	active := (*pendingEntries)[:0]
	for _, pending := range *pendingEntries {
		fill, filled := p.pollDeterministicOrder(ctx, pending.order, pending.orderID, fillTime)
		if filled {
			p.cfg.Portfolio.ApplyExecution(fill)
			if p.cfg.Recorder != nil {
				p.cfg.Recorder.RecordExecution(fill)
			}
			continue
		}

		status, _, _, err := p.cfg.Broker.PollOrderStatus(ctx, pending.orderID)
		if err != nil || status == "new" {
			active = append(active, pending)
			continue
		}

		p.cfg.Portfolio.ClearPendingOrder(pending.order.Symbol)
	}
	*pendingEntries = active
	return true
}

func (p *Pipeline) pollDeterministicOrder(
	ctx context.Context,
	order domain.OrderRequest,
	orderID string,
	fillTime time.Time,
) (domain.ExecutionReport, bool) {
	status, fillPrice, filledQty, err := p.cfg.Broker.PollOrderStatus(ctx, orderID)
	if err != nil {
		log.Printf("pipeline/execution: poll failed for %s %s orderID=%s: %v", order.Symbol, order.Side, orderID, err)
		return domain.ExecutionReport{}, false
	}
	if status != "filled" {
		return domain.ExecutionReport{}, false
	}
	return domain.ExecutionReport{
		Symbol:           order.Symbol,
		Side:             order.Side,
		Intent:           order.Intent,
		PositionSide:     order.PositionSide,
		Price:            fillPrice,
		Quantity:         filledQtyOrRequested(filledQty, order.Quantity),
		StopPrice:        order.StopPrice,
		RiskPerShare:     order.RiskPerShare,
		EntryATR:         order.EntryATR,
		SetupType:        order.SetupType,
		Reason:           order.Reason,
		MarketRegime:     order.MarketRegime,
		RegimeConfidence: order.RegimeConfidence,
		Playbook:         order.Playbook,
		Sector:           order.Sector,
		LeaderRank:       order.LeaderRank,
		VolumeLeaderPct:  order.VolumeLeaderPct,
		StockSelectScore: order.StockSelectScore,
		PriceVsVWAPPct:   order.PriceVsVWAPPct,
		DistanceHighPct:  order.DistanceHighPct,
		BrokerOrderID:    orderID,
		BrokerStatus:     status,
		FilledAt:         fillTime,
	}, true
}

func filledQtyOrRequested(filledQty, requestedQty int64) int64 {
	if filledQty <= 0 {
		return requestedQty
	}
	if requestedQty > 0 && filledQty > requestedQty {
		return requestedQty
	}
	return filledQty
}

// startProductionStages uses component Start() methods (efficient, no diagnostic overhead).
func (p *Pipeline) startProductionStages(ctx context.Context,
	scannerTicks, strategyTicks <-chan domain.Tick,
	candidateCh chan domain.Candidate,
	signalCh chan signalEnvelope,
	orderCh chan<- domain.OrderRequest,
	orderWriters *sync.WaitGroup,
) {
	// Scanner
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(candidateCh)
		if err := p.cfg.Scanner.Start(ctx, scannerTicks, candidateCh); err != nil {
			log.Printf("pipeline/scanner: %v", err)
		}
	}()

	// Candidate tap: record to runtime state, forward to strategy
	strategyCandidates := make(chan domain.Candidate, 256)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(strategyCandidates)
		for c := range candidateCh {
			p.cfg.Runtime.AddCandidate(c)
			select {
			case strategyCandidates <- c:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Strategy
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(signalCh)
		for {
			select {
			case <-ctx.Done():
				return
			case candidate, ok := <-strategyCandidates:
				if !ok {
					strategyCandidates = nil
					if strategyTicks == nil {
						return
					}
					continue
				}
				decision := p.cfg.Strategy.EvaluateCandidateDecision(candidate)
				candidateEval := p.buildCandidateEvaluation(candidate, decision)
				if !decision.Emit {
					p.recordCandidateEvaluation(candidateEval)
					continue
				}
				select {
				case signalCh <- signalEnvelope{signal: decision.Signal, candidateEval: &candidateEval}:
				case <-ctx.Done():
					return
				}
			case tick, ok := <-strategyTicks:
				if !ok {
					strategyTicks = nil
					if strategyCandidates == nil {
						return
					}
					continue
				}
				signal, shouldEmit := p.cfg.Strategy.EvaluateExit(tick)
				if !shouldEmit {
					continue
				}
				select {
				case signalCh <- signalEnvelope{signal: signal}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Risk engine
	p.startRiskWriter(ctx, signalCh, orderCh, nil, orderWriters)
}

// startDiagnosticStages uses inline evaluate loops with full diagnostic access.
func (p *Pipeline) startDiagnosticStages(ctx context.Context,
	scannerTicks, strategyTicks <-chan domain.Tick,
	candidateCh chan domain.Candidate,
	signalCh chan signalEnvelope,
	orderCh chan<- domain.OrderRequest,
	orderWriters *sync.WaitGroup,
) {
	_ = strategyTicks
	_ = candidateCh

	// Diagnostic path: process each tick fully in order so backtests are reproducible.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(signalCh)
		for tick := range scannerTicks {
			candidate, ok, reason := p.cfg.Scanner.EvaluateTickDetailed(tick)
			if p.cfg.OnScanResult != nil {
				p.cfg.OnScanResult(tick, candidate, ok, reason)
			}
			if ok {
				p.cfg.Runtime.AddCandidate(candidate)
				decision := p.cfg.Strategy.EvaluateCandidateDecision(candidate)
				candidateEval := p.buildCandidateEvaluation(candidate, decision)
				if p.cfg.OnEntryDecision != nil {
					p.cfg.OnEntryDecision(candidate, decision)
				}
				if decision.Emit {
					select {
					case signalCh <- signalEnvelope{signal: decision.Signal, candidateEval: &candidateEval}:
					case <-ctx.Done():
						return
					}
				} else {
					p.recordCandidateEvaluation(candidateEval)
				}
			}

			signal, shouldExit, exitReason := p.cfg.Strategy.EvaluateExitDetailed(tick)
			if p.cfg.OnExitCheck != nil {
				p.cfg.OnExitCheck(tick, signal, shouldExit, exitReason)
			}
			if shouldExit {
				select {
				case signalCh <- signalEnvelope{signal: signal}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Risk engine (inline: calls Evaluate with callback)
	p.startRiskWriter(ctx, signalCh, orderCh, p.cfg.OnRiskDecision, orderWriters)
}

// startRiskWriter starts the risk evaluation goroutine and registers it as
// an orderCh writer with the parent's orderWriters WaitGroup.
func (p *Pipeline) startRiskWriter(ctx context.Context,
	signalCh <-chan signalEnvelope,
	orderCh chan<- domain.OrderRequest,
	onDecision func(domain.TradeSignal, domain.OrderRequest, bool, string),
	orderWriters *sync.WaitGroup,
) {
	orderWriters.Add(1)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer orderWriters.Done()
		for env := range signalCh {
			adjustedSignal, allowed := p.applyMLAdvisory(env.signal, env.candidateEval)
			if env.candidateEval != nil && !allowed {
				p.recordCandidateEvaluationPtr(env.candidateEval)
				continue
			}
			env.signal = adjustedSignal
			order, approved, reason := p.cfg.RiskEngine.Evaluate(env.signal)
			if onDecision != nil {
				onDecision(env.signal, order, approved, reason)
			}
			if env.candidateEval != nil {
				env.candidateEval.RiskEvaluated = true
				env.candidateEval.RiskApproved = approved
				env.candidateEval.RiskReason = reason
				env.candidateEval.Order = order
				p.recordCandidateEvaluationPtr(env.candidateEval)
			}
			if approved {
				p.cfg.Portfolio.MarkPendingOrder(order)
				select {
				case orderCh <- order:
				case <-ctx.Done():
					p.cfg.Portfolio.ClearPendingOrder(order.Symbol)
					return
				}
			}
		}
	}()
}

// Wait blocks until all pipeline goroutines complete.
// Call this after Close() to drain the pipeline.
func (p *Pipeline) Wait() {
	p.wg.Wait()
}
