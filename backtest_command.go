package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

var dateOnlyPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func runBacktest(args []string) error {
	flags := flag.NewFlagSet("backtest", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	dataPath := flags.String("data", "", "Optional CSV fallback with timestamp,symbol,open,high,low,close,volume columns")
	startRaw := flags.String("start", "", "Inclusive backtest start timestamp")
	endRaw := flags.String("end", "", "Inclusive backtest end timestamp; defaults to now")
	if err := flags.Parse(args); err != nil {
		return err
	}

	start, _, err := parseCLIBacktestTime(*startRaw)
	if err != nil {
		return err
	}
	end, endDateOnly, err := parseCLIBacktestTime(*endRaw)
	if err != nil {
		return err
	}
	start, end, err = inferBacktestWindows(start, end, endDateOnly, *dataPath == "")
	if err != nil {
		return err
	}
	log.Printf("Backtest window start=%s end=%s", formatLogTime(start), formatLogTime(end))

	cfg := config.DefaultTradingConfig()
	runCfg := backtest.RunConfig{
		DataPath: *dataPath,
		Start:    start,
		End:      end,
	}

	if *dataPath == "" {
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
			log.Printf("Backtest using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
		} else {
			log.Printf("Backtest capability detection failed, using defaults: %v", capErr)
		}
		if account, accountErr := client.GetAccount(setupCtx); accountErr == nil {
			if equity, _, ok := brokerAccountValues(account); ok {
				cfg = config.TuneTradingConfig(cfg, equity, historicalRateLimit)
			}
		} else {
			log.Printf("Backtest account tuning skipped: %v", accountErr)
		}
		logBacktestConfig(cfg)

		symbols, err := resolveBacktestSymbols(setupCtx, client)
		if err != nil {
			return err
		}
		fetchTimeout := estimateHistoricalFetchTimeout(len(symbols), start, end, historicalRateLimit)
		log.Printf("Historical fetch timeout set to %s", fetchTimeout)
		log.Printf("Historical fetch coverage start=%s end=%s", formatLogTime(start), formatLogTime(end))
		fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer fetchCancel()
		dataset, err := prepareHistoricalDataset(fetchCtx, client, symbols, start, end, historicalRateLimit)
		if err != nil {
			return err
		}
		runCfg.Iterator = newHistoricalDatasetIterator(dataset)
		log.Printf("Historical dataset ready shards=%d symbols=%d", len(dataset.jobs), len(symbols))
	}

	result, err := backtest.Run(context.Background(), cfg, runCfg)
	if err != nil {
		return err
	}
	logBacktestDiagnostics(result.Diagnostics)
	logBacktestSummary(start, end, result)
	logClosedTradeSamples(result.ClosedTrades)
	return nil
}

func inferBacktestWindows(start, end time.Time, endDateOnly, requireStart bool) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	if end.IsZero() {
		end = now
	}
	if requireStart && start.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("start time is required when loading historical data from Alpaca")
	}
	if start.IsZero() {
		return time.Time{}, end, nil
	}
	if endDateOnly {
		if sameMarketDay(end, now) {
			end = now
		} else {
			end = endOfMarketDay(end)
		}
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end time must be after start time")
	}
	return start, end, nil
}

func resolveBacktestSymbols(ctx context.Context, client *alpaca.Client) ([]string, error) {
	symbols, err := client.ListActiveEquitySymbols(ctx)
	if err != nil {
		return nil, err
	}
	return symbols, nil
}

func parseCLIBacktestTime(value string) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	if dateOnlyPattern.MatchString(strings.TrimSpace(value)) {
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), marketTimeLocation())
		if err != nil {
			return time.Time{}, true, fmt.Errorf("unsupported date format %q", value)
		}
		return parsed.UTC(), true, nil
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, value, marketTimeLocation())
		if err == nil {
			return parsed.UTC(), false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unsupported date format %q", value)
}

func marketTimeLocation() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return location
}

func endOfMarketDay(value time.Time) time.Time {
	local := value.In(marketTimeLocation())
	return time.Date(local.Year(), local.Month(), local.Day(), 23, 59, 59, 0, marketTimeLocation()).UTC()
}

func sameMarketDay(a, b time.Time) bool {
	al := a.In(marketTimeLocation())
	bl := b.In(marketTimeLocation())
	return al.Year() == bl.Year() && al.Month() == bl.Month() && al.Day() == bl.Day()
}

func formatLogTime(value time.Time) string {
	if value.IsZero() {
		return "n/a"
	}
	return value.In(marketTimeLocation()).Format(time.RFC3339)
}

