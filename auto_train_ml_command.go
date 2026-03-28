package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/ml"
	"github.com/edwintcloud/momentum-trading-bot/internal/storage"
)

type mlAutoRunFunc func(ctx context.Context, asOf time.Time, outDir string) (mlAutoRunReport, error)

type mlAutoScheduler struct {
	TargetModelDir string
	ArtifactDir    string
	Schedule       string
	DryRun         bool
	Guardrails     mlAutoPromotionGuardrails
	RunTraining    mlAutoRunFunc
}

type mlAutoRunReport struct {
	CreatedAt                time.Time              `json:"createdAt"`
	AsOf                     time.Time              `json:"asOf"`
	ProfilePath              string                 `json:"profilePath"`
	TargetModelDir           string                 `json:"targetModelDir"`
	CandidateModelDir        string                 `json:"candidateModelDir"`
	DatasetManifestPath      string                 `json:"datasetManifestPath"`
	TrainingReportPath       string                 `json:"trainingReportPath"`
	RegressionSummaryPath    string                 `json:"regressionSummaryPath"`
	AnnualSummaryPath        string                 `json:"annualSummaryPath"`
	CurrentAnnualSummaryPath string                 `json:"currentAnnualSummaryPath,omitempty"`
	CurrentRegressionPath    string                 `json:"currentRegressionPath,omitempty"`
	CandidateRegression      mlRegressionSummary    `json:"candidateRegression"`
	CurrentRegression        *mlRegressionSummary   `json:"currentRegression,omitempty"`
	CandidateAnnual          batchBacktestSummary   `json:"candidateAnnual"`
	CurrentAnnual            *batchBacktestSummary  `json:"currentAnnual,omitempty"`
	Validation               mlAutoValidationResult `json:"validation"`
}

type mlAutoValidationResult struct {
	Passed bool          `json:"passed"`
	Reason string        `json:"reason,omitempty"`
	Checks []mlAutoCheck `json:"checks"`
}

type mlAutoCheck struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

type mlPromotionMetadata struct {
	PromotedAt             time.Time             `json:"promotedAt"`
	TargetModelDir         string                `json:"targetModelDir"`
	SourceCandidateModel   string                `json:"sourceCandidateModelDir"`
	TrainingReportPath     string                `json:"trainingReportPath"`
	RegressionSummaryPath  string                `json:"regressionSummaryPath"`
	AnnualSummaryPath      string                `json:"annualSummaryPath"`
	CurrentAnnualSummary   string                `json:"currentAnnualSummaryPath,omitempty"`
	CurrentRegressionPath  string                `json:"currentRegressionSummaryPath,omitempty"`
	CandidateRegression    mlRegressionSummary   `json:"candidateRegression"`
	CandidateAnnual        batchBacktestSummary  `json:"candidateAnnual"`
	CurrentRegression      *mlRegressionSummary  `json:"currentRegression,omitempty"`
	CurrentAnnual          *batchBacktestSummary `json:"currentAnnual,omitempty"`
	Validation             mlAutoValidationResult `json:"validation"`
}

type mlAutoPromotionGuardrails struct {
	Spec               mlArtifactGuardrails
	RequireImprovement bool
}

type mlAutoTrainConfig struct {
	ProfilePath           string
	TargetModelDir        string
	GuardrailsPath        string
	CurrentAnnualSummary  string
	CurrentRegressionPath string
	DatasetStart          string
	DatasetEnd            string
	DatasetLookbackDays   int
	DatasetWindowDays     int
	DatasetStepDays       int
	DatasetMaxWindows     int
	DatasetIDPrefix       string
	DatasetPurpose        string
	LabelUpperPct         float64
	LabelLowerPct         float64
	LabelMaxBars          int
	TrainConfig           mlTrainingRunConfig
	RegressionSuitePath   string
	RegressionWindows     []string
	AdvisoryMinProb       float64
	AnnualStart           string
	AnnualEnd             string
	AnnualWindowDays      int
	AnnualStepDays        int
	AnnualMaxWindows      int
}

type mlTrainingRunConfig struct {
	TrainDays       int
	ValidDays       int
	PurgeDays       int
	StepDays        int
	SampleScope     string
	MinTrainSamples int
	MinValidSamples int
	Epochs          int
	LearningRate    float64
	L2              float64
	CalibrationBins int
}

