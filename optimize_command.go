package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
	"github.com/edwincloud/momentum-trading-bot/internal/optimizer"
)

func runOptimize(args []string) error {
	flags := flag.NewFlagSet("optimize", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	asOfRaw := flags.String("as-of", "", "Optimization cutoff date/time; uses completed weeks through the prior Friday close")
	startRaw := flags.String("start", "", "Optional custom optimization start timestamp")
	endRaw := flags.String("end", "", "Optional custom optimization end timestamp")
	dataPath := flags.String("data", "", "Optional CSV bars file for offline optimization")
	artifactDir := flags.String("out", optimizer.DefaultArtifactDir, "Directory for optimizer reports and candidate profiles")
	if err := flags.Parse(args); err != nil {
		return err
	}

	customWindowMode := strings.TrimSpace(*startRaw) != "" || strings.TrimSpace(*endRaw) != ""
	if customWindowMode && strings.TrimSpace(*startRaw) == "" {
		return fmt.Errorf("start is required when using a custom optimization window")
	}
	if !customWindowMode && *asOfRaw == "" {
		return fmt.Errorf("as-of is required unless start/end are provided")
	}

	asOf := time.Time{}
	start := time.Time{}
	end := time.Time{}
	searchWeeks := []optimizer.WeeklyWindow(nil)
	validationWeeks := []optimizer.WeeklyWindow(nil)
	if customWindowMode {
		var err error
		start, _, err = parseCLIBacktestTime(*startRaw)
		if err != nil {
			return err
		}
		var endDateOnly bool
		end, endDateOnly, err = parseCLIBacktestTime(*endRaw)
		if err != nil {
			return err
		}
		start, end, err = inferBacktestWindows(start, end, endDateOnly, true)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("%s..%s", start.In(markethours.Location()).Format("2006-01-02"), end.In(markethours.Location()).Format("2006-01-02"))
		window := optimizer.WeeklyWindow{
			Label: label,
			Start: start,
			End:   end,
		}
		searchWeeks = []optimizer.WeeklyWindow{window}
		validationWeeks = []optimizer.WeeklyWindow{window}
		asOf = end
	} else {
		parsedAsOf, dateOnly, err := parseCLIBacktestTime(*asOfRaw)
		if err != nil {
			return err
		}
		if dateOnly {
			parsedAsOf = endOfMarketDay(parsedAsOf)
		}
		asOf = parsedAsOf
		completedWeekEnd := optimizer.PriorCompletedWeekEnd(asOf)
		weeks := optimizer.BuildWeeklyWindows(completedWeekEnd, 20)
		if len(weeks) == 0 {
			return fmt.Errorf("unable to derive completed weekly windows")
		}
		start = weeks[0].Start
		end = weeks[len(weeks)-1].End
	}

	cfg := config.NormalizeStrategyProfile(config.DefaultTradingConfig())
	var loadWeek func(context.Context, optimizer.WeeklyWindow) ([]backtest.InputBar, error)
	if *dataPath != "" {
		loadWeek = func(_ context.Context, window optimizer.WeeklyWindow) ([]backtest.InputBar, error) {
			return backtest.LoadInputBars(*dataPath, window.Start, window.End)
		}
		log.Printf("Optimizer using incremental CSV week loader data=%s", *dataPath)
	} else {
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
			log.Printf("Optimizer using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
		} else {
			log.Printf("Optimizer capability detection failed, using defaults: %v", capErr)
		}
		if account, accountErr := client.GetAccount(setupCtx); accountErr == nil {
			if cash, ok := brokerCashValue(account); ok {
				cfg = config.TuneTradingConfig(cfg, cash, historicalRateLimit)
			} else if equity, _, ok := brokerAccountValues(account); ok {
				cfg = config.TuneTradingConfig(cfg, equity, historicalRateLimit)
			}
		} else {
			log.Printf("Optimizer account tuning skipped: %v", accountErr)
		}

		symbols, err := resolveBacktestSymbols(setupCtx, client)
		if err != nil {
			return err
		}
		fetchTimeout := estimateHistoricalFetchTimeout(len(symbols), start, end, historicalRateLimit)
		log.Printf("Optimizer historical fetch timeout set to %s", fetchTimeout)
		fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer fetchCancel()
		dataset, err := prepareHistoricalDataset(fetchCtx, client, symbols, start, end, historicalRateLimit)
		if err != nil {
			return err
		}
		log.Printf("Optimizer historical dataset ready shards=%d symbols=%d", len(dataset.jobs), len(symbols))
		loadWeek = func(_ context.Context, window optimizer.WeeklyWindow) ([]backtest.InputBar, error) {
			return loadHistoricalBarsForOptimizerWeek(dataset, window)
		}
	}

	if customWindowMode {
		log.Printf("Optimizer custom window start=%s end=%s", formatLogTime(start), formatLogTime(end))
	} else {
		log.Printf("Optimizer window start=%s end=%s completed_week_end=%s", formatLogTime(start), formatLogTime(end), formatLogTime(optimizer.PriorCompletedWeekEnd(asOf)))
	}
	report, profile, err := optimizer.Run(context.Background(), optimizer.Params{
		BaseConfig:      cfg,
		LoadWeek:        loadWeek,
		AsOf:            asOf,
		ArtifactDir:     *artifactDir,
		SearchWeeks:     searchWeeks,
		ValidationWeeks: validationWeeks,
	})
	if err != nil {
		return err
	}

	log.Printf("Optimizer candidates=%d finalists=%d artifact=%s", report.Run.CoarseCandidates+report.Run.RefinedCandidates, report.Run.Finalists, report.ArtifactPath)
	if report.Winner == nil || profile == nil {
		log.Printf("Optimizer completed with no ranked candidate")
		return nil
	}
	log.Printf(
		"Optimizer winner profile=%s version=%s promotable=%t status=%s holdout_median=%.2f%% positive_weeks=%.2f%% holdout_p25=%.2f%% profit_factor=%.2f max_drawdown=%.2f%% holdout_long_trades=%d holdout_short_trades=%d holdout_long_pnl=%.2f holdout_short_pnl=%.2f profile_path=%s",
		profile.Name,
		profile.Version,
		report.Winner.Promotable,
		profile.Promotion.Status,
		report.Winner.Score.HoldoutMedianWeeklyReturnPct,
		report.Winner.Score.PositiveWeeksPct,
		report.Winner.Score.HoldoutP25WeeklyReturnPct,
		report.Winner.Score.ProfitFactor,
		report.Winner.Score.MaxDrawdownPct,
		report.Winner.HoldoutSummary.LongTrades,
		report.Winner.HoldoutSummary.ShortTrades,
		report.Winner.HoldoutSummary.LongPnL,
		report.Winner.HoldoutSummary.ShortPnL,
		report.ProfilePath,
	)
	return nil
}

func loadHistoricalBarsForOptimizerWeek(dataset historicalDataset, window optimizer.WeeklyWindow) ([]backtest.InputBar, error) {
	jobs := historicalJobsForWindow(dataset.jobs, window.Start, window.End)
	if len(jobs) == 0 {
		return nil, nil
	}
	iterator := newHistoricalDatasetIterator(historicalDataset{
		feed: dataset.feed,
		jobs: jobs,
	})
	defer iterator.Close()

	bars := make([]backtest.InputBar, 0, 1024)
	for {
		bar, ok, err := iterator.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		bars = append(bars, bar)
	}
	return bars, nil
}

func historicalJobsForWindow(jobs []historicalFetchJob, start, end time.Time) []historicalFetchJob {
	if len(jobs) == 0 {
		return nil
	}
	out := make([]historicalFetchJob, 0, len(jobs))
	for _, job := range jobs {
		if job.end.Before(start) || job.start.After(end) {
			continue
		}
		out = append(out, job)
	}
	return out
}
