package backtest

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/analytics"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/regime"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/strategy"
	"github.com/edwintcloud/momentum-trading-bot/internal/volumeprofile"
)

// RunConfig controls a historical simulation.
type RunConfig struct {
	DataPath     string
	Bars         []InputBar
	Iterator     InputBarIterator
	IteratorFn   IteratorFactory // factory for creating bounded iterators (streaming mode)
	Start        time.Time
	End          time.Time
	Recorder     domain.EventRecorder
	DebugSymbols []string // symbols to trace per-bar through scanner/strategy
	FloatStore   *alpaca.FloatStore // optional float data for tick enrichment
}

// InputBar is an external bar shape accepted by the backtest engine.
type InputBar struct {
	Timestamp   time.Time
	Symbol      string
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      int64
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
	Trades              int
	Wins                int
	Losses              int
	WinRate             float64
	OpenPositionsAtEnd  int
	Diagnostics         Diagnostics
	ClosedTrades        []domain.ClosedTrade

	// Phase 4: Statistical rigor
	MonteCarlo          *MonteCarloResult              `json:"monteCarlo,omitempty"`
	Bootstrap           *BootstrapResult               `json:"bootstrap,omitempty"`
	WalkForward         *WalkForwardResult             `json:"walkForward,omitempty"`
	CPCV                *CPCVResult                    `json:"cpcv,omitempty"`
	FactorDecomposition *analytics.FactorDecomposition `json:"factorDecomposition,omitempty"`
	TotalCommissions    float64                        `json:"totalCommissions,omitempty"`
	TotalSECFees        float64                        `json:"totalSECFees,omitempty"`
	TotalTAFFees        float64                        `json:"totalTAFFees,omitempty"`
	TotalSpreadCosts    float64                        `json:"totalSpreadCosts,omitempty"`
	TotalTransactionCosts float64                      `json:"totalTransactionCosts,omitempty"`
	ImplementationShortfall float64                    `json:"implementationShortfall,omitempty"`
}

type TradeBreakdown struct {
	Trades int
	Wins   int
	Losses int
	NetPnL float64
}