func runAutoTrainML(args []string) error {
	fs := flag.NewFlagSet("auto-train-ml", flag.ContinueOnError)
	profilePath := fs.String("profile", config.ResolveTradingProfilePath(os.Getenv("TRADING_PROFILE_PATH")), "path to the active trading profile")
	targetModelDir := fs.String("target-model-dir", "", "target model directory to promote into; defaults to MLModelPath from the profile")
	schedule := fs.String("schedule", "weekly", "schedule: weekly or daily")
	outDir := fs.String("out", ".cache/mlauto", "ML automation artifact directory")
	guardrailsPath := fs.String("guardrails", "docs/ml_artifact_guardrails.json", "ML artifact guardrails JSON path")
	currentAnnualSummary := fs.String("current-annual-summary", "", "optional current promoted annual weekly summary path")
	currentRegressionPath := fs.String("current-regression-summary", "", "optional current promoted regression summary path")
	runNow := fs.Bool("now", false, "run immediately once, then continue on schedule")
	runOnce := fs.Bool("once", false, "run once and exit")
	dryRun := fs.Bool("dry-run", false, "do not promote even if guardrails pass")
	requireImprovement := fs.Bool("require-improvement", true, "require the candidate artifact to beat the current promoted model")
	datasetStart := fs.String("dataset-start", "", "optional explicit dataset start date in YYYY-MM-DD")
	datasetEnd := fs.String("dataset-end", "", "optional explicit dataset end date in YYYY-MM-DD")
	datasetLookbackDays := fs.Int("dataset-lookback-days", 240, "lookback window in calendar days for automatic dataset preparation")
	datasetWindowDays := fs.Int("dataset-window-days", 14, "calendar dataset window length in days")
	datasetStepDays := fs.Int("dataset-step-days", 28, "calendar dataset step length in days")
	datasetMaxWindows := fs.Int("dataset-max-windows", 0, "optional cap on automatic dataset windows; 0 means no cap")
	labelUpperPct := fs.Float64("upper-pct", 0.10, "triple-barrier profit target percentage")
	labelLowerPct := fs.Float64("lower-pct", 0.05, "triple-barrier stop percentage")
	labelMaxBars := fs.Int("max-bars", 60, "triple-barrier maximum forward bar count")
	trainDays := fs.Int("train-days", 24, "rolling training window size in trading days")
	validDays := fs.Int("valid-days", 8, "rolling validation window size in trading days")
	purgeDays := fs.Int("purge-days", 2, "purge gap in trading days")
	trainStepDays := fs.Int("step-days", 8, "rolling training step size in trading days")
	sampleScope := fs.String("sample-scope", "risk_approved", "training sample scope")
	minTrainSamples := fs.Int("min-train-samples", 50, "minimum samples required to train a side model in a window")
	minValidSamples := fs.Int("min-valid-samples", 10, "minimum validation samples required to score a side window")
	epochs := fs.Int("epochs", 50, "logistic SGD epochs")
	learningRate := fs.Float64("learning-rate", 0.05, "logistic SGD learning rate")
	l2 := fs.Float64("l2", 0.0001, "L2 regularization strength")
	calibrationBins := fs.Int("calibration-bins", 10, "probability calibration bin count")
	regressionSuite := fs.String("regression-suite", "docs/ml_regression_suite.json", "regression suite path")
	regressionWindows := fs.String("regression-windows", "", "optional comma-separated regression window IDs")
	advisoryMinProb := fs.Float64("advisory-min-prob", 0.55, "ML advisory minimum probability for regression and annual checks")
	annualStart := fs.String("annual-start", "", "optional annual validation start date in YYYY-MM-DD")
	annualEnd := fs.String("annual-end", "", "optional annual validation end date in YYYY-MM-DD")
	annualWindowDays := fs.Int("annual-window-days", 7, "annual validation batch window length in days")
	annualStepDays := fs.Int("annual-step-days", 7, "annual validation batch step length in days")
	annualMaxWindows := fs.Int("annual-max-windows", 0, "optional cap on annual validation windows")
	if err := fs.Parse(args); err != nil {
		return err
	}

	profileCfg, profileLabel, err := loadMLAutoProfile(strings.TrimSpace(*profilePath))
	if err != nil {
		return err
	}
	log.Printf("auto-train-ml: loaded trading profile %s", profileLabel)

	resolvedTargetModelDir := strings.TrimSpace(*targetModelDir)
	if resolvedTargetModelDir == "" {
		resolvedTargetModelDir = strings.TrimSpace(profileCfg.MLModelPath)
	}
	if resolvedTargetModelDir == "" {
		return fmt.Errorf("target model dir is required; set MLModelPath in the profile or pass -target-model-dir")
	}

	guardrails, err := loadMLAutoPromotionGuardrails(strings.TrimSpace(*guardrailsPath), *requireImprovement)
	if err != nil {
		return err
	}

	cfg := mlAutoTrainConfig{
		ProfilePath:           strings.TrimSpace(*profilePath),
		TargetModelDir:        resolvedTargetModelDir,
		GuardrailsPath:        strings.TrimSpace(*guardrailsPath),
		CurrentAnnualSummary:  resolveCurrentAnnualSummary(strings.TrimSpace(*currentAnnualSummary)),
		CurrentRegressionPath: resolveCurrentRegressionSummary(resolvedTargetModelDir, strings.TrimSpace(*currentRegressionPath)),
		DatasetStart:          strings.TrimSpace(*datasetStart),
		DatasetEnd:            strings.TrimSpace(*datasetEnd),
		DatasetLookbackDays:   *datasetLookbackDays,
		DatasetWindowDays:     *datasetWindowDays,
		DatasetStepDays:       *datasetStepDays,
		DatasetMaxWindows:     *datasetMaxWindows,
		DatasetIDPrefix:       "auto_ml",
		DatasetPurpose:        "Automatic rolling ML dataset window",
		LabelUpperPct:         *labelUpperPct,
		LabelLowerPct:         *labelLowerPct,
		LabelMaxBars:          *labelMaxBars,
		TrainConfig: mlTrainingRunConfig{
			TrainDays:       *trainDays,
			ValidDays:       *validDays,
			PurgeDays:       *purgeDays,
			StepDays:        *trainStepDays,
			SampleScope:     *sampleScope,
			MinTrainSamples: *minTrainSamples,
			MinValidSamples: *minValidSamples,
			Epochs:          *epochs,
			LearningRate:    *learningRate,
			L2:              *l2,
			CalibrationBins: *calibrationBins,
		},
		RegressionSuitePath: *regressionSuite,
		RegressionWindows:   splitCSV(*regressionWindows),
		AdvisoryMinProb:     *advisoryMinProb,
		AnnualStart:         strings.TrimSpace(*annualStart),
		AnnualEnd:           strings.TrimSpace(*annualEnd),
		AnnualWindowDays:    *annualWindowDays,
		AnnualStepDays:      *annualStepDays,
		AnnualMaxWindows:    *annualMaxWindows,
	}

	runFn := func(ctx context.Context, asOf time.Time, artifactDir string) (mlAutoRunReport, error) {
		return executeMLAutoTrain(ctx, asOf, artifactDir, guardrails, cfg)
	}

	sched := &mlAutoScheduler{
		TargetModelDir: resolvedTargetModelDir,
		ArtifactDir:    *outDir,
		Schedule:       *schedule,
		DryRun:         *dryRun,
		Guardrails:     guardrails,
		RunTraining:    runFn,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("auto-train-ml: shutdown signal received")
		cancel()
	}()

	if *runOnce {
		return sched.RunOnce(ctx)
	}
	if *runNow {
		if err := sched.RunOnce(ctx); err != nil {
			log.Printf("auto-train-ml: initial run failed: %v", err)
		}
	}
	return sched.Run(ctx)
}

