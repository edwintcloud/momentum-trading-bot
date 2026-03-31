package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/ml"
)

type labeledCandidateRow struct {
	CandidateEvaluation domain.CandidateEvaluation `json:"candidateEvaluation"`
	Label               ml.CandidateOutcomeLabel   `json:"label"`
}

type candidateLabelSummary struct {
	InputPath               string         `json:"inputPath"`
	OutputPath              string         `json:"outputPath"`
	DataPath                string         `json:"dataPath,omitempty"`
	CreatedAt               time.Time      `json:"createdAt"`
	Candidates              int            `json:"candidates"`
	TradeLinked             int            `json:"tradeLinked"`
	RiskApproved            int            `json:"riskApproved"`
	UpperBarrierPct         float64        `json:"upperBarrierPct"`
	LowerBarrierPct         float64        `json:"lowerBarrierPct"`
	MaxBars                 int            `json:"maxBars"`
	BarrierCounts           map[string]int `json:"barrierCounts"`
	OutcomeBucketCounts     map[string]int `json:"outcomeBucketCounts"`
	ProfitableCount         int            `json:"profitableCount"`
	InsufficientForwardBars int            `json:"insufficientForwardBars"`
	Symbols                 []string       `json:"symbols"`
	Start                   time.Time      `json:"start"`
	End                     time.Time      `json:"end"`
}

func RunLabelCandidates(args []string) error {
	flags := flag.NewFlagSet("label-candidates", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	inputPath := flags.String("in", "", "Required input candidate evaluation JSONL path")
	outputPath := flags.String("out", "", "Required output labeled candidate JSONL path")
	summaryOut := flags.String("summary-out", "", "Optional JSON summary output path")
	dataPath := flags.String("data", "", "Optional CSV bar file path for offline labeling")
	upperPct := flags.Float64("upper-pct", 0.10, "Profit target barrier percentage as a decimal")
	lowerPct := flags.Float64("lower-pct", 0.05, "Stop barrier percentage as a decimal")
	maxBars := flags.Int("max-bars", 60, "Maximum forward 1-minute bars for time barrier resolution")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*inputPath) == "" {
		return fmt.Errorf("input candidate evaluation path is required")
	}
	if strings.TrimSpace(*outputPath) == "" {
		return fmt.Errorf("output labeled candidate path is required")
	}
	if *upperPct <= 0 || *lowerPct <= 0 {
		return fmt.Errorf("upper-pct and lower-pct must both be positive")
	}
	if *maxBars <= 0 {
		return fmt.Errorf("max-bars must be positive")
	}

	summary, err := labelCandidateEvaluationFile(*inputPath, *outputPath, *summaryOut, *dataPath, *upperPct, *lowerPct, *maxBars)
	if err != nil {
		return err
	}
	log.Printf("Label candidates wrote rows=%d output=%s profitable=%d trade_linked=%d risk_approved=%d", summary.Candidates, *outputPath, summary.ProfitableCount, summary.TradeLinked, summary.RiskApproved)
	return nil
}

