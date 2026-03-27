package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/storage"
	"github.com/edwintcloud/momentum-trading-bot/internal/telemetry"
)

var dateOnlyPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func runBacktest(args []string) error {
	flags := flag.NewFlagSet("backtest", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	dataPath := flags.String("data", "", "Optional CSV fallback with timestamp,symbol,open,high,low,close,volume columns")
	startRaw := flags.String("start", "", "Inclusive backtest start timestamp")
	endRaw := flags.String("end", "", "Inclusive backtest end timestamp; defaults to the current hour when omitted")
	reportOut := flags.String("report-out", ".cache/backtest/latest-report.json", "Optional JSON report artifact path; set empty string to disable")
	candidateOut := flags.String("candidate-out", "", "Optional JSONL candidate evaluation export path")
	mlModelPath := flags.String("ml-model", "", "Optional ML model artifact path or directory for shadow scoring")
	mlThreshold := flags.Float64("ml-threshold", 0, "Optional ML shadow scoring threshold override")
	mlAdvisory := flags.Bool("ml-advisory", false, "Enable ML advisory mode (veto/downsize/upsize) on top of rules")
	mlAdvisoryMinProb := flags.Float64("ml-advisory-min-prob", 0, "Optional ML advisory minimum probability override")
	mlAdvisoryMaxVetos := flags.Int("ml-advisory-max-vetos", 0, "Optional ML advisory max daily vetos override")
	disableBearPressureLongBlock := flags.Bool("disable-bear-pressure-long-block", false, "Disable the bear-pressure long veto for comparison backtests")
	debugSymbols := flags.String("debug", "", "Comma-separated symbols to trace per-bar through scanner/strategy")
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
	if end.After(time.Now().Add(24 * time.Hour)) {
		return fmt.Errorf("this a backtest not a time machine, end time cannot be in the far future: %s", end.Format(time.RFC3339))
	}
	start, end, err = inferBacktestWindows(start, end, endDateOnly, *dataPath == "")
	if err != nil {
		return err
	}
	log.Printf("Backtest window start=%s end=%s", formatLogTime(start), formatLogTime(end))
	configuredSymbols := configuredUniverseSymbols()
	if len(configuredSymbols) > 0 {
		log.Printf("Backtest constrained to configured symbols=%d", len(configuredSymbols))
	}

	cfg := config.DefaultTradingConfig()
	profilePath := config.ResolveTradingProfilePath(os.Getenv("TRADING_PROFILE_PATH"))

	logDir := os.Getenv("BACKTEST_LOG_DIR")
	if logDir == "" {
		logDir = filepath.Join(".", "logs")
	}
	fsRecorder, fsErr := storage.NewFilesystemRecorder(context.Background(), logDir)
	if fsErr != nil {
		log.Printf("Backtest filesystem recorder disabled: %v", fsErr)
		fsRecorder = nil
	} else {
		log.Printf("Backtest logs writing to %s", logDir)
	}
	recorder := fsRecorder
	if *candidateOut != "" {
		candidateRecorder, err := storage.NewCandidateEvaluationFileRecorder(*candidateOut)
		if err != nil {
			return err
		}
		if recorder != nil {
			recorder = telemetry.NewCompositeRecorder(recorder, candidateRecorder)
		} else {
			recorder = candidateRecorder
		}
		log.Printf("Backtest candidate evaluations writing to %s", *candidateOut)
	}

	// Load float data for backtest tick enrichment
	floatStore := alpaca.NewFloatStore()
	if _, err := floatStore.LoadOrFetchFloatData(context.Background()); err != nil {
		log.Printf("Backtest float data warning (SEC EDGAR): %v", err)
	}

	runCfg := backtest.RunConfig{
		DataPath:   *dataPath,
		Start:      start,
		End:        end,
		Recorder:   recorder,
		FloatStore: floatStore,
	}
	if *debugSymbols != "" {
		for _, sym := range strings.Split(*debugSymbols, ",") {
			sym = strings.TrimSpace(sym)
			if sym != "" {
				runCfg.DebugSymbols = append(runCfg.DebugSymbols, sym)
			}
		}
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
			log.Printf("Backtest using Alpaca feed=%s historical_limit=%d/min", client.DataFeed(), capabilities.HistoricalRateLimitPerMin)
		} else {
			log.Printf("Backtest capability detection failed, using defaults: %v", capErr)
		}
		// Backtests must start from the profile/default capital, not the live broker
		// account balance, otherwise ROI and sizing depend on whatever happened in the
		// paper/live account outside the historical window.
		cfg = config.TuneTradingConfig(cfg, cfg.StartingCapital, float64(historicalRateLimit))
		universe, err := resolveBacktestSymbols(setupCtx, client, end, configuredSymbols)
		if err != nil {
			return err
		}
		runCfg.BlockedSymbols = universe.BlockedSymbols
		runCfg.EasyToBorrow = universe.EasyToBorrow
		// Go back 3 calendar days so the warmup window includes at least one
		// prior trading day even when the backtest starts on Monday (−1 only
		// reaches Sunday, which the weekend filter skips).
		prevDayStart := start.AddDate(0, 0, -3)
		fetchTimeout := estimateHistoricalFetchTimeout(len(universe.Symbols), prevDayStart, end, historicalRateLimit)
		log.Printf("Historical fetch timeout set to %s", fetchTimeout)
		log.Printf("Historical fetch coverage start=%s end=%s", formatLogTime(prevDayStart), formatLogTime(end))
		fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer fetchCancel()
		dataset, err := prepareHistoricalDataset(fetchCtx, client, universe.Symbols, prevDayStart, end, historicalRateLimit)
		if err != nil {
			return err
		}
		runCfg.Iterator = newHistoricalDatasetIterator(dataset)
		log.Printf(
			"Historical dataset ready shards=%d symbols=%d easy_to_borrow=%d",
			len(dataset.jobs),
			len(universe.Symbols),
			len(universe.EasyToBorrow),
		)
	}
	profileLabel := ""
	if profilePath != "" {
		cfg, profileLabel, err = applyConfiguredTradingProfile(cfg, profilePath)
		if err != nil {
			return err
		}
		if profileLabel != "" {
			log.Printf("Backtest loaded trading profile %s", profileLabel)
		}
	} else {
		log.Printf("Backtest using broker-tuned baseline config (no bundled trading profile found)")
	}
	if *mlModelPath != "" {
		cfg.MLScoringEnabled = true
		cfg.MLModelPath = *mlModelPath
	}
	if *mlThreshold > 0 {
		cfg.MLScoringEnabled = true
		cfg.MLScoringThreshold = *mlThreshold
	}
	if *mlAdvisory {
		cfg.MLScoringEnabled = true
		cfg.MLAdvisoryEnabled = true
	}
	if *mlAdvisoryMinProb > 0 {
		cfg.MLScoringEnabled = true
		cfg.MLAdvisoryEnabled = true
		cfg.MLAdvisoryMinProb = *mlAdvisoryMinProb
	}
	if *mlAdvisoryMaxVetos > 0 {
		cfg.MLAdvisoryEnabled = true
		cfg.MLAdvisoryMaxVetosPerDay = *mlAdvisoryMaxVetos
	}
	if *disableBearPressureLongBlock {
		cfg.DisableBearPressureLongBlock = true
	}
	if cfg.MLScoringEnabled {
		log.Printf("Backtest ML shadow scoring enabled model=%s threshold=%.4f", cfg.MLModelPath, cfg.MLScoringThreshold)
	}
	if cfg.MLAdvisoryEnabled {
		log.Printf(
			"Backtest ML advisory enabled min_prob=%.4f max_vetos_per_day=%d veto=%t downsize=%t upsize=%t",
			cfg.MLAdvisoryMinProb,
			cfg.MLAdvisoryMaxVetosPerDay,
			cfg.MLAdvisoryVetoEnabled,
			cfg.MLAdvisoryDownsizeEnabled,
			cfg.MLAdvisoryUpsizeEnabled,
		)
	}
	logBacktestConfig(cfg)

	result, err := backtest.Run(context.Background(), cfg, runCfg)
	if err != nil {
		return err
	}
	logBacktestDiagnostics(result.Diagnostics)
	logBacktestSummary(start, end, result)
	logClosedTradeSamples(result.ClosedTrades)
	if err := writeBacktestReport(*reportOut, result); err != nil {
		return err
	}
	return nil
}