func (s *mlAutoScheduler) Run(ctx context.Context) error {
	for {
		nextRun := nextMLAutoRunTime(time.Now(), s.Schedule)
		log.Printf("auto-train-ml: next run scheduled for %s", nextRun.Format("2006-01-02 15:04:05 MST"))
		sleepDur := time.Until(nextRun)
		if sleepDur > 0 {
			timer := time.NewTimer(sleepDur)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := s.runOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("auto-train-ml: run failed: %v", err)
		}
	}
}

func (s *mlAutoScheduler) RunOnce(ctx context.Context) error {
	return s.runOnce(ctx)
}

func (s *mlAutoScheduler) runOnce(ctx context.Context) error {
	asOf := time.Now().In(markethours.Location())
	log.Printf("auto-train-ml: starting training as-of %s", asOf.Format("2006-01-02"))
	report, err := s.RunTraining(ctx, asOf, s.ArtifactDir)
	if err != nil {
		s.writeStatusFile(false, err.Error(), nil)
		return err
	}

	statusReason := report.Validation.Reason
	if !report.Validation.Passed {
		log.Printf("auto-train-ml: candidate rejected: %s", statusReason)
		s.writeStatusFile(false, statusReason, &report)
		return nil
	}
	if s.DryRun {
		log.Printf("auto-train-ml: dry-run enabled; skipping promotion")
		s.writeStatusFile(false, "dry-run", &report)
		return nil
	}

	if err := promoteMLArtifactDir(report.CandidateModelDir, s.TargetModelDir); err != nil {
		s.writeStatusFile(false, err.Error(), &report)
		return err
	}
	if err := writeMLPromotionMetadata(s.TargetModelDir, report); err != nil {
		s.writeStatusFile(false, err.Error(), &report)
		return err
	}
	log.Printf("auto-train-ml: promoted model to %s", s.TargetModelDir)
	s.writeStatusFile(true, "", &report)
	return nil
}

