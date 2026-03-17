package backtest

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
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
	DataPath           string
	Bars               []InputBar
	Start              time.Time
	End                time.Time
	TrainStart         time.Time
	TrainEnd           time.Time
	LabelLookaheadBars int
	ModelOutputPath    string
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
	ModelName            string
	ModelTrainingWarning string
	StartingCapital      float64
	RealizedPnL          float64
	UnrealizedPnL        float64
	EndingEquity         float64
	NetPnL               float64
	MaxDrawdownPct       float64
	ProfitFactor         float64
	AvgWinPnL            float64
	AvgLossPnL           float64
	AvgWinR              float64
	AvgLossR             float64
	AvgMFER              float64
	AvgMAER              float64
	TrailingStopExitPct  float64
	AvgTimeToStopMin     float64
	Trades               int
	Wins                 int
	Losses               int
	WinRate              float64
	OpenPositionsAtEnd   int
	Diagnostics          Diagnostics
	ClosedTrades         []domain.ClosedTrade
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
	TrainingCandidates int
	TrainingSamples    int
	TrainingRuns       int
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
	Symbol                  string
	Timestamp               time.Time
	Reason                  string
	Price                   float64
	GapPercent              float64
	RelativeVolume          float64
	PriceVsOpenPct          float64
	DistanceFromHighPct     float64
	AllowedDistanceHighPct  float64
	OneMinuteReturnPct      float64
	ThreeMinuteReturnPct    float64
	FifteenMinuteReturnPct  float64
	VolumeRate              float64
	VolumeLeaderPct         float64
	LeaderRank              int
	ATRPct                  float64
	PriceVsVWAPPct          float64
	BreakoutPct             float64
	SetupType               string
	Score                   float64
	PredictedReturnPct      float64
	RequiredPredictedRetPct float64
	StrongSqueeze           bool
}

