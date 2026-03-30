package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/ml"
)

func runTrainML(args []string) error {
	flags := flag.NewFlagSet("train-ml", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	inputPaths := flags.String("in", "", "Comma-separated labeled candidate JSONL paths")
	manifestPath := flags.String("manifest", "", "Optional prepared ML dataset manifest JSON path")
	outDir := flags.String("out-dir", "", "Required output directory for model artifacts and report")
	trainDays := flags.Int("train-days", 3, "Rolling training window size in trading days")
	validDays := flags.Int("valid-days", 1, "Rolling validation window size in trading days")
	purgeDays := flags.Int("purge-days", 1, "Purge gap between train and validation windows in trading days")
	stepDays := flags.Int("step-days", 1, "Rolling window step size in trading days")
	sampleScope := flags.String("sample-scope", "all", "Training sample scope: all, trade-linked, or risk-approved")
	sampleScopeGrid := flags.String("sample-scope-grid", "", "Optional comma-separated training sample scopes to train and compare sequentially")
	minTrainSamples := flags.Int("min-train-samples", 50, "Minimum samples required to train a side model in a window")
	minValidSamples := flags.Int("min-valid-samples", 20, "Minimum validation samples required to score a side window")
	epochs := flags.Int("epochs", 50, "Logistic SGD epochs")
	learningRate := flags.Float64("learning-rate", 0.05, "Logistic SGD learning rate")
	l2 := flags.Float64("l2", 0.0001, "L2 regularization strength")
	calibrationBins := flags.Int("calibration-bins", 10, "Probability calibration bin count")
	regressionSuitePath := flags.String("regression-suite", "", "Optional ML regression suite JSON path for post-train rules-only vs advisory evaluation")
	regressionOutDir := flags.String("regression-out-dir", "", "Optional output directory for regression comparison artifacts (defaults to out-dir/regression)")
	regressionWindows := flags.String("regression-windows", "", "Optional comma-separated regression window IDs to run; defaults to all suite windows")
	regressionProfile := flags.String("regression-profile", "", "Optional trading profile path override for regression backtests")
	regressionAdvisory := flags.Bool("regression-advisory", true, "Run ML advisory comparison after training when regression-suite is set")
	regressionAdvisoryMinProb := flags.Float64("regression-advisory-min-prob", 0, "Optional ML advisory minimum probability override for regression runs")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*outDir) == "" {
		return fmt.Errorf("out-dir is required")
	}
	paths := splitCSV(*inputPaths)
	if strings.TrimSpace(*manifestPath) != "" {
		manifestPaths, err := loadLabeledPathsFromManifest(*manifestPath)
		if err != nil {
			return err
		}
		paths = append(paths, manifestPaths...)
	}
	if len(paths) == 0 {
		return fmt.Errorf("at least one labeled input path or manifest is required")
	}
	paths = uniqueSortedStrings(paths)

	rows, err := ml.LoadLabeledCandidateRows(paths)
	if err != nil {
		return err
	}
	cfg := ml.TrainingConfig{
		WindowSpec: ml.RollingWindowSpec{
			TrainDays: *trainDays,
			ValidDays: *validDays,
			PurgeDays: *purgeDays,
			StepDays:  *stepDays,
		},
		SampleScope:     *sampleScope,
		MinTrainSamples: *minTrainSamples,
		MinValidSamples: *minValidSamples,
		Epochs:          *epochs,
		LearningRate:    *learningRate,
		L2:              *l2,
		CalibrationBins: *calibrationBins,
	}
	regressionCfg := (*mlRegressionConfig)(nil)
	if strings.TrimSpace(*regressionSuitePath) != "" {
		runOutDir := strings.TrimSpace(*regressionOutDir)
		if runOutDir == "" {
			runOutDir = filepath.Join(*outDir, "regression")
		}
		regressionCfg = &mlRegressionConfig{
			SuitePath:         *regressionSuitePath,
			OutDir:            runOutDir,
			ProfilePath:       strings.TrimSpace(*regressionProfile),
			WindowIDs:         splitCSV(*regressionWindows),
			RunAdvisory:       *regressionAdvisory,
			AdvisoryMinProb:   *regressionAdvisoryMinProb,
			AdvisoryThreshold: 0,
		}
	}

	scopeGrid := normalizeSampleScopeList(splitCSV(*sampleScopeGrid))
	if len(scopeGrid) > 0 {
		return runTrainMLScopeGrid(rows, paths, *outDir, cfg, regressionCfg, scopeGrid)
	}

	if _, _, err := runTrainMLForScope(rows, paths, *outDir, cfg, regressionCfg); err != nil {
		return err
	}
	return nil
}

