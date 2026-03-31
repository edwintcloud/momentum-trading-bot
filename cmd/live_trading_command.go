package cmd

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/api"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/market"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/ml"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
	"github.com/edwintcloud/momentum-trading-bot/internal/pipeline"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/regime"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/signals"
	"github.com/edwintcloud/momentum-trading-bot/internal/storage"
	"github.com/edwintcloud/momentum-trading-bot/internal/strategy"
	"github.com/edwintcloud/momentum-trading-bot/internal/telemetry"
)

func RunLiveTrading() {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Load trading config
	tradingCfg := config.DefaultTradingConfig()

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
	telegramNotifier := telemetry.NewTelegramNotifierFromEnv()

	// Connect to Alpaca
	alpacaClient := alpaca.NewClient(appCfg, tradingCfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Probe broker account
	acct, err := alpacaClient.GetAccount(ctx)
	if err != nil {
		log.Fatalf("alpaca: %v", err)
	}
	log.Printf("alpaca: equity=%.2f buying_power=%.2f status=%s", acct.Equity, acct.BuyingPower, acct.Status)
	tradingCfg.StartingCapital = acct.Equity.InexactFloat64()
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
	portfolioMgr.SetBrokerEquity(acct.Equity.InexactFloat64())

	// Seed broker positions
	brokerPositions, err := alpacaClient.GetPositions(ctx)
	if err != nil {
		log.Printf("alpaca: position seed warning: %v", err)
	} else {
		openedAtBySymbol, openedAtErr := inferBrokerPositionOpenedAtMap(ctx, alpacaClient, brokerPositions)
		if openedAtErr != nil {
			log.Printf("alpaca: position timing warning: %v", openedAtErr)
		}
		for _, bp := range brokerPositions {
			if seeded, ok := seedBrokerPosition(portfolioMgr, bp, openedAtBySymbol[bp.Symbol]); ok {
				log.Printf("alpaca: seeded position %s %s qty=%d avg=%.2f", seeded.Symbol, seeded.Side, seeded.Quantity, seeded.AvgPrice)
			}
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
	strategyInst := strategy.NewStrategy(tradingCfg, portfolioMgr, runtimeState, riskEngine)
	regimeTracker := regime.NewTracker(tradingCfg, runtimeState)
	normalizer := market.NewNormalizer()
	scorer, err := ml.ResolveScorer(tradingCfg.MLScoringEnabled, tradingCfg.MLModelPath)
	if err != nil {
		log.Fatalf("ml scorer: %v", err)
	}

	// Build signal aggregator from alpha config.
	var sigAgg *signals.Aggregator
	if tradingCfg.OFIEnabled || tradingCfg.VPINEnabled {
		var sources []signals.SignalSource
		if tradingCfg.OFIEnabled {
			sources = append(sources, signals.NewOFI(signals.OFIConfig{
				Enabled:           true,
				WindowBars:        tradingCfg.OFIWindowBars,
				ThresholdSigma:    tradingCfg.OFIThresholdSigma,
				PersistenceMinBar: tradingCfg.OFIPersistenceMin,
			}))
		}
		if tradingCfg.VPINEnabled {
			sources = append(sources, signals.NewVPIN(signals.VPINConfig{
				Enabled:         true,
				BucketDivisor:   tradingCfg.VPINBucketDivisor,
				LookbackBuckets: tradingCfg.VPINLookbackBuckets,
				HighThreshold:   tradingCfg.VPINHighThreshold,
				LowThreshold:    tradingCfg.VPINLowThreshold,
			}))
		}
		sigAgg = signals.NewAggregator(sources...)
	}

	pipe := pipeline.New(pipeline.Config{
		TradingCfg:                tradingCfg,
		Runtime:                   runtimeState,
		Portfolio:                 portfolioMgr,
		Normalizer:                normalizer,
		Scanner:                   scannerInst,
		Strategy:                  strategyInst,
		RiskEngine:                riskEngine,
		Broker:                    alpacaClient,
		Recorder:                  logger,
		Scorer:                    scorer,
		RegimeTracker:             regimeTracker,
		SignalAggregator:          sigAgg,
		FloatLookup:               floatStore.Get,
		CandidateEvaluationSource: "live",
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
			fmt.Sscanf(bp.CurrentPrice.String(), "%f", &currentPrice)
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
	if err := stream.SyncTradeSubscriptions(ctx, openPositionSymbols(portfolioMgr)); err != nil {
		log.Printf("stream: trade subscription warning: %v", err)
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
			if snap.PrevDailyBar == nil || snap.DailyBar == nil {
				continue
			}
			preMarketVol := uint64(0)
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

	// Update open position prices from live trade ticks instead of waiting for the next bar.
	go func() {
		for trade := range stream.Trades() {
			if trade.Price <= 0 {
				continue
			}
			portfolioMgr.MarkPriceAt(trade.Symbol, trade.Price, trade.Timestamp)
		}
	}()

	// Keep trade subscriptions aligned with the currently open positions.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := stream.SyncTradeSubscriptions(ctx, openPositionSymbols(portfolioMgr)); err != nil {
					log.Printf("stream: trade sync warning: %v", err)
				}
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
					portfolioMgr.SetBrokerEquity(bAcct.Equity.InexactFloat64())
				}
				// Update portfolio prices from broker and detect mismatches
				pmPositions := portfolioMgr.GetPositions()
				pmSymbols := make(map[string]bool)
				for _, p := range pmPositions {
					pmSymbols[p.Symbol] = true
				}
				brokerSymbols := make(map[string]bool)
				missingOpenedAtBySymbol := map[string]time.Time{}
				missingOpenedAtLoaded := false
				for _, bp := range bPositions {
					brokerSymbols[bp.Symbol] = true
					if !pmSymbols[bp.Symbol] {
						if !missingOpenedAtLoaded {
							missingOpenedAtLoaded = true
							missingOpenedAtBySymbol, _ = inferBrokerPositionOpenedAtMap(ctx, alpacaClient, bPositions)
						}
						if seeded, ok := seedBrokerPosition(portfolioMgr, bp, missingOpenedAtBySymbol[bp.Symbol]); ok {
							computeSeededPositionStops(portfolioMgr, tradingCfg, snapshots)
							runtimeState.RecordLog("warn", "portfolio", fmt.Sprintf("reconcile: adopted broker position %s qty=%d avg=%.2f after local tracking mismatch", seeded.Symbol, seeded.Quantity, seeded.AvgPrice))
							log.Printf("reconcile: adopted broker position %s %s qty=%d avg=%.2f after local tracking mismatch", seeded.Symbol, seeded.Side, seeded.Quantity, seeded.AvgPrice)
							pmSymbols[bp.Symbol] = true
						} else {
							runtimeState.RecordLog("warn", "portfolio", fmt.Sprintf("broker has position %s not in portfolio manager", bp.Symbol))
							log.Printf("reconcile: WARNING broker has position %s not in portfolio manager", bp.Symbol)
						}
						continue
					}
					// Update portfolio price from broker's current price so positions
					// stay fresh even when no streaming bars are received.
					var currentPrice float64
					fmt.Sscanf(bp.CurrentPrice.String(), "%f", &currentPrice)
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

	// End-of-day Telegram summary after the extended-hours close (8:00 PM ET).
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		lastSentDay := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now().In(markethours.Location())
				dayKey := now.Format("2006-01-02")
				if dayKey == lastSentDay {
					continue
				}
				if !markethours.IsMarketDay(now) {
					continue
				}
				if now.Before(markethours.SessionClose(now)) {
					continue
				}

				status := portfolioMgr.StatusSnapshot()
				telegramNotifier.NotifyDailySummary(status, now)
				lastSentDay = dayKey
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
	symbols, blockedSymbols := alpaca.FilterScannerUniverseAssets(assets, cfg.AlpacaSymbols)
	if len(cfg.AlpacaSymbols) > 0 {
		log.Printf("symbols: using %d configured symbols after blocking %d ETF/derivative instruments", len(symbols), len(blockedSymbols))
		return symbols, blockedSymbols, nil
	}
	log.Printf("symbols: resolved %d NASDAQ+NYSE symbols from Alpaca after blocking %d ETF/derivative instruments", len(symbols), len(blockedSymbols))
	return symbols, blockedSymbols, nil
}

// computeSeededPositionStops sets defensive stop prices for broker-seeded positions
// that are missing risk metadata (StopPrice, RiskPerShare, OriginalQuantity, EntryATR).
// When snapshots are provided, the previous day's low/high is used as a natural support/resistance
// level. Otherwise, a percentage-based fallback is used (EntryATRPercentFallback or 2% default).
func computeSeededPositionStops(portfolioMgr *portfolio.Manager, tradingCfg config.TradingConfig, snapshots map[string]*alpaca.Snapshot) {
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

func seedBrokerPosition(portfolioMgr *portfolio.Manager, bp alpaca.AlpacaPosition, openedAt time.Time) (domain.Position, bool) {
	position, ok := brokerPositionToDomainPosition(bp, openedAt)
	if !ok {
		return domain.Position{}, false
	}
	portfolioMgr.SeedBrokerPosition(position)
	return position, true
}

func brokerPositionToDomainPosition(bp alpaca.AlpacaPosition, openedAt time.Time) (domain.Position, bool) {
	qty := bp.Qty.IntPart()
	if qty <= 0 {
		return domain.Position{}, false
	}

	side := domain.DirectionLong
	if bp.Side == "short" {
		side = domain.DirectionShort
	}

	var avgPrice, currentPrice, marketValue, unrealizedPL float64
	fmt.Sscanf(bp.AvgEntryPrice.String(), "%f", &avgPrice)
	fmt.Sscanf(bp.CurrentPrice.String(), "%f", &currentPrice)
	fmt.Sscanf(bp.MarketValue.String(), "%f", &marketValue)
	fmt.Sscanf(bp.UnrealizedPL.String(), "%f", &unrealizedPL)
	now := time.Now()
	if openedAt.IsZero() {
		openedAt = now
	}
	return domain.Position{
		Symbol:        bp.Symbol,
		Side:          side,
		Quantity:      qty,
		AvgPrice:      avgPrice,
		LastPrice:     currentPrice,
		HighestPrice:  currentPrice,
		LowestPrice:   currentPrice,
		MarketValue:   marketValue,
		UnrealizedPnL: unrealizedPL,
		OpenedAt:      openedAt,
		UpdatedAt:     now,
	}, true
}

func inferBrokerPositionOpenedAtMap(ctx context.Context, alpacaClient *alpaca.Client, positions []alpaca.AlpacaPosition) (map[string]time.Time, error) {
	out := make(map[string]time.Time)
	if len(positions) == 0 {
		return out, nil
	}

	orderCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	orders, err := alpacaClient.ListRecentOrders(orderCtx, 500)
	if err != nil {
		return out, err
	}

	wanted := make(map[string]alpaca.AlpacaPosition, len(positions))
	orderGroups := make(map[string][]alpaca.AlpacaOrder, len(positions))
	for _, pos := range positions {
		wanted[pos.Symbol] = pos
	}
	for _, order := range orders {
		if _, ok := wanted[order.Symbol]; !ok {
			continue
		}
		if order.FilledQty.IntPart() <= 0 {
			continue
		}
		if alpaca.OrderEventTime(order).IsZero() {
			continue
		}
		orderGroups[order.Symbol] = append(orderGroups[order.Symbol], order)
	}

	for _, pos := range positions {
		openedAt, ok := inferPositionOpenedAt(pos, orderGroups[pos.Symbol])
		if ok {
			out[pos.Symbol] = openedAt
		}
	}
	return out, nil
}

func inferPositionOpenedAt(position alpaca.AlpacaPosition, orders []alpaca.AlpacaOrder) (time.Time, bool) {
	remainingQty := position.Qty.IntPart()
	if remainingQty <= 0 || len(orders) == 0 {
		return time.Time{}, false
	}

	sort.Slice(orders, func(i, j int) bool {
		return alpaca.OrderEventTime(orders[i]).After(alpaca.OrderEventTime(orders[j]))
	})

	isShort := position.Side == "short"
	for _, order := range orders {
		filledQty := order.FilledQty.IntPart()
		if filledQty <= 0 {
			continue
		}
		switch {
		case !isShort && order.Side == domain.SideBuy:
			remainingQty -= filledQty
			if remainingQty <= 0 {
				return alpaca.OrderEventTime(order), true
			}
		case !isShort && order.Side == domain.SideSell:
			remainingQty += filledQty
		case isShort && order.Side == domain.SideSell:
			remainingQty -= filledQty
			if remainingQty <= 0 {
				return alpaca.OrderEventTime(order), true
			}
		case isShort && order.Side == domain.SideBuy:
			remainingQty += filledQty
		}
	}

	return time.Time{}, false
}

func openPositionSymbols(portfolioMgr *portfolio.Manager) []string {
	positions := portfolioMgr.GetPositions()
	symbols := make([]string, 0, len(positions))
	for _, pos := range positions {
		if pos.Symbol == "" {
			continue
		}
		symbols = append(symbols, pos.Symbol)
	}
	sort.Strings(symbols)
	return symbols
}
