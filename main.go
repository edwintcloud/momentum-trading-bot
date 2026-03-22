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
			runBacktest(os.Args[2:])
			return
		case "optimize":
			runOptimize(os.Args[2:])
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
				Symbol:       order.Symbol,
				Side:         order.Side,
				Intent:       order.Intent,
				PositionSide: order.PositionSide,
				Price:        order.Price,
				Quantity:     order.Quantity,
				StopPrice:    order.StopPrice,
				RiskPerShare: order.RiskPerShare,
				EntryATR:     order.EntryATR,
				SetupType:    order.SetupType,
				Reason:       order.Reason,
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

func runBacktest(args []string) {
	fs := flag.NewFlagSet("backtest", flag.ExitOnError)
	startStr := fs.String("start", "", "start date (YYYY-MM-DD)")
	endStr := fs.String("end", "", "end date (YYYY-MM-DD)")
	dataPath := fs.String("data", "", "path to CSV data file")
	fs.Parse(args)

	if *startStr == "" {
		log.Fatal("backtest: -start is required")
	}

	start, err := time.Parse("2006-01-02", *startStr)
	if err != nil {
		log.Fatalf("backtest: invalid start date: %v", err)
	}

	end := time.Now()
	if *endStr != "" {
		end, err = time.Parse("2006-01-02", *endStr)
		if err != nil {
			log.Fatalf("backtest: invalid end date: %v", err)
		}
	}

	tradingCfg := config.DefaultTradingConfig()

	var bars []domain.Tick
	if *dataPath != "" {
		bars, err = backtest.LoadCSV(*dataPath, start, end)
		if err != nil {
			log.Fatalf("backtest: load csv: %v", err)
		}
	} else {
		log.Fatal("backtest: -data path to CSV is required (Alpaca historical fetch not yet wired)")
	}

	engine := backtest.NewEngine(tradingCfg)
	result := engine.Run(bars)

	output, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(output))
}

func runOptimize(args []string) {
	fs := flag.NewFlagSet("optimize", flag.ExitOnError)
	asOfStr := fs.String("as-of", "", "as-of date (YYYY-MM-DD)")
	dataPath := fs.String("data", "", "path to CSV data file")
	outDir := fs.String("out", ".cache/optimizer", "output directory")
	fs.Parse(args)

	if *asOfStr == "" {
		log.Fatal("optimize: -as-of is required")
	}

	asOf, err := time.Parse("2006-01-02", *asOfStr)
	if err != nil {
		log.Fatalf("optimize: invalid as-of date: %v", err)
	}

	var bars []domain.Tick
	if *dataPath != "" {
		bars, err = backtest.LoadCSV(*dataPath, asOf.AddDate(0, -6, 0), asOf)
		if err != nil {
			log.Fatalf("optimize: load csv: %v", err)
		}
	} else {
		log.Fatal("optimize: -data path to CSV is required")
	}

	opt := optimizer.NewOptimizer(bars, asOf, *outDir)
	report, err := opt.Run()
	if err != nil {
		log.Fatalf("optimize: %v", err)
	}

	output, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(output))
}
