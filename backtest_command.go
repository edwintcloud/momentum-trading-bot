package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

var dateOnlyPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func runBacktestCommand(args []string) error {
	flags := flag.NewFlagSet("backtest", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	dataPath := flags.String("data", "", "Optional CSV fallback with timestamp,symbol,open,high,low,close,volume columns")
	startRaw := flags.String("start", "", "Inclusive backtest start timestamp")
	endRaw := flags.String("end", "", "Inclusive backtest end timestamp; defaults to now")
	if err := flags.Parse(args); err != nil {
		return err
	}

	start, startDateOnly, err := parseCLIBacktestTime(*startRaw)
	if err != nil {
		return err
	}
	end, endDateOnly, err := parseCLIBacktestTime(*endRaw)
	if err != nil {
		return err
	}
	start, end, trainStart, trainEnd, err := inferBacktestWindows(start, end, startDateOnly, endDateOnly, *dataPath == "")
	if err != nil {
		return err
	}
	log.Printf(
		"Backtest window start=%s end=%s train_start=%s train_end=%s",
		formatLogTime(start),
		formatLogTime(end),
		formatLogTime(trainStart),
		formatLogTime(trainEnd),
	)

	cfg := config.DefaultTradingConfig()
	runCfg := backtest.RunConfig{
		DataPath:           *dataPath,
		Start:              start,
		End:                end,
		TrainStart:         trainStart,
		TrainEnd:           trainEnd,
		LabelLookaheadBars: 8,
	}

	if *dataPath == "" {
		setupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		alpacaCfg, err := config.LoadBacktestAlpacaConfig(nil)
		if err != nil {
			return err
		}
		client := alpaca.NewBacktestClient(alpacaCfg)

		historicalRateLimit := 0
		if capabilities, capErr := client.DetectMarketDataCapabilities(setupCtx); capErr == nil {
			historicalRateLimit = capabilities.HistoricalRateLimitPerMin
			if alpacaCfg.AutoSelectDataFeed {
				client.SetDataFeed(capabilities.DetectedFeed)
			}
			log.Printf("Backtest using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
		} else {
			log.Printf("Backtest capability detection failed, using defaults: %v", capErr)
		}
		if account, accountErr := client.GetAccount(setupCtx); accountErr == nil {
			if equity, parseErr := strconv.ParseFloat(account.Equity, 64); parseErr == nil && equity > 0 {
				cfg = config.TuneTradingConfig(cfg, equity, 0)
			}
		} else {
			log.Printf("Backtest account tuning skipped: %v", accountErr)
		}
		logBacktestConfig(cfg)

		symbols, err := resolveBacktestSymbols(setupCtx, client)
		if err != nil {
			return err
		}
		fetchStart, fetchEnd := backtestFetchWindow(start, end, trainStart, trainEnd)
		prevDayStart := fetchStart.AddDate(0, 0, -1)
		fetchTimeout := estimateHistoricalFetchTimeout(len(symbols), prevDayStart, fetchEnd, historicalRateLimit)
		log.Printf("Historical fetch timeout set to %s", fetchTimeout)
		log.Printf("Historical fetch coverage start=%s end=%s", formatLogTime(prevDayStart), formatLogTime(fetchEnd))
		fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer fetchCancel()
		dataset, err := prepareHistoricalDataset(fetchCtx, client, symbols, prevDayStart, fetchEnd, historicalRateLimit)
		if err != nil {
			return err
		}
		log.Printf("Historical dataset ready shards=%d symbols=%d", len(dataset.jobs), len(symbols))

		// Stream bars from cache via merge-sort iterator into RunConfig.Bars
		iter := newHistoricalDatasetIterator(dataset)
		defer iter.Close()
		var inputBars []backtest.InputBar
		for {
			bar, ok, iterErr := iter.Next()
			if iterErr != nil {
				return iterErr
			}
			if !ok {
				break
			}
			inputBars = append(inputBars, bar)
		}
		runCfg.Bars = inputBars
		log.Printf("Loaded %d historical bars from Alpaca across %d symbols", len(inputBars), uniqueInputSymbols(inputBars))
	}

	// Convert InputBars or CSV data to domain.Tick for the existing engine
	var bars []domain.Tick
	if runCfg.DataPath != "" {
		bars, err = backtest.LoadCSV(runCfg.DataPath, runCfg.Start, runCfg.End)
		if err != nil {
			return fmt.Errorf("load csv: %w", err)
		}
	} else {
		bars = inputBarsToDomainTicks(runCfg.Bars)
	}

	engine := backtest.NewEngine(cfg)
	result := engine.Run(bars)

	output, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(output))
	return nil
}

func inputBarsToDomainTicks(input []backtest.InputBar) []domain.Tick {
	ticks := make([]domain.Tick, 0, len(input))
	for _, bar := range input {
		tick := domain.Tick{
			Symbol:      strings.ToUpper(bar.Symbol),
			Price:       bar.Close,
			BarOpen:     bar.Open,
			BarHigh:     bar.High,
			BarLow:      bar.Low,
			Open:        bar.Open,
			HighOfDay:   bar.High,
			Volume:      bar.Volume,
			Timestamp:   bar.Timestamp,
			Catalyst:    bar.Catalyst,
			CatalystURL: bar.CatalystURL,
		}
		if bar.PrevClose > 0 {
			tick.GapPercent = (bar.Open - bar.PrevClose) / bar.PrevClose * 100
		}
		ticks = append(ticks, tick)
	}
	return ticks
}

func inferBacktestWindows(start, end time.Time, startDateOnly, endDateOnly, requireStart bool) (time.Time, time.Time, time.Time, time.Time, error) {
	now := time.Now().UTC()
	if end.IsZero() {
		end = now
	}
	if requireStart && start.IsZero() {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("start time is required when loading historical data from Alpaca")
	}
	if start.IsZero() {
		return time.Time{}, end, time.Time{}, time.Time{}, nil
	}
	if endDateOnly {
		if sameMarketDay(end, now) {
			end = now
		} else {
			end = endOfMarketDay(end)
		}
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("end time must be after start time")
	}
	duration := end.Sub(start)
	minTrainingDuration := 5 * 24 * time.Hour
	if duration < minTrainingDuration {
		duration = minTrainingDuration
	}
	trainEnd := start.Add(-time.Minute)
	trainStart := trainEnd.Add(-duration)
	return start, end, trainStart, trainEnd, nil
}

func resolveBacktestSymbols(ctx context.Context, client *alpaca.BacktestClient) ([]string, error) {
	symbols, err := client.ListEquitySymbols(ctx, true)
	if err != nil {
		return nil, err
	}
	return symbols, nil
}

func backtestFetchWindow(start, end, trainStart, trainEnd time.Time) (time.Time, time.Time) {
	fetchStart := earliestNonZero(start, trainStart)
	fetchEnd := latestNonZero(end, trainEnd)
	if !fetchStart.IsZero() {
		fetchStart = fetchStart.Add(-24 * time.Hour)
	}
	return fetchStart, fetchEnd
}

func earliestNonZero(values ...time.Time) time.Time {
	var earliest time.Time
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		if earliest.IsZero() || value.Before(earliest) {
			earliest = value
		}
	}
	return earliest
}