type bar struct {
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

type record struct {
	bar       bar
	tick      domain.Tick
	candidate *domain.Candidate
}

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

type trainingStats struct {
	candidateBars int
	samples       int
}

type trainingRow struct {
	candidateAt time.Time
	availableAt time.Time
	sample      strategy.TrainingSample
}

type trainingCorpus struct {
	candidateTimestamps []time.Time
	rows                []trainingRow
}

// Run executes a CSV-driven backtest using the live strategy/risk/portfolio components.
func Run(ctx context.Context, cfg config.TradingConfig, runCfg RunConfig) (Result, error) {
	if runCfg.LabelLookaheadBars <= 0 {
		runCfg.LabelLookaheadBars = 8
	}

	bars, err := resolveBars(runCfg)
	if err != nil {
		return Result{}, err
	}
	if len(bars) == 0 {
		return Result{}, fmt.Errorf("no bars found for requested backtest window")
	}

	runtimeState := runtime.NewState()
	records, symbolIndices := buildRecords(cfg, runtimeState, bars)
	runCfg.Bars = nil
	bars = nil
	corpus := trainingCorpus{}
	if !runCfg.TrainStart.IsZero() {
		cacheKey := trainingCorpusCacheKey(cfg, runCfg, records, symbolIndices)
		loadedCorpus := false
		if cached, ok, err := loadTrainingCorpusCache(cacheKey); err != nil {
			runtimeState.RecordLog("warn", "backtest", "could not load training corpus cache: "+err.Error())
		} else if ok {
			corpus = cached
			loadedCorpus = true
			runtimeState.RecordLog(
				"info",
				"backtest",
				fmt.Sprintf(
					"loaded training corpus cache rows=%d candidates=%d",
					len(corpus.rows),
					len(corpus.candidateTimestamps),
				),
			)
		}
		if !loadedCorpus {
			corpus = precomputeTrainingCorpus(cfg, records, symbolIndices, runCfg)
			if err := saveTrainingCorpusCache(cacheKey, corpus); err != nil {
				runtimeState.RecordLog("warn", "backtest", "could not save training corpus cache: "+err.Error())
			} else {
				runtimeState.RecordLog(
					"info",
					"backtest",
					fmt.Sprintf(
						"saved training corpus cache rows=%d candidates=%d",
						len(corpus.rows),
						len(corpus.candidateTimestamps),
					),
				)
			}
		}
	}
	diagnostics := Diagnostics{
		BarsLoaded:         len(records),
		ScannerRejects:     make(map[string]int),
		EntryRejects:       make(map[string]int),
		EntryRiskRejects:   make(map[string]int),
		ExitRejects:        make(map[string]int),
		ExitRiskRejects:    make(map[string]int),
		EntryRejectSamples: make(map[string]EntrySample),
	}

	model := strategy.DefaultEntryModel()
	trainingWarning := ""

	book := portfolio.NewManager(cfg, runtimeState)
	scan := scanner.NewScanner(cfg, runtimeState)
	strat := strategy.NewStrategy(cfg, book, runtimeState)
	strat.SetEntryModel(model)
	riskEngine := risk.NewEngine(cfg, book, runtimeState)
	pendingEntries := make(map[string]pendingEntry)
	openAnalytics := make(map[string]tradeAnalytics)
	closedAnalytics := make([]tradeAnalytics, 0)
	lastTrainingDay := ""

	equityCurve := make([]float64, 0, len(records))
	for _, rec := range records {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
		if !withinWindow(rec.bar.Timestamp, runCfg.Start, runCfg.End) {
			continue
		}
		diagnostics.BarsInWindow++
		if !runCfg.TrainStart.IsZero() {
			trainingDay := tradingDayKey(rec.bar.Timestamp)
			if trainingDay != lastTrainingDay {
				windowStart, windowEnd, ok := walkForwardTrainWindow(runCfg, rec.bar.Timestamp)
				if ok {
					trainCfg := runCfg
					trainCfg.TrainStart = windowStart
					trainCfg.TrainEnd = windowEnd
					trained, stats, trainErr := trainModel(corpus, trainCfg.TrainStart, trainCfg.TrainEnd)
					diagnostics.TrainingRuns++
					diagnostics.TrainingCandidates += stats.candidateBars
					diagnostics.TrainingSamples += stats.samples
					if trainErr != nil {
						trainingWarning = trainErr.Error()
					} else {
						model = trained
						strat.SetEntryModel(model)
					}
				}
				lastTrainingDay = trainingDay
			}
		}

		if pending, exists := pendingEntries[rec.bar.Symbol]; exists {
			if fill, updatedPending, filled, expired := maybeFillPendingEntry(pending, rec.bar); filled {
				delete(pendingEntries, rec.bar.Symbol)
				book.ApplyExecution(fill)
				openAnalytics[fill.Symbol] = tradeAnalytics{
					entryPrice:   fill.Price,
					riskPerShare: fill.RiskPerShare,
					openedAt:     fill.FilledAt,
					mfeR:         0,
					maeR:         0,
				}
			} else if expired {
				delete(pendingEntries, rec.bar.Symbol)
			} else {
				pendingEntries[rec.bar.Symbol] = updatedPending
			}
		}

		hadPosition := book.HasPosition(rec.tick.Symbol)
		exitSignal, exitOK, exitReason := strat.EvaluateExitDetailed(rec.tick)
		if hadPosition {
			diagnostics.ExitChecks++
			if analytics, exists := openAnalytics[rec.tick.Symbol]; exists {
				if exitOK && rec.tick.BarOpen > 0 && round2(exitSignal.Price) == round2(rec.tick.BarOpen) {
					updateTradeAnalytics(&analytics, rec.tick.BarOpen, rec.tick.BarOpen)
				} else {
					updateTradeAnalytics(&analytics, rec.tick.BarHigh, rec.tick.BarLow)
				}
				openAnalytics[rec.tick.Symbol] = analytics
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
				applyPaperFill(book, order, rec.tick.Timestamp)
			} else {
				incrementReason(diagnostics.ExitRiskRejects, riskReason)
			}
		} else if hadPosition {
			incrementReason(diagnostics.ExitRejects, exitReason)
		}
		if book.HasPosition(rec.tick.Symbol) {
			book.MarkPriceAt(rec.tick.Symbol, rec.tick.BarHigh, rec.tick.Timestamp)
			book.MarkPriceAt(rec.tick.Symbol, rec.tick.BarLow, rec.tick.Timestamp)
			book.MarkPriceAt(rec.tick.Symbol, rec.tick.Price, rec.tick.Timestamp)
		}

		candidate, ok, scanReason := scan.EvaluateTickDetailed(rec.tick)
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

		equityCurve = append(equityCurve, book.EffectiveCapital()+book.RealizedPnL()+book.UnrealizedPnL()-cfg.StartingCapital)
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
	if runCfg.ModelOutputPath != "" {
		if err := strategy.SaveLinearModel(runCfg.ModelOutputPath, model); err != nil {
			return Result{}, err
		}
	}

	return Result{
		ModelName:            model.Name,
		ModelTrainingWarning: trainingWarning,
		StartingCapital:      cfg.StartingCapital,
		RealizedPnL:          round2(realizedPnL),
		UnrealizedPnL:        round2(unrealizedPnL),
		EndingEquity:         round2(endingEquity),
		NetPnL:               round2(netPnL),
		MaxDrawdownPct:       round2(maxDrawdownPct(equityCurve, cfg.StartingCapital)),
		ProfitFactor:         round2(profitFactor),
		AvgWinPnL:            round2(avgWinPnL),
		AvgLossPnL:           round2(avgLossPnL),
		AvgWinR:              round2(avgWinR),
		AvgLossR:             round2(avgLossR),
		AvgMFER:              round2(avgMFER),
		AvgMAER:              round2(avgMAER),
		TrailingStopExitPct:  round2(trailingStopExitPct),
		AvgTimeToStopMin:     round2(avgTimeToStopMin),
		Trades:               len(closedTrades),
		Wins:                 wins,
		Losses:               losses,
		WinRate:              round2(winRate),
		OpenPositionsAtEnd:   openPositionsAtEnd,
		Diagnostics:          diagnostics,
		ClosedTrades:         closedTrades,
	}, nil
}

func resolveBars(runCfg RunConfig) ([]bar, error) {
	switch {
	case len(runCfg.Bars) > 0:
		return convertInputBars(runCfg.Bars, runCfg.Start, runCfg.End, runCfg.TrainStart, runCfg.TrainEnd), nil
	case runCfg.DataPath != "":
		return loadBars(runCfg.DataPath, runCfg.Start, runCfg.End, runCfg.TrainStart, runCfg.TrainEnd)
	default:
		return nil, fmt.Errorf("either data path or historical bars are required")
	}
}

func loadBars(path string, start, end, trainStart, trainEnd time.Time) ([]bar, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.ToLower(strings.TrimSpace(name))] = index
	}

	var bars []bar
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		entry, parseErr := parseBar(columns, row)
		if parseErr != nil {
			return nil, parseErr
		}
		if !withinAnyWindow(entry.Timestamp, start, end, trainStart, trainEnd) {
			continue
		}
		bars = append(bars, entry)
	}

	slices.SortFunc(bars, func(a, b bar) int {
		if cmp := a.Timestamp.Compare(b.Timestamp); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Symbol, b.Symbol)
	})
	return bars, nil
}