type trainMLScopeRunSummary struct {
	Scope                 string               `json:"scope"`
	NormalizedScope       string               `json:"normalizedScope"`
	ModelDir              string               `json:"modelDir"`
	TrainingReportPath    string               `json:"trainingReportPath"`
	RegressionSummaryPath string               `json:"regressionSummaryPath,omitempty"`
	TrainingReport        ml.TrainingReport    `json:"trainingReport"`
	RegressionSummary     *mlRegressionSummary `json:"regressionSummary,omitempty"`
}

type mlScopeComparisonSummary struct {
	CreatedAt        time.Time                     `json:"createdAt"`
	SuitePath        string                        `json:"suitePath,omitempty"`
	GuardrailsPath   string                        `json:"guardrailsPath,omitempty"`
	RecommendedScope string                        `json:"recommendedScope,omitempty"`
	Scopes           []mlScopeComparisonScopeEntry `json:"scopes"`
}

type mlScopeComparisonScopeEntry struct {
	Scope                 string              `json:"scope"`
	ModelDir              string              `json:"modelDir"`
	TrainingReportPath    string              `json:"trainingReportPath"`
	RegressionSummaryPath string              `json:"regressionSummaryPath,omitempty"`
	MustPassTotal         int                 `json:"mustPassTotal"`
	MustPassPassed        int                 `json:"mustPassPassed"`
	PassedAllMustPass     bool                `json:"passedAllMustPass"`
	NetDeltaTotal         float64             `json:"netDeltaTotal"`
	RegressionChecks      []mlRegressionCheck `json:"regressionChecks"`
}

type mlRegressionCheck struct {
	WindowID        string   `json:"windowId"`
	Passed          bool     `json:"passed"`
	Required        bool     `json:"required"`
	Reasons         []string `json:"reasons,omitempty"`
	RulesOnlyNetPnL float64  `json:"rulesOnlyNetPnL"`
	AdvisoryNetPnL  float64  `json:"advisoryNetPnL"`
	NetDelta        float64  `json:"netDelta"`
}

type mlArtifactGuardrails struct {
	RequiredRegressions map[string]mlRegressionGuardrail `json:"required_regressions"`
}

type mlRegressionGuardrail struct {
	MustPass                              bool    `json:"must_pass"`
	MinimumNetPnL                         float64 `json:"minimum_net_pnl,omitempty"`
	MinimumTradeCount                     int     `json:"minimum_trade_count,omitempty"`
	MaximumNetPnLDegradationVsBaselinePct float64 `json:"maximum_net_pnl_degradation_vs_baseline_pct,omitempty"`
	PrimaryObjective                      string  `json:"primary_objective,omitempty"`
	MaxDrawdownPctCeiling                 float64 `json:"max_drawdown_pct_ceiling,omitempty"`
}

func runTrainMLScopeGrid(
	rows []ml.LabeledCandidateRow,
	paths []string,
	outDir string,
	cfg ml.TrainingConfig,
	regressionCfg *mlRegressionConfig,
	scopeGrid []string,
) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	runs := make([]trainMLScopeRunSummary, 0, len(scopeGrid))
	for _, scope := range scopeGrid {
		scopeOutDir := filepath.Join(outDir, scope)
		runCfg := cfg
		runCfg.SampleScope = scope
		scopeRegressionCfg := cloneRegressionConfig(regressionCfg)
		if scopeRegressionCfg != nil {
			scopeRegressionCfg.OutDir = filepath.Join(outDir, "regression", scope)
		}
		runSummary, _, err := runTrainMLForScope(rows, paths, scopeOutDir, runCfg, scopeRegressionCfg)
		if err != nil {
			return err
		}
		runs = append(runs, runSummary)
	}

	comparison := summarizeScopeComparison(runs, regressionCfg)
	summaryPath := filepath.Join(outDir, "scope_selection_summary.json")
	data, err := json.MarshalIndent(comparison, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(summaryPath, data, 0o644); err != nil {
		return err
	}
	log.Printf("train-ml: wrote scope comparison summary=%s recommended=%s", summaryPath, comparison.RecommendedScope)
	return nil
}