func (s *mlAutoScheduler) writeStatusFile(promoted bool, reason string, report *mlAutoRunReport) {
	if err := os.MkdirAll(s.ArtifactDir, 0o755); err != nil {
		log.Printf("auto-train-ml: failed to create artifact dir: %v", err)
		return
	}
	status := map[string]any{
		"lastRun":   time.Now().In(markethours.Location()),
		"promoted":  promoted,
		"reason":    reason,
		"targetDir": s.TargetModelDir,
	}
	if report != nil {
		status["candidateModelDir"] = report.CandidateModelDir
		status["annualNetPnL"] = report.CandidateAnnual.Totals.TotalNetPnL
		status["validationPassed"] = report.Validation.Passed
	}
	if err := writeJSONAtomic(filepath.Join(s.ArtifactDir, "latest-status.json"), status); err != nil {
		log.Printf("auto-train-ml: failed to write status: %v", err)
	}
	if report != nil {
		if err := writeJSONAtomic(filepath.Join(s.ArtifactDir, "latest-report.json"), report); err != nil {
			log.Printf("auto-train-ml: failed to write report: %v", err)
		}
	}
}

func executeMLAutoTrain(ctx context.Context, asOf time.Time, outDir string, guardrails mlAutoPromotionGuardrails, cfg mlAutoTrainConfig) (mlAutoRunReport, error) {
	runID := asOf.Format("20060102_150405")
	runDir := filepath.Join(outDir, "runs", runID)
	datasetDir := filepath.Join(runDir, "dataset")
	modelDir := filepath.Join(runDir, "candidate_model")
	regressionDir := filepath.Join(modelDir, "regression")
	annualDir := filepath.Join(modelDir, "annual")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return mlAutoRunReport{}, err
	}

	start, end, err := resolveConfiguredAutoDatasetWindow(asOf, cfg)
	if err != nil {
		return mlAutoRunReport{}, err
	}
	manifestPath, err := prepareMLDatasetForAutoRun(datasetDir, cfg.ProfilePath, start, end, cfg)
	if err != nil {
		return mlAutoRunReport{}, err
	}
	labeledPaths, err := loadLabeledPathsFromManifest(manifestPath)
	if err != nil {
		return mlAutoRunReport{}, err
	}
	rows, err := ml.LoadLabeledCandidateRows(labeledPaths)
	if err != nil {
		return mlAutoRunReport{}, err
	}

	trainCfg := ml.TrainingConfig{
		WindowSpec: ml.RollingWindowSpec{
			TrainDays: cfg.TrainConfig.TrainDays,
			ValidDays: cfg.TrainConfig.ValidDays,
			PurgeDays: cfg.TrainConfig.PurgeDays,
			StepDays:  cfg.TrainConfig.StepDays,
		},
		SampleScope:     cfg.TrainConfig.SampleScope,
		MinTrainSamples: cfg.TrainConfig.MinTrainSamples,
		MinValidSamples: cfg.TrainConfig.MinValidSamples,
		Epochs:          cfg.TrainConfig.Epochs,
		LearningRate:    cfg.TrainConfig.LearningRate,
		L2:              cfg.TrainConfig.L2,
		CalibrationBins: cfg.TrainConfig.CalibrationBins,
	}
	regressionCfg := &mlRegressionConfig{
		SuitePath:       cfg.RegressionSuitePath,
		OutDir:          regressionDir,
		ProfilePath:     cfg.ProfilePath,
		WindowIDs:       append([]string(nil), cfg.RegressionWindows...),
		RunAdvisory:     true,
		AdvisoryMinProb: cfg.AdvisoryMinProb,
	}
	runSummary, _, err := runTrainMLForScope(rows, labeledPaths, modelDir, trainCfg, regressionCfg)
	if err != nil {
		return mlAutoRunReport{}, err
	}
	if !hasMLModelArtifacts(modelDir) {
		return mlAutoRunReport{}, fmt.Errorf("auto-train-ml: no model artifacts were produced at %s; broaden the dataset window or relax the training sample requirements", modelDir)
	}

	candidateRegression, err := loadMLRegressionSummary(filepath.Join(regressionDir, "regression_summary.json"))
	if err != nil {
		return mlAutoRunReport{}, err
	}
	candidateAnnual, err := runMLAutoAnnualValidation(cfg, modelDir, annualDir)
	if err != nil {
		return mlAutoRunReport{}, err
	}

	report := mlAutoRunReport{
		CreatedAt:                time.Now().In(markethours.Location()),
		AsOf:                     asOf,
		ProfilePath:              cfg.ProfilePath,
		TargetModelDir:           cfg.TargetModelDir,
		CandidateModelDir:        modelDir,
		DatasetManifestPath:      manifestPath,
		TrainingReportPath:       runSummary.TrainingReportPath,
		RegressionSummaryPath:    filepath.Join(regressionDir, "regression_summary.json"),
		AnnualSummaryPath:        filepath.Join(annualDir, "summary.json"),
		CurrentAnnualSummaryPath: cfg.CurrentAnnualSummary,
		CurrentRegressionPath:    cfg.CurrentRegressionPath,
		CandidateRegression:      candidateRegression,
		CandidateAnnual:          candidateAnnual,
	}
	if cfg.CurrentAnnualSummary != "" {
		if currentAnnual, err := loadBatchBacktestSummary(cfg.CurrentAnnualSummary); err == nil {
			report.CurrentAnnual = &currentAnnual
		}
	}
	if cfg.CurrentRegressionPath != "" {
		if currentRegression, err := loadMLRegressionSummary(cfg.CurrentRegressionPath); err == nil {
			report.CurrentRegression = &currentRegression
		}
	}

	report.Validation = guardrails.Validate(report)
	return report, writeJSONAtomic(filepath.Join(runDir, "run_report.json"), report)
}

