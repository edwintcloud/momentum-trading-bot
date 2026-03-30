package backtest

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/execution"
	"github.com/edwintcloud/momentum-trading-bot/internal/market"
	"github.com/edwintcloud/momentum-trading-bot/internal/ml"
	"github.com/edwintcloud/momentum-trading-bot/internal/pipeline"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/regime"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/signals"
	"github.com/edwintcloud/momentum-trading-bot/internal/strategy"
)

// RunConfig controls a historical simulation.
type RunConfig struct {
	DataPath       string
	Bars           []InputBar
	Iterator       InputBarIterator
	IteratorFn     IteratorFactory // factory for creating bounded iterators (streaming mode)
	Start          time.Time
	End            time.Time
	Recorder       domain.EventRecorder
	DebugSymbols   []string           // symbols to trace per-bar through scanner/strategy
	FloatStore     *alpaca.FloatStore // optional float data for tick enrichment
	BlockedSymbols map[string]string  // optional hard blocklist for ETF/derivative instruments
	EasyToBorrow   map[string]bool    // optional symbol set allowed for opening short positions
}

// InputBar is an external bar shape accepted by the backtest engine.
type InputBar struct {
	Timestamp   time.Time
	Symbol      string
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      uint64
	PrevClose   float64
	Catalyst    string
	CatalystURL string
}

// Result summarizes a completed backtest.
type Result struct {
	StartingCapital     float64
	RealizedPnL         float64
	UnrealizedPnL       float64
	EndingEquity        float64
	NetPnL              float64
	MaxDrawdownPct      float64
	ProfitFactor        float64
	AvgWinPnL           float64
	AvgLossPnL          float64
	AvgWinR             float64
	AvgLossR            float64
	AvgMFER             float64
	AvgMAER             float64
	TrailingStopExitPct float64
	AvgTimeToStopMin    float64
	EntriesExecuted     int
	Trades              int
	Wins                int
	Losses              int
	WinRate             float64
	OpenPositionsAtEnd  int
	OpenSymbols         []string
	Diagnostics         Diagnostics
	ClosedTrades        []domain.ClosedTrade

	// Phase 4: Statistical rigor
	MonteCarlo              *MonteCarloResult  `json:"monteCarlo,omitempty"`
	WalkForward             *WalkForwardResult `json:"walkForward,omitempty"`
	CPCV                    *CPCVResult        `json:"cpcv,omitempty"`
	TotalCommissions        float64            `json:"totalCommissions,omitempty"`
	TotalSECFees            float64            `json:"totalSECFees,omitempty"`
	TotalTAFFees            float64            `json:"totalTAFFees,omitempty"`
	TotalSpreadCosts        float64            `json:"totalSpreadCosts,omitempty"`
	TotalTransactionCosts   float64            `json:"totalTransactionCosts,omitempty"`
	ImplementationShortfall float64            `json:"implementationShortfall,omitempty"`
}

type TradeBreakdown struct {
	Trades int
	Wins   int
	Losses int
	NetPnL float64
}

// Diagnostics summarizes how bars moved through the backtest decision funnel.
type Diagnostics struct {
	BarsLoaded          int
	BarsInWindow        int
	EntryCandidates     int
	EntrySignals        int
	EntryRiskApproved   int
	FillExpiries        int
	ExitChecks          int
	ExitSignals         int
	ExitRiskApproved    int
	ScannerRejects      map[string]int
	EntryRejects        map[string]int
	EntryRiskRejects    map[string]int
	ExitRejects         map[string]int
	ExitRiskRejects     map[string]int
	ByRegime            map[string]TradeBreakdown
	BySetup             map[string]TradeBreakdown
	BySide              map[string]TradeBreakdown
	EntrySignalSamples  []EntrySample
	EntryRejectSamples  map[string]EntrySample
	RiskRejectSamples   []RiskRejectSample
	FillExpirySamples   []FillExpirySample
	DebugTrace          []DebugTraceEvent
	MLShadowScored      int
	MLShadowVetos       int
	MLShadowUpsizes     int
	MLShadowSamples     []MLShadowSample
	MLAdvisoryEvaluated int
	MLAdvisoryApplied   int
	MLAdvisoryVetos     int
	MLAdvisoryUpsizes   int
	MLAdvisoryDownsizes int
	MLAdvisorySamples   []MLAdvisorySample
}