func runTrainMLForScope(
	rows []ml.LabeledCandidateRow,
	paths []string,
	outDir string,
	cfg ml.TrainingConfig,
	regressionCfg *mlRegressionConfig,
) (trainMLScopeRunSummary, map[string]ml.LogisticModelArtifact, error) {
	samples := ml.ExtractTrainingSamples(rows, cfg.SampleScope)
	report, models, err := ml.RunRollingWindowTraining(samples, cfg)
	if err != nil {
		return trainMLScopeRunSummary{}, nil, err
	}
	report.InputPaths = paths
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return trainMLScopeRunSummary{}, nil, err
	}
	if err := ml.SaveTrainingArtifacts(outDir, &report, models); err != nil {
		return trainMLScopeRunSummary{}, nil, err
	}

	runSummary := trainMLScopeRunSummary{
		Scope:              cfg.SampleScope,
		NormalizedScope:    normalizeSampleScopeLabel(cfg.SampleScope),
		ModelDir:           outDir,
		TrainingReportPath: filepath.Join(outDir, "training_report.json"),
		TrainingReport:     report,
	}
	log.Printf(
		"train-ml: wrote report=%s scope=%s long_windows=%d short_windows=%d",
		runSummary.TrainingReportPath,
		runSummary.NormalizedScope,
		len(report.SideReports["long"].Windows),
		len(report.SideReports["short"].Windows),
	)

	if regressionCfg != nil {
		runCfg := *regressionCfg
		runCfg.ModelPath = outDir
		if err := runMLRegressionComparison(runCfg); err != nil {
			return trainMLScopeRunSummary{}, nil, err
		}
		summaryPath := filepath.Join(runCfg.OutDir, "regression_summary.json")
		summary, err := loadMLRegressionSummary(summaryPath)
		if err != nil {
			return trainMLScopeRunSummary{}, nil, err
		}
		runSummary.RegressionSummaryPath = summaryPath
		runSummary.RegressionSummary = &summary
	}

	return runSummary, models, nil
}

func cloneRegressionConfig(cfg *mlRegressionConfig) *mlRegressionConfig {
	if cfg == nil {
		return nil
	}
	cloned := *cfg
	cloned.WindowIDs = append([]string(nil), cfg.WindowIDs...)
	return &cloned
}

type mlRegressionConfig struct {
	SuitePath         string
	OutDir            string
	ProfilePath       string
	ModelPath         string
	WindowIDs         []string
	RunAdvisory       bool
	AdvisoryMinProb   float64
	AdvisoryThreshold float64
}

type mlRegressionSuite struct {
	Version int                       `json:"version"`
	Windows []mlRegressionSuiteWindow `json:"windows"`
}

type mlRegressionSuiteWindow struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Start        string   `json:"start"`
	End          string   `json:"end"`
	DebugSymbols []string `json:"debug_symbols"`
	Purpose      string   `json:"purpose"`
}

type mlRegressionSummary struct {
	CreatedAt   time.Time                   `json:"createdAt"`
	SuitePath   string                      `json:"suitePath"`
	ProfilePath string                      `json:"profilePath,omitempty"`
	ModelPath   string                      `json:"modelPath"`
	WindowIDs   []string                    `json:"windowIds"`
	Comparisons []mlRegressionWindowSummary `json:"comparisons"`
}

