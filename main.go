package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/api"
	"github.com/edwintcloud/momentum-trading-bot/internal/autooptimize"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/market"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
	"github.com/edwintcloud/momentum-trading-bot/internal/pipeline"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/regime"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/storage"
	"github.com/edwintcloud/momentum-trading-bot/internal/strategy"
	"github.com/edwintcloud/momentum-trading-bot/internal/telemetry"
)

func main() {
	_ = godotenv.Load()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backtest":
			if err := runBacktest(os.Args[2:]); err != nil {
				log.Fatalf("backtest: %v", err)
			}
			return
		case "optimize":
			if err := runOptimize(os.Args[2:]); err != nil {
				log.Fatalf("optimize: %v", err)
			}
			return
		case "auto-optimize":
			if err := runAutoOptimize(os.Args[2:]); err != nil {
				log.Fatalf("auto-optimize: %v", err)
			}
			return
		case "live":
			runLive()
			return
		}
	}

	runLive()
}

func runLive() {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Load trading config
	tradingCfg := config.DefaultTradingConfig()
	if appCfg.TradingProfilePath != "" {
		profile, err := config.LoadTradingProfile(appCfg.TradingProfilePath)
		if err != nil {
			log.Fatalf("load trading profile: %v", err)
		}
		tradingCfg = profile.Config
		tradingCfg.StrategyProfileName = string(profile.Name)
		tradingCfg.StrategyProfileVersion = profile.Version
		log.Printf("config: loaded trading profile %s version %s", profile.Name, profile.Version)
	}

	// Connect to storage
	var eventRecorder domain.EventRecorder
	if appCfg.DatabaseURL != "" {
		store, err := storage.NewPostgresStore(appCfg.DatabaseURL)
		if err != nil {
			log.Fatalf("storage: %v", err)
		}
		defer store.Close()
		eventRecorder = store
	} else {
		eventRecorder = storage.NewFilesystemStore(".data")
		log.Println("storage: using filesystem fallback (no DATABASE_URL)")
	}

	logger := telemetry.NewLogger(eventRecorder)
	runtimeState := runtime.NewState(logger)

	// Connect to Alpaca
	alpacaClient := alpaca.NewClient(appCfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Probe broker account
	acct, err := alpacaClient.GetAccount(ctx)
	if err != nil {
		log.Fatalf("alpaca: %v", err)
	}
	log.Printf("alpaca: equity=%.2f buying_power=%.2f status=%s", acct.Equity, acct.BuyingPower, acct.Status)
	tradingCfg = config.TuneTradingConfig(tradingCfg, acct.Equity, acct.DayPnL)
	runtimeState.SetDependencyStatus("alpaca", true)
	runtimeState.SetDependencyStatus("storage", true)

	// Initialize float store for momentum filtering
	floatStore := alpaca.NewFloatStore()
	if _, err := floatStore.LoadOrFetchFloatData(ctx); err != nil {
		log.Printf("float-store: SEC EDGAR fetch warning: %v", err)
	}
	log.Printf("float-store: %d symbols with float data", floatStore.Len())

	// Initialize pipeline components
	portfolioMgr := portfolio.NewManager(tradingCfg, logger)
	portfolioMgr.SetBrokerEquity(acct.Equity)
	volEstimator := risk.NewVolatilityEstimator(tradingCfg.DefaultVolatility, tradingCfg.MaxVolEstimate)

	// Seed broker positions
	brokerPositions, err := alpacaClient.GetPositions(ctx)
	if err != nil {
		log.Printf("alpaca: position seed warning: %v", err)
	} else {
		for _, bp := range brokerPositions {
			var qty int64
			fmt.Sscanf(bp.Qty, "%d", &qty)
			var avgPrice, currentPrice, marketValue, unrealizedPL float64
			fmt.Sscanf(bp.AvgEntryPrice, "%f", &avgPrice)
			fmt.Sscanf(bp.CurrentPrice, "%f", &currentPrice)
			fmt.Sscanf(bp.MarketValue, "%f", &marketValue)
			fmt.Sscanf(bp.UnrealizedPL, "%f", &unrealizedPL)

			side := domain.DirectionLong
			if bp.Side == "short" {
				side = domain.DirectionShort
			}

			portfolioMgr.SeedBrokerPosition(domain.Position{
				Symbol:        bp.Symbol,
				Side:          side,
				Quantity:      qty,
				AvgPrice:      avgPrice,
				LastPrice:     currentPrice,
				HighestPrice:  currentPrice,
				LowestPrice:   currentPrice,
				MarketValue:   marketValue,
				UnrealizedPnL: unrealizedPL,
				OpenedAt:      time.Now(),
				UpdatedAt:     time.Now(),
			})
			log.Printf("alpaca: seeded position %s %s qty=%d avg=%.2f", bp.Symbol, side, qty, avgPrice)
		}
	}

	// Compute defensive stops for broker-seeded positions (snapshot-enhanced pass runs later)
	computeSeededPositionStops(portfolioMgr, tradingCfg, nil)

	// Restore today's closed trades from storage
	if loader, ok := eventRecorder.(domain.TradeLoader); ok {
		trades, err := loader.LoadTodayClosedTrades()
		if err != nil {
			log.Printf("trades: failed to load today's closed trades: %v", err)
		} else if len(trades) > 0 {
			portfolioMgr.SeedClosedTrades(trades)
			log.Printf("trades: restored %d closed trades from storage (day PnL: %.2f)", len(trades), portfolioMgr.RealizedPnL())
		}
	}

	// Pipeline channels
	scannerInst := scanner.NewScanner(tradingCfg, runtimeState)
	riskEngine := risk.NewEngine(tradingCfg, portfolioMgr, runtimeState, alpacaClient)
	strategyInst := strategy.NewStrategy(tradingCfg, portfolioMgr, runtimeState, riskEngine, volEstimator)
	regimeTracker := regime.NewTracker(tradingCfg, runtimeState)
	normalizer := market.NewNormalizer()

	pipe := pipeline.New(pipeline.Config{
		TradingCfg:    tradingCfg,
		Runtime:       runtimeState,
		Portfolio:     portfolioMgr,
		Normalizer:    normalizer,
		Scanner:       scannerInst,
		Strategy:      strategyInst,
		RiskEngine:    riskEngine,
		VolEstimator:  volEstimator,
		Broker:        alpacaClient,
		Recorder:      logger,
		RegimeTracker: regimeTracker,
		FloatLookup:   floatStore.Get,
	})
	pipe.Start(ctx)

	runtimeState.SetReady(true)
	runtimeState.RecordLog("info", "system", "momentum trading bot started")

	// Start API server
	configUpdaters := []api.ConfigUpdater{scannerInst, strategyInst, riskEngine}
	apiServer := api.NewServer(portfolioMgr, runtimeState, pipe.CloseAllCh(), appCfg, tradingCfg, optimizer.DefaultArtifactDir, eventRecorder)
	for _, u := range configUpdaters {
		apiServer.RegisterConfigUpdater(u)
	}
	// Wire price-refresh callback: fetches broker positions and updates portfolio
	// prices so close-all orders use current market prices, not stale cached values.
	apiServer.SetPriceRefresher(func() {
		bPositions, err := alpacaClient.GetPositions(ctx)
		if err != nil {
			log.Printf("price-refresh: failed to fetch broker positions: %v", err)
			return
		}
		for _, bp := range bPositions {
			var currentPrice float64
			fmt.Sscanf(bp.CurrentPrice, "%f", &currentPrice)
			if currentPrice > 0 {
				portfolioMgr.UpdatePrice(bp.Symbol, currentPrice)
			}
		}
	})
	go func() {
		if err := apiServer.Start(ctx, appCfg.ListenAddr); err != nil {
			log.Fatalf("api: %v", err)
		}
	}()

	// Start profile watcher for hot-reload
	if appCfg.TradingProfilePath != "" {
		done := make(chan struct{})
		go func() {
			<-ctx.Done()
			close(done)
		}()
		watcher := config.NewProfileWatcher(appCfg.TradingProfilePath, 10*time.Second, func(cfg config.TradingConfig) {
			for _, u := range configUpdaters {
				u.UpdateConfig(cfg)
			}
			runtimeState.RecordLog("info", "config", "hot-reloaded trading profile")
		})
		go watcher.Start(done)
	}

	// Start market data streaming
	streamCfg := alpaca.StreamConfig{
		APIKey:    appCfg.AlpacaAPIKey,
		APISecret: appCfg.AlpacaAPISecret,
		Feed:      "sip",
	}
	stream := alpaca.NewStream(streamCfg, 4096)

	barCh, err := stream.Start(ctx)
	if err != nil {
		log.Fatalf("stream: %v", err)
	}
	defer stream.Close()

	// Resolve symbols and subscribe
	symbols, blockedSymbols, err := resolveStreamSymbols(ctx, alpacaClient, appCfg)
	if err != nil {
		log.Fatalf("symbols: %v", err)
	}
	scannerInst.SetBlockedSymbols(blockedSymbols)
	log.Printf("stream: subscribing to %d symbols", len(symbols))
	if err := stream.Subscribe(ctx, symbols); err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	// Seed normalizer with historical context from Alpaca snapshots.
	// Without this, previousClose=0 and prevDayVolume=0 on fresh start,
	// causing GapPercent=0 and RelativeVolume=1.0 — all scanner filters fail.
	log.Println("normalizer: fetching snapshots for historical context...")
	seedCtx, seedCancel := context.WithTimeout(ctx, 2*time.Minute)
	snapshots, snapshotErr := alpacaClient.GetSnapshots(seedCtx, symbols)
	seedCancel()
	if snapshotErr != nil {
		log.Printf("normalizer: snapshot seed warning (continuing without historical context): %v", snapshotErr)
	} else {
		now := time.Now()
		seeded := 0
		for symbol, snap := range snapshots {
			if snap.PrevDailyBar.Close <= 0 {
				continue
			}
			preMarketVol := int64(0)
			if markethours.IsPreMarket(now) {
				// During premarket, all of today's volume is premarket volume
				preMarketVol = snap.DailyBar.Volume
			} else {
				// After open, estimate premarket volume from gap size:
				// stocks with large gaps typically had significant premarket activity
				gap := math.Abs((snap.DailyBar.Open - snap.PrevDailyBar.Close) / snap.PrevDailyBar.Close * 100)
				if gap > 3.0 {
					preMarketVol = snap.DailyBar.Volume / 10
				}
			}
			normalizer.Seed(symbol, market.SeedState{
				PreviousClose: snap.PrevDailyBar.Close,
				PrevDayVolume: snap.PrevDailyBar.Volume,
				TodayOpen:     snap.DailyBar.Open,
				TodayHigh:     snap.DailyBar.High,
				TodayVolume:   snap.DailyBar.Volume,
				PreMarketVol:  preMarketVol,
			}, now)
			seeded++
		}
		log.Printf("normalizer: seeded %d/%d symbols with historical context", seeded, len(symbols))
	}

	// Improve seeded position stops using snapshot data (if available)
	computeSeededPositionStops(portfolioMgr, tradingCfg, snapshots)

	// Feed streaming bars into the pipeline
	go func() {
		for sbar := range barCh {
			select {
			case pipe.BarCh() <- domain.Bar{
				Symbol:    sbar.Symbol,
				Timestamp: sbar.Timestamp,
				Open:      sbar.Open,
				High:      sbar.High,
				Low:       sbar.Low,
				Close:     sbar.Close,
				Volume:    sbar.Volume,
			}:
			default:
			}
		}
	}()

	// Process daily bar updates for normalizer context (session OHLCV)
	go func() {
		for dbar := range stream.DailyBars() {
			normalizer.UpdateDailyBar(dbar.Symbol, dbar.High, dbar.Volume, dbar.Open)
		}
	}()

	// Broker reconciliation loop
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runtimeState.Heartbeat("reconciliation")
				bPositions, err := alpacaClient.GetPositions(ctx)
				if err != nil {
					log.Printf("reconcile: failed to fetch broker positions: %v", err)
					continue
				}
				// Update broker equity
				bAcct, err := alpacaClient.GetAccount(ctx)
				if err == nil {
					portfolioMgr.SetBrokerEquity(bAcct.Equity)
				}
				// Update portfolio prices from broker and detect mismatches
				pmPositions := portfolioMgr.GetPositions()
				pmSymbols := make(map[string]bool)
				for _, p := range pmPositions {
					pmSymbols[p.Symbol] = true
				}
				brokerSymbols := make(map[string]bool)
				for _, bp := range bPositions {
					brokerSymbols[bp.Symbol] = true
					if !pmSymbols[bp.Symbol] {
						runtimeState.RecordLog("warn", "portfolio", fmt.Sprintf("broker has position %s not in portfolio manager", bp.Symbol))
						log.Printf("reconcile: WARNING broker has position %s not in portfolio manager", bp.Symbol)
						continue
					}
					// Update portfolio price from broker's current price so positions
					// stay fresh even when no streaming bars are received.
					var currentPrice float64
					fmt.Sscanf(bp.CurrentPrice, "%f", &currentPrice)
					if currentPrice > 0 {
						portfolioMgr.UpdatePrice(bp.Symbol, currentPrice)
						// Inject a synthetic bar so the strategy evaluates exit conditions.
						select {
						case pipe.BarCh() <- domain.Bar{
							Symbol:    bp.Symbol,
							Open:      currentPrice,
							High:      currentPrice,
							Low:       currentPrice,
							Close:     currentPrice,
							Volume:    0,
							Timestamp: time.Now(),
						}:
						default:
						}
					}
				}
				for _, p := range pmPositions {
					if !brokerSymbols[p.Symbol] {
						runtimeState.RecordLog("warn", "portfolio", fmt.Sprintf("reconcile: removing stale position %s (closed at broker)", p.Symbol))
						log.Printf("reconcile: removing stale position %s (closed at broker)", p.Symbol)
						portfolioMgr.RemoveStalePosition(p.Symbol)
					}
				}
			}
		}
	}()

	// Watchdog monitors component health
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stale := runtimeState.StaleComponents(2 * time.Minute)
				for _, name := range stale {
					log.Printf("watchdog: WARNING component %s appears stale (no heartbeat in 2m)", name)
					runtimeState.RecordLog("warn", "watchdog", fmt.Sprintf("component %s stale", name))
				}
			}
		}
	}()

	log.Printf("system: ready — dashboard at http://localhost%s", appCfg.ListenAddr)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("system: shutting down")
	cancel()
}