func convertInputBars(input []InputBar, start, end, trainStart, trainEnd time.Time) []bar {
	bars := make([]bar, 0, len(input))
	for _, item := range input {
		entry := bar{
			Timestamp:   item.Timestamp.UTC(),
			Symbol:      strings.ToUpper(item.Symbol),
			Open:        item.Open,
			High:        item.High,
			Low:         item.Low,
			Close:       item.Close,
			Volume:      item.Volume,
			PrevClose:   item.PrevClose,
			Catalyst:    item.Catalyst,
			CatalystURL: item.CatalystURL,
		}
		if !withinAnyWindow(entry.Timestamp, start, end, trainStart, trainEnd) {
			continue
		}
		bars = append(bars, entry)
	}
	slices.SortFunc(bars, func(a, b bar) int {
		if cmp := a.Timestamp.Compare(b.Timestamp); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Symbol, b.Symbol)
	})
	return bars
}

func parseBar(columns map[string]int, row []string) (bar, error) {
	timestamp, err := parseTimestamp(cell(columns, row, "timestamp", "time", "datetime"))
	if err != nil {
		return bar{}, err
	}
	open, err := strconv.ParseFloat(cell(columns, row, "open"), 64)
	if err != nil {
		return bar{}, err
	}
	high, err := strconv.ParseFloat(cell(columns, row, "high"), 64)
	if err != nil {
		return bar{}, err
	}
	low, err := strconv.ParseFloat(cell(columns, row, "low"), 64)
	if err != nil {
		return bar{}, err
	}
	closePrice, err := strconv.ParseFloat(cell(columns, row, "close"), 64)
	if err != nil {
		return bar{}, err
	}
	volume, err := strconv.ParseInt(cell(columns, row, "volume"), 10, 64)
	if err != nil {
		return bar{}, err
	}

	prevClose := 0.0
	if rawPrevClose := cell(columns, row, "prev_close", "previous_close"); rawPrevClose != "" {
		prevClose, _ = strconv.ParseFloat(rawPrevClose, 64)
	}

	return bar{
		Timestamp:   timestamp.UTC(),
		Symbol:      strings.ToUpper(cell(columns, row, "symbol")),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       closePrice,
		Volume:      volume,
		PrevClose:   prevClose,
		Catalyst:    cell(columns, row, "catalyst", "headline"),
		CatalystURL: cell(columns, row, "catalyst_url", "headline_url", "url"),
	}, nil
}

