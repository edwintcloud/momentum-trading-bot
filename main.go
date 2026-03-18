package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/api"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/execution"
	"github.com/edwincloud/momentum-trading-bot/internal/market"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/risk"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
	"github.com/edwincloud/momentum-trading-bot/internal/scanner"
	"github.com/edwincloud/momentum-trading-bot/internal/storage"
	"github.com/edwincloud/momentum-trading-bot/internal/strategy"
	"github.com/edwincloud/momentum-trading-bot/internal/telemetry"
)

func main() {
	if len(os.Args) > 1 && strings.EqualFold(os.Args[1], "backtest") {
		if err := runBacktest(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	log.Println("Starting Momentum Trading Bot")
	appConfig, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeState := runtime.NewState()
	runtimeState.SetDependencyStatus("database", false, "not checked")
	runtimeState.SetDependencyStatus("alpaca_trading", false, "not checked")
	runtimeState.SetDependencyStatus("market_data_stream", false, "not connected")

	pgRecorder, err := storage.NewRecorder(ctx, appConfig.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pgRecorder.Close()

	fileLogger, err := telemetry.NewLogger("./logs")
	if err != nil {
		log.Fatal(err)
	}
	defer fileLogger.Close()

	recorder := telemetry.NewCompositeRecorder(pgRecorder, fileLogger)

	startupCtx, startupCancel := context.WithTimeout(ctx, time.Duration(appConfig.StartupTimeoutSec)*time.Second)
	defer startupCancel()
	if err := pgRecorder.Ping(startupCtx); err != nil {
		log.Fatalf("database health check failed: %v", err)
	}
	runtimeState.SetDependencyStatus("database", true, "postgres reachable")
	runtimeState.SetRecorder(recorder)
	alpacaClient := alpaca.NewClient(appConfig.Alpaca)
	if err := alpacaClient.Ping(startupCtx); err != nil {
		log.Fatalf("alpaca health check failed: %v", err)
	}
	historicalRateLimit := 0
	capabilities, err := alpacaClient.DetectMarketDataCapabilities(startupCtx)
	if err != nil {
		runtimeState.RecordLog("warn", "market", fmt.Sprintf("market-data capability probe failed: %v", err))
	} else {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
		if appConfig.Alpaca.AutoSelectDataFeed {
			appConfig.Alpaca.DataFeed = capabilities.DetectedFeed
			alpacaClient.SetDataFeed(capabilities.DetectedFeed)
		}
		appConfig.Trading.HydrationRequestsPerMin = recommendedHydrationBudget(capabilities.HistoricalRateLimitPerMin)
		if capabilities.UnlimitedWebsocketSymbols {
			runtimeState.RecordLog("info", "market", fmt.Sprintf("detected Alpaca %s plan: feed=%s historical_limit=%d/min websocket_symbols=unlimited", capabilities.PlanName, alpacaClient.DataFeed(), capabilities.HistoricalRateLimitPerMin))
		} else {
			runtimeState.RecordLog("info", "market", fmt.Sprintf("detected Alpaca %s plan: feed=%s historical_limit=%d/min websocket_symbols=30", capabilities.PlanName, alpacaClient.DataFeed(), capabilities.HistoricalRateLimitPerMin))
		}
	}
	account, err := alpacaClient.GetAccount(startupCtx)
	if err != nil {
		log.Fatalf("alpaca account fetch failed: %v", err)
	}
	if equity, _, ok := brokerAccountValues(account); ok {
		appConfig.Trading = config.TuneTradingConfig(appConfig.Trading, equity, historicalRateLimit)
	} else if equity, parseErr := strconv.ParseFloat(account.Equity, 64); parseErr == nil && equity > 0 {
		appConfig.Trading = config.TuneTradingConfig(appConfig.Trading, equity, historicalRateLimit)
	} else {
		appConfig.Trading = config.TuneTradingConfig(appConfig.Trading, appConfig.Trading.StartingCapital, historicalRateLimit)
	}
	runtimeState.RecordLog("info", "risk", fmt.Sprintf(
		"broker-tuned config risk_per_trade=%.2f%% daily_loss=%.2f%% max_open=%d max_exposure=%.2f%% min_gap=%.1f%% min_rvol=%.1f min_score=%.1f min_1m=%.2f",
		appConfig.Trading.RiskPerTradePct*100,
		appConfig.Trading.DailyLossLimitPct*100,
		appConfig.Trading.MaxOpenPositions,
		appConfig.Trading.MaxExposurePct*100,
		appConfig.Trading.MinGapPercent,
		appConfig.Trading.MinRelativeVolume,
		appConfig.Trading.MinEntryScore,
		appConfig.Trading.MinOneMinuteReturnPct,
	))
	portfolioManager := portfolio.NewManager(appConfig.Trading, runtimeState)
	portfolioManager.SetRecorder(recorder)
	runtimeState.SetDependencyStatus("alpaca_trading", true, liveModeLabel(appConfig.Alpaca.Paper))
	runtimeState.RecordLog("info", "system", "live alpaca mode enabled")
	seedFromBroker(ctx, alpacaClient, portfolioManager, runtimeState, account)
	seedClosedTradesFromDB(ctx, pgRecorder, portfolioManager, runtimeState)
	startBrokerAccountSync(ctx, alpacaClient, portfolioManager, runtimeState)

	// Graceful shutdown on SIGINT/SIGTERM
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("Shutdown signal received")
		cancel()
	}()

	// Shared channels between components
	marketUpdates := make(chan domain.Tick, 256)
	scannerTicks := make(chan domain.Tick, 256)
	strategyTicks := make(chan domain.Tick, 256)
	candidateSignals := make(chan domain.Candidate, 256)
	tradeSignals := make(chan domain.TradeSignal, 256)
	approvedOrders := make(chan domain.OrderRequest, 256)
	operatorOrders := make(chan domain.OrderRequest, 64)

	// Core components
	marketEngine := market.NewEngine(alpacaClient, appConfig.Trading, portfolioManager, runtimeState)
	scannerEngine := scanner.NewScanner(appConfig.Trading, runtimeState)
	strategyEngine := strategy.NewStrategy(appConfig.Trading, portfolioManager, runtimeState)
	riskEngine := risk.NewEngine(appConfig.Trading, portfolioManager, runtimeState)
	executionEngine := execution.NewEngine(alpacaClient, appConfig.Alpaca, runtimeState)

	// Start the market data engine
	go func() {
		if err := marketEngine.Start(ctx, marketUpdates); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("market engine stopped: %v", err)
				cancel()
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case tick, ok := <-marketUpdates:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case scannerTicks <- tick:
				}
				select {
				case <-ctx.Done():
					return
				case strategyTicks <- tick:
				}
			}
		}
	}()

	// Start the scanner
	go func() {
		if err := scannerEngine.Start(ctx, scannerTicks, candidateSignals); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("scanner stopped: %v", err)
				cancel()
			}
		}
	}()

	// Start the strategy
	go func() {
		if err := strategyEngine.Start(ctx, candidateSignals, strategyTicks, tradeSignals); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("strategy stopped: %v", err)
				cancel()
			}
		}
	}()

	// Start the risk engine + execution
	go func() {
		if err := riskEngine.Start(ctx, tradeSignals, approvedOrders); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("risk engine stopped: %v", err)
				cancel()
			}
		}
	}()

	go func() {
		if err := executionEngine.Start(ctx, approvedOrders, portfolioManager); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("execution engine stopped: %v", err)
				cancel()
			}
		}
	}()

	go func() {
		if err := executionEngine.Start(ctx, operatorOrders, portfolioManager); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("operator execution stopped: %v", err)
				cancel()
			}
		}
	}()

	// Start API server
	apiServer := api.NewServer(portfolioManager, runtimeState, operatorOrders, appConfig)
	go func() {
		if err := apiServer.Start(ctx, appConfig.HTTPAddr); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("api server stopped: %v", err)
				cancel()
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Duration(appConfig.SnapshotPersistIntervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recorder.RecordDashboard(domain.DashboardSnapshot{
					Status:       portfolioManager.StatusSnapshot(),
					Candidates:   runtimeState.Candidates(),
					Positions:    portfolioManager.GetPositions(),
					ClosedTrades: portfolioManager.GetClosedTrades(),
					Logs:         runtimeState.Logs(),
					UpdatedAt:    time.Now().UTC(),
				})
			}
		}
	}()

	// Periodically reconcile local positions against Alpaca to drop any that
	// were closed outside the bot (manual closes, fill-poll timeouts, etc.).
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcileCtx, reconcileCancel := context.WithTimeout(ctx, 15*time.Second)
				brokerPositions, reconcileErr := alpacaClient.ListOpenPositions(reconcileCtx)
				reconcileCancel()
				if reconcileErr != nil {
					runtimeState.RecordLog("warn", "portfolio", fmt.Sprintf("position reconciliation failed: %v", reconcileErr))
					continue
				}
				brokerQuantities := make(map[string]int64, len(brokerPositions))
				for _, p := range brokerPositions {
					quantity, qtyErr := strconv.ParseInt(stringsBeforeDecimal(p.Qty), 10, 64)
					if qtyErr != nil {
						continue
					}
					brokerQuantities[strings.ToUpper(p.Symbol)] = quantity
				}
				portfolioManager.ReconcileWithBroker(brokerQuantities)
			}
		}
	}()

	// Run until context is canceled
	<-ctx.Done()

	// Give components a moment to shutdown cleanly
	time.Sleep(time.Duration(appConfig.ShutdownTimeoutSec) * time.Second)
	log.Println("Momentum Trading Bot stopped")
}