func resolveStreamSymbols(ctx context.Context, client *alpaca.Client, cfg config.AppConfig) ([]string, map[string]string, error) {
	assets, err := client.ListEquityAssets(ctx, true)
	if err != nil {
		return nil, nil, fmt.Errorf("list equity assets: %w", err)
	}
	symbols, blockedSymbols := filterScannerUniverseAssets(assets, cfg.AlpacaSymbols)
	if len(cfg.AlpacaSymbols) > 0 {
		log.Printf("symbols: using %d configured symbols after blocking %d ETF/derivative instruments", len(symbols), len(blockedSymbols))
		return symbols, blockedSymbols, nil
	}
	log.Printf("symbols: resolved %d NASDAQ+NYSE symbols from Alpaca after blocking %d ETF/derivative instruments", len(symbols), len(blockedSymbols))
	return symbols, blockedSymbols, nil
}

func runOptimize(args []string) error {
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
	client := alpaca.NewClient(alpacaCfg)

	historicalRateLimit := 0
	if capabilities, capErr := client.DetectMarketDataCapabilities(setupCtx); capErr == nil {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
		log.Printf("Optimize using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
	} else {
		log.Printf("Optimize capability detection failed, using defaults: %v", capErr)
	}

	symbols, _, err := resolveBacktestSymbols(setupCtx, client, time.Now())
	if err != nil {
		return err
	}

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
	fetchTimeout := estimateHistoricalFetchTimeout(len(symbols), prevDayStart, asOf, historicalRateLimit)
	log.Printf("Optimize historical fetch timeout=%s coverage start=%s end=%s", fetchTimeout, formatLogTime(prevDayStart), formatLogTime(asOf))

	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer fetchCancel()

	dataset, err := prepareHistoricalDataset(fetchCtx, client, symbols, prevDayStart, asOf, historicalRateLimit)
	if err != nil {
		return err
	}

	log.Printf("Optimize historical dataset ready shards=%d symbols=%d (streaming mode)", len(dataset.jobs), len(symbols))
	iterFactory := newDatasetIteratorFactory(dataset)
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

func brokerCashValue(acct alpaca.Account) (float64, bool) {
	if acct.Cash > 0 {
		return acct.Cash, true
	}
	return 0, false
}

func brokerAccountValues(acct alpaca.Account) (float64, float64, bool) {
	if acct.Equity > 0 {
		return acct.Equity, acct.BuyingPower, true
	}
	return 0, 0, false
}

func runAutoOptimize(args []string) error {
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
	client := alpaca.NewClient(alpacaCfg)

	historicalRateLimit := 0
	if capabilities, capErr := client.DetectMarketDataCapabilities(setupCtx); capErr == nil {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
		log.Printf("auto-optimize: Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
	}

	symbols, _, err := resolveBacktestSymbols(setupCtx, client, time.Now())
	if err != nil {
		return optimizer.Report{}, err
	}

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
	fetchTimeout := estimateHistoricalFetchTimeout(len(symbols), prevDayStart, asOf, historicalRateLimit)
	log.Printf("auto-optimize: historical fetch timeout=%s", fetchTimeout)

	fetchCtx, fetchCancel := context.WithTimeout(ctx, fetchTimeout)
	defer fetchCancel()

	dataset, err := prepareHistoricalDataset(fetchCtx, client, symbols, prevDayStart, asOf, historicalRateLimit)
	if err != nil {
		return optimizer.Report{}, err
	}

	log.Printf("auto-optimize: dataset ready shards=%d symbols=%d", len(dataset.jobs), len(symbols))
	iterFactory := newDatasetIteratorFactory(dataset)
	opt := optimizer.NewStreamingOptimizer(iterFactory, lookbackStart, asOf, outDir)
	autoFloatStore := alpaca.NewFloatStore()
	if _, loadErr := autoFloatStore.LoadOrFetchFloatData(context.Background()); loadErr != nil {
		log.Printf("auto-optimize: float data warning: %v", loadErr)
	}

	opt.SetFloatStore(autoFloatStore)
	return opt.Run()
}

// computeSeededPositionStops sets defensive stop prices for broker-seeded positions
// that are missing risk metadata (StopPrice, RiskPerShare, OriginalQuantity, EntryATR).
// When snapshots are provided, the previous day's low/high is used as a natural support/resistance
// level. Otherwise, a percentage-based fallback is used (EntryATRPercentFallback or 2% default).
func computeSeededPositionStops(portfolioMgr *portfolio.Manager, tradingCfg config.TradingConfig, snapshots map[string]alpaca.Snapshot) {
	for _, pos := range portfolioMgr.GetPositions() {
		if !pos.BrokerSeeded || pos.StopPrice != 0 {
			continue
		}

		// Try snapshot-based stop first (more accurate)
		if snapshots != nil {
			snap, ok := snapshots[pos.Symbol]
			if ok && snap.PrevDailyBar.Low > 0 {
				var stopPrice, riskPerShare float64
				if domain.IsLong(pos.Side) {
					stopPrice = snap.PrevDailyBar.Low * 0.99 // 1% below prev day low
					riskPerShare = pos.AvgPrice - stopPrice
				} else {
					stopPrice = snap.PrevDailyBar.High * 1.01 // 1% above prev day high
					riskPerShare = stopPrice - pos.AvgPrice
				}
				if riskPerShare > 0 {
					portfolioMgr.UpdateSeededPositionRisk(pos.Symbol, stopPrice, riskPerShare, pos.Quantity)
					log.Printf("alpaca: snapshot-based stop for %s: stop=%.2f (prev day low=%.2f)",
						pos.Symbol, stopPrice, snap.PrevDailyBar.Low)
					continue
				}
			}
		}

		// Fallback to percentage-based stop
		riskPct := tradingCfg.EntryATRPercentFallback
		switch {
		case riskPct <= 0:
			riskPct = 2.0
		case riskPct < 0.5:
			riskPct = 2.0
		}
		riskPerShare := pos.AvgPrice * riskPct / 100.0
		var stopPrice float64
		if domain.IsLong(pos.Side) {
			stopPrice = pos.AvgPrice - riskPerShare*tradingCfg.EntryStopATRMultiplier
		} else {
			stopPrice = pos.AvgPrice + riskPerShare*tradingCfg.EntryStopATRMultiplier
		}
		portfolioMgr.UpdateSeededPositionRisk(pos.Symbol, stopPrice, riskPerShare, pos.Quantity)
		log.Printf("alpaca: fallback stop for %s: stop=%.2f risk/share=%.4f",
			pos.Symbol, stopPrice, riskPerShare)
	}
}