type mlRegressionWindowSummary struct {
	ID        string                `json:"id"`
	Purpose   string                `json:"purpose"`
	Start     string                `json:"start"`
	End       string                `json:"end"`
	RulesOnly mlRegressionRunResult `json:"rulesOnly"`
	Advisory  mlRegressionRunResult `json:"advisory,omitempty"`
}

type mlRegressionRunResult struct {
	Mode                string  `json:"mode"`
	ReportPath          string  `json:"reportPath"`
	NetPnL              float64 `json:"netPnL"`
	ROI                 float64 `json:"roi"`
	Trades              int     `json:"trades"`
	WinRate             float64 `json:"winRate"`
	MaxDrawdownPct      float64 `json:"maxDrawdownPct"`
	ProfitFactor        float64 `json:"profitFactor"`
	EntrySignals        int     `json:"entrySignals"`
	EntryRiskApproved   int     `json:"entryRiskApproved"`
	MLAdvisoryEvaluated int     `json:"mlAdvisoryEvaluated"`
	MLAdvisoryApplied   int     `json:"mlAdvisoryApplied"`
	MLAdvisoryVetos     int     `json:"mlAdvisoryVetos"`
	MLAdvisoryUpsizes   int     `json:"mlAdvisoryUpsizes"`
	MLAdvisoryDownsizes int     `json:"mlAdvisoryDownsizes"`
}

func runMLRegressionComparison(cfg mlRegressionConfig) error {
	suite, err := loadMLRegressionSuite(cfg.SuitePath)
	if err != nil {
		return err
	}
	windows := filterMLRegressionWindows(suite.Windows, cfg.WindowIDs)
	if len(windows) == 0 {
		return fmt.Errorf("no regression windows selected")
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}

	profilePath := cfg.ProfilePath
	if profilePath == "" {
		profilePath = config.ResolveTradingProfilePath(os.Getenv("TRADING_PROFILE_PATH"))
	}

	summary := mlRegressionSummary{
		CreatedAt:   time.Now().In(markethours.Location()),
		SuitePath:   cfg.SuitePath,
		ProfilePath: profilePath,
		ModelPath:   cfg.ModelPath,
		WindowIDs:   make([]string, 0, len(windows)),
		Comparisons: make([]mlRegressionWindowSummary, 0, len(windows)),
	}

	for _, window := range windows {
		summary.WindowIDs = append(summary.WindowIDs, window.ID)
		log.Printf("train-ml regression: running window=%s %s..%s mode=rules-only", window.ID, window.Start, window.End)
		rulesOnly, err := runMLRegressionWindow(window, profilePath, "", false, 0, cfg.OutDir)
		if err != nil {
			return err
		}
		comparison := mlRegressionWindowSummary{
			ID:        window.ID,
			Purpose:   window.Purpose,
			Start:     window.Start,
			End:       window.End,
			RulesOnly: rulesOnly,
		}
		if cfg.RunAdvisory {
			log.Printf("train-ml regression: running window=%s %s..%s mode=advisory", window.ID, window.Start, window.End)
			advisory, err := runMLRegressionWindow(window, profilePath, cfg.ModelPath, true, cfg.AdvisoryMinProb, cfg.OutDir)
			if err != nil {
				return err
			}
			comparison.Advisory = advisory
		}
		summary.Comparisons = append(summary.Comparisons, comparison)
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	summaryPath := filepath.Join(cfg.OutDir, "regression_summary.json")
	if err := os.WriteFile(summaryPath, data, 0o644); err != nil {
		return err
	}
	log.Printf("train-ml regression: wrote summary=%s windows=%d", summaryPath, len(summary.Comparisons))
	return nil
}

func loadMLRegressionSuite(path string) (mlRegressionSuite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return mlRegressionSuite{}, err
	}
	var suite mlRegressionSuite
	if err := json.Unmarshal(raw, &suite); err != nil {
		return mlRegressionSuite{}, err
	}
	return suite, nil
}

func loadMLRegressionSummary(path string) (mlRegressionSummary, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return mlRegressionSummary{}, err
	}
	var summary mlRegressionSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return mlRegressionSummary{}, err
	}
	return summary, nil
}

