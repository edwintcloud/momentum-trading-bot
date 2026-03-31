package cmd

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/autooptimize"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

func RunAutoOptimize(args []string) error {
	fs := flag.NewFlagSet("auto-optimize", flag.ContinueOnError)
	profilePath := fs.String("profile", "profiles/default.json", "path to the active profile to update")
	schedule := fs.String("schedule", "weekly", "schedule: weekly or daily")
	outDir := fs.String("out", ".cache/optimizer", "optimizer output directory")
	minSharpe := fs.Float64("min-sharpe", 0.5, "minimum Sharpe ratio (profit factor)")
	minWinrate := fs.Float64("min-winrate", 0.30, "minimum win rate")
	maxDrawdown := fs.Float64("max-drawdown", 0.20, "maximum drawdown percentage")
	requireImprovement := fs.Bool("require-improvement", true, "require improvement over current profile")
	maxSymbols := fs.Int("max-symbols", 500, "maximum symbols for optimization (0=unlimited)")
	runNow := fs.Bool("now", false, "run optimization immediately, then continue on schedule")
	runOnce := fs.Bool("once", false, "run a single optimization and exit (no scheduling loop)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	guardrails := autooptimize.Guardrails{
		MinSharpeRatio:     *minSharpe,
		MinWinRate:         *minWinrate,
		MaxDrawdownPct:     *maxDrawdown,
		MinTradeCount:      20,
		RequireImprovement: *requireImprovement,
		ImprovementMinPct:  0.10,
	}

	maxSym := *maxSymbols

	runFn := func(ctx context.Context, asOf time.Time, artifactDir string) (optimizer.Report, error) {
		return executeOptimization(ctx, asOf, artifactDir, maxSym)
	}

	sched := &autooptimize.Scheduler{
		ProfilePath:  *profilePath,
		OptimizerDir: *outDir,
		Schedule:     *schedule,
		Guardrails:   guardrails,
		Notifier:     autooptimize.NewNotifier(),
		RunOptimizer: runFn,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("auto-optimize: shutdown signal received")
		cancel()
	}()

	if *runOnce {
		return sched.RunOnce(ctx)
	}
	if *runNow {
		if err := sched.RunOnce(ctx); err != nil {
			log.Printf("auto-optimize: initial run failed: %v", err)
			// Continue to schedule loop anyway
		}
	}
	return sched.Run(ctx)
}

// executeOptimization runs the data fetch + optimizer, reusing the same logic
// as runOptimize but with a 3-month lookback from asOf.
func executeOptimization(ctx context.Context, asOf time.Time, outDir string, maxSymbols int) (optimizer.Report, error) {
	lookbackStart := asOf.AddDate(0, -3, 0)
	log.Printf("auto-optimize: lookback %s to %s", lookbackStart.Format("2006-01-02"), asOf.Format("2006-01-02"))

	setupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	alpacaCfg, err := config.LoadBacktestAlpacaConfig(nil)
	if err != nil {
		return optimizer.Report{}, err
	}
	client := alpaca.NewClient(alpacaCfg, config.DefaultTradingConfig())

	historicalRateLimit := 0
	if capabilities, capErr := client.DetectMarketDataCapabilities(setupCtx); capErr == nil {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
		log.Printf("auto-optimize: Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
	}

	universe, err := resolveBacktestSymbols(setupCtx, client, time.Now(), configuredUniverseSymbols())
	if err != nil {
		return optimizer.Report{}, err
	}
	symbols := universe.Symbols

	if maxSymbols > 0 && len(symbols) > maxSymbols {
		screened, screenErr := client.ListMostActiveSymbols(setupCtx, maxSymbols)
		if screenErr == nil && len(screened) > 0 {
			log.Printf("auto-optimize: using top %d symbols (was %d)", len(screened), len(symbols))
			symbols = screened
		} else {
			symbols = symbols[:maxSymbols]
		}
	}

	prevDayStart := lookbackStart.AddDate(0, 0, -1)
	fetchTimeout := backtest.EstimateHistoricalFetchTimeout(len(symbols), prevDayStart, asOf, historicalRateLimit)
	log.Printf("auto-optimize: historical fetch timeout=%s", fetchTimeout)

	fetchCtx, fetchCancel := context.WithTimeout(ctx, fetchTimeout)
	defer fetchCancel()

	dataset, err := backtest.PrepareHistoricalDataset(fetchCtx, client, symbols, prevDayStart, asOf, historicalRateLimit)
	if err != nil {
		return optimizer.Report{}, err
	}

	log.Printf("auto-optimize: dataset ready shards=%d symbols=%d", len(dataset.Jobs), len(symbols))
	iterFactory := backtest.NewDatasetIteratorFactory(dataset)
	opt := optimizer.NewStreamingOptimizer(iterFactory, lookbackStart, asOf, outDir)
	autoFloatStore := alpaca.NewFloatStore()
	if _, loadErr := autoFloatStore.LoadOrFetchFloatData(context.Background()); loadErr != nil {
		log.Printf("auto-optimize: float data warning: %v", loadErr)
	}

	opt.SetFloatStore(autoFloatStore)
	return opt.Run()
}