func loadMLAutoProfile(profilePath string) (config.TradingConfig, string, error) {
	cfg := config.DefaultTradingConfig()
	label := "default"
	if profilePath == "" {
		return cfg, label, nil
	}
	applied, appliedLabel, err := applyConfiguredTradingProfile(cfg, profilePath)
	if err != nil {
		return config.TradingConfig{}, "", err
	}
	return applied, appliedLabel, nil
}

func resolveCurrentAnnualSummary(explicit string) string {
	if explicit != "" {
		return explicit
	}
	defaultPath := filepath.Join(".cache", "backtest", "annual_2025_weekly_phase5_short_context_v2", "summary.json")
	if fileExists(defaultPath) {
		return defaultPath
	}
	return ""
}

func resolveCurrentRegressionSummary(targetModelDir, explicit string) string {
	if explicit != "" {
		return explicit
	}
	candidate := filepath.Join(targetModelDir, "regression_summary.json")
	if fileExists(candidate) {
		return candidate
	}
	return ""
}

func resolveAutoDatasetWindow(asOf time.Time, lookbackDays int) (string, string) {
	end := asOf.In(markethours.Location()).Format("2006-01-02")
	startTime := asOf.In(markethours.Location()).AddDate(0, 0, -lookbackDays)
	start := startTime.Format("2006-01-02")
	return start, end
}

func resolveConfiguredAutoDatasetWindow(asOf time.Time, cfg mlAutoTrainConfig) (string, string, error) {
	start := strings.TrimSpace(cfg.DatasetStart)
	end := strings.TrimSpace(cfg.DatasetEnd)
	if start == "" && end == "" {
		autoStart, autoEnd := resolveAutoDatasetWindow(asOf, cfg.DatasetLookbackDays)
		return autoStart, autoEnd, nil
	}
	if start == "" || end == "" {
		return "", "", fmt.Errorf("auto-train-ml: dataset-start and dataset-end must be set together")
	}
	if _, err := time.ParseInLocation("2006-01-02", start, markethours.Location()); err != nil {
		return "", "", fmt.Errorf("auto-train-ml: invalid dataset-start %q: %w", start, err)
	}
	if _, err := time.ParseInLocation("2006-01-02", end, markethours.Location()); err != nil {
		return "", "", fmt.Errorf("auto-train-ml: invalid dataset-end %q: %w", end, err)
	}
	return start, end, nil
}