// EntrySample captures a representative entry decision for diagnostics.
type EntrySample struct {
	Symbol                 string
	Timestamp              time.Time
	Reason                 string
	Price                  float64
	GapPercent             float64
	RelativeVolume         float64
	PriceVsOpenPct         float64
	DistanceFromHighPct    float64
	AllowedDistanceHighPct float64
	OneMinuteReturnPct     float64
	ThreeMinuteReturnPct   float64
	FiveMinuteReturnPct    float64
	VolumeRate             float64
	VolumeLeaderPct        float64
	LeaderRank             int
	StockSelectionScore    float64
	ATRPct                 float64
	PriceVsVWAPPct         float64
	BreakoutPct            float64
	SetupType              string
	Score                  float64
	StrongSqueeze          bool
	Volume                 uint64
}

// RiskRejectSample captures when a strategy-approved signal is blocked by risk.
type RiskRejectSample struct {
	Symbol    string
	Timestamp time.Time
	Side      string
	Price     float64
	Quantity  int64
	Reason    string
	SetupType string
	Score     float64
}

// FillExpirySample captures when a risk-approved order fails to fill within the bar window.
type FillExpirySample struct {
	Symbol     string
	OrderTime  time.Time
	ExpiryTime time.Time
	Side       string
	LimitPrice float64
	Quantity   int64
	SetupType  string
}

type DebugTraceEvent struct {
	Stage                 string    `json:"stage"`
	Symbol                string    `json:"symbol"`
	Timestamp             time.Time `json:"timestamp"`
	Passed                bool      `json:"passed"`
	Reason                string    `json:"reason"`
	SetupType             string    `json:"setupType,omitempty"`
	Price                 float64   `json:"price,omitempty"`
	Open                  float64   `json:"open,omitempty"`
	HighOfDay             float64   `json:"highOfDay,omitempty"`
	GapPercent            float64   `json:"gapPercent,omitempty"`
	RelativeVolume        float64   `json:"relativeVolume,omitempty"`
	FiveMinuteVolume      uint64    `json:"fiveMinuteVolume,omitempty"`
	Score                 float64   `json:"score,omitempty"`
	DistanceFromHighPct   float64   `json:"distanceFromHighPct,omitempty"`
	OneMinuteReturnPct    float64   `json:"oneMinuteReturnPct,omitempty"`
	ThreeMinuteReturnPct  float64   `json:"threeMinuteReturnPct,omitempty"`
	FiveMinuteReturnPct   float64   `json:"fiveMinuteReturnPct,omitempty"`
	PriceVsVWAPPct        float64   `json:"priceVsVWAPPct,omitempty"`
	BreakoutPct           float64   `json:"breakoutPct,omitempty"`
	PullbackDepthPct      float64   `json:"pullbackDepthPct,omitempty"`
	ConsolidationRangePct float64   `json:"consolidationRangePct,omitempty"`
	CloseOffHighPct       float64   `json:"closeOffHighPct,omitempty"`
}

type MLShadowSample struct {
	Symbol          string    `json:"symbol"`
	Timestamp       time.Time `json:"timestamp"`
	SetupType       string    `json:"setupType"`
	StrategyReason  string    `json:"strategyReason"`
	Probability     float64   `json:"probability"`
	Threshold       float64   `json:"threshold"`
	Decision        string    `json:"decision"`
	DayRankSoFar    int       `json:"dayRankSoFar"`
	BarRankSoFar    int       `json:"barRankSoFar"`
	StrategyEmitted bool      `json:"strategyEmitted"`
	RiskApproved    bool      `json:"riskApproved"`
}

type MLAdvisorySample struct {
	Symbol           string    `json:"symbol"`
	Timestamp        time.Time `json:"timestamp"`
	SetupType        string    `json:"setupType"`
	StrategyReason   string    `json:"strategyReason"`
	Probability      float64   `json:"probability"`
	Threshold        float64   `json:"threshold"`
	Decision         string    `json:"decision"`
	OriginalQuantity int64     `json:"originalQuantity"`
	AdjustedQuantity int64     `json:"adjustedQuantity"`
	SizeMultiplier   float64   `json:"sizeMultiplier"`
	RiskApproved     bool      `json:"riskApproved"`
}

type bar = InputBar