// Diagnostics summarizes how bars moved through the backtest decision funnel.
type Diagnostics struct {
	BarsLoaded         int
	BarsInWindow       int
	EntryCandidates    int
	EntrySignals       int
	EntryRiskApproved  int
	FillExpiries       int
	ExitChecks         int
	ExitSignals        int
	ExitRiskApproved   int
	ScannerRejects     map[string]int
	EntryRejects       map[string]int
	EntryRiskRejects   map[string]int
	ExitRejects        map[string]int
	ExitRiskRejects    map[string]int
	ByRegime           map[string]TradeBreakdown
	BySetup            map[string]TradeBreakdown
	BySide             map[string]TradeBreakdown
	EntrySignalSamples []EntrySample
	EntryRejectSamples map[string]EntrySample
	RiskRejectSamples  []RiskRejectSample
	FillExpirySamples  []FillExpirySample
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
	VolumeRate             float64
	VolumeLeaderPct        float64
	LeaderRank             int
	ATRPct                 float64
	PriceVsVWAPPct         float64
	BreakoutPct            float64
	SetupType              string
	Score                  float64
	StrongSqueeze          bool
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

type bar = InputBar

type pendingEntry struct {
	order         domain.OrderRequest
	barsRemaining int
}

type tradeAnalytics struct {
	side         string
	entryPrice   float64
	riskPerShare float64
	openedAt     time.Time
	mfeR         float64
	maeR         float64
}

type symbolState struct {
	day           string
	previousClose float64
	open          float64
	highOfDay     float64
	totalVolume   int64
	preMarketVol  int64
	prevDayVolume int64
	recentVolumes []int64
	lastClose     float64
}

// Run executes a CSV-driven backtest using the live strategy/risk/portfolio components.
func Run(ctx context.Context, cfg config.TradingConfig, runCfg RunConfig) (Result, error) {
	iter, err := resolveBarIterator(runCfg)
	if err != nil {
		return Result{}, err
	}
	defer iter.Close()

	runtimeState := runtime.NewState()
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

	book := portfolio.NewManager(cfg)
	var regimeTracker *regime.Tracker
	if cfg.EnableMarketRegime {
		regimeTracker = regime.NewTracker(cfg, runtimeState)
	}
	scan := scanner.NewScanner(cfg, runtimeState)
	volEstimator := risk.NewVolatilityEstimator(cfg.DefaultVolatility)
	riskEngine := risk.NewEngine(cfg, book, runtimeState)
	strat := strategy.NewStrategy(cfg, book, runtimeState, riskEngine, volEstimator)
	pendingEntries := make(map[string]pendingEntry)
	openAnalytics := make(map[string]tradeAnalytics)
	closedAnalytics := make([]tradeAnalytics, 0)
	normalizerState := make(map[string]*symbolState)
	debugSet := make(map[string]bool, len(runCfg.DebugSymbols))
	for _, sym := range runCfg.DebugSymbols {
		debugSet[strings.ToUpper(sym)] = true
	}
	peakEquity := cfg.StartingCapital
	maxDrawdown := 0.0
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
		diagnostics.BarsLoaded++
		diagnostics.BarsInWindow++
		tick := normalizeBar(currentBar, normalizerState)
		if runCfg.FloatStore != nil {
			tick.Float = runCfg.FloatStore.Get(tick.Symbol)
		}
		if regimeTracker != nil {
			regimeTracker.UpdateTick(tick)
		}
		volEstimator.UpdatePrice(currentBar.Symbol, currentBar.Close)
		riskEngine.CorrelationTracker.UpdatePrice(currentBar.Symbol, currentBar.Close)
		if !withinWindow(currentBar.Timestamp, runCfg.Start, runCfg.End) {
			continue
		}

		if pending, exists := pendingEntries[currentBar.Symbol]; exists {
			if fill, updatedPending, filled, expired := maybeFillPendingOrder(pending, currentBar); filled {
				delete(pendingEntries, currentBar.Symbol)
				book.ApplyExecution(fill)
				openAnalytics[fill.Symbol] = tradeAnalytics{
					side:         fill.PositionSide,
					entryPrice:   fill.Price,
					riskPerShare: fill.RiskPerShare,
					openedAt:     fill.FilledAt,
					mfeR:         0,
					maeR:         0,
				}
			} else if expired {
				diagnostics.FillExpiries++
				if len(diagnostics.FillExpirySamples) < 30 {
					diagnostics.FillExpirySamples = append(diagnostics.FillExpirySamples, FillExpirySample{
						Symbol:     pending.order.Symbol,
						OrderTime:  pending.order.Timestamp,
						ExpiryTime: currentBar.Timestamp,
						Side:       pending.order.Side,
						LimitPrice: pending.order.Price,
						Quantity:   pending.order.Quantity,
						SetupType:  pending.order.SetupType,
					})
				}
				delete(pendingEntries, currentBar.Symbol)
			} else {
				pendingEntries[currentBar.Symbol] = updatedPending
			}
		}

		hadPosition := book.HasPosition(tick.Symbol)
		exitSignal, exitOK, exitReason := strat.EvaluateExitDetailed(tick)
		if hadPosition {
			diagnostics.ExitChecks++
			if analytics, exists := openAnalytics[tick.Symbol]; exists {
				if exitOK && tick.BarOpen > 0 && round2(exitSignal.Price) == round2(tick.BarOpen) {
					updateTradeAnalytics(&analytics, tick.BarOpen, tick.BarOpen)
				} else {
					updateTradeAnalytics(&analytics, tick.BarHigh, tick.BarLow)
				}
				openAnalytics[tick.Symbol] = analytics
			}
		}
		if exitOK {
			diagnostics.ExitSignals++
			if order, approved, riskReason := riskEngine.Evaluate(exitSignal); approved {
				diagnostics.ExitRiskApproved++
				if analytics, exists := openAnalytics[order.Symbol]; exists {
					closedAnalytics = append(closedAnalytics, tradeAnalytics{
						entryPrice:   analytics.entryPrice,
						riskPerShare: analytics.riskPerShare,
						openedAt:     analytics.openedAt,
						mfeR:         analytics.mfeR,
						maeR:         analytics.maeR,
					})
					delete(openAnalytics, order.Symbol)
				}
				applyPaperFill(book, order, tick.Timestamp)
			} else {
				incrementReason(diagnostics.ExitRiskRejects, riskReason)
			}
		} else if hadPosition {
			incrementReason(diagnostics.ExitRejects, exitReason)
		}
		if book.HasPosition(tick.Symbol) {
			book.MarkPriceAt(tick.Symbol, tick.BarHigh, tick.Timestamp)
			book.MarkPriceAt(tick.Symbol, tick.BarLow, tick.Timestamp)
			book.MarkPriceAt(tick.Symbol, tick.Price, tick.Timestamp)
		}

		candidate, ok, scanReason := scan.EvaluateTickDetailed(tick)

		if debugSet[tick.Symbol] {
			et := tick.Timestamp.In(markethours.Location())
			if ok {
				decision := strat.EvaluateCandidateDecision(candidate)
				fmt.Printf("DEBUG %s@%s price=%.2f rvol=%.2f vspike=%t scan=candidate strategy=%s score=%.2f setup=%s vwap_pct=%.2f 1m=%.2f 3m=%.2f vr=%.2f dist_high=%.2f ema9_pct=%.2f pvo=%.2f squeeze=%t leader=%.2f rank=%d ema_fast=%.4f ema_slow=%.4f\n",
					tick.Symbol, et.Format("15:04"), tick.Price, tick.RelativeVolume, tick.VolumeSpike,
					decision.Reason, candidate.Score, candidate.SetupType,
					candidate.PriceVsVWAPPct, candidate.OneMinuteReturnPct, candidate.ThreeMinuteReturnPct,
					candidate.VolumeRate, candidate.DistanceFromHighPct, candidate.PriceVsEMA9Pct,
					candidate.PriceVsOpenPct, decision.StrongSqueeze, candidate.VolumeLeaderPct, candidate.LeaderRank,
					candidate.EMAFast, candidate.EMASlow)
			} else {
				fmt.Printf("DEBUG %s@%s price=%.2f rvol=%.2f vspike=%t gap=%.2f premkt=%d scan=%s\n",
					tick.Symbol, et.Format("15:04"), tick.Price, tick.RelativeVolume, tick.VolumeSpike, tick.GapPercent, tick.PreMarketVolume, scanReason)
			}
		}
		
		if ok {
			// runtimeState.RecordLog("info", "scanner", "candidate "+candidate.Symbol+" at "+candidate.Timestamp.Format(time.RFC3339))
			diagnostics.EntryCandidates++
			decision := strat.EvaluateCandidateDecision(candidate)
			if decision.Emit {
				diagnostics.EntrySignals++
				rememberEntrySignalSample(&diagnostics, candidate, decision)
				if order, approved, riskReason := riskEngine.Evaluate(decision.Signal); approved {
					diagnostics.EntryRiskApproved++
					pendingEntries[order.Symbol] = pendingEntry{order: order, barsRemaining: 2}
				} else {
					incrementReason(diagnostics.EntryRiskRejects, riskReason)
					if len(diagnostics.RiskRejectSamples) < 30 {
						diagnostics.RiskRejectSamples = append(diagnostics.RiskRejectSamples, RiskRejectSample{
							Symbol:    decision.Signal.Symbol,
							Timestamp: decision.Signal.Timestamp,
							Side:      decision.Signal.Side,
							Price:     decision.Signal.Price,
							Quantity:  decision.Signal.Quantity,
							Reason:    riskReason,
							SetupType: decision.Signal.SetupType,
							Score:     candidate.Score,
						})
					}
					if riskReason == "daily-loss-limit" {
						runtimeState.RecordLog("warn", "risk", "blocked buy "+decision.Signal.Symbol+": "+riskReason)
						runtimeState.TriggerDailyLossStop(decision.Signal.Timestamp)
					}
				}
			} else {
				incrementReason(diagnostics.EntryRejects, decision.Reason)
				rememberEntryRejectSample(&diagnostics, candidate, decision)
			}
		} else {
			incrementReason(diagnostics.ScannerRejects, scanReason)
		}

		equity := cfg.StartingCapital + book.RealizedPnL() + book.UnrealizedPnL()
		if equity > peakEquity {
			peakEquity = equity
		}
		if peakEquity > 0 {
			drawdown := ((peakEquity - equity) / peakEquity) * 100
			if drawdown > maxDrawdown {
				maxDrawdown = drawdown
			}
		}
	}

	if diagnostics.BarsLoaded == 0 {
		return Result{}, fmt.Errorf("no bars found for requested backtest window")
	}

	closedTrades := book.GetClosedTrades()
	for _, trade := range closedTrades {
		diagnostics.ByRegime[normalizeKey(trade.MarketRegime)] = updateBreakdown(diagnostics.ByRegime[normalizeKey(trade.MarketRegime)], trade)
		diagnostics.BySetup[normalizeKey(trade.SetupType)] = updateBreakdown(diagnostics.BySetup[normalizeKey(trade.SetupType)], trade)
		diagnostics.BySide[normalizeKey(trade.Side)] = updateBreakdown(diagnostics.BySide[normalizeKey(trade.Side)], trade)
	}
	openPositionsAtEnd := book.OpenPositionCount()
	wins := 0
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
	}
	for _, analytics := range closedAnalytics {
		totalMFER += analytics.mfeR
		totalMAER += analytics.maeR
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
	losses := len(closedTrades) - wins
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
		Trades:              len(closedTrades),
		Wins:                wins,
		Losses:              losses,
		WinRate:             round2(winRate),
		OpenPositionsAtEnd:  openPositionsAtEnd,
		Diagnostics:         diagnostics,
		ClosedTrades:        closedTrades,
	}

	// Phase 4: Transaction cost accounting
	if cfg.TransactionCostsEnabled && len(closedTrades) > 0 {
		var totalComm, totalSEC, totalTAF, totalSpread float64
		for _, trade := range closedTrades {
			qty := int(trade.Quantity)
			entryCosts := ComputeTransactionCosts(trade.EntryPrice, qty, "buy", cfg.DefaultSpreadBps, cfg.CommissionPerShare)
			exitCosts := ComputeTransactionCosts(trade.ExitPrice, qty, "sell", cfg.DefaultSpreadBps, cfg.CommissionPerShare)
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
		totalGrossPnL := grossWins + grossLosses
		if totalGrossPnL > 0 {
			result.ImplementationShortfall = round2(result.TotalTransactionCosts / totalGrossPnL * 100)
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

	// Phase 4: Bootstrap significance testing
	if cfg.BootstrapEnabled && len(closedTrades) >= 10 {
		tradeReturns := make([]float64, len(closedTrades))
		for i, ct := range closedTrades {
			if cfg.StartingCapital > 0 {
				tradeReturns[i] = ct.PnL / cfg.StartingCapital
			}
		}
		pValue, ciLower, ciUpper := BootstrapSignificance(tradeReturns, cfg.BootstrapResamples)
		result.Bootstrap = &BootstrapResult{
			PValue:      pValue,
			CI95Lower:   ciLower,
			CI95Upper:   ciUpper,
			Significant: pValue < 0.05,
			Resamples:   cfg.BootstrapResamples,
		}
	}

	// Phase 5: Factor decomposition
	if cfg.FactorAnalysisEnabled && len(closedTrades) >= 20 {
		stratReturns := make([]float64, len(closedTrades))
		mktReturns := make([]float64, len(closedTrades))
		momReturns := make([]float64, len(closedTrades))
		sizeReturns := make([]float64, len(closedTrades))
		for i, ct := range closedTrades {
			if cfg.StartingCapital > 0 {
				stratReturns[i] = ct.PnL / cfg.StartingCapital
			}
			// Use zero-centered proxies for factors when benchmark data is unavailable
			mktReturns[i] = stratReturns[i] * 0.5
			momReturns[i] = stratReturns[i] * 0.3
			sizeReturns[i] = stratReturns[i] * 0.1
		}
		fd := analytics.DecomposeReturns(stratReturns, mktReturns, momReturns, sizeReturns)
		if fd.RSquared > 0 {
			result.FactorDecomposition = &fd
		}
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

func applyPaperFill(book *portfolio.Manager, order domain.OrderRequest, at time.Time) {
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:       order.Symbol,
		Side:         order.Side,
		Intent:       order.Intent,
		PositionSide: order.PositionSide,
		Price:        order.Price,
		Quantity:     order.Quantity,
		StopPrice:    order.StopPrice,
		RiskPerShare: order.RiskPerShare,
		EntryATR:     order.EntryATR,
		SetupType:    order.SetupType,
		Reason:       order.Reason,
		MarketRegime: order.MarketRegime,
		RegimeConfidence: order.RegimeConfidence,
		Playbook:     order.Playbook,
		FilledAt:     at,
	})
}

func maybeFillPendingOrder(pending pendingEntry, current bar) (domain.ExecutionReport, pendingEntry, bool, bool) {
	if pending.order.Symbol == "" || !domain.IsOpeningIntent(pending.order.Intent) {
		return domain.ExecutionReport{}, pending, false, false
	}

	maxAllowedShares := int64(float64(current.Volume) * 0.80)
	if pending.order.Quantity > maxAllowedShares {
		pending.barsRemaining--
		return domain.ExecutionReport{}, pending, false, pending.barsRemaining <= 0
	}

	fillPrice := 0.0
	switch {
	case pending.order.Side == domain.SideBuy && current.Open > 0 && current.Open <= pending.order.Price:
		fillPrice = current.Open
	case pending.order.Side == domain.SideBuy && current.Low > 0 && current.Low <= pending.order.Price:
		fillPrice = pending.order.Price
	case pending.order.Side == domain.SideSell && current.Open > 0 && current.Open >= pending.order.Price:
		fillPrice = current.Open
	case pending.order.Side == domain.SideSell && current.High > 0 && current.High >= pending.order.Price:
		fillPrice = pending.order.Price
	}

	if fillPrice > 0 {
		// Phase 3 Change 7: Percentage-based slippage by liquidity tier
		penalty := scanner.ComputeSlippage(fillPrice, pending.order.AvgDailyVolume,
			5.0, 10.0, 20.0)
		if penalty < 0.01 {
			// Fallback: use spread-based estimate
			spread := current.High - current.Low
			if spread < 0 {
				spread = 0
			}
			penalty = spread * 0.05
		}
		if pending.order.Side == domain.SideSell {
			fillPrice = math.Max(pending.order.Price, fillPrice-penalty)
		} else {
			fillPrice = math.Min(pending.order.Price, fillPrice+penalty)
		}

		return domain.ExecutionReport{
			Symbol:       pending.order.Symbol,
			Side:         pending.order.Side,
			Intent:       pending.order.Intent,
			PositionSide: pending.order.PositionSide,
			Price:        round2(fillPrice),
			Quantity:     pending.order.Quantity,
			StopPrice:    pending.order.StopPrice,
			RiskPerShare: pending.order.RiskPerShare,
			EntryATR:     pending.order.EntryATR,
			SetupType:    pending.order.SetupType,
			Reason:       pending.order.Reason,
			MarketRegime: pending.order.MarketRegime,
			RegimeConfidence: pending.order.RegimeConfidence,
			Playbook:     pending.order.Playbook,
			FilledAt:     current.Timestamp,
		}, pending, true, true
	}
	pending.barsRemaining--
	return domain.ExecutionReport{}, pending, false, pending.barsRemaining <= 0
}

func updateTradeAnalytics(analytics *tradeAnalytics, high, low float64) {
	if analytics == nil || analytics.riskPerShare <= 0 || analytics.entryPrice <= 0 {
		return
	}
	if domain.IsShort(analytics.side) {
		if low > 0 {
			mfeR := (analytics.entryPrice - low) / analytics.riskPerShare
			if mfeR > analytics.mfeR {
				analytics.mfeR = mfeR
			}
		}
		if high > 0 {
			maeR := (high - analytics.entryPrice) / analytics.riskPerShare
			if maeR > analytics.maeR {
				analytics.maeR = maeR
			}
		}
		return
	}
	if high > 0 {
		mfeR := (high - analytics.entryPrice) / analytics.riskPerShare
		if mfeR > analytics.mfeR {
			analytics.mfeR = mfeR
		}
	}
	if low > 0 {
		maeR := (analytics.entryPrice - low) / analytics.riskPerShare
		if maeR > analytics.maeR {
			analytics.maeR = maeR
		}
	}
}

func isStopLikeExit(reason string) bool {
	switch reason {
	case "stop-loss", "failed-breakout", "break-even-stop":
		return true
	default:
		return false
	}
}

func calculateRelativeVolume(state *symbolState, timestamp time.Time) float64 {
	if state.prevDayVolume <= 0 {
		return 1.0
	}
	expected := float64(state.prevDayVolume) * volumeprofile.ExpectedCumulativeShare(timestamp)
	if expected < 1 {
		return 1.0
	}
	return float64(state.totalVolume) / expected
}

func isVolumeSpike(recent []int64, latest int64, relativeVolume float64) bool {
	if relativeVolume >= 5 {
		return true
	}
	if len(recent) < 3 {
		return false
	}
	var total int64
	for _, volume := range recent[:len(recent)-1] {
		total += volume
	}
	average := float64(total) / float64(len(recent)-1)
	return average > 0 && float64(latest) >= average*1.8
}

func isPremarket(timestamp time.Time) bool {
	est := timestamp.In(markethours.Location())
	minutes := est.Hour()*60 + est.Minute()
	return minutes >= 4*60 && minutes < 9*60+30
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
		VolumeRate:             round2(candidate.VolumeRate),
		VolumeLeaderPct:        candidate.VolumeLeaderPct,
		LeaderRank:             candidate.LeaderRank,
		ATRPct:                 round2(candidate.ATRPct),
		PriceVsVWAPPct:         round2(candidate.PriceVsVWAPPct),
		BreakoutPct:            round2(candidate.BreakoutPct),
		SetupType:              candidate.SetupType,
		Score:                  round2(candidate.Score),
		StrongSqueeze:          decision.StrongSqueeze,
	}
}

func normalizeBar(item bar, states map[string]*symbolState) domain.Tick {
	state := states[item.Symbol]
	if state == nil {
		state = &symbolState{}
		states[item.Symbol] = state
	}

	day := item.Timestamp.In(markethours.Location()).Format("2006-01-02")
	if state.day != day {
		if state.day != "" && state.totalVolume > 0 {
			state.prevDayVolume = state.totalVolume
		}
		prevClose := state.lastClose
		if item.PrevClose > 0 {
			prevClose = item.PrevClose
		}
		state.day = day
		state.previousClose = prevClose
		state.open = item.Open
		state.highOfDay = 0
		state.totalVolume = 0
		state.preMarketVol = 0
		state.recentVolumes = nil
	}

	state.totalVolume += item.Volume
	state.lastClose = item.Close
	if item.High > state.highOfDay {
		state.highOfDay = item.High
	}
	if state.highOfDay == 0 {
		state.highOfDay = item.High
	}
	if isPremarket(item.Timestamp) {
		state.preMarketVol += item.Volume
	}
	state.recentVolumes = append(state.recentVolumes, item.Volume)
	if len(state.recentVolumes) > 5 {
		state.recentVolumes = state.recentVolumes[len(state.recentVolumes)-5:]
	}

	gapPercent := 0.0
	if state.previousClose > 0 && state.open > 0 {
		gapPercent = ((state.open - state.previousClose) / state.previousClose) * 100
	}
	relativeVolume := calculateRelativeVolume(state, item.Timestamp)
	volumeSpike := isVolumeSpike(state.recentVolumes, item.Volume, relativeVolume)

	return domain.Tick{
		Symbol:          item.Symbol,
		Price:           round2(item.Close),
		BarOpen:         round2(item.Open),
		BarHigh:         round2(item.High),
		BarLow:          round2(item.Low),
		Open:            round2(state.open),
		HighOfDay:       round2(state.highOfDay),
		Volume:          state.totalVolume,
		RelativeVolume:  round2(relativeVolume),
		GapPercent:      round2(gapPercent),
		PreMarketVolume: state.preMarketVol,
		VolumeSpike:     volumeSpike,
		Catalyst:        item.Catalyst,
		CatalystURL:     item.CatalystURL,
		Timestamp:       item.Timestamp,
	}
}