func prepareMLDatasetForAutoRun(outDir, profilePath, startDate, endDate string, cfg mlAutoTrainConfig) (string, error) {
	windows, err := buildCalendarMLDatasetWindows(
		startDate,
		endDate,
		cfg.DatasetWindowDays,
		cfg.DatasetStepDays,
		cfg.DatasetIDPrefix,
		cfg.DatasetPurpose,
	)
	if err != nil {
		return "", err
	}
	if cfg.DatasetMaxWindows > 0 && len(windows) > cfg.DatasetMaxWindows {
		windows = windows[:cfg.DatasetMaxWindows]
	}
	if len(windows) == 0 {
		return "", fmt.Errorf("auto-train-ml: no dataset windows selected")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}

	manifest := mlDatasetManifest{
		CreatedAt:   time.Now().In(markethours.Location()),
		ProfilePath: profilePath,
		Source:      "calendar",
		Windows:     make([]mlDatasetManifestEntry, 0, len(windows)),
	}
	for _, window := range windows {
		slug := sanitizeMLDatasetID(window.ID)
		candidatePath := filepath.Join(outDir, slug+"_candidates.jsonl")
		labelPath := filepath.Join(outDir, slug+"_labels.jsonl")
		labelSummaryPath := filepath.Join(outDir, slug+"_label_summary.json")
		reportPath := filepath.Join(outDir, slug+"_backtest_report.json")

		runCfg, tradingCfg, err := buildRegressionBacktestConfigFromSuiteWindow(window, profilePath)
		if err != nil {
			return "", err
		}
		recorder, err := storage.NewCandidateEvaluationFileRecorder(candidatePath)
		if err != nil {
			return "", err
		}
		runCfg.Recorder = recorder
		result, err := backtest.Run(context.Background(), tradingCfg, runCfg)
		if err != nil {
			return "", err
		}
		if err := writeBacktestReport(reportPath, result); err != nil {
			return "", err
		}
		summary, err := labelCandidateEvaluationFile(candidatePath, labelPath, labelSummaryPath, "", cfg.LabelUpperPct, cfg.LabelLowerPct, cfg.LabelMaxBars)
		if err != nil {
			return "", err
		}
		manifest.Windows = append(manifest.Windows, mlDatasetManifestEntry{
			ID:               window.ID,
			Purpose:          window.Purpose,
			Start:            window.Start,
			End:              window.End,
			CandidatePath:    candidatePath,
			LabelPath:        labelPath,
			LabelSummaryPath: labelSummaryPath,
			BacktestReport:   reportPath,
			LabelSummary:     summary,
		})
	}
	manifestPath := filepath.Join(outDir, "manifest.json")
	return manifestPath, writeJSONAtomic(manifestPath, manifest)
}

func runMLAutoAnnualValidation(cfg mlAutoTrainConfig, modelDir, outDir string) (batchBacktestSummary, error) {
	startDate, endDate := resolveAnnualWindow(cfg)
	windows, err := buildCalendarMLDatasetWindows(
		startDate,
		endDate,
		cfg.AnnualWindowDays,
		cfg.AnnualStepDays,
		"batch",
		"Automatic ML annual validation window",
	)
	if err != nil {
		return batchBacktestSummary{}, err
	}
	if cfg.AnnualMaxWindows > 0 && len(windows) > cfg.AnnualMaxWindows {
		windows = windows[:cfg.AnnualMaxWindows]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return batchBacktestSummary{}, err
	}

	summary := batchBacktestSummary{
		CreatedAt:       time.Now().In(markethours.Location()),
		Start:           startDate,
		End:             endDate,
		WindowDays:      cfg.AnnualWindowDays,
		StepDays:        cfg.AnnualStepDays,
		ProfilePath:     cfg.ProfilePath,
		ModelPath:       modelDir,
		MLAdvisory:      true,
		AdvisoryMinProb: cfg.AdvisoryMinProb,
		Windows:         make([]batchBacktestWindowRun, 0, len(windows)),
	}
	for _, window := range windows {
		run, err := runBatchBacktestWindow(window, cfg.ProfilePath, outDir, batchBacktestWindowOptions{
			MLModelPath:       modelDir,
			MLAdvisory:        true,
			MLAdvisoryMinProb: cfg.AdvisoryMinProb,
		})
		if err != nil {
			return batchBacktestSummary{}, err
		}
		summary.Windows = append(summary.Windows, run)
	}
	summary.Totals = summarizeBatchBacktestWindows(summary.Windows)
	if err := writeBatchBacktestCSV(filepath.Join(outDir, "summary.csv"), summary.Windows); err != nil {
		return batchBacktestSummary{}, err
	}
	if err := writeJSONAtomic(filepath.Join(outDir, "summary.json"), summary); err != nil {
		return batchBacktestSummary{}, err
	}
	return summary, nil
}