func logBacktestConfig(cfg config.TradingConfig) {
	log.Printf(
		"Backtest config min_price=%.2f min_gap=%.2f min_rel_volume=%.2f min_premarket=%d min_score=%.2f min_1m=%.2f min_3m=%.2f min_volume_rate=%.2f max_vs_open=%.2f risk_per_trade=%.4f max_trades=%d max_open=%d max_exposure=%.2f stop_loss=%.2f trail_activation_r=%.2f trail_atr_mult=%.2f tight_trail_trigger_r=%.2f tight_trail_atr_mult=%.2f profit_target_r=%.2f",
		cfg.MinPrice,
		cfg.MinGapPercent,
		cfg.MinRelativeVolume,
		cfg.MinPremarketVolume,
		cfg.MinEntryScore,
		cfg.MinOneMinuteReturnPct,
		cfg.MinThreeMinuteReturnPct,
		cfg.MinVolumeRate,
		cfg.MaxPriceVsOpenPct,
		cfg.RiskPerTradePct,
		cfg.MaxTradesPerDay,
		cfg.MaxOpenPositions,
		cfg.MaxExposurePct,
		cfg.StopLossPct,
		cfg.TrailActivationR,
		cfg.TrailATRMultiplier,
		cfg.TightTrailTriggerR,
		cfg.TightTrailATRMultiplier,
		cfg.ProfitTargetR,
	)
}

func logBacktestDiagnostics(diag backtest.Diagnostics) {
	log.Printf(
		"Backtest funnel bars_loaded=%d bars_in_window=%d entry_candidates=%d entry_signals=%d entry_risk_approved=%d exit_checks=%d exit_signals=%d exit_risk_approved=%d",
		diag.BarsLoaded,
		diag.BarsInWindow,
		diag.EntryCandidates,
		diag.EntrySignals,
		diag.EntryRiskApproved,
		diag.ExitChecks,
		diag.ExitSignals,
		diag.ExitRiskApproved,
	)
	logReasonCounts("scanner rejects", diag.ScannerRejects, diag.BarsInWindow)
	logReasonCounts("strategy entry rejects", diag.EntryRejects, diag.EntryCandidates)
	logReasonCounts("risk entry rejects", diag.EntryRiskRejects, diag.EntrySignals)
	logReasonCounts("strategy exit rejects", diag.ExitRejects, diag.ExitChecks)
	logReasonCounts("risk exit rejects", diag.ExitRiskRejects, diag.ExitSignals)
	logEntrySamples(diag.EntrySignalSamples)
	logEntryRejectSamples(diag)
}

func logBacktestSummary(start, end time.Time, result backtest.Result) {
	for _, line := range backtestSummaryLines(start, end, result) {
		log.Print(line)
	}
}

func backtestSummaryLines(start, end time.Time, result backtest.Result) []string {
	return []string{
		"Backtest Summary",
		fmt.Sprintf("  Window       %s -> %s", formatLogTime(start), formatLogTime(end)),
		fmt.Sprintf("  PnL          net=%s realized=%s unrealized=%s ending_equity=%s max_drawdown=%.2f%%",
			formatMoney(result.NetPnL),
			formatMoney(result.RealizedPnL),
			formatMoney(result.UnrealizedPnL),
			formatMoney(result.EndingEquity),
			result.MaxDrawdownPct,
		),
		fmt.Sprintf("  Trades       total=%d wins=%d losses=%d win_rate=%.2f%% profit_factor=%.2f open_positions=%d",
			result.Trades,
			result.Wins,
			result.Losses,
			result.WinRate,
			result.ProfitFactor,
			result.OpenPositionsAtEnd,
		),
		fmt.Sprintf("  Avg PnL/R    avg_win=%s avg_loss=%s avg_win_r=%.2f avg_loss_r=%.2f",
			formatMoney(result.AvgWinPnL),
			formatMoney(result.AvgLossPnL),
			result.AvgWinR,
			result.AvgLossR,
		),
		fmt.Sprintf("  Exit Stats   avg_mfe_r=%.2f avg_mae_r=%.2f trailing_exit_pct=%.2f%% avg_time_to_stop_min=%.2f",
			result.AvgMFER,
			result.AvgMAER,
			result.TrailingStopExitPct,
			result.AvgTimeToStopMin,
		),
	}
}