func buildRecords(cfg config.TradingConfig, runtimeState *runtime.State, bars []bar) ([]record, map[string][]int) {
	scan := scanner.NewScanner(cfg, runtimeState)
	normalizerState := make(map[string]*symbolState)
	records := make([]record, 0, len(bars))
	symbolIndices := make(map[string][]int)
	for _, item := range bars {
		tick := normalizeBar(item, normalizerState)
		candidate, ok := scan.EvaluateTick(tick)
		var ptr *domain.Candidate
		if ok {
			ptr = &candidate
		}
		rec := record{
			bar:       item,
			tick:      tick,
			candidate: ptr,
		}
		records = append(records, rec)
		symbolIndices[item.Symbol] = append(symbolIndices[item.Symbol], len(records)-1)
	}
	return records, symbolIndices
}

func precomputeTrainingCorpus(cfg config.TradingConfig, records []record, symbolIndices map[string][]int, runCfg RunConfig) trainingCorpus {
	corpus := trainingCorpus{
		candidateTimestamps: make([]time.Time, 0),
		rows:                make([]trainingRow, 0),
	}
	for _, indices := range symbolIndices {
		for pos, idx := range indices {
			rec := records[idx]
			if rec.candidate == nil || !withinWindow(rec.bar.Timestamp, runCfg.TrainStart, runCfg.End) {
				continue
			}
			corpus.candidateTimestamps = append(corpus.candidateTimestamps, rec.bar.Timestamp)
			plan, ok, _ := strategy.BuildEntryPlan(*rec.candidate)
			if !ok {
				continue
			}
			forwardReturn, availableAt, ok := trainingTargetOutcome(cfg, rec, indices, pos, records, runCfg.LabelLookaheadBars, plan)
			if !ok {
				continue
			}
			corpus.rows = append(corpus.rows, trainingRow{
				candidateAt: rec.bar.Timestamp,
				availableAt: availableAt,
				sample: strategy.TrainingSample{
					Candidate:        *rec.candidate,
					ForwardReturnPct: forwardReturn,
				},
			})
		}
	}
	return corpus
}

func trainModel(corpus trainingCorpus, trainStart, trainEnd time.Time) (strategy.LinearModel, trainingStats, error) {
	stats := trainingStats{}
	samples := make([]strategy.TrainingSample, 0)
	for _, timestamp := range corpus.candidateTimestamps {
		if withinWindow(timestamp, trainStart, trainEnd) {
			stats.candidateBars++
		}
	}
	for _, row := range corpus.rows {
		if !withinWindow(row.candidateAt, trainStart, trainEnd) {
			continue
		}
		if row.availableAt.After(trainEnd) {
			continue
		}
		samples = append(samples, row.sample)
		stats.samples++
	}
	if len(samples) == 0 {
		return strategy.LinearModel{}, stats, fmt.Errorf("no training samples produced from the requested train window")
	}
	model, err := strategy.TrainLinearModel(samples)
	return model, stats, err
}

func trainingTarget(cfg config.TradingConfig, rec record, indices []int, pos int, records []record, runCfg RunConfig, plan strategy.EntryPlan) (float64, bool) {
	target, _, ok := trainingTargetOutcome(cfg, rec, indices, pos, records, runCfg.LabelLookaheadBars, plan)
	return target, ok
}