// Run executes a historical backtest by wiring components through the shared Pipeline.
func Run(ctx context.Context, cfg config.TradingConfig, runCfg RunConfig) (Result, error) {
	iter, err := resolveBarIterator(runCfg)
	if err != nil {
		return Result{}, err
	}
	defer iter.Close()

	runtimeState := runtime.NewState()
	var mu sync.Mutex
	var simTimeNano atomic.Int64
	diagnostics := Diagnostics{
		ScannerRejects:     make(map[string]int),
		EntryRejects:       make(map[string]int),
		EntryRiskRejects:   make(map[string]int),
		ExitRejects:        make(map[string]int),
		ExitRiskRejects:    make(map[string]int),
		ByRegime:           make(map[string]TradeBreakdown),
		BySetup:            make(map[string]TradeBreakdown),
		BySide:             make(map[string]TradeBreakdown),
		EntryRejectSamples: make(map[string]EntrySample),
	}
	debugSymbols := make(map[string]bool, len(runCfg.DebugSymbols))
	for _, symbol := range runCfg.DebugSymbols {
		debugSymbols[strings.ToUpper(strings.TrimSpace(symbol))] = true
	}

	book := portfolio.NewManager(cfg)
	book.SetNowFunc(func() time.Time {
		return time.Unix(0, simTimeNano.Load())
	})
	var regimeTracker *regime.Tracker
	if cfg.EnableMarketRegime {
		regimeTracker = regime.NewTracker(cfg, runtimeState)
	}
	scan := scanner.NewScanner(cfg, runtimeState)
	scan.SetBlockedSymbols(runCfg.BlockedSymbols)
	broker := execution.NewPaperBroker(runCfg.EasyToBorrow)
	riskEngine := risk.NewEngine(cfg, book, runtimeState, broker)
	strat := strategy.NewStrategy(cfg, book, runtimeState, riskEngine)
	normalizer := market.NewNormalizer()
	scorer, err := ml.ResolveScorer(cfg.MLScoringEnabled, cfg.MLModelPath)
	if err != nil {
		return Result{}, err
	}

	// Build signal aggregator from alpha config.
	var sigAgg *signals.Aggregator
	if cfg.OFIEnabled || cfg.VPINEnabled {
		var sources []signals.SignalSource
		if cfg.OFIEnabled {
			sources = append(sources, signals.NewOFI(signals.OFIConfig{
				Enabled:           true,
				WindowBars:        cfg.OFIWindowBars,
				ThresholdSigma:    cfg.OFIThresholdSigma,
				PersistenceMinBar: cfg.OFIPersistenceMin,
			}))
		}
		if cfg.VPINEnabled {
			sources = append(sources, signals.NewVPIN(signals.VPINConfig{
				Enabled:         true,
				BucketDivisor:   cfg.VPINBucketDivisor,
				LookbackBuckets: cfg.VPINLookbackBuckets,
				HighThreshold:   cfg.VPINHighThreshold,
				LowThreshold:    cfg.VPINLowThreshold,
			}))
		}
		sigAgg = signals.NewAggregator(sources...)
	}

	peakEquity := cfg.StartingCapital
	maxDrawdown := 0.0

	pipe := pipeline.New(pipeline.Config{
		TradingCfg:                cfg,
		Runtime:                   runtimeState,
		Portfolio:                 book,
		Normalizer:                normalizer,
		Scanner:                   scan,
		Strategy:                  strat,
		RiskEngine:                riskEngine,
		Broker:                    broker,
		Recorder:                  runCfg.Recorder,
		Scorer:                    scorer,
		RegimeTracker:             regimeTracker,
		SignalAggregator:          sigAgg,
		CandidateEvaluationSource: "backtest",
		FloatLookup: func(sym string) int64 {
			if runCfg.FloatStore != nil {
				return runCfg.FloatStore.Get(sym)
			}
			return 0
		},
		EngineOptions: []execution.EngineOption{
			execution.WithPollInterval(1 * time.Millisecond),
			execution.WithPollTimeout(5 * time.Second),
			execution.WithSynchronous(true),
			execution.WithNowFunc(func() time.Time {
				return time.Unix(0, simTimeNano.Load())
			}),
		},
		Deterministic: true,
		OnTick: func(tick domain.Tick, domBar domain.Bar) {
			broker.UpdateBar(domBar)
			simTimeNano.Store(domBar.Timestamp.UnixNano())
		},
		OnCandidateEvaluation: func(eval domain.CandidateEvaluation) {
			mu.Lock()
			defer mu.Unlock()
			if eval.MLScored {
				diagnostics.MLShadowScored++
				if eval.MLShadowVeto {
					diagnostics.MLShadowVetos++
				}
				if eval.MLShadowUpsize {
					diagnostics.MLShadowUpsizes++
				}
				if len(diagnostics.MLShadowSamples) < 8 {
					diagnostics.MLShadowSamples = append(diagnostics.MLShadowSamples, MLShadowSample{
						Symbol:          eval.Candidate.Symbol,
						Timestamp:       eval.RecordedAt,
						SetupType:       eval.Candidate.SetupType,
						StrategyReason:  eval.StrategyReason,
						Probability:     eval.MLProbability,
						Threshold:       eval.MLThreshold,
						Decision:        eval.MLShadowDecision,
						DayRankSoFar:    eval.MLDayRankSoFar,
						BarRankSoFar:    eval.MLBarRankSoFar,
						StrategyEmitted: eval.StrategyEmitted,
						RiskApproved:    eval.RiskApproved,
					})
				}
			}
			if eval.MLAdvisoryEnabled && eval.StrategyEmitted {
				diagnostics.MLAdvisoryEvaluated++
				if eval.MLAdvisoryApplied {
					diagnostics.MLAdvisoryApplied++
					switch eval.MLAdvisoryDecision {
					case "veto":
						diagnostics.MLAdvisoryVetos++
					case "upsize":
						diagnostics.MLAdvisoryUpsizes++
					case "downsize":
						diagnostics.MLAdvisoryDownsizes++
					}
					if len(diagnostics.MLAdvisorySamples) < 8 {
						diagnostics.MLAdvisorySamples = append(diagnostics.MLAdvisorySamples, MLAdvisorySample{
							Symbol:           eval.Candidate.Symbol,
							Timestamp:        eval.RecordedAt,
							SetupType:        eval.Candidate.SetupType,
							StrategyReason:   eval.StrategyReason,
							Probability:      eval.MLProbability,
							Threshold:        eval.MLThreshold,
							Decision:         eval.MLAdvisoryDecision,
							OriginalQuantity: eval.MLAdvisoryOriginalQuantity,
							AdjustedQuantity: eval.MLAdvisoryAdjustedQuantity,
							SizeMultiplier:   eval.MLAdvisorySizeMultiplier,
							RiskApproved:     eval.RiskApproved,
						})
					}
				}
			}
		},
		TickFilter: func(t domain.Tick) bool {
			return withinWindow(t.Timestamp, runCfg.Start, runCfg.End)
		},
		OnScanResult: func(tick domain.Tick, candidate domain.Candidate, passed bool, reason string) {
			mu.Lock()
			defer mu.Unlock()
			if passed {
				diagnostics.EntryCandidates++
			} else {
				incrementReason(diagnostics.ScannerRejects, reason)
			}
			if debugSymbols[strings.ToUpper(tick.Symbol)] {
				event := DebugTraceEvent{
					Stage:            "scan",
					Symbol:           tick.Symbol,
					Timestamp:        tick.Timestamp,
					Passed:           passed,
					Reason:           reason,
					Price:            tick.Price,
					Open:             tick.Open,
					HighOfDay:        tick.HighOfDay,
					GapPercent:       tick.GapPercent,
					RelativeVolume:   tick.RelativeVolume,
					FiveMinuteVolume: tick.FiveMinuteVolume,
				}
				if passed {
					event.SetupType = candidate.SetupType
					event.Score = candidate.Score
					event.DistanceFromHighPct = candidate.DistanceFromHighPct
					event.OneMinuteReturnPct = candidate.OneMinuteReturnPct
					event.ThreeMinuteReturnPct = candidate.ThreeMinuteReturnPct
					event.FiveMinuteReturnPct = candidate.FiveMinuteReturnPct
					event.PriceVsVWAPPct = candidate.PriceVsVWAPPct
					event.BreakoutPct = candidate.BreakoutPct
					event.PullbackDepthPct = candidate.PullbackDepthPct
					event.ConsolidationRangePct = candidate.ConsolidationRangePct
					event.CloseOffHighPct = candidate.CloseOffHighPct
				}
				diagnostics.DebugTrace = append(diagnostics.DebugTrace, event)
			}
		},
		OnEntryDecision: func(candidate domain.Candidate, decision strategy.CandidateDecision) {
			mu.Lock()
			defer mu.Unlock()
			if decision.Emit {
				diagnostics.EntrySignals++
				rememberEntrySignalSample(&diagnostics, candidate, decision)
			} else {
				incrementReason(diagnostics.EntryRejects, decision.Reason)
				rememberEntryRejectSample(&diagnostics, candidate, decision)
			}
			if debugSymbols[strings.ToUpper(candidate.Symbol)] {
				diagnostics.DebugTrace = append(diagnostics.DebugTrace, DebugTraceEvent{
					Stage:                 "entry",
					Symbol:                candidate.Symbol,
					Timestamp:             candidate.Timestamp,
					Passed:                decision.Emit,
					Reason:                decision.Reason,
					SetupType:             candidate.SetupType,
					Price:                 candidate.Price,
					Open:                  candidate.Open,
					HighOfDay:             candidate.HighOfDay,
					GapPercent:            candidate.GapPercent,
					RelativeVolume:        candidate.RelativeVolume,
					FiveMinuteVolume:      0,
					Score:                 candidate.Score,
					DistanceFromHighPct:   candidate.DistanceFromHighPct,
					OneMinuteReturnPct:    candidate.OneMinuteReturnPct,
					ThreeMinuteReturnPct:  candidate.ThreeMinuteReturnPct,
					FiveMinuteReturnPct:   candidate.FiveMinuteReturnPct,
					PriceVsVWAPPct:        candidate.PriceVsVWAPPct,
					BreakoutPct:           candidate.BreakoutPct,
					PullbackDepthPct:      candidate.PullbackDepthPct,
					ConsolidationRangePct: candidate.ConsolidationRangePct,
					CloseOffHighPct:       candidate.CloseOffHighPct,
				})
			}
		},
		OnExitCheck: func(tick domain.Tick, signal domain.TradeSignal, shouldExit bool, reason string) {
			hasPosition := book.HasPosition(tick.Symbol)
			mu.Lock()
			defer mu.Unlock()
			if hasPosition {
				diagnostics.ExitChecks++
			}
			if shouldExit {
				diagnostics.ExitSignals++
			} else if hasPosition {
				incrementReason(diagnostics.ExitRejects, reason)
			}
		},
		OnRiskDecision: func(signal domain.TradeSignal, order domain.OrderRequest, approved bool, reason string) {
			mu.Lock()
			defer mu.Unlock()
			isEntry := domain.IsOpeningIntent(signal.Intent)
			if approved {
				if isEntry {
					diagnostics.EntryRiskApproved++
				} else {
					diagnostics.ExitRiskApproved++
				}
			} else {
				if isEntry {
					incrementReason(diagnostics.EntryRiskRejects, reason)
					if len(diagnostics.RiskRejectSamples) < 30 {
						diagnostics.RiskRejectSamples = append(diagnostics.RiskRejectSamples, RiskRejectSample{
							Symbol:    signal.Symbol,
							Timestamp: signal.Timestamp,
							Side:      signal.Side,
							Price:     signal.Price,
							Quantity:  signal.Quantity,
							Reason:    reason,
							SetupType: signal.SetupType,
						})
					}
					if reason == "daily-loss-limit" {
						runtimeState.RecordLog("warn", "risk", "blocked buy "+signal.Symbol+": "+reason)
						runtimeState.TriggerDailyLossStop(signal.Timestamp)
					}
				} else {
					incrementReason(diagnostics.ExitRiskRejects, reason)
				}
			}
			if debugSymbols[strings.ToUpper(signal.Symbol)] {
				diagnostics.DebugTrace = append(diagnostics.DebugTrace, DebugTraceEvent{
					Stage:     "risk",
					Symbol:    signal.Symbol,
					Timestamp: signal.Timestamp,
					Passed:    approved,
					Reason:    reason,
					SetupType: signal.SetupType,
					Price:     signal.Price,
				})
			}
		},
		OnTickFanOut: func(tick domain.Tick) {
			equity := cfg.StartingCapital + book.RealizedPnL() + book.UnrealizedPnL()
			mu.Lock()
			defer mu.Unlock()
			if equity > peakEquity {
				peakEquity = equity
			}
			if peakEquity > 0 {
				drawdown := ((peakEquity - equity) / peakEquity) * 100
				if drawdown > maxDrawdown {
					maxDrawdown = drawdown
				}
			}
		},
	})
	pipe.Start(ctx)

	// Feed bars from iterator into the pipeline.
	barsLoaded := 0
	barsInWindow := 0
	progressEvery := 250000
	lastProgressAt := time.Now()
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
		item, ok, err := iter.Next()
		if err != nil {
			return Result{}, err
		}
		if !ok {
			break
		}
		currentBar := normalizeInputBar(item)
		barsLoaded++
		if withinWindow(currentBar.Timestamp, runCfg.Start, runCfg.End) {
			barsInWindow++
		}
		if barsLoaded%progressEvery == 0 && time.Since(lastProgressAt) >= 15*time.Second {
			log.Printf(
				"Backtest progress bars_loaded=%d bars_in_window=%d last_bar=%s %s",
				barsLoaded,
				barsInWindow,
				currentBar.Symbol,
				currentBar.Timestamp.Format(time.RFC3339),
			)
			lastProgressAt = time.Now()
		}
		pipe.BarCh() <- domain.Bar{
			Symbol:      currentBar.Symbol,
			Timestamp:   currentBar.Timestamp,
			Open:        currentBar.Open,
			High:        currentBar.High,
			Low:         currentBar.Low,
			Close:       currentBar.Close,
			Volume:      currentBar.Volume,
			PrevClose:   currentBar.PrevClose,
			Catalyst:    currentBar.Catalyst,
			CatalystURL: currentBar.CatalystURL,
		}
	}
	log.Printf("Backtest bar feed complete bars_loaded=%d bars_in_window=%d waiting_for_pipeline=true", barsLoaded, barsInWindow)
	pipe.Close()
	pipe.Wait()
	log.Printf("Backtest pipeline drain complete bars_loaded=%d bars_in_window=%d", barsLoaded, barsInWindow)

	diagnostics.BarsLoaded = barsLoaded
	diagnostics.BarsInWindow = barsInWindow
	diagnostics.FillExpiries = broker.Expiries()

	if barsLoaded == 0 {
		return Result{}, fmt.Errorf("no bars found for requested backtest window")
	}

	closedTrades := book.GetTradeHistory()
	entriesExecuted := book.StatusSnapshot().EntriesToday
	openPositions := book.GetPositions()
	openSymbols := make([]string, 0, len(openPositions))
	for _, pos := range openPositions {
		openSymbols = append(openSymbols, pos.Symbol)
	}
	sort.Strings(openSymbols)
	for _, trade := range closedTrades {
		diagnostics.ByRegime[normalizeKey(trade.MarketRegime)] = updateBreakdown(diagnostics.ByRegime[normalizeKey(trade.MarketRegime)], trade)
		diagnostics.BySetup[normalizeKey(trade.SetupType)] = updateBreakdown(diagnostics.BySetup[normalizeKey(trade.SetupType)], trade)
		diagnostics.BySide[normalizeKey(trade.Side)] = updateBreakdown(diagnostics.BySide[normalizeKey(trade.Side)], trade)
	}
	openPositionsAtEnd := book.OpenPositionCount()
	wins := 0
	losses := 0
	grossWins := 0.0
	grossLosses := 0.0
	totalWinR := 0.0
	totalLossR := 0.0
	totalWinPnL := 0.0
	totalLossPnL := 0.0
	totalMFER := 0.0
	totalMAER := 0.0
	trailingStopExits := 0
	stopMinutesTotal := 0.0
	stopMinutesCount := 0
	for _, trade := range closedTrades {
		if trade.PnL > 0 {
			wins++
			grossWins += trade.PnL
			totalWinR += trade.RMultiple
			totalWinPnL += trade.PnL
		} else if trade.PnL < 0 {
			losses++
			grossLosses += math.Abs(trade.PnL)
			totalLossR += trade.RMultiple
			totalLossPnL += trade.PnL
		}
		if trade.ExitReason == "trailing-stop" {
			trailingStopExits++
		}
		if isStopLikeExit(trade.ExitReason) {
			stopMinutesTotal += trade.ClosedAt.Sub(trade.OpenedAt).Minutes()
			stopMinutesCount++
		}
		totalMFER += trade.MFER
		totalMAER += trade.MAER
	}

	realizedPnL := book.RealizedPnL()
	unrealizedPnL := book.UnrealizedPnL()
	netPnL := realizedPnL + unrealizedPnL
	endingEquity := cfg.StartingCapital + netPnL
	winRate := 0.0
	profitFactor := 0.0
	avgWinPnL := 0.0
	avgLossPnL := 0.0
	avgWinR := 0.0
	avgLossR := 0.0
	avgMFER := 0.0
	avgMAER := 0.0
	trailingStopExitPct := 0.0
	avgTimeToStopMin := 0.0
	if len(closedTrades) > 0 {
		winRate = (float64(wins) / float64(len(closedTrades))) * 100
		avgMFER = totalMFER / float64(len(closedTrades))
		avgMAER = totalMAER / float64(len(closedTrades))
		trailingStopExitPct = (float64(trailingStopExits) / float64(len(closedTrades))) * 100
	}
	if grossLosses > 0 {
		profitFactor = grossWins / grossLosses
	}
	if wins > 0 {
		avgWinR = totalWinR / float64(wins)
		avgWinPnL = totalWinPnL / float64(wins)
	}
	if losses > 0 {
		avgLossR = totalLossR / float64(losses)
		avgLossPnL = totalLossPnL / float64(losses)
	}
	if stopMinutesCount > 0 {
		avgTimeToStopMin = stopMinutesTotal / float64(stopMinutesCount)
	}

	result := Result{
		StartingCapital:     cfg.StartingCapital,
		RealizedPnL:         round2(realizedPnL),
		UnrealizedPnL:       round2(unrealizedPnL),
		EndingEquity:        round2(endingEquity),
		NetPnL:              round2(netPnL),
		MaxDrawdownPct:      round2(maxDrawdown),
		ProfitFactor:        round2(profitFactor),
		AvgWinPnL:           round2(avgWinPnL),
		AvgLossPnL:          round2(avgLossPnL),
		AvgWinR:             round2(avgWinR),
		AvgLossR:            round2(avgLossR),
		AvgMFER:             round2(avgMFER),
		AvgMAER:             round2(avgMAER),
		TrailingStopExitPct: round2(trailingStopExitPct),
		AvgTimeToStopMin:    round2(avgTimeToStopMin),
		EntriesExecuted:     entriesExecuted,
		Trades:              len(closedTrades),
		Wins:                wins,
		Losses:              losses,
		WinRate:             round2(winRate),
		OpenPositionsAtEnd:  openPositionsAtEnd,
		OpenSymbols:         openSymbols,
		Diagnostics:         diagnostics,
		ClosedTrades:        closedTrades,
	}

	// Phase 4: Transaction cost accounting
	if cfg.TransactionCostsEnabled && len(closedTrades) > 0 {
		var totalComm, totalSEC, totalTAF, totalSpread float64
		for _, trade := range closedTrades {
			qty := int(trade.Quantity)
			entrySide := "buy"
			exitSide := "sell"
			if trade.Side == "short" {
				entrySide = "sell"
				exitSide = "buy"
			}
			entryCosts := ComputeTransactionCosts(trade.EntryPrice, qty, entrySide, cfg.DefaultSpreadBps, cfg.CommissionPerShare)
			exitCosts := ComputeTransactionCosts(trade.ExitPrice, qty, exitSide, cfg.DefaultSpreadBps, cfg.CommissionPerShare)
			totalComm += entryCosts.Commission + exitCosts.Commission
			totalSEC += entryCosts.SECFee + exitCosts.SECFee
			totalTAF += entryCosts.TAFFee + exitCosts.TAFFee
			totalSpread += entryCosts.SpreadCost + exitCosts.SpreadCost
		}
		result.TotalCommissions = round2(totalComm)
		result.TotalSECFees = round2(totalSEC)
		result.TotalTAFFees = round2(totalTAF)
		result.TotalSpreadCosts = round2(totalSpread)
		result.TotalTransactionCosts = round2(totalComm + totalSEC + totalTAF + totalSpread)
		var totalNotional float64
		for _, trade := range closedTrades {
			qty := float64(trade.Quantity)
			totalNotional += trade.EntryPrice*qty + trade.ExitPrice*qty
		}
		if totalNotional > 0 {
			result.ImplementationShortfall = round2(result.TotalTransactionCosts / totalNotional * 100)
		}
	}

	// Phase 4: Monte Carlo simulation
	if cfg.MonteCarloEnabled && len(closedTrades) > 0 {
		trades := make([]TradeResult, len(closedTrades))
		for i, ct := range closedTrades {
			trades[i] = TradeResult{PnL: ct.PnL}
		}
		tradingDays := estimateTradingDays(closedTrades)
		mcResult := RunMonteCarlo(trades, cfg.StartingCapital, cfg.MonteCarloSims, tradingDays)
		result.MonteCarlo = &mcResult
	}

	return result, nil
}