func latestNonZero(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		if latest.IsZero() || value.After(latest) {
			latest = value
		}
	}
	return latest
}

func uniqueInputSymbols(input []backtest.InputBar) int {
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		seen[strings.ToUpper(item.Symbol)] = struct{}{}
	}
	return len(seen)
}

func parseCLIBacktestTime(value string) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	if dateOnlyPattern.MatchString(strings.TrimSpace(value)) {
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), marketTimeLocation())
		if err != nil {
			return time.Time{}, true, fmt.Errorf("unsupported date format %q", value)
		}
		return parsed.UTC(), true, nil
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, value, marketTimeLocation())
		if err == nil {
			return parsed.UTC(), false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unsupported date format %q", value)
}

func marketTimeLocation() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return location
}

func endOfMarketDay(value time.Time) time.Time {
	local := value.In(marketTimeLocation())
	return time.Date(local.Year(), local.Month(), local.Day(), 23, 59, 59, 0, marketTimeLocation()).UTC()
}

func sameMarketDay(a, b time.Time) bool {
	al := a.In(marketTimeLocation())
	bl := b.In(marketTimeLocation())
	return al.Year() == bl.Year() && al.Month() == bl.Month() && al.Day() == bl.Day()
}

func formatLogTime(value time.Time) string {
	if value.IsZero() {
		return "n/a"
	}
	return value.In(marketTimeLocation()).Format(time.RFC3339)
}

