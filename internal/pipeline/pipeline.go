package pipeline

import (
	"context"
	"log"
	"sync"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/execution"
	"github.com/edwintcloud/momentum-trading-bot/internal/market"
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
}

// Pipeline is a reusable channel-based trading pipeline shared
// between live trading, backtests, and optimizer runs.
type Pipeline struct {
	cfg        Config
	barCh      chan domain.Bar
	closeAllCh chan domain.OrderRequest
	fillCh     chan domain.ExecutionReport
	wg         sync.WaitGroup
}

// New creates a pipeline but does not start it.
func New(cfg Config) *Pipeline {
	return &Pipeline{
		cfg:        cfg,
		barCh:      make(chan domain.Bar, 1024),
		closeAllCh: make(chan domain.OrderRequest, 64),
		fillCh:     make(chan domain.ExecutionReport, 64),
	}
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
	tickCh := make(chan domain.Tick, 1024)
	candidateCh := make(chan domain.Candidate, 256)
	signalCh := make(chan domain.TradeSignal, 64)
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
			select {
			case tickCh <- tick:
			default:
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

			select {
			case scannerTicks <- tick:
			default:
			}
			select {
			case strategyTicks <- tick:
			default:
			}
		}
	}()

	// Order channel coordination: both risk and close-all write to orderCh.
	// A coordinator closes orderCh only after both writers finish.
	var orderWriters sync.WaitGroup

	if p.hasDiagnostics() {
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
		execEngine := execution.NewEngine(p.cfg.Broker, p.cfg.Runtime, p.cfg.Recorder, p.cfg.EngineOptions...)
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

// startProductionStages uses component Start() methods (efficient, no diagnostic overhead).
func (p *Pipeline) startProductionStages(ctx context.Context,
	scannerTicks, strategyTicks <-chan domain.Tick,
	candidateCh chan domain.Candidate,
	signalCh chan domain.TradeSignal,
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
			default:
			}
		}
	}()

	// Strategy
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(signalCh)
		if err := p.cfg.Strategy.Start(ctx, strategyCandidates, strategyTicks, signalCh); err != nil {
			log.Printf("pipeline/strategy: %v", err)
		}
	}()

	// Risk engine
	p.startRiskWriter(ctx, signalCh, orderCh, nil, orderWriters)
}

// startDiagnosticStages uses inline evaluate loops with full diagnostic access.
func (p *Pipeline) startDiagnosticStages(ctx context.Context,
	scannerTicks, strategyTicks <-chan domain.Tick,
	candidateCh chan domain.Candidate,
	signalCh chan domain.TradeSignal,
	orderCh chan<- domain.OrderRequest,
	orderWriters *sync.WaitGroup,
) {
	// Scanner (inline: single-threaded, calls EvaluateTickDetailed)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(candidateCh)
		for tick := range scannerTicks {
			candidate, ok, reason := p.cfg.Scanner.EvaluateTickDetailed(tick)
			if p.cfg.OnScanResult != nil {
				p.cfg.OnScanResult(tick, candidate, ok, reason)
			}
			if ok {
				select {
				case candidateCh <- candidate:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Candidate tap
	strategyCandidates := make(chan domain.Candidate, 256)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(strategyCandidates)
		for c := range candidateCh {
			p.cfg.Runtime.AddCandidate(c)
			select {
			case strategyCandidates <- c:
			default:
			}
		}
	}()

	// Strategy (inline: calls EvaluateCandidateDecision + EvaluateExitDetailed)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(signalCh)
		sc := strategyCandidates
		st := strategyTicks
		for sc != nil || st != nil {
			select {
			case <-ctx.Done():
				return
			case candidate, ok := <-sc:
				if !ok {
					sc = nil
					continue
				}
				decision := p.cfg.Strategy.EvaluateCandidateDecision(candidate)
				if p.cfg.OnEntryDecision != nil {
					p.cfg.OnEntryDecision(candidate, decision)
				}
				if decision.Emit {
					select {
					case signalCh <- decision.Signal:
					case <-ctx.Done():
						return
					}
				}
			case tick, ok := <-st:
				if !ok {
					st = nil
					continue
				}
				signal, shouldExit, reason := p.cfg.Strategy.EvaluateExitDetailed(tick)
				if p.cfg.OnExitCheck != nil {
					p.cfg.OnExitCheck(tick, signal, shouldExit, reason)
				}
				if shouldExit {
					select {
					case signalCh <- signal:
					case <-ctx.Done():
						return
					}
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
	signalCh <-chan domain.TradeSignal,
	orderCh chan<- domain.OrderRequest,
	onDecision func(domain.TradeSignal, domain.OrderRequest, bool, string),
	orderWriters *sync.WaitGroup,
) {
	orderWriters.Add(1)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer orderWriters.Done()
		for signal := range signalCh {
			order, approved, reason := p.cfg.RiskEngine.Evaluate(signal)
			if onDecision != nil {
				onDecision(signal, order, approved, reason)
			}
			if approved {
				select {
				case orderCh <- order:
				case <-ctx.Done():
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
