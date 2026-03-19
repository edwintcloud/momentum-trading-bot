package backtest

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/risk"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
	"github.com/edwincloud/momentum-trading-bot/internal/scanner"
	"github.com/edwincloud/momentum-trading-bot/internal/strategy"
	"github.com/edwincloud/momentum-trading-bot/internal/volumeprofile"
)

var marketTZ = mustLoadLocation("America/New_York")

// RunConfig controls a historical simulation.
type RunConfig struct {
	DataPath string
	Bars     []InputBar
	Iterator InputBarIterator
	Start    time.Time
	End      time.Time
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
}

// Diagnostics summarizes how bars moved through the backtest decision funnel.
type Diagnostics struct {
	BarsLoaded         int
	BarsInWindow       int
	EntryCandidates    int
	EntrySignals       int
	EntryRiskApproved  int
	ExitChecks         int
	ExitSignals        int
	ExitRiskApproved   int
	ScannerRejects     map[string]int
	EntryRejects       map[string]int
	EntryRiskRejects   map[string]int
	ExitRejects        map[string]int
	ExitRiskRejects    map[string]int
	EntrySignalSamples []EntrySample
	EntryRejectSamples map[string]EntrySample
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

type bar = InputBar

type pendingEntry struct {
	order         domain.OrderRequest
	barsRemaining int
}

type tradeAnalytics struct {
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
		EntryRejectSamples: make(map[string]EntrySample),
	}

	book := portfolio.NewManager(cfg, runtimeState)
	scan := scanner.NewScanner(cfg, runtimeState)
	strat := strategy.NewStrategy(cfg, book, runtimeState)
	riskEngine := risk.NewEngine(cfg, book, runtimeState)
	pendingEntries := make(map[string]pendingEntry)
	openAnalytics := make(map[string]tradeAnalytics)
	closedAnalytics := make([]tradeAnalytics, 0)
	normalizerState := make(map[string]*symbolState)
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
		if !withinWindow(currentBar.Timestamp, runCfg.Start, runCfg.End) {
			continue
		}
		diagnostics.BarsLoaded++
		diagnostics.BarsInWindow++
		tick := normalizeBar(currentBar, normalizerState)

		if pending, exists := pendingEntries[currentBar.Symbol]; exists {
			if fill, updatedPending, filled, expired := maybeFillPendingEntry(pending, currentBar); filled {
				delete(pendingEntries, currentBar.Symbol)
				book.ApplyExecution(fill)
				openAnalytics[fill.Symbol] = tradeAnalytics{
					entryPrice:   fill.Price,
					riskPerShare: fill.RiskPerShare,
					openedAt:     fill.FilledAt,
					mfeR:         0,
					maeR:         0,
				}
			} else if expired {
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
					analytics.openedAt = analytics.openedAt.UTC()
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
		if ok {
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

	return Result{
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
	}, nil
}

func applyPaperFill(book *portfolio.Manager, order domain.OrderRequest, at time.Time) {
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:       order.Symbol,
		Side:         order.Side,
		Price:        order.Price,
		Quantity:     order.Quantity,
		StopPrice:    order.StopPrice,
		RiskPerShare: order.RiskPerShare,
		EntryATR:     order.EntryATR,
		SetupType:    order.SetupType,
		Reason:       order.Reason,
		FilledAt:     at.UTC(),
	})
}

func maybeFillPendingEntry(pending pendingEntry, current bar) (domain.ExecutionReport, pendingEntry, bool, bool) {
	if pending.order.Symbol == "" || pending.order.Side != "buy" {
		return domain.ExecutionReport{}, pending, false, false
	}

	maxAllowedShares := int64(float64(current.Volume) * 0.10)
	if pending.order.Quantity > maxAllowedShares {
		pending.barsRemaining--
		return domain.ExecutionReport{}, pending, false, pending.barsRemaining <= 0
	}

	fillPrice := 0.0
	switch {
	case current.Open > 0 && current.Open <= pending.order.Price:
		fillPrice = current.Open
	case current.Low > 0 && current.Low <= pending.order.Price:
		fillPrice = pending.order.Price
	}

	if fillPrice > 0 {
		spread := current.High - current.Low
		if spread < 0 {
			spread = 0
		}
		penalty := spread * 0.05
		fillPrice = math.Min(pending.order.Price, fillPrice+penalty)

		return domain.ExecutionReport{
			Symbol:       pending.order.Symbol,
			Side:         pending.order.Side,
			Price:        round2(fillPrice),
			Quantity:     pending.order.Quantity,
			StopPrice:    pending.order.StopPrice,
			RiskPerShare: pending.order.RiskPerShare,
			EntryATR:     pending.order.EntryATR,
			SetupType:    pending.order.SetupType,
			Reason:       pending.order.Reason,
			FilledAt:     current.Timestamp.UTC(),
		}, pending, true, true
	}
	pending.barsRemaining--
	return domain.ExecutionReport{}, pending, false, pending.barsRemaining <= 0
}

func updateTradeAnalytics(analytics *tradeAnalytics, high, low float64) {
	if analytics == nil || analytics.riskPerShare <= 0 || analytics.entryPrice <= 0 {
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
	est := timestamp.In(marketTZ)
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

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}

func incrementReason(counts map[string]int, reason string) {
	if reason == "" {
		reason = "unknown"
	}
	counts[reason]++
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
		Timestamp:              candidate.Timestamp.UTC(),
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

	day := item.Timestamp.In(marketTZ).Format("2006-01-02")
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
	if state.previousClose > 0 {
		gapPercent = ((item.Close - state.previousClose) / state.previousClose) * 100
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
		Timestamp:       item.Timestamp.UTC(),
	}
}