func inferBacktestWindows(start, end time.Time, endDateOnly, requireStart bool) (time.Time, time.Time, error) {
	now := time.Now().In(markethours.Location())
	if end.IsZero() {
		end = defaultBacktestEnd(now)
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

func defaultBacktestEnd(now time.Time) time.Time {
	local := now.In(markethours.Location())
	return local.Truncate(time.Hour)
}

type backtestUniverse struct {
	Symbols        []string
	BlockedSymbols map[string]string
	EasyToBorrow   map[string]bool
}

func resolveBacktestSymbols(ctx context.Context, client *alpaca.Client, backtestEnd time.Time, configured []string) (backtestUniverse, error) {
	type symbolCache struct {
		Version             int      `json:"version"`
		Date                string   `json:"date"`
		Symbols             []string `json:"symbols"`
		EasyToBorrowSymbols []string `json:"easyToBorrowSymbols,omitempty"`
	}

	const symbolCacheVersion = 3
	cachePath := filepath.Join(".cache", "backtest", "symbols.json")

	// Try to load cached symbols if backtest end date is not after cache date.
	if raw, err := os.ReadFile(cachePath); err == nil {
		var cached symbolCache
		if err := json.Unmarshal(raw, &cached); err == nil && len(cached.Symbols) > 0 {
			if cached.Version == symbolCacheVersion {
				if cacheDate, err := time.Parse("2006-01-02", cached.Date); err == nil {
					if !backtestEnd.After(cacheDate.AddDate(0, 0, 1)) {
						easyToBorrow := sliceToSymbolSet(cached.EasyToBorrowSymbols)
						if len(configured) > 0 {
							log.Printf("Using cached symbol metadata for configured universe (%d symbols, cached %s)", len(configured), cached.Date)
							return backtestUniverse{
								Symbols:      append([]string(nil), configured...),
								EasyToBorrow: filterSymbolSet(easyToBorrow, configured),
							}, nil
						}
						log.Printf("Using cached symbol list (%d symbols, cached %s)", len(cached.Symbols), cached.Date)
						return backtestUniverse{
							Symbols:      cached.Symbols,
							EasyToBorrow: easyToBorrow,
						}, nil
					}
				}
			}
		}
	}

	assets, err := client.ListEquityAssets(ctx, false)
	if err != nil {
		return backtestUniverse{}, err
	}
	allSymbols, allBlockedSymbols := filterScannerUniverseAssets(assets, nil)
	symbols, blockedSymbols := filterScannerUniverseAssets(assets, configured)
	easyToBorrow := easyToBorrowSymbolSet(assets)

	// Write cache.
	cache := symbolCache{
		Version:             symbolCacheVersion,
		Date:                backtestEnd.Format("2006-01-02"),
		Symbols:             allSymbols,
		EasyToBorrowSymbols: sortedSymbolKeys(easyToBorrow),
	}
	if data, err := json.Marshal(cache); err == nil {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		_ = os.WriteFile(cachePath, data, 0o644)
		log.Printf(
			"Cached %d symbols for date %s (easy_to_borrow=%d blocked %d ETF/derivative instruments)",
			len(allSymbols),
			cache.Date,
			len(easyToBorrow),
			len(allBlockedSymbols),
		)
	}

	return backtestUniverse{
		Symbols:        symbols,
		BlockedSymbols: blockedSymbols,
		EasyToBorrow:   filterSymbolSet(easyToBorrow, symbols),
	}, nil
}

func configuredUniverseSymbols() []string {
	raw := strings.TrimSpace(os.Getenv("ALPACA_SYMBOLS"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		symbol := strings.ToUpper(strings.TrimSpace(part))
		if symbol == "" {
			continue
		}
		if _, exists := seen[symbol]; exists {
			continue
		}
		seen[symbol] = struct{}{}
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}

func filterSymbolSet(symbols map[string]bool, allowed []string) map[string]bool {
	if len(symbols) == 0 || len(allowed) == 0 {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(allowed))
	for _, symbol := range allowed {
		if symbols[symbol] {
			out[symbol] = true
		}
	}
	return out
}

func easyToBorrowSymbolSet(assets []alpaca.EquityAsset) map[string]bool {
	out := make(map[string]bool)
	for _, asset := range assets {
		if asset.Shortable && asset.EasyToBorrow {
			out[asset.Symbol] = true
		}
	}
	return out
}

func sortedSymbolKeys(symbols map[string]bool) []string {
	out := make([]string, 0, len(symbols))
	for symbol, allowed := range symbols {
		if allowed {
			out = append(out, symbol)
		}
	}
	sort.Strings(out)
	return out
}

func sliceToSymbolSet(symbols []string) map[string]bool {
	out := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		if symbol == "" {
			continue
		}
		out[symbol] = true
	}
	return out
}

func parseCLIBacktestTime(value string) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	if dateOnlyPattern.MatchString(strings.TrimSpace(value)) {
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), markethours.Location())
		if err != nil {
			return time.Time{}, true, fmt.Errorf("unsupported date format %q", value)
		}
		return parsed, true, nil
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, value, markethours.Location())
		if err == nil {
			return parsed, false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unsupported date format %q", value)
}

func endOfMarketDay(value time.Time) time.Time {
	local := value.In(markethours.Location())
	return time.Date(local.Year(), local.Month(), local.Day(), 20, 0, 0, 0, markethours.Location())
}

func sameMarketDay(a, b time.Time) bool {
	al := a.In(markethours.Location())
	bl := b.In(markethours.Location())
	return al.Year() == bl.Year() && al.Month() == bl.Month() && al.Day() == bl.Day()
}

func formatLogTime(value time.Time) string {
	if value.IsZero() {
		return "n/a"
	}
	return value.In(markethours.Location()).Format(time.RFC3339)
}

func logBacktestConfig(cfg config.TradingConfig) {
	log.Printf(
		"Backtest config shorts_enabled=%t max_short_open=%d max_short_exposure=%.2f short_min_score=%.2f min_price=%.2f min_gap=%.2f min_rel_volume=%.2f min_premarket=%d min_score=%.2f min_1m=%.2f min_3m=%.2f min_volume_rate=%.2f risk_per_trade=%.4f max_trades=%d max_open=%d max_exposure=%.2f trail_activation_r=%.2f trail_atr_mult=%.2f tight_trail_trigger_r=%.2f tight_trail_atr_mult=%.2f profit_target_r=%.2f disable_bear_pressure_long_block=%t",
		cfg.EnableShorts,
		cfg.MaxShortOpenPositions,
		cfg.MaxShortExposurePct,
		cfg.ShortMinEntryScore,
		cfg.MinPrice,
		cfg.MinGapPercent,
		cfg.MinRelativeVolume,
		cfg.MinPremarketVolume,
		cfg.MinEntryScore,
		cfg.MinOneMinuteReturnPct,
		cfg.MinThreeMinuteReturnPct,
		cfg.MinVolumeRate,
		cfg.RiskPerTradePct,
		cfg.MaxTradesPerDay,
		cfg.MaxOpenPositions,
		cfg.MaxExposurePct,
		cfg.TrailActivationR,
		cfg.TrailATRMultiplier,
		cfg.TightTrailTriggerR,
		cfg.TightTrailATRMultiplier,
		cfg.ProfitTargetR,
		cfg.DisableBearPressureLongBlock,
	)
}

func logBacktestDiagnostics(diag backtest.Diagnostics) {
	log.Printf(
		"Backtest funnel bars_loaded=%d bars_in_window=%d entry_candidates=%d entry_signals=%d entry_risk_approved=%d fill_expiries=%d exit_checks=%d exit_signals=%d exit_risk_approved=%d",
		diag.BarsLoaded,
		diag.BarsInWindow,
		diag.EntryCandidates,
		diag.EntrySignals,
		diag.EntryRiskApproved,
		diag.FillExpiries,
		diag.ExitChecks,
		diag.ExitSignals,
		diag.ExitRiskApproved,
	)
	if diag.MLShadowScored > 0 {
		log.Printf("Backtest ML shadow scored=%d vetos=%d upsizes=%d", diag.MLShadowScored, diag.MLShadowVetos, diag.MLShadowUpsizes)
		logMLShadowSamples(diag.MLShadowSamples)
	}
	if diag.MLAdvisoryEvaluated > 0 {
		log.Printf(
			"Backtest ML advisory evaluated=%d applied=%d vetos=%d downsizes=%d upsizes=%d",
			diag.MLAdvisoryEvaluated,
			diag.MLAdvisoryApplied,
			diag.MLAdvisoryVetos,
			diag.MLAdvisoryDownsizes,
			diag.MLAdvisoryUpsizes,
		)
		logMLAdvisorySamples(diag.MLAdvisorySamples)
	}
	logReasonCounts("scanner rejects", diag.ScannerRejects, diag.BarsInWindow)
	logReasonCounts("strategy entry rejects", diag.EntryRejects, diag.EntryCandidates)
	logReasonCounts("risk entry rejects", diag.EntryRiskRejects, diag.EntrySignals)
	logReasonCounts("strategy exit rejects", diag.ExitRejects, diag.ExitChecks)
	logReasonCounts("risk exit rejects", diag.ExitRiskRejects, diag.ExitSignals)
	logEntrySamples(diag.EntrySignalSamples)
	logEntryRejectSamples(diag)
	logRiskRejectSamples(diag.RiskRejectSamples)
	logFillExpirySamples(diag.FillExpirySamples)
}

func logMLShadowSamples(samples []backtest.MLShadowSample) {
	if len(samples) == 0 {
		return
	}
	parts := make([]string, 0, len(samples))
	for _, sample := range samples {
		parts = append(parts, fmt.Sprintf(
			"%s@%s prob=%.3f thresh=%.3f decision=%s day_rank=%d bar_rank=%d setup=%s strategy=%s emitted=%t risk=%t",
			sample.Symbol,
			sample.Timestamp.In(markethours.Location()).Format("2006-01-02 15:04"),
			sample.Probability,
			sample.Threshold,
			sample.Decision,
			sample.DayRankSoFar,
			sample.BarRankSoFar,
			sample.SetupType,
			sample.StrategyReason,
			sample.StrategyEmitted,
			sample.RiskApproved,
		))
	}
	log.Printf("Backtest ML shadow samples %s", strings.Join(parts, " | "))
}

func logMLAdvisorySamples(samples []backtest.MLAdvisorySample) {
	if len(samples) == 0 {
		return
	}
	parts := make([]string, 0, len(samples))
	for _, sample := range samples {
		parts = append(parts, fmt.Sprintf(
			"%s@%s prob=%.3f thresh=%.3f decision=%s qty=%d->%d mult=%.2f setup=%s strategy=%s risk=%t",
			sample.Symbol,
			sample.Timestamp.In(markethours.Location()).Format("2006-01-02 15:04"),
			sample.Probability,
			sample.Threshold,
			sample.Decision,
			sample.OriginalQuantity,
			sample.AdjustedQuantity,
			sample.SizeMultiplier,
			sample.SetupType,
			sample.StrategyReason,
			sample.RiskApproved,
		))
	}
	log.Printf("Backtest ML advisory samples %s", strings.Join(parts, " | "))
}

func logBacktestSummary(start, end time.Time, result backtest.Result) {
	for _, line := range backtestSummaryLines(start, end, result) {
		log.Print(line)
	}
}

func backtestSummaryLines(start, end time.Time, result backtest.Result) []string {
	lines := []string{
		"Backtest Summary",
		fmt.Sprintf("  Window       %s -> %s", formatLogTime(start), formatLogTime(end)),
		fmt.Sprintf("  PnL          roi=%.0f%% net=%s realized=%s unrealized=%s ending_equity=%s max_drawdown=%.2f%%",
			result.NetPnL/result.StartingCapital*100,
			formatMoney(result.NetPnL),
			formatMoney(result.RealizedPnL),
			formatMoney(result.UnrealizedPnL),
			formatMoney(result.EndingEquity),
			result.MaxDrawdownPct,
		),
		fmt.Sprintf("  Trades       entries=%d closed=%d wins=%d losses=%d win_rate=%.2f%% profit_factor=%.2f open_positions=%d",
			result.EntriesExecuted,
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
	if len(result.OpenSymbols) > 0 {
		lines = append(lines, fmt.Sprintf("  Open Symbols %s", strings.Join(result.OpenSymbols, ", ")))
	}
	lines = append(lines, formatBreakdownLines("  Regimes", result.Diagnostics.ByRegime)...)
	lines = append(lines, formatBreakdownLines("  Setups", result.Diagnostics.BySetup)...)
	lines = append(lines, formatBreakdownLines("  Sides", result.Diagnostics.BySide)...)
	return lines
}

func formatBreakdownLines(title string, breakdowns map[string]backtest.TradeBreakdown) []string {
	if len(breakdowns) == 0 {
		return nil
	}
	keys := make([]string, 0, len(breakdowns))
	for key := range breakdowns {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []string{title}
	for _, key := range keys {
		summary := breakdowns[key]
		lines = append(lines, fmt.Sprintf("    %s trades=%d wins=%d losses=%d pnl=%s",
			key,
			summary.Trades,
			summary.Wins,
			summary.Losses,
			formatMoney(summary.NetPnL),
		))
	}
	return lines
}

func writeBacktestReport(path string, result backtest.Result) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	log.Printf("Backtest report written to %s", path)
	return nil
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
	if limit > 15 {
		limit = 15
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
			sample.Timestamp.In(markethours.Location()).Format("2006-01-02 15:04"),
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
	if limit > 15 {
		limit = 15
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
			sample.Timestamp.In(markethours.Location()).Format("2006-01-02 15:04"),
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
		side := trade.Side
		if side == "" {
			side = domain.DirectionLong
		}
		log.Printf("  %s side=%s qty=%d entry=%s exit=%s pnl=%s reason=%s opened=%s closed=%s",
			trade.Symbol,
			side,
			trade.Quantity,
			formatMoney(trade.EntryPrice),
			formatMoney(trade.ExitPrice),
			formatMoney(trade.PnL),
			trade.ExitReason,
			trade.OpenedAt.In(markethours.Location()).Format("2006-01-02 15:04"),
			trade.ClosedAt.In(markethours.Location()).Format("2006-01-02 15:04"),
		)
	}
}

func logRiskRejectSamples(samples []backtest.RiskRejectSample) {
	if len(samples) == 0 {
		return
	}
	log.Printf("Risk-rejected signals (%d):", len(samples))
	for _, s := range samples {
		log.Printf("  %s@%s side=%s price=%.2f qty=%d score=%.2f setup=%s reason=%s",
			s.Symbol,
			s.Timestamp.In(markethours.Location()).Format("2006-01-02 15:04"),
			s.Side,
			s.Price,
			s.Quantity,
			s.Score,
			s.SetupType,
			s.Reason,
		)
	}
}

func logFillExpirySamples(samples []backtest.FillExpirySample) {
	if len(samples) == 0 {
		return
	}
	log.Printf("Fill expirations (%d):", len(samples))
	for _, s := range samples {
		log.Printf("  %s side=%s limit=%.2f qty=%d setup=%s ordered=%s expired=%s",
			s.Symbol,
			s.Side,
			s.LimitPrice,
			s.Quantity,
			s.SetupType,
			s.OrderTime.In(markethours.Location()).Format("2006-01-02 15:04"),
			s.ExpiryTime.In(markethours.Location()).Format("2006-01-02 15:04"),
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