func resolveAnnualWindow(cfg mlAutoTrainConfig) (string, string) {
	if cfg.AnnualStart != "" && cfg.AnnualEnd != "" {
		return cfg.AnnualStart, cfg.AnnualEnd
	}
	now := time.Now().In(markethours.Location())
	end := now.Format("2006-01-02")
	start := now.AddDate(-1, 0, 1).Format("2006-01-02")
	return start, end
}

func hasMLModelArtifacts(modelDir string) bool {
	return fileExists(filepath.Join(modelDir, "long_model.json")) || fileExists(filepath.Join(modelDir, "short_model.json"))
}

func loadMLAutoPromotionGuardrails(path string, requireImprovement bool) (mlAutoPromotionGuardrails, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return mlAutoPromotionGuardrails{}, err
	}
	var spec mlArtifactGuardrails
	if err := json.Unmarshal(raw, &spec); err != nil {
		return mlAutoPromotionGuardrails{}, err
	}
	return mlAutoPromotionGuardrails{
		Spec:               spec,
		RequireImprovement: requireImprovement,
	}, nil
}

func (g mlAutoPromotionGuardrails) Validate(report mlAutoRunReport) mlAutoValidationResult {
	checks := make([]mlAutoCheck, 0)
	required := g.Spec.RequiredRegressions
	for _, comparison := range report.CandidateRegression.Comparisons {
		rule, ok := required[comparison.ID]
		if !ok || !rule.MustPass {
			continue
		}
		netPnL := comparison.Advisory.NetPnL
		trades := comparison.Advisory.Trades
		if comparison.Advisory.Mode == "" {
			netPnL = comparison.RulesOnly.NetPnL
			trades = comparison.RulesOnly.Trades
		}
		if rule.MinimumNetPnL != 0 {
			checks = append(checks, mlAutoCheck{
				Name:     comparison.ID + "_min_net",
				Passed:   netPnL >= rule.MinimumNetPnL,
				Expected: fmt.Sprintf(">= %.2f", rule.MinimumNetPnL),
				Actual:   fmt.Sprintf("%.2f", netPnL),
			})
		}
		if rule.MinimumTradeCount > 0 {
			checks = append(checks, mlAutoCheck{
				Name:     comparison.ID + "_min_trades",
				Passed:   trades >= rule.MinimumTradeCount,
				Expected: fmt.Sprintf(">= %d", rule.MinimumTradeCount),
				Actual:   fmt.Sprintf("%d", trades),
			})
		}
		if rule.MaximumNetPnLDegradationVsBaselinePct > 0 {
			minAllowed := allowedNetPnLFloor(comparison.RulesOnly.NetPnL, rule.MaximumNetPnLDegradationVsBaselinePct)
			checks = append(checks, mlAutoCheck{
				Name:     comparison.ID + "_degradation",
				Passed:   netPnL >= minAllowed,
				Expected: fmt.Sprintf(">= %.2f", minAllowed),
				Actual:   fmt.Sprintf("%.2f", netPnL),
			})
		}
	}

	annualRule, annualRequired := required["annual_2025"]
	if annualRequired && annualRule.MustPass {
		candidateNet := report.CandidateAnnual.Totals.TotalNetPnL
		expected := "> current annual net"
		passed := candidateNet > 0
		if report.CurrentAnnual != nil && g.RequireImprovement && comparableBatchBacktestSummaries(report.CandidateAnnual, *report.CurrentAnnual) {
			expected = fmt.Sprintf("> %.2f", report.CurrentAnnual.Totals.TotalNetPnL)
			passed = candidateNet > report.CurrentAnnual.Totals.TotalNetPnL
		}
		checks = append(checks, mlAutoCheck{
			Name:     "annual_net_improvement",
			Passed:   passed,
			Expected: expected,
			Actual:   fmt.Sprintf("%.2f", candidateNet),
		})
		if annualRule.MaxDrawdownPctCeiling > 0 {
			maxDD := candidateAnnualMaxDrawdown(report.CandidateAnnual)
			checks = append(checks, mlAutoCheck{
				Name:     "annual_max_drawdown",
				Passed:   maxDD <= annualRule.MaxDrawdownPctCeiling,
				Expected: fmt.Sprintf("<= %.2f", annualRule.MaxDrawdownPctCeiling),
				Actual:   fmt.Sprintf("%.2f", maxDD),
			})
		}
	}

	if report.CurrentRegression != nil && g.RequireImprovement {
		candidateTotal := advisoryRegressionNetTotal(report.CandidateRegression)
		currentTotal := advisoryRegressionNetTotal(*report.CurrentRegression)
		checks = append(checks, mlAutoCheck{
			Name:     "regression_total_improvement",
			Passed:   candidateTotal >= currentTotal,
			Expected: fmt.Sprintf(">= %.2f", currentTotal),
			Actual:   fmt.Sprintf("%.2f", candidateTotal),
		})
	}

	passed := true
	reasons := make([]string, 0)
	for _, check := range checks {
		if check.Passed {
			continue
		}
		passed = false
		reasons = append(reasons, fmt.Sprintf("%s expected %s got %s", check.Name, check.Expected, check.Actual))
	}
	return mlAutoValidationResult{
		Passed: passed,
		Reason: strings.Join(reasons, "; "),
		Checks: checks,
	}
}