func recommendedHydrationBudget(limitPerMinute int) int {
	if limitPerMinute <= 0 {
		return 120
	}
	budget := int(float64(limitPerMinute) * 0.60)
	if budget < 120 {
		budget = 120
	}
	if budget > 2400 {
		budget = 2400
	}
	return budget
}

func seedClosedTradesFromDB(ctx context.Context, recorder *storage.Recorder, portfolioManager *portfolio.Manager, runtimeState *runtime.State) {
	loadCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	trades, err := recorder.LoadTodayClosedTrades(loadCtx)
	if err != nil {
		runtimeState.RecordLog("warn", "startup", fmt.Sprintf("could not load today's closed trades: %v", err))
		return
	}
	portfolioManager.SeedClosedTrades(trades)
	if len(trades) > 0 {
		runtimeState.RecordLog("info", "startup", fmt.Sprintf("restored %d closed trade(s) from today", len(trades)))
	}
}

func seedFromBroker(ctx context.Context, client *alpaca.Client, portfolioManager *portfolio.Manager, runtimeState *runtime.State, account alpaca.Account) {
	if equity, parseErr := strconv.ParseFloat(account.Equity, 64); parseErr == nil && equity > 0 {
		portfolioManager.SetStartingCapital(math.Round(equity*100) / 100)
	}
	if equity, lastEquity, ok := brokerAccountValues(account); ok {
		portfolioManager.SyncBrokerAccount(equity, lastEquity)
	}

	positionsCtx, positionsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer positionsCancel()
	positions, err := client.ListOpenPositions(positionsCtx)
	if err != nil {
		runtimeState.RecordLog("warn", "startup", fmt.Sprintf("could not load broker positions: %v", err))
		return
	}
	for _, brokerPosition := range positions {
		quantity, qtyErr := strconv.ParseInt(stringsBeforeDecimal(brokerPosition.Qty), 10, 64)
		avgPrice, avgErr := strconv.ParseFloat(brokerPosition.AvgEntryPrice, 64)
		currentPrice, currentErr := strconv.ParseFloat(brokerPosition.CurrentPrice, 64)
		if qtyErr != nil || avgErr != nil || currentErr != nil || quantity <= 0 {
			continue
		}
		now := time.Now().UTC()
		portfolioManager.SeedPosition(domain.Position{
			Symbol:        brokerPosition.Symbol,
			Quantity:      quantity,
			AvgPrice:      avgPrice,
			LastPrice:     currentPrice,
			HighestPrice:  math.Max(avgPrice, currentPrice),
			MarketValue:   float64(quantity) * currentPrice,
			UnrealizedPnL: (currentPrice - avgPrice) * float64(quantity),
			BrokerSeeded:  true,
			OpenedAt:      now,
			UpdatedAt:     now,
		})
	}
}

func startBrokerAccountSync(ctx context.Context, client *alpaca.Client, portfolioManager *portfolio.Manager, runtimeState *runtime.State) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				accountCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				account, err := client.GetAccount(accountCtx)
				cancel()
				if err != nil {
					runtimeState.RecordLog("warn", "portfolio", fmt.Sprintf("broker account sync failed: %v", err))
					continue
				}
				if equity, lastEquity, ok := brokerAccountValues(account); ok {
					portfolioManager.SyncBrokerAccount(equity, lastEquity)
				}
			}
		}
	}()
}

func brokerAccountValues(account alpaca.Account) (float64, float64, bool) {
	equity, equityErr := strconv.ParseFloat(account.Equity, 64)
	lastEquity, lastEquityErr := strconv.ParseFloat(account.LastEquity, 64)
	if equityErr != nil || lastEquityErr != nil || equity <= 0 || lastEquity <= 0 {
		return 0, 0, false
	}
	return equity, lastEquity, true
}

func stringsBeforeDecimal(value string) string {
	parts := strings.SplitN(value, ".", 2)
	return parts[0]
}

func liveModeLabel(paper bool) string {
	if paper {
		return "paper trading enabled"
	}
	return "live trading enabled"
}