func logBacktestConfig(cfg config.TradingConfig) {
	log.Printf(
		"Backtest config min_price=%.2f min_gap=%.2f min_rel_volume=%.2f min_premarket=%d min_score=%.2f min_1m=%.2f min_3m=%.2f min_volume_rate=%.2f max_vs_open=%.2f risk_per_trade=%.4f max_trades=%d max_open=%d max_exposure=%.2f stop_loss=%.2f",
		cfg.MinPrice,
		cfg.MinGapPercent,
		cfg.MinRelativeVolume,
		cfg.MinPremarketVolume,
		cfg.MinEntryScore,
		cfg.MinOneMinuteReturnPct,
		cfg.MinThreeMinuteReturnPct,
		cfg.MinVolumeRate,
		cfg.MaxPriceVsOpenPct,
		cfg.RiskPerTradePct,
		cfg.MaxTradesPerDay,
		cfg.MaxOpenPositions,
		cfg.MaxExposurePct,
		cfg.StopLossPct,
	)
}

func logBacktestDiagnostics(diag backtest.Diagnostics) {
	log.Printf(
		"Backtest funnel bars_loaded=%d bars_in_window=%d entry_candidates=%d entry_signals=%d exit_checks=%d exit_signals=%d",
		diag.BarsLoaded,
		diag.BarsInWindow,
		diag.EntryCandidates,
		diag.EntrySignals,
		diag.ExitChecks,
		diag.ExitSignals,
	)
	logReasonCounts("scanner rejects", diag.ScannerRejects, diag.BarsInWindow)
	logReasonCounts("strategy entry rejects", diag.EntryRejects, diag.EntryCandidates)
}

func logReasonCounts(label string, counts map[string]int, total int) {
	if len(counts) == 0 {
		return
	}
	type reasonCount struct {
		reason string
		count  int
	}
	reasons := make([]reasonCount, 0, len(counts))
	for reason, count := range counts {
		reasons = append(reasons, reasonCount{reason: reason, count: count})
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].count == reasons[j].count {
			return reasons[i].reason < reasons[j].reason
		}
		return reasons[i].count > reasons[j].count
	})
	limit := len(reasons)
	if limit > 5 {
		limit = 5
	}
	parts := make([]string, 0, limit)
	for _, item := range reasons[:limit] {
		share := 0.0
		if total > 0 {
			share = (float64(item.count) / float64(total)) * 100
		}
		parts = append(parts, fmt.Sprintf("%s=%d(%.2f%%)", item.reason, item.count, share))
	}
	log.Printf("Backtest %s %s", label, strings.Join(parts, " "))
}

// parseSymbolList splits a comma-separated symbol string into a clean slice.
func parseSymbolList(s string) []string {
	if s == "" {
		return nil
	}
	var symbols []string
	for _, sym := range strings.Split(s, ",") {
		sym = strings.TrimSpace(strings.ToUpper(sym))
		if sym != "" {
			symbols = append(symbols, sym)
		}
	}
	return symbols
}
