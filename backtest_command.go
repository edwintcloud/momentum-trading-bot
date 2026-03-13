package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
)

func runBacktest(args []string) error {
	flags := flag.NewFlagSet("backtest", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	dataPath := flags.String("data", "", "Optional CSV fallback with timestamp,symbol,open,high,low,close,volume columns")
	startRaw := flags.String("start", "", "Inclusive backtest start timestamp")
	endRaw := flags.String("end", "", "Inclusive backtest end timestamp; defaults to now")
	if err := flags.Parse(args); err != nil {
		return err
	}

	start, err := parseCLIBacktestTime(*startRaw)
	if err != nil {
		return err
	}
	end, err := parseCLIBacktestTime(*endRaw)
	if err != nil {
		return err
	}
	start, end, trainStart, trainEnd, err := inferBacktestWindows(start, end, *dataPath == "")
	if err != nil {
		return err
	}

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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		alpacaCfg, err := config.LoadBacktestAlpacaConfig(nil)
		if err != nil {
			return err
		}
		client := alpaca.NewClient(alpacaCfg)

		historicalRateLimit := 0
		if capabilities, capErr := client.DetectMarketDataCapabilities(ctx); capErr == nil {
			historicalRateLimit = capabilities.HistoricalRateLimitPerMin
			if alpacaCfg.AutoSelectDataFeed {
				client.SetDataFeed(capabilities.DetectedFeed)
			}
			log.Printf("Backtest using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
		}
		if account, accountErr := client.GetAccount(ctx); accountErr == nil {
			if equity, _, ok := brokerAccountValues(account); ok {
				cfg = config.TuneTradingConfig(cfg, equity, historicalRateLimit)
			}
		}

		symbols, err := resolveBacktestSymbols(ctx, client)
		if err != nil {
			return err
		}
		fetchStart, fetchEnd := backtestFetchWindow(start, end, trainStart, trainEnd)
		inputBars, err := fetchBarsFromAlpaca(ctx, client, symbols, fetchStart, fetchEnd)
		if err != nil {
			return err
		}
		runCfg.Bars = inputBars
		log.Printf("Loaded %d historical bars from Alpaca across %d symbols", len(inputBars), uniqueInputSymbols(inputBars))
	}

	result, err := backtest.Run(context.Background(), cfg, runCfg)
	if err != nil {
		return err
	}

	log.Printf(
		"Backtest complete trades=%d wins=%d losses=%d win_rate=%.2f%% net_pnl=%.2f ending_equity=%.2f max_drawdown=%.2f%% model=%s",
		result.Trades,
		result.Wins,
		result.Losses,
		result.WinRate,
		result.NetPnL,
		result.EndingEquity,
		result.MaxDrawdownPct,
		result.ModelName,
	)
	return nil
}

func inferBacktestWindows(start, end time.Time, requireStart bool) (time.Time, time.Time, time.Time, time.Time, error) {
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if requireStart && start.IsZero() {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("start time is required when loading historical data from Alpaca")
	}
	if start.IsZero() {
		return time.Time{}, end, time.Time{}, time.Time{}, nil
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("end time must be after start time")
	}
	duration := end.Sub(start)
	trainEnd := start.Add(-time.Minute)
	trainStart := trainEnd.Add(-duration)
	return start, end, trainStart, trainEnd, nil
}

func resolveBacktestSymbols(ctx context.Context, client *alpaca.Client) ([]string, error) {
	symbols, err := client.ListActiveEquitySymbols(ctx)
	if err != nil {
		return nil, err
	}
	return symbols, nil
}

func fetchBarsFromAlpaca(ctx context.Context, client *alpaca.Client, symbols []string, start, end time.Time) ([]backtest.InputBar, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("no symbols available for historical fetch")
	}

	const batchSize = 100
	inputBars := make([]backtest.InputBar, 0)
	for batchStart := 0; batchStart < len(symbols); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(symbols) {
			batchEnd = len(symbols)
		}
		barMap, err := client.GetHistoricalBars(ctx, symbols[batchStart:batchEnd], start, end, "1Min")
		if err != nil {
			return nil, err
		}
		for symbol, bars := range barMap {
			for _, item := range bars {
				inputBars = append(inputBars, backtest.InputBar{
					Timestamp: item.Timestamp.UTC(),
					Symbol:    symbol,
					Open:      item.Open,
					High:      item.High,
					Low:       item.Low,
					Close:     item.Close,
					Volume:    item.Volume,
				})
			}
		}
	}

	sort.Slice(inputBars, func(i, j int) bool {
		if inputBars[i].Timestamp.Equal(inputBars[j].Timestamp) {
			return inputBars[i].Symbol < inputBars[j].Symbol
		}
		return inputBars[i].Timestamp.Before(inputBars[j].Timestamp)
	})
	return inputBars, nil
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

func parseCLIBacktestTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date format %q", value)
}