func trainingTargetOutcome(cfg config.TradingConfig, rec record, indices []int, pos int, records []record, lookaheadBars int, plan strategy.EntryPlan) (float64, time.Time, bool) {
	entryOrder := domain.OrderRequest{
		Symbol:       rec.candidate.Symbol,
		Side:         "buy",
		Price:        backtestLimitPrice(rec.candidate.Price, "buy", cfg.LimitOrderSlippageDollars),
		Quantity:     1,
		StopPrice:    plan.StopPrice,
		RiskPerShare: plan.RiskPerShare,
		EntryATR:     plan.EntryATR,
		SetupType:    plan.SetupType,
		Reason:       "training-entry",
		Timestamp:    rec.candidate.Timestamp,
	}
	pending := pendingEntry{order: entryOrder, barsRemaining: 2}
	position := domain.Position{}
	filled := false
	if lookaheadBars < 20 {
		lookaheadBars = 20
	}
	for lookahead := 1; lookahead <= lookaheadBars && pos+lookahead < len(indices); lookahead++ {
		future := records[indices[pos+lookahead]]
		if !filled {
			if fill, updatedPending, didFill, expired := maybeFillPendingEntry(pending, future.bar); didFill {
				filled = true
				position = domain.Position{
					Symbol:           fill.Symbol,
					Quantity:         fill.Quantity,
					AvgPrice:         fill.Price,
					StopPrice:        fill.StopPrice,
					InitialStopPrice: fill.StopPrice,
					RiskPerShare:     fill.RiskPerShare,
					EntryATR:         fill.EntryATR,
					SetupType:        fill.SetupType,
					LastPrice:        fill.Price,
					HighestPrice:     fill.Price,
					OpenedAt:         future.bar.Timestamp.UTC(),
					UpdatedAt:        future.bar.Timestamp.UTC(),
				}
			} else if expired {
				return 0, time.Time{}, false
			} else {
				pending = updatedPending
				continue
			}
		}

		if exitPrice, _, exited := simulateManagedExit(position, future.tick, cfg); exited {
			return strategy.CurrentRMultiple(position, exitPrice), future.tick.Timestamp.UTC(), true
		}
		position.HighestPrice = maxFloat(position.HighestPrice, future.tick.BarHigh, future.tick.Price)
		position.LastPrice = future.tick.Price
		position.UpdatedAt = future.tick.Timestamp.UTC()
	}
	if !filled {
		return 0, time.Time{}, false
	}
	return strategy.CurrentRMultiple(position, position.LastPrice), position.UpdatedAt.UTC(), true
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

func simulateManagedExit(position domain.Position, tick domain.Tick, cfg config.TradingConfig) (float64, string, bool) {
	decisionAt := tick.Timestamp.UTC()
	highWatermark := maxFloat(position.HighestPrice, tick.BarHigh, tick.Price)
	previousStop, previousReason := strategy.ProtectiveStop(position, position.HighestPrice, firstPositive(position.LastPrice, position.AvgPrice), decisionAt)
	if previousStop <= 0 {
		previousStop, previousReason = strategy.ProtectiveStop(position, highWatermark, firstPositive(position.LastPrice, tick.Price), decisionAt)
	}
	barOpen := firstPositive(tick.BarOpen, tick.Price)
	barLow := firstPositive(tick.BarLow, tick.Price)
	barClose := firstPositive(tick.Price, tick.BarOpen)
	peakReturn := strategy.PeakRMultiple(position, highWatermark)
	holdingTime := decisionAt.Sub(position.OpenedAt)
	sameDayHold := sameTrainingDay(position.OpenedAt, decisionAt)

	spread := tick.BarHigh - tick.BarLow
	if spread < 0 {
		spread = 0
	}
	penalty := spread * 0.05

	localTime := decisionAt.In(marketTZ)
	minutes := localTime.Hour()*60 + localTime.Minute()

	switch {
	case minutes >= 15*60+55:
		fillPrice := math.Max(0.01, round2(barClose-penalty))
		return fillPrice, "end-of-day-liquidation", true
	case barOpen > 0 && previousStop > 0 && barOpen <= previousStop:
		fillPrice := math.Max(0.01, round2(barOpen-penalty))
		fmt.Printf("DEBUG EXIT: %s open-stop previousStop=%.2f barOpen=%.2f penalty=%.2f\n", position.Symbol, previousStop, barOpen, penalty)
		return fillPrice, previousReason, true
	case sameDayHold &&
		holdingTime >= time.Duration(cfg.BreakoutFailureWindowMin)*time.Minute &&
		peakReturn < 1.0 &&
		barLow > 0 &&
		barLow <= strategy.FailedBreakoutPrice(position):
		fillPrice := math.Max(0.01, round2(strategy.FailedBreakoutPrice(position)-penalty))
		fmt.Printf("DEBUG EXIT: %s failed-breakout fbp=%.2f barLow=%.2f penalty=%.2f peakReturn=%.2f\n", position.Symbol, strategy.FailedBreakoutPrice(position), barLow, penalty, peakReturn)
		return fillPrice, "failed-breakout", true
	case func() bool {
		stopPrice, _ := strategy.ProtectiveStop(position, highWatermark, firstPositive(tick.Price, barOpen), decisionAt)
		return stopPrice > 0 && barLow > 0 && barLow <= stopPrice
	}():
		stopPrice, reason := strategy.ProtectiveStop(position, highWatermark, firstPositive(tick.Price, barOpen), decisionAt)
		fillPrice := math.Max(0.01, round2(stopPrice-penalty))
		fmt.Printf("DEBUG EXIT: %s %s stopPrice=%.2f barLow=%.2f penalty=%.2f peakReturn=%.2f initialStop=%.2f\n", position.Symbol, reason, stopPrice, barLow, penalty, peakReturn, position.InitialStopPrice)
		return fillPrice, reason, true
	default:
		return 0, "", false
	}
}

func backtestLimitPrice(price float64, side string, maxBuffer float64) float64 {
	if price <= 0 {
		return 0
	}
	buffer := price * 0.004
	if buffer < 0.01 {
		buffer = 0.01
	}
	if buffer > maxBuffer {
		buffer = maxBuffer
	}
	buffer = round2(buffer)
	if side == "sell" {
		return round2(math.Max(0.01, price-buffer))
	}
	return round2(price + buffer)
}

func sameTrainingDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return a.In(marketTZ).Format("2006-01-02") == b.In(marketTZ).Format("2006-01-02")
}