func filterMLRegressionWindows(windows []mlRegressionSuiteWindow, ids []string) []mlRegressionSuiteWindow {
	if len(ids) == 0 {
		return windows
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[strings.TrimSpace(id)] = struct{}{}
	}
	filtered := make([]mlRegressionSuiteWindow, 0, len(ids))
	for _, window := range windows {
		if _, ok := allowed[window.ID]; ok {
			filtered = append(filtered, window)
		}
	}
	return filtered
}

func runMLRegressionWindow(
	window mlRegressionSuiteWindow,
	profilePath string,
	modelPath string,
	advisory bool,
	advisoryMinProb float64,
	outDir string,
) (mlRegressionRunResult, error) {
	start, _, err := parseCLIBacktestTime(window.Start)
	if err != nil {
		return mlRegressionRunResult{}, err
	}
	end, endDateOnly, err := parseCLIBacktestTime(window.End)
	if err != nil {
		return mlRegressionRunResult{}, err
	}
	start, end, err = inferBacktestWindows(start, end, endDateOnly, true)
	if err != nil {
		return mlRegressionRunResult{}, err
	}

	runCfg, cfg, err := buildRegressionBacktestConfig(start, end, profilePath, window.DebugSymbols)
	if err != nil {
		return mlRegressionRunResult{}, err
	}
	mode := "rules_only"
	slug := "rules_only"
	if advisory {
		mode = "ml_advisory"
		slug = "ml_advisory"
		cfg.MLScoringEnabled = true
		cfg.MLModelPath = modelPath
		cfg.MLAdvisoryEnabled = true
		if advisoryMinProb > 0 {
			cfg.MLAdvisoryMinProb = advisoryMinProb
		}
	}

	result, err := backtest.Run(context.Background(), cfg, runCfg)
	if err != nil {
		return mlRegressionRunResult{}, err
	}

	reportPath := filepath.Join(outDir, fmt.Sprintf("%s_%s.json", window.ID, slug))
	if err := writeBacktestReport(reportPath, result); err != nil {
		return mlRegressionRunResult{}, err
	}
	runResult := mlRegressionRunResult{
		Mode:                mode,
		ReportPath:          reportPath,
		NetPnL:              result.NetPnL,
		ROI:                 safeROIPct(result.NetPnL, result.StartingCapital),
		Trades:              result.Trades,
		WinRate:             result.WinRate,
		MaxDrawdownPct:      result.MaxDrawdownPct,
		ProfitFactor:        result.ProfitFactor,
		EntrySignals:        result.Diagnostics.EntrySignals,
		EntryRiskApproved:   result.Diagnostics.EntryRiskApproved,
		MLAdvisoryEvaluated: result.Diagnostics.MLAdvisoryEvaluated,
		MLAdvisoryApplied:   result.Diagnostics.MLAdvisoryApplied,
		MLAdvisoryVetos:     result.Diagnostics.MLAdvisoryVetos,
		MLAdvisoryUpsizes:   result.Diagnostics.MLAdvisoryUpsizes,
		MLAdvisoryDownsizes: result.Diagnostics.MLAdvisoryDownsizes,
	}
	return runResult, nil
}

