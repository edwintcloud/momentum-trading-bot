package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
)

func runExport(args []string) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

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

	// We use inferBacktestWindows just like the regular backtest to get baseline properties
	start, end, _, _, err = inferBacktestWindows(start, end, startDateOnly, endDateOnly, true)
	if err != nil {
		return err
	}

	log.Printf("Export window start=%s end=%s", formatLogTime(start), formatLogTime(end))

	symbolsFile, err := os.Create("export_symbols.csv")
	if err != nil {
		return fmt.Errorf("failed to create symbols csv: %w", err)
	}
	defer symbolsFile.Close()

	summaryFile, err := os.Create("export_summary.csv")
	if err != nil {
		return fmt.Errorf("failed to create summary csv: %w", err)
	}
	defer summaryFile.Close()

	symbolsWriter := csv.NewWriter(symbolsFile)
	defer symbolsWriter.Flush()

	summaryWriter := csv.NewWriter(summaryFile)
	defer summaryWriter.Flush()

	// Write Headers
	if err := symbolsWriter.Write([]string{
		"Symbol", "Quantity", "EntryPrice", "ExitPrice", "PnL", "RMultiple", "OpenedAt", "ClosedAt", "ExitReason",
	}); err != nil {
		return err
	}
	symbolsLineCount := 1 // Account for header row (1-indexed for row references)

	if err := summaryWriter.Write([]string{
		"StartDate", "EndDate", "NetProfit", "NetProfitPct", "SymbolsStartLine", "SymbolsEndLine",
	}); err != nil {
		return err
	}

	setupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	alpacaCfg, err := config.LoadBacktestAlpacaConfig(nil)
	if err != nil {
		return err
	}
	client := alpaca.NewClient(alpacaCfg)
	historicalRateLimit := 0
	if capabilities, capErr := client.DetectMarketDataCapabilities(setupCtx); capErr == nil {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
		if alpacaCfg.AutoSelectDataFeed {
			client.SetDataFeed(capabilities.DetectedFeed)
		}
	}

	symbols, err := resolveBacktestSymbols(setupCtx, client)
	if err != nil {
		return err
	}

	currentStart := start
	sevenDays := 7 * 24 * time.Hour

	for currentStart.Before(end) {
		currentEnd := currentStart.Add(sevenDays)
		if currentEnd.After(end) {
			currentEnd = end
		}

		log.Printf("Running chunk start=%s end=%s", formatLogTime(currentStart), formatLogTime(currentEnd))

		_, _, trainStart, trainEnd, err := inferBacktestWindows(currentStart, currentEnd, startDateOnly, endDateOnly, true)
		if err != nil {
			return err
		}

		cfg := config.DefaultTradingConfig()
		if account, accountErr := client.GetAccount(setupCtx); accountErr == nil {
			if equity, _, ok := brokerAccountValues(account); ok {
				cfg = config.TuneTradingConfig(cfg, equity, historicalRateLimit)
			}
		}

		runCfg := backtest.RunConfig{
			Start:              currentStart,
			End:                currentEnd,
			TrainStart:         trainStart,
			TrainEnd:           trainEnd,
			LabelLookaheadBars: 8,
		}

		fetchStart, fetchEnd := backtestFetchWindow(currentStart, currentEnd, trainStart, trainEnd)
		fetchTimeout := estimateHistoricalFetchTimeout(len(symbols), fetchStart, fetchEnd, historicalRateLimit)
		fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
		inputBars, err := fetchBarsFromAlpaca(fetchCtx, client, symbols, fetchStart, fetchEnd, historicalRateLimit)
		fetchCancel()
		
		if err != nil {
			log.Printf("failed to fetch bars for chunk %s to %s: %v", currentStart, currentEnd, err)
			currentStart = currentEnd
			continue
		}
		runCfg.Bars = inputBars

		result, err := backtest.Run(context.Background(), cfg, runCfg)
		if err != nil {
			log.Printf("failed to run backtest for chunk %s to %s: %v", currentStart, currentEnd, err)
			currentStart = currentEnd
			continue
		}

		startLine := symbolsLineCount + 1
		for _, trade := range result.ClosedTrades {
			err := symbolsWriter.Write([]string{
				trade.Symbol,
				strconv.FormatInt(trade.Quantity, 10),
				fmt.Sprintf("%.2f", trade.EntryPrice),
				fmt.Sprintf("%.2f", trade.ExitPrice),
				fmt.Sprintf("%.2f", trade.PnL),
				fmt.Sprintf("%.2f", trade.RMultiple),
				trade.OpenedAt.In(marketTimeLocation()).Format("2006-01-02 15:04"),
				trade.ClosedAt.In(marketTimeLocation()).Format("2006-01-02 15:04"),
				trade.ExitReason,
			})
			if err != nil {
				return err
			}
			symbolsLineCount++
		}
		endLine := symbolsLineCount
		
		// If there were no trades, we shouldn't reference lines that don't belong to this week
		if len(result.ClosedTrades) == 0 {
			startLine = -1
			endLine = -1
		}

		netProfitPct := 0.0
		if cfg.StartingCapital > 0 {
			netProfitPct = (result.NetPnL / cfg.StartingCapital) * 100
		}

		err = summaryWriter.Write([]string{
			currentStart.In(marketTimeLocation()).Format("2006-01-02"),
			currentEnd.In(marketTimeLocation()).Format("2006-01-02"),
			fmt.Sprintf("%.2f", result.NetPnL),
			fmt.Sprintf("%.2f%%", netProfitPct),
			strconv.Itoa(startLine),
			strconv.Itoa(endLine),
		})
		if err != nil {
			return err
		}

		// Flush periodically during the loop
		symbolsWriter.Flush()
		summaryWriter.Flush()

		currentStart = currentEnd
	}

	log.Println("Export complete.")
	return nil
}
