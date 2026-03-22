package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/api"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/regime"
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

	// Initialize pipeline components
	portfolioMgr := portfolio.NewManager(tradingCfg, logger)
	portfolioMgr.SetBrokerEquity(acct.Equity)

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

	// Pipeline channels
	tickCh := make(chan domain.Tick, 1024)
	candidateCh := make(chan domain.Candidate, 256)
	signalCh := make(chan domain.TradeSignal, 64)
	orderCh := make(chan domain.OrderRequest, 64)
	closeAllCh := make(chan domain.OrderRequest, 64)

	// Start components
	scannerInst := scanner.NewScanner(tradingCfg, runtimeState)
	strategyInst := strategy.NewStrategy(tradingCfg, portfolioMgr, runtimeState)
	regimeTracker := regime.NewTracker(tradingCfg, runtimeState)
	_ = regimeTracker

	// Fan-out ticks to strategy and scanner
	scannerTicks := make(chan domain.Tick, 1024)
	strategyTicks := make(chan domain.Tick, 1024)
	go func() {
		for tick := range tickCh {
			// Update regime tracker
			if regimeTracker.IsBenchmark(tick.Symbol) {
				regimeTracker.UpdateTick(tick)
			}
			// Update portfolio prices
			portfolioMgr.UpdatePrice(tick.Symbol, tick.Price)

			select {
			case scannerTicks <- tick:
			default:
			}
			select {
			case strategyTicks <- tick:
			default:
			}
		}
	}()

	// Start pipeline stages
	go func() {
		if err := scannerInst.Start(ctx, scannerTicks, candidateCh); err != nil {
			log.Printf("scanner: %v", err)
		}
	}()

	go func() {
		if err := strategyInst.Start(ctx, candidateCh, strategyTicks, signalCh); err != nil {
			log.Printf("strategy: %v", err)
		}
	}()

	// Risk engine processes signals (inline for now)
	go func() {
		for signal := range signalCh {
			select {
			case orderCh <- domain.OrderRequest{
				Symbol:           signal.Symbol,
				Side:             signal.Side,
				Intent:           signal.Intent,
				PositionSide:     signal.PositionSide,
				Price:            signal.Price,
				Quantity:         signal.Quantity,
				StopPrice:        signal.StopPrice,
				RiskPerShare:     signal.RiskPerShare,
				EntryATR:         signal.EntryATR,
				SetupType:        signal.SetupType,
				Reason:           signal.Reason,
				MarketRegime:     signal.MarketRegime,
				RegimeConfidence: signal.RegimeConfidence,
				Playbook:         signal.Playbook,
				Timestamp:        signal.Timestamp,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Process close-all requests
	go func() {
		for order := range closeAllCh {
			orderCh <- order
		}
	}()

	// Execution placeholder — in production, submit to Alpaca
	go func() {
		for order := range orderCh {
			log.Printf("execution: %s %s %s qty=%d price=%.2f",
				order.Intent, order.PositionSide, order.Symbol, order.Quantity, order.Price)
			// In live mode: alpacaClient.SubmitOrder(ctx, order)
			report := domain.ExecutionReport{
				Symbol:        order.Symbol,
				Side:          order.Side,
				Intent:        order.Intent,
				PositionSide:  order.PositionSide,
				Price:         order.Price,
				Quantity:      order.Quantity,
				StopPrice:     order.StopPrice,
				RiskPerShare:  order.RiskPerShare,
				EntryATR:      order.EntryATR,
				SetupType:     order.SetupType,
				Reason:        order.Reason,
				BrokerOrderID: "sim-" + fmt.Sprintf("%d", time.Now().UnixNano()),
				BrokerStatus:  "filled",
				FilledAt:      time.Now(),
			}
			if domain.IsOpeningIntent(order.Intent) {
				portfolioMgr.OpenPosition(report)
			} else {
				portfolioMgr.ClosePosition(report)
			}
			logger.RecordExecution(report)
		}
	}()

	runtimeState.SetReady(true)
	runtimeState.RecordLog("info", "system", "momentum trading bot started")

	// Start API server
	apiServer := api.NewServer(portfolioMgr, runtimeState, closeAllCh, appCfg, tradingCfg)
	go func() {
		if err := apiServer.Start(ctx, appCfg.ListenAddr); err != nil {
			log.Fatalf("api: %v", err)
		}
	}()

	// TODO: Start Alpaca WebSocket streaming into tickCh
	// For now, the bot is ready and waiting for market data
	log.Printf("system: ready — dashboard at http://localhost%s", appCfg.ListenAddr)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("system: shutting down")
	cancel()
}

func runOptimize(args []string) error {
	fs := flag.NewFlagSet("optimize", flag.ContinueOnError)
	asOfStr := fs.String("as-of", "", "as-of date (YYYY-MM-DD)")
	startStr := fs.String("start", "", "explicit lookback start date (YYYY-MM-DD); defaults to as-of minus 6 months")
	dataPath := fs.String("data", "", "path to CSV data file (optional; fetches from Alpaca when omitted)")
	outDir := fs.String("out", ".cache/optimizer", "output directory")
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

	lookbackStart := asOf.AddDate(0, -6, 0)
	if *startStr != "" {
		lookbackStart, err = time.ParseInLocation("2006-01-02", *startStr, markethours.Location())
		if err != nil {
			return fmt.Errorf("invalid start date: %v", err)
		}
	}

	var bars []backtest.InputBar

	if *dataPath != "" {
		bars, err = backtest.LoadInputBars(*dataPath, lookbackStart, asOf)
		if err != nil {
			return fmt.Errorf("load csv: %v", err)
		}
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
			log.Printf("Optimize using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
		} else {
			log.Printf("Optimize capability detection failed, using defaults: %v", capErr)
		}

		symbols, err := resolveBacktestSymbols(setupCtx, client)
		if err != nil {
			return err
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

		bars, err = drainHistoricalDataset(dataset)
		if err != nil {
			return fmt.Errorf("drain historical dataset: %v", err)
		}
		log.Printf("Optimize historical dataset ready bars=%d symbols=%d", len(bars), len(symbols))
	}

	opt := optimizer.NewOptimizer(bars, asOf, *outDir)
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