func logReasonCounts(label string, counts map[string]int, total int) {
	if len(counts) == 0 {
		return
	}
	type reasonCount struct {
		reason string
		count  int
	}
	reasons := make([]reasonCount, 0, len(counts))
	for reason, count := range counts {
		reasons = append(reasons, reasonCount{reason: reason, count: count})
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].count == reasons[j].count {
			return reasons[i].reason < reasons[j].reason
		}
		return reasons[i].count > reasons[j].count
	})
	limit := len(reasons)
	if limit > 5 {
		limit = 5
	}
	parts := make([]string, 0, limit)
	for _, item := range reasons[:limit] {
		share := 0.0
		if total > 0 {
			share = (float64(item.count) / float64(total)) * 100
		}
		parts = append(parts, fmt.Sprintf("%s=%d(%.2f%%)", item.reason, item.count, share))
	}
	log.Printf("Backtest %s %s", label, strings.Join(parts, " "))
}

func logEntrySamples(samples []backtest.EntrySample) {
	if len(samples) == 0 {
		return
	}
	parts := make([]string, 0, len(samples))
	for _, sample := range samples {
		parts = append(parts, fmt.Sprintf(
			"%s@%s price=%.2f score=%.2f dist_high=%.2f/%.2f rvol=%.2f leader=%.4f rank=%d atr_pct=%.2f vwap_pct=%.2f breakout=%.2f setup=%s 1m=%.2f 3m=%.2f vr=%.2f",
			sample.Symbol,
			sample.Timestamp.In(marketTimeLocation()).Format("2006-01-02 15:04"),
			sample.Price,
			sample.Score,
			sample.DistanceFromHighPct,
			sample.AllowedDistanceHighPct,
			sample.RelativeVolume,
			sample.VolumeLeaderPct,
			sample.LeaderRank,
			sample.ATRPct,
			sample.PriceVsVWAPPct,
			sample.BreakoutPct,
			sample.SetupType,
			sample.OneMinuteReturnPct,
			sample.ThreeMinuteReturnPct,
			sample.VolumeRate,
		))
	}
	log.Printf("Backtest entry samples %s", strings.Join(parts, " | "))
}

func logEntryRejectSamples(diag backtest.Diagnostics) {
	if len(diag.EntryRejectSamples) == 0 || len(diag.EntryRejects) == 0 {
		return
	}
	type reasonCount struct {
		reason string
		count  int
	}
	reasons := make([]reasonCount, 0, len(diag.EntryRejects))
	for reason, count := range diag.EntryRejects {
		reasons = append(reasons, reasonCount{reason: reason, count: count})
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].count == reasons[j].count {
			return reasons[i].reason < reasons[j].reason
		}
		return reasons[i].count > reasons[j].count
	})
	limit := len(reasons)
	if limit > 3 {
		limit = 3
	}
	for _, item := range reasons[:limit] {
		sample, ok := diag.EntryRejectSamples[item.reason]
		if !ok {
			continue
		}
		log.Printf(
			"Backtest reject sample reason=%s symbol=%s at=%s price=%.2f score=%.2f dist_high=%.2f/%.2f rvol=%.2f leader=%.4f rank=%d atr_pct=%.2f vwap_pct=%.2f breakout=%.2f setup=%s 1m=%.2f 3m=%.2f vr=%.2f squeeze=%t",
			item.reason,
			sample.Symbol,
			sample.Timestamp.In(marketTimeLocation()).Format("2006-01-02 15:04"),
			sample.Price,
			sample.Score,
			sample.DistanceFromHighPct,
			sample.AllowedDistanceHighPct,
			sample.RelativeVolume,
			sample.VolumeLeaderPct,
			sample.LeaderRank,
			sample.ATRPct,
			sample.PriceVsVWAPPct,
			sample.BreakoutPct,
			sample.SetupType,
			sample.OneMinuteReturnPct,
			sample.ThreeMinuteReturnPct,
			sample.VolumeRate,
			sample.StrongSqueeze,
		)
	}
}

func logClosedTradeSamples(trades []domain.ClosedTrade) {
	if len(trades) == 0 {
		log.Print("Closed trades: none")
		return
	}
	log.Printf("Closed trades (%d):", len(trades))
	for _, trade := range trades {
		log.Printf("  %s qty=%d entry=%s exit=%s pnl=%s reason=%s opened=%s closed=%s",
			trade.Symbol,
			trade.Quantity,
			formatMoney(trade.EntryPrice),
			formatMoney(trade.ExitPrice),
			formatMoney(trade.PnL),
			trade.ExitReason,
			trade.OpenedAt.In(marketTimeLocation()).Format("2006-01-02 15:04"),
			trade.ClosedAt.In(marketTimeLocation()).Format("2006-01-02 15:04"),
		)
	}
}

func formatMoney(value float64) string {
	sign := ""
	if value > 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%.2f", sign, value)
}