func labelCandidateEvaluationFile(inputPath, outputPath, summaryOut, dataPath string, upperPct, lowerPct float64, maxBars int) (candidateLabelSummary, error) {
	rows, symbols, start, end, err := loadCandidateEvaluationRows(inputPath)
	if err != nil {
		return candidateLabelSummary{}, err
	}
	if len(rows) == 0 {
		return candidateLabelSummary{}, fmt.Errorf("no candidate evaluations found in %s", inputPath)
	}
	log.Printf("Label candidates input=%s rows=%d symbols=%d start=%s end=%s", inputPath, len(rows), len(symbols), start.Format(time.RFC3339), end.Format(time.RFC3339))

	barStart := alignLabelWindowStart(start)
	barEnd := end.Add(time.Duration(maxBars+1) * time.Minute)
	cacheEnd := endOfMarketDay(barEnd)
	barsBySymbol, err := loadLabelBars(dataPath, symbols, barStart, cacheEnd)
	if err != nil {
		return candidateLabelSummary{}, err
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return candidateLabelSummary{}, err
	}
	outFile, err := os.Create(outputPath)
	if err != nil {
		return candidateLabelSummary{}, err
	}
	defer outFile.Close()
	writer := bufio.NewWriter(outFile)
	defer writer.Flush()

	summary := candidateLabelSummary{
		InputPath:           inputPath,
		OutputPath:          outputPath,
		DataPath:            dataPath,
		CreatedAt:           time.Now(),
		UpperBarrierPct:     upperPct,
		LowerBarrierPct:     lowerPct,
		MaxBars:             maxBars,
		BarrierCounts:       make(map[string]int),
		OutcomeBucketCounts: make(map[string]int),
		Symbols:             symbols,
		Start:               start,
		End:                 end,
	}

	for _, row := range rows {
		candidate := row.Candidate
		label := ml.LabelCandidateAt(
			barsBySymbol[candidate.Symbol],
			row.RecordedAt,
			candidate.Price,
			candidate.Direction,
			upperPct,
			lowerPct,
			maxBars,
		)
		label.TradeLinked = row.StrategyEmitted
		label.RiskApproved = row.RiskApproved

		labeled := labeledCandidateRow{
			CandidateEvaluation: row,
			Label:               label,
		}
		data, err := json.Marshal(labeled)
		if err != nil {
			return candidateLabelSummary{}, err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return candidateLabelSummary{}, err
		}

		summary.Candidates++
		if label.TradeLinked {
			summary.TradeLinked++
		}
		if label.RiskApproved {
			summary.RiskApproved++
		}
		if label.Profitable {
			summary.ProfitableCount++
		}
		if label.InsufficientForwardBars {
			summary.InsufficientForwardBars++
		}
		summary.BarrierCounts[label.Barrier]++
		summary.OutcomeBucketCounts[label.OutcomeBucket]++
	}
	if err := writer.Flush(); err != nil {
		return candidateLabelSummary{}, err
	}

	if summaryOut != "" {
		if err := os.MkdirAll(filepath.Dir(summaryOut), 0o755); err != nil {
			return candidateLabelSummary{}, err
		}
		data, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return candidateLabelSummary{}, err
		}
		if err := os.WriteFile(summaryOut, data, 0o644); err != nil {
			return candidateLabelSummary{}, err
		}
	}
	return summary, nil
}

func loadCandidateEvaluationRows(path string) ([]domain.CandidateEvaluation, []string, time.Time, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}
	defer file.Close()

	rows := make([]domain.CandidateEvaluation, 0, 1024)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	symbolSet := make(map[string]struct{})
	var start time.Time
	var end time.Time
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row domain.CandidateEvaluation
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("parse candidate evaluation: %w", err)
		}
		rows = append(rows, row)
		symbol := strings.ToUpper(strings.TrimSpace(row.Candidate.Symbol))
		if symbol != "" {
			symbolSet[symbol] = struct{}{}
		}
		if start.IsZero() || row.RecordedAt.Before(start) {
			start = row.RecordedAt
		}
		if end.IsZero() || row.RecordedAt.After(end) {
			end = row.RecordedAt
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}

	symbols := make([]string, 0, len(symbolSet))
	for symbol := range symbolSet {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return rows, symbols, start, end, nil
}

func alignLabelWindowStart(start time.Time) time.Time {
	local := start.In(markethours.Location())
	return time.Date(local.Year(), local.Month(), local.Day(), 4, 0, 0, 0, markethours.Location())
}

func loadLabelBars(dataPath string, symbols []string, start, end time.Time) (map[string][]ml.Bar, error) {
	if strings.TrimSpace(dataPath) != "" {
		bars, err := backtest.LoadInputBars(dataPath, start, end)
		if err != nil {
			return nil, err
		}
		return buildMLBarsBySymbol(bars), nil
	}
	if cachedBars, ok, err := loadLabelBarsFromHistoricalCache(symbols, start, end); err != nil {
		return nil, err
	} else if ok {
		return cachedBars, nil
	}

	alpacaCfg, err := config.LoadBacktestAlpacaConfig(nil)
	if err != nil {
		return nil, err
	}
	client := alpaca.NewClient(alpacaCfg, config.DefaultTradingConfig())
	fetchCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	historicalRateLimit := 0
	if capabilities, capErr := client.DetectMarketDataCapabilities(fetchCtx); capErr == nil {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
	} else {
		log.Printf("Label candidates capability detection warning: %v", capErr)
	}
	dataset, err := backtest.PrepareHistoricalDataset(fetchCtx, client, symbols, start, end, historicalRateLimit)
	if err != nil {
		return nil, err
	}
	iter := backtest.NewHistoricalDatasetIterator(dataset)
	defer iter.Close()

	bars := make([]backtest.InputBar, 0, 4096)
	for {
		bar, ok, err := iter.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		bars = append(bars, bar)
	}
	return buildMLBarsBySymbol(bars), nil
}