func buildRegressionBacktestConfig(start, end time.Time, profilePath string, debugSymbols []string) (backtest.RunConfig, config.TradingConfig, error) {
	cfg := config.DefaultTradingConfig()

	floatStore := alpaca.NewFloatStore()
	if _, err := floatStore.LoadOrFetchFloatData(context.Background()); err != nil {
		log.Printf("train-ml regression: float data warning: %v", err)
	}

	setupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	alpacaCfg, err := config.LoadBacktestAlpacaConfig(nil)
	if err != nil {
		return backtest.RunConfig{}, config.TradingConfig{}, err
	}
	client := alpaca.NewClient(alpacaCfg, cfg)

	historicalRateLimit := 0
	if capabilities, capErr := client.DetectMarketDataCapabilities(setupCtx); capErr == nil {
		historicalRateLimit = capabilities.HistoricalRateLimitPerMin
	}

	universe, err := resolveBacktestSymbols(setupCtx, client, end, configuredUniverseSymbols())
	if err != nil {
		return backtest.RunConfig{}, config.TradingConfig{}, err
	}

	prevDayStart := start.AddDate(0, 0, -3)
	fetchTimeout := estimateHistoricalFetchTimeout(len(universe.Symbols), prevDayStart, end, historicalRateLimit)
	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer fetchCancel()

	dataset, err := prepareHistoricalDataset(fetchCtx, client, universe.Symbols, prevDayStart, end, historicalRateLimit)
	if err != nil {
		return backtest.RunConfig{}, config.TradingConfig{}, err
	}

	runCfg := backtest.RunConfig{
		Start:          start,
		End:            end,
		Iterator:       newHistoricalDatasetIterator(dataset),
		DebugSymbols:   append([]string(nil), debugSymbols...),
		FloatStore:     floatStore,
		BlockedSymbols: universe.BlockedSymbols,
		EasyToBorrow:   universe.EasyToBorrow,
	}
	sort.Strings(runCfg.DebugSymbols)
	return runCfg, cfg, nil
}

func safeROIPct(netPnL, startingCapital float64) float64 {
	if startingCapital == 0 {
		return 0
	}
	return netPnL / startingCapital * 100
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func loadLabeledPathsFromManifest(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest mlDatasetManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(manifest.Windows))
	for _, window := range manifest.Windows {
		if strings.TrimSpace(window.LabelPath) != "" {
			paths = append(paths, window.LabelPath)
		}
	}
	return paths, nil
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeSampleScopeList(scopes []string) []string {
	normalized := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = normalizeSampleScopeLabel(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		normalized = append(normalized, scope)
	}
	return normalized
}

func normalizeSampleScopeLabel(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "trade-linked", "trade_linked", "emitted", "strategy-emitted", "strategy_emitted":
		return "trade_linked"
	case "risk-approved", "risk_approved", "approved":
		return "risk_approved"
	case "all":
		return "all"
	default:
		return strings.ToLower(strings.TrimSpace(scope))
	}
}

func summarizeScopeComparison(runs []trainMLScopeRunSummary, regressionCfg *mlRegressionConfig) mlScopeComparisonSummary {
	summary := mlScopeComparisonSummary{
		CreatedAt: time.Now().In(markethours.Location()),
		Scopes:    make([]mlScopeComparisonScopeEntry, 0, len(runs)),
	}
	if regressionCfg != nil {
		summary.SuitePath = regressionCfg.SuitePath
		summary.GuardrailsPath = "docs/ml_artifact_guardrails.json"
	}

	var guardrails mlArtifactGuardrails
	if regressionCfg != nil {
		if loaded, err := loadMLArtifactGuardrails(summary.GuardrailsPath); err == nil {
			guardrails = loaded
		}
	}

	bestIndex := -1
	bestPassed := -1
	bestDelta := math.Inf(-1)
	bestPreference := math.Inf(-1)

	for i, run := range runs {
		entry := mlScopeComparisonScopeEntry{
			Scope:                 run.NormalizedScope,
			ModelDir:              run.ModelDir,
			TrainingReportPath:    run.TrainingReportPath,
			RegressionSummaryPath: run.RegressionSummaryPath,
			PassedAllMustPass:     true,
		}
		if run.RegressionSummary != nil {
			entry.RegressionChecks = evaluateRegressionChecks(*run.RegressionSummary, guardrails)
			for _, check := range entry.RegressionChecks {
				if check.Required {
					entry.MustPassTotal++
					if check.Passed {
						entry.MustPassPassed++
					} else {
						entry.PassedAllMustPass = false
					}
				}
				entry.NetDeltaTotal += check.NetDelta
			}
		} else {
			entry.PassedAllMustPass = false
		}
		if entry.MustPassTotal == 0 {
			entry.PassedAllMustPass = false
		}
		summary.Scopes = append(summary.Scopes, entry)

		preference := sampleScopePreference(entry.Scope)
		if entry.MustPassPassed > bestPassed ||
			(entry.MustPassPassed == bestPassed && entry.NetDeltaTotal > bestDelta) ||
			(entry.MustPassPassed == bestPassed && entry.NetDeltaTotal == bestDelta && preference > bestPreference) {
			bestIndex = i
			bestPassed = entry.MustPassPassed
			bestDelta = entry.NetDeltaTotal
			bestPreference = preference
		}
	}
	if bestIndex >= 0 {
		summary.RecommendedScope = summary.Scopes[bestIndex].Scope
	}
	return summary
}

