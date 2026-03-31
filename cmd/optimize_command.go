package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

func RunOptimize(args []string) error {
	fs := flag.NewFlagSet("optimize", flag.ContinueOnError)
	asOfStr := fs.String("as-of", "", "as-of date (YYYY-MM-DD)")
	startStr := fs.String("start", "", "explicit lookback start date (YYYY-MM-DD); defaults to as-of minus 3 months")
	outDir := fs.String("out", ".cache/optimizer", "output directory")
	maxSymbols := fs.Int("max-symbols", 500, "maximum symbols for optimization (0=unlimited)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *asOfStr == "" {
		return fmt.Errorf("-as-of is required")
	}

	asOf, err := time.ParseInLocation("2006-01-02", *asOfStr, markethours.Location())
	if err != nil {
		return fmt.Errorf("invalid as-of date: %v", err)
	}

	lookbackStart := asOf.AddDate(0, -3, 0)
	if *startStr != "" {
		lookbackStart, err = time.ParseInLocation("2006-01-02", *startStr, markethours.Location())
		if err != nil {
			return fmt.Errorf("invalid start date: %v", err)
		}
	}
	log.Printf("Optimize lookback: %s to %s", lookbackStart.Format("2006-01-02"), asOf.Format("2006-01-02"))

	setupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	alpacaCfg, err := config.LoadBacktestAlpacaConfig(nil)
	if err != nil {
		return err
	}
	client := alpaca.NewClient(alpacaCfg, config.DefaultTradingConfig())

	historicalRateLimit := 0
	if capabilities, capErr := client.DetectMarketDataCapabilities(setupCtx); capErr == nil {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
		log.Printf("Optimize using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
	} else {
		log.Printf("Optimize capability detection failed, using defaults: %v", capErr)
	}

	universe, err := resolveBacktestSymbols(setupCtx, client, time.Now(), configuredUniverseSymbols())
	if err != nil {
		return err
	}
	symbols := universe.Symbols

	if *maxSymbols > 0 && len(symbols) > *maxSymbols {
		screened, screenErr := client.ListMostActiveSymbols(setupCtx, *maxSymbols)
		if screenErr == nil && len(screened) > 0 {
			log.Printf("Optimize using top %d symbols (screener=true, was %d)", len(screened), len(symbols))
			symbols = screened
		} else {
			if screenErr != nil {
				log.Printf("Optimize screener unavailable: %v; truncating to %d symbols", screenErr, *maxSymbols)
			} else {
				log.Printf("Optimize screener returned empty; truncating to %d symbols", *maxSymbols)
			}
			symbols = symbols[:*maxSymbols]
		}
	}

	prevDayStart := lookbackStart.AddDate(0, 0, -1)
	fetchTimeout := backtest.EstimateHistoricalFetchTimeout(len(symbols), prevDayStart, asOf, historicalRateLimit)
	log.Printf("Optimize historical fetch timeout=%s coverage start=%s end=%s", fetchTimeout, formatLogTime(prevDayStart), formatLogTime(asOf))

	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer fetchCancel()

	dataset, err := backtest.PrepareHistoricalDataset(fetchCtx, client, symbols, prevDayStart, asOf, historicalRateLimit)
	if err != nil {
		return err
	}

	log.Printf("Optimize historical dataset ready shards=%d symbols=%d (streaming mode)", len(dataset.Jobs), len(symbols))
	iterFactory := backtest.NewDatasetIteratorFactory(dataset)
	opt := optimizer.NewStreamingOptimizer(iterFactory, lookbackStart, asOf, *outDir)
	streamFloatStore := alpaca.NewFloatStore()
	if _, loadErr := streamFloatStore.LoadOrFetchFloatData(context.Background()); loadErr != nil {
		log.Printf("Optimize float data warning: %v", loadErr)
	}
	opt.SetFloatStore(streamFloatStore)
	report, err := opt.Run()
	if err != nil {
		return err
	}
	output, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(output))

	return nil
}
