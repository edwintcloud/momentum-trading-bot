package backtest

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
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
	VolumeRate              float64
	VolumeLeaderPct         float64
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
	bar          bar
	tick         domain.Tick
	candidate    domain.Candidate
	hasCandidate bool
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
	if !runCfg.TrainStart.IsZero() && !runCfg.TrainEnd.IsZero() {
		trained, stats, trainErr := trainModel(records, symbolIndices, runCfg)
		diagnostics.TrainingCandidates = stats.candidateBars
		diagnostics.TrainingSamples = stats.samples
		if trainErr != nil {
			trainingWarning = trainErr.Error()
		}
		if trainErr == nil {
			model = trained
		}
		if trainErr == nil && runCfg.ModelOutputPath != "" {
			if err := strategy.SaveLinearModel(runCfg.ModelOutputPath, model); err != nil {
				return Result{}, err
			}
		}
	}

	book := portfolio.NewManager(cfg, runtimeState)
	scan := scanner.NewScanner(cfg, runtimeState)
	strat := strategy.NewStrategy(cfg, book, runtimeState)
	strat.SetEntryModel(model)
	riskEngine := risk.NewEngine(cfg, book, runtimeState)

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

		book.MarkPriceAt(rec.tick.Symbol, rec.tick.Price, rec.tick.Timestamp)
		hadPosition := book.HasPosition(rec.tick.Symbol)
		exitSignal, exitOK, exitReason := strat.EvaluateExitDetailed(rec.tick)
		if hadPosition {
			diagnostics.ExitChecks++
		}
		if exitOK {
			diagnostics.ExitSignals++
			if order, approved, riskReason := riskEngine.Evaluate(exitSignal); approved {
				diagnostics.ExitRiskApproved++
				applyPaperFill(book, order, rec.tick.Timestamp)
			} else {
				incrementReason(diagnostics.ExitRiskRejects, riskReason)
			}
		} else if hadPosition {
			incrementReason(diagnostics.ExitRejects, exitReason)
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
					applyPaperFill(book, order, rec.tick.Timestamp)
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
	for _, trade := range closedTrades {
		if trade.PnL > 0 {
			wins++
		}
	}

	realizedPnL := book.RealizedPnL()
	unrealizedPnL := book.UnrealizedPnL()
	netPnL := realizedPnL + unrealizedPnL
	endingEquity := cfg.StartingCapital + netPnL
	winRate := 0.0
	if len(closedTrades) > 0 {
		winRate = (float64(wins) / float64(len(closedTrades))) * 100
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
		Trades:               len(closedTrades),
		Wins:                 wins,
		Losses:               len(closedTrades) - wins,
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

	sort.Slice(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
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
	sort.Slice(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
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
		rec := record{
			bar:          item,
			tick:         tick,
			candidate:    candidate,
			hasCandidate: ok,
		}
		records = append(records, rec)
		symbolIndices[item.Symbol] = append(symbolIndices[item.Symbol], len(records)-1)
	}
	return records, symbolIndices
}

func trainModel(records []record, symbolIndices map[string][]int, runCfg RunConfig) (strategy.LinearModel, trainingStats, error) {
	samples := make([]strategy.TrainingSample, 0)
	stats := trainingStats{}
	for symbol, indices := range symbolIndices {
		_ = symbol
		for pos, idx := range indices {
			rec := records[idx]
			if !rec.hasCandidate || !withinWindow(rec.bar.Timestamp, runCfg.TrainStart, runCfg.TrainEnd) {
				continue
			}
			stats.candidateBars++
			maxHigh := rec.bar.Close
			minLow := rec.bar.Close
			endClose := rec.bar.Close
			barsSeen := 0
			for lookahead := 1; lookahead <= runCfg.LabelLookaheadBars && pos+lookahead < len(indices); lookahead++ {
				future := records[indices[pos+lookahead]]
				if !withinWindow(future.bar.Timestamp, runCfg.TrainStart, runCfg.TrainEnd) {
					break
				}
				if future.bar.High > maxHigh {
					maxHigh = future.bar.High
				}
				if future.bar.Low < minLow {
					minLow = future.bar.Low
				}
				endClose = future.bar.Close
				barsSeen++
			}
			if barsSeen == 0 {
				continue
			}
			forwardReturn := trainingTarget(rec.bar.Close, maxHigh, minLow, endClose)
			samples = append(samples, strategy.TrainingSample{
				Candidate:        rec.candidate,
				ForwardReturnPct: forwardReturn,
			})
			stats.samples++
		}
	}
	if len(samples) == 0 {
		return strategy.LinearModel{}, stats, fmt.Errorf("no training samples produced from the requested train window")
	}
	model, err := strategy.TrainLinearModel(samples)
	return model, stats, err
}

func trainingTarget(entryClose, maxHigh, minLow, endClose float64) float64 {
	if entryClose <= 0 {
		return 0
	}
	continuationReturn := ((endClose - entryClose) / entryClose) * 100
	maxExcursion := ((maxHigh - entryClose) / entryClose) * 100
	adverseExcursion := ((minLow - entryClose) / entryClose) * 100
	return (continuationReturn * 0.65) + (maxExcursion * 0.35) + (adverseExcursion * 1.10)
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
		Symbol:   order.Symbol,
		Side:     order.Side,
		Price:    order.Price,
		Quantity: order.Quantity,
		Reason:   order.Reason,
		FilledAt: at.UTC(),
	})
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
	est := timestamp.In(marketTZ)
	marketOpen := time.Date(est.Year(), est.Month(), est.Day(), 9, 30, 0, 0, marketTZ)
	minutesSinceOpen := est.Sub(marketOpen).Minutes()
	if minutesSinceOpen < 1 {
		expected := float64(state.prevDayVolume) * 0.05
		if expected < 1 {
			return 1.0
		}
		return float64(state.totalVolume) / expected
	}
	if minutesSinceOpen > 390 {
		minutesSinceOpen = 390
	}
	expected := float64(state.prevDayVolume) * (minutesSinceOpen / 390.0)
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
		VolumeRate:              round2(candidate.VolumeRate),
		VolumeLeaderPct:         round2(candidate.VolumeLeaderPct),
		Score:                   round2(candidate.Score),
		PredictedReturnPct:      round2(decision.PredictedReturnPct),
		RequiredPredictedRetPct: round2(decision.RequiredReturnPct),
		StrongSqueeze:           decision.StrongSqueeze,
	}
}