func loadMLArtifactGuardrails(path string) (mlArtifactGuardrails, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return mlArtifactGuardrails{}, err
	}
	var guardrails mlArtifactGuardrails
	if err := json.Unmarshal(raw, &guardrails); err != nil {
		return mlArtifactGuardrails{}, err
	}
	return guardrails, nil
}

func evaluateRegressionChecks(summary mlRegressionSummary, guardrails mlArtifactGuardrails) []mlRegressionCheck {
	comparisons := make([]mlRegressionCheck, 0, len(summary.Comparisons))
	for _, comparison := range summary.Comparisons {
		check := mlRegressionCheck{
			WindowID:        comparison.ID,
			Passed:          true,
			Required:        false,
			RulesOnlyNetPnL: comparison.RulesOnly.NetPnL,
			AdvisoryNetPnL:  comparison.Advisory.NetPnL,
			NetDelta:        comparison.Advisory.NetPnL - comparison.RulesOnly.NetPnL,
		}
		rule, ok := guardrails.RequiredRegressions[comparison.ID]
		if !ok {
			comparisons = append(comparisons, check)
			continue
		}
		check.Required = rule.MustPass
		if comparison.Advisory.Mode == "" {
			check.Passed = false
			check.Reasons = append(check.Reasons, "missing-advisory-result")
			comparisons = append(comparisons, check)
			continue
		}
		if rule.MinimumNetPnL != 0 && comparison.Advisory.NetPnL < rule.MinimumNetPnL {
			check.Passed = false
			check.Reasons = append(check.Reasons, fmt.Sprintf("net_pnl %.2f < %.2f", comparison.Advisory.NetPnL, rule.MinimumNetPnL))
		}
		if rule.MinimumTradeCount != 0 && comparison.Advisory.Trades < rule.MinimumTradeCount {
			check.Passed = false
			check.Reasons = append(check.Reasons, fmt.Sprintf("trades %d < %d", comparison.Advisory.Trades, rule.MinimumTradeCount))
		}
		if rule.MaximumNetPnLDegradationVsBaselinePct > 0 {
			floor := comparison.RulesOnly.NetPnL - math.Abs(comparison.RulesOnly.NetPnL)*(rule.MaximumNetPnLDegradationVsBaselinePct/100)
			if comparison.Advisory.NetPnL < floor {
				check.Passed = false
				check.Reasons = append(check.Reasons, fmt.Sprintf("net_pnl %.2f below degradation floor %.2f", comparison.Advisory.NetPnL, floor))
			}
		}
		if rule.PrimaryObjective == "improve_net_pnl_vs_baseline" && comparison.Advisory.NetPnL <= comparison.RulesOnly.NetPnL {
			check.Passed = false
			check.Reasons = append(check.Reasons, fmt.Sprintf("net_pnl %.2f did not improve baseline %.2f", comparison.Advisory.NetPnL, comparison.RulesOnly.NetPnL))
		}
		if rule.MaxDrawdownPctCeiling > 0 && comparison.Advisory.MaxDrawdownPct > rule.MaxDrawdownPctCeiling {
			check.Passed = false
			check.Reasons = append(check.Reasons, fmt.Sprintf("max_drawdown %.2f > %.2f", comparison.Advisory.MaxDrawdownPct, rule.MaxDrawdownPctCeiling))
		}
		comparisons = append(comparisons, check)
	}
	return comparisons
}

func sampleScopePreference(scope string) float64 {
	switch normalizeSampleScopeLabel(scope) {
	case "risk_approved":
		return 3
	case "trade_linked":
		return 2
	case "all":
		return 1
	default:
		return 0
	}
}