func loadLabelBarsFromHistoricalCache(symbols []string, start, end time.Time) (map[string][]ml.Bar, bool, error) {
	allSymbols, err := loadCachedBacktestSymbols()
	if err != nil || len(allSymbols) == 0 {
		return nil, false, err
	}

	requested := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		requested[strings.ToUpper(strings.TrimSpace(symbol))] = struct{}{}
	}

	jobs := backtest.BuildHistoricalFetchJobs(allSymbols, start, end)
	inputBars := make([]backtest.InputBar, 0, 4096)
	foundSymbols := make(map[string]struct{})
	for _, job := range jobs {
		if !jobContainsRequestedSymbol(job, requested) {
			continue
		}
		reader, err := openHistoricalCacheReaderForKnownFeeds(job)
		if err != nil {
			return nil, false, err
		}
		if reader == nil {
			continue
		}
		for {
			bar, ok, err := reader.Next()
			if err != nil {
				_ = reader.Close()
				return nil, false, err
			}
			if !ok {
				break
			}
			if _, keep := requested[strings.ToUpper(bar.Symbol)]; !keep {
				continue
			}
			inputBars = append(inputBars, bar)
			foundSymbols[strings.ToUpper(bar.Symbol)] = struct{}{}
		}
		if err := reader.Close(); err != nil {
			return nil, false, err
		}
	}

	if len(foundSymbols) != len(requested) {
		return nil, false, nil
	}
	log.Printf("Label candidates loaded bars from historical cache symbols=%d start=%s end=%s", len(foundSymbols), start.Format(time.RFC3339), end.Format(time.RFC3339))
	return buildMLBarsBySymbol(inputBars), true, nil
}

func openHistoricalCacheReaderForKnownFeeds(job backtest.HistoricalFetchJob) (*backtest.HistoricalJobCacheReader, error) {
	for _, feed := range []string{"sip", "iex"} {
		if !backtest.HistoricalJobCacheExists(job, feed) {
			continue
		}
		return backtest.OpenHistoricalJobCacheReader(job, feed)
	}
	return nil, nil
}

func jobContainsRequestedSymbol(job backtest.HistoricalFetchJob, requested map[string]struct{}) bool {
	for _, symbol := range job.Symbols {
		if _, ok := requested[strings.ToUpper(strings.TrimSpace(symbol))]; ok {
			return true
		}
	}
	return false
}

func loadCachedBacktestSymbols() ([]string, error) {
	type symbolCache struct {
		Version int      `json:"version"`
		Date    string   `json:"date"`
		Symbols []string `json:"symbols"`
	}

	raw, err := os.ReadFile(filepath.Join(".cache", "backtest", "symbols.json"))
	if err != nil {
		return nil, err
	}
	var cache symbolCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cache.Symbols))
	for _, symbol := range cache.Symbols {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol != "" {
			out = append(out, symbol)
		}
	}
	return out, nil
}

func buildMLBarsBySymbol(input []backtest.InputBar) map[string][]ml.Bar {
	out := make(map[string][]ml.Bar)
	for _, item := range input {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		out[symbol] = append(out[symbol], ml.Bar{
			Timestamp: item.Timestamp,
			Open:      item.Open,
			High:      item.High,
			Low:       item.Low,
			Close:     item.Close,
			Volume:    item.Volume,
		})
	}
	for symbol := range out {
		sort.Slice(out[symbol], func(i, j int) bool {
			return out[symbol][i].Timestamp.Before(out[symbol][j].Timestamp)
		})
	}
	return out
}