func estimateTradingDays(trades []domain.ClosedTrade) int {
	if len(trades) == 0 {
		return 1
	}
	first := trades[0].OpenedAt
	last := trades[len(trades)-1].ClosedAt
	calDays := last.Sub(first).Hours() / 24
	tradingDays := int(calDays * 5 / 7) // approximate
	if tradingDays < 1 {
		tradingDays = 1
	}
	return tradingDays
}

func isStopLikeExit(reason string) bool {
	switch reason {
	case "stop-loss", "failed-breakout", "break-even-stop":
		return true
	default:
		return false
	}
}

func parseTimestamp(value string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp format: %s", value)
}

func cell(columns map[string]int, row []string, names ...string) string {
	for _, name := range names {
		if index, ok := columns[name]; ok && index < len(row) {
			return strings.TrimSpace(row[index])
		}
	}
	return ""
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func withinWindow(timestamp, start, end time.Time) bool {
	if !start.IsZero() && timestamp.Before(start) {
		return false
	}
	if !end.IsZero() && timestamp.After(end) {
		return false
	}
	return true
}

func incrementReason(counts map[string]int, reason string) {
	if reason == "" {
		reason = "unknown"
	}
	counts[reason]++
}

func normalizeKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func updateBreakdown(summary TradeBreakdown, trade domain.ClosedTrade) TradeBreakdown {
	summary.Trades++
	summary.NetPnL = round2(summary.NetPnL + trade.PnL)
	if trade.PnL > 0 {
		summary.Wins++
	} else if trade.PnL < 0 {
		summary.Losses++
	}
	return summary
}

func rememberEntrySignalSample(diag *Diagnostics, candidate domain.Candidate, decision strategy.CandidateDecision) {
	if len(diag.EntrySignalSamples) >= 20 {
		return
	}
	diag.EntrySignalSamples = append(diag.EntrySignalSamples, buildEntrySample(candidate, decision))
}

func rememberEntryRejectSample(diag *Diagnostics, candidate domain.Candidate, decision strategy.CandidateDecision) {
	if _, exists := diag.EntryRejectSamples[decision.Reason]; exists {
		return
	}
	diag.EntryRejectSamples[decision.Reason] = buildEntrySample(candidate, decision)
}

func buildEntrySample(candidate domain.Candidate, decision strategy.CandidateDecision) EntrySample {
	return EntrySample{
		Symbol:                 candidate.Symbol,
		Timestamp:              candidate.Timestamp,
		Reason:                 decision.Reason,
		Price:                  round2(candidate.Price),
		GapPercent:             round2(candidate.GapPercent),
		RelativeVolume:         round2(candidate.RelativeVolume),
		PriceVsOpenPct:         round2(candidate.PriceVsOpenPct),
		DistanceFromHighPct:    round2(candidate.DistanceFromHighPct),
		AllowedDistanceHighPct: round2(decision.AllowedDistanceHighPct),
		OneMinuteReturnPct:     round2(candidate.OneMinuteReturnPct),
		ThreeMinuteReturnPct:   round2(candidate.ThreeMinuteReturnPct),
		FiveMinuteReturnPct:    round2(candidate.FiveMinuteReturnPct),
		VolumeRate:             round2(candidate.VolumeRate),
		VolumeLeaderPct:        candidate.VolumeLeaderPct,
		LeaderRank:             candidate.LeaderRank,
		StockSelectionScore:    round2(candidate.StockSelectionScore),
		ATRPct:                 round2(candidate.ATRPct),
		PriceVsVWAPPct:         round2(candidate.PriceVsVWAPPct),
		BreakoutPct:            round2(candidate.BreakoutPct),
		SetupType:              candidate.SetupType,
		Score:                  round2(candidate.Score),
		StrongSqueeze:          decision.StrongSqueeze,
		Volume:                 candidate.Volume,
	}
}