func tradingDayKey(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.In(marketTZ).Format("2006-01-02")
}

func walkForwardTrainWindow(runCfg RunConfig, at time.Time) (time.Time, time.Time, bool) {
	if runCfg.TrainStart.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	local := at.In(marketTZ)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, marketTZ).UTC()
	windowEnd := dayStart.Add(-time.Minute)
	if !windowEnd.After(runCfg.TrainStart) {
		return time.Time{}, time.Time{}, false
	}
	return runCfg.TrainStart, windowEnd, true
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

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
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

func maxDrawdownPct(curve []float64, startingCapital float64) float64 {
	if len(curve) == 0 || startingCapital <= 0 {
		return 0
	}
	peak := startingCapital
	maxDrawdown := 0.0
	for _, pnl := range curve {
		equity := startingCapital + pnl
		if equity > peak {
			peak = equity
		}
		drawdown := ((peak - equity) / peak) * 100
		if drawdown > maxDrawdown {
			maxDrawdown = drawdown
		}
	}
	return maxDrawdown
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

func withinAnyWindow(timestamp, start, end, trainStart, trainEnd time.Time) bool {
	return withinWindow(timestamp, start, end) || withinWindow(timestamp, trainStart, trainEnd)
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
	if len(diag.EntrySignalSamples) >= 3 {
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
		Symbol:                  candidate.Symbol,
		Timestamp:               candidate.Timestamp.UTC(),
		Reason:                  decision.Reason,
		Price:                   round2(candidate.Price),
		GapPercent:              round2(candidate.GapPercent),
		RelativeVolume:          round2(candidate.RelativeVolume),
		PriceVsOpenPct:          round2(candidate.PriceVsOpenPct),
		DistanceFromHighPct:     round2(candidate.DistanceFromHighPct),
		AllowedDistanceHighPct:  round2(decision.AllowedDistanceHighPct),
		OneMinuteReturnPct:      round2(candidate.OneMinuteReturnPct),
		ThreeMinuteReturnPct:    round2(candidate.ThreeMinuteReturnPct),
		FifteenMinuteReturnPct:  round2(candidate.FifteenMinuteReturnPct),
		VolumeRate:              round2(candidate.VolumeRate),
		VolumeLeaderPct:         candidate.VolumeLeaderPct,
		LeaderRank:              candidate.LeaderRank,
		ATRPct:                  round2(candidate.ATRPct),
		PriceVsVWAPPct:          round2(candidate.PriceVsVWAPPct),
		BreakoutPct:             round2(candidate.BreakoutPct),
		SetupType:               candidate.SetupType,
		Score:                   round2(candidate.Score),
		PredictedReturnPct:      round2(decision.PredictedReturnPct),
		RequiredPredictedRetPct: round2(decision.RequiredReturnPct),
		StrongSqueeze:           decision.StrongSqueeze,
	}
}