func comparableBatchBacktestSummaries(candidate, current batchBacktestSummary) bool {
	return candidate.Start == current.Start &&
		candidate.End == current.End &&
		candidate.WindowDays == current.WindowDays &&
		candidate.StepDays == current.StepDays &&
		candidate.Totals.WindowCount == current.Totals.WindowCount
}

func allowedNetPnLFloor(baseline, maxDegradationPct float64) float64 {
	if baseline >= 0 {
		return baseline * (1 - maxDegradationPct/100.0)
	}
	return baseline * (1 + maxDegradationPct/100.0)
}

func advisoryRegressionNetTotal(summary mlRegressionSummary) float64 {
	total := 0.0
	for _, comparison := range summary.Comparisons {
		if comparison.Advisory.Mode != "" {
			total += comparison.Advisory.NetPnL
			continue
		}
		total += comparison.RulesOnly.NetPnL
	}
	return total
}

func candidateAnnualMaxDrawdown(summary batchBacktestSummary) float64 {
	maxDD := 0.0
	for _, window := range summary.Windows {
		if window.MaxDrawdownPct > maxDD {
			maxDD = window.MaxDrawdownPct
		}
	}
	return maxDD
}

func loadBatchBacktestSummary(path string) (batchBacktestSummary, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return batchBacktestSummary{}, err
	}
	var summary batchBacktestSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return batchBacktestSummary{}, err
	}
	return summary, nil
}

func nextMLAutoRunTime(now time.Time, schedule string) time.Time {
	loc := markethours.Location()
	local := now.In(loc)
	if schedule == "daily" {
		next := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, loc)
		if !next.After(local) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	}
	daysUntilSaturday := (time.Saturday - local.Weekday() + 7) % 7
	if daysUntilSaturday == 0 {
		saturdayRun := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, loc)
		if local.Before(saturdayRun) {
			return saturdayRun
		}
		daysUntilSaturday = 7
	}
	return time.Date(local.Year(), local.Month(), local.Day()+int(daysUntilSaturday), 6, 0, 0, 0, loc)
}

func promoteMLArtifactDir(srcDir, dstDir string) error {
	srcDir = strings.TrimSpace(srcDir)
	dstDir = strings.TrimSpace(dstDir)
	if srcDir == "" || dstDir == "" {
		return fmt.Errorf("source and target model dirs are required")
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("candidate model dir must be a directory: %s", srcDir)
	}
	parent := filepath.Dir(dstDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmpDir := dstDir + ".tmp"
	_ = os.RemoveAll(tmpDir)
	if err := copyDir(srcDir, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	if _, err := os.Stat(dstDir); err == nil {
		backupDir := dstDir + ".backup-" + time.Now().In(markethours.Location()).Format("20060102_150405")
		if err := os.Rename(dstDir, backupDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			return err
		}
	}
	return os.Rename(tmpDir, dstDir)
}

func copyDir(srcDir, dstDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func writeMLPromotionMetadata(targetDir string, report mlAutoRunReport) error {
	metadata := mlPromotionMetadata{
		PromotedAt:            time.Now().In(markethours.Location()),
		TargetModelDir:        targetDir,
		SourceCandidateModel:  report.CandidateModelDir,
		TrainingReportPath:    report.TrainingReportPath,
		RegressionSummaryPath: report.RegressionSummaryPath,
		AnnualSummaryPath:     report.AnnualSummaryPath,
		CurrentAnnualSummary:  report.CurrentAnnualSummaryPath,
		CurrentRegressionPath: report.CurrentRegressionPath,
		CandidateRegression:   report.CandidateRegression,
		CandidateAnnual:       report.CandidateAnnual,
		CurrentRegression:     report.CurrentRegression,
		CurrentAnnual:         report.CurrentAnnual,
		Validation:            report.Validation,
	}
	return writeJSONAtomic(filepath.Join(targetDir, "promotion_metadata.json"), metadata)
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
