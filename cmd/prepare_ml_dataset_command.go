package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/storage"
)

type mlDatasetManifest struct {
	CreatedAt   time.Time                `json:"createdAt"`
	SuitePath   string                   `json:"suitePath"`
	ProfilePath string                   `json:"profilePath,omitempty"`
	Source      string                   `json:"source,omitempty"`
	Windows     []mlDatasetManifestEntry `json:"windows"`
}

type mlDatasetManifestEntry struct {
	ID               string                `json:"id"`
	Purpose          string                `json:"purpose"`
	Start            string                `json:"start"`
	End              string                `json:"end"`
	CandidatePath    string                `json:"candidatePath"`
	LabelPath        string                `json:"labelPath"`
	LabelSummaryPath string                `json:"labelSummaryPath"`
	BacktestReport   string                `json:"backtestReport"`
	LabelSummary     candidateLabelSummary `json:"labelSummary"`
}

func RunPrepareMLDataset(args []string) error {
	flags := flag.NewFlagSet("prepare-ml-dataset", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	suitePath := flags.String("suite", "", "Optional regression or dataset suite JSON path")
	startDate := flags.String("start", "", "Optional calendar dataset start date in YYYY-MM-DD format")
	endDate := flags.String("end", "", "Optional calendar dataset end date in YYYY-MM-DD format")
	windowDays := flags.Int("window-days", 7, "Calendar dataset window length in days when start/end mode is used")
	stepDays := flags.Int("step-days", 7, "Calendar dataset step length in days when start/end mode is used")
	idPrefix := flags.String("id-prefix", "window", "Calendar dataset window id prefix")
	purpose := flags.String("purpose", "Rolling calendar ML dataset window", "Purpose label for generated calendar dataset windows")
	maxWindows := flags.Int("max-windows", 0, "Optional cap on generated windows after filtering; 0 means no limit")
	outDir := flags.String("out-dir", "", "Required output directory for candidate and labeled datasets")
	windowIDs := flags.String("windows", "", "Optional comma-separated suite window IDs")
	profilePath := flags.String("profile", "", "Optional trading profile path override")
	upperPct := flags.Float64("upper-pct", 0.10, "Profit target barrier percentage as a decimal")
	lowerPct := flags.Float64("lower-pct", 0.05, "Stop barrier percentage as a decimal")
	maxBars := flags.Int("max-bars", 60, "Maximum forward 1-minute bars for time barrier resolution")
	skipExisting := flags.Bool("skip-existing", true, "Skip windows whose candidate and label artifacts already exist")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*outDir) == "" {
		return fmt.Errorf("out-dir is required")
	}

	mode, windows, suiteReference, err := resolveMLDatasetWindows(
		strings.TrimSpace(*suitePath),
		strings.TrimSpace(*startDate),
		strings.TrimSpace(*endDate),
		*windowDays,
		*stepDays,
		strings.TrimSpace(*idPrefix),
		strings.TrimSpace(*purpose),
		splitCSV(*windowIDs),
		*maxWindows,
	)
	if err != nil {
		return err
	}
	if len(windows) == 0 {
		return fmt.Errorf("no dataset windows selected")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}

	resolvedProfilePath := strings.TrimSpace(*profilePath)
	if resolvedProfilePath == "" {
		resolvedProfilePath = config.ResolveTradingProfilePath(os.Getenv("TRADING_PROFILE_PATH"))
	}

	manifest := mlDatasetManifest{
		CreatedAt:   time.Now(),
		SuitePath:   suiteReference,
		ProfilePath: resolvedProfilePath,
		Source:      mode,
		Windows:     make([]mlDatasetManifestEntry, 0, len(windows)),
	}

	for _, window := range windows {
		slug := sanitizeMLDatasetID(window.ID)
		candidatePath := filepath.Join(*outDir, slug+"_candidates.jsonl")
		labelPath := filepath.Join(*outDir, slug+"_labels.jsonl")
		labelSummaryPath := filepath.Join(*outDir, slug+"_label_summary.json")
		reportPath := filepath.Join(*outDir, slug+"_backtest_report.json")

		var summary candidateLabelSummary
		if *skipExisting && fileExists(candidatePath) && fileExists(labelPath) && fileExists(labelSummaryPath) && fileExists(reportPath) {
			if existing, err := loadCandidateLabelSummary(labelSummaryPath); err == nil {
				summary = existing
				log.Printf("prepare-ml-dataset: reusing existing window=%s", window.ID)
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
				continue
			}
		}

		runCfg, tradingCfg, err := buildRegressionBacktestConfigFromSuiteWindow(window, resolvedProfilePath)
		if err != nil {
			return err
		}
		recorder, err := storage.NewCandidateEvaluationFileRecorder(candidatePath)
		if err != nil {
			return err
		}
		runCfg.Recorder = recorder

		log.Printf("prepare-ml-dataset: exporting candidates window=%s %s..%s", window.ID, window.Start, window.End)
		result, err := backtest.Run(context.Background(), tradingCfg, runCfg)
		if err != nil {
			return err
		}
		if err := writeBacktestReport(reportPath, result); err != nil {
			return err
		}

		log.Printf("prepare-ml-dataset: labeling window=%s", window.ID)
		summary, err = labelCandidateEvaluationFile(candidatePath, labelPath, labelSummaryPath, "", *upperPct, *lowerPct, *maxBars)
		if err != nil {
			return err
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

	manifestPath := filepath.Join(*outDir, "manifest.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return err
	}
	log.Printf("prepare-ml-dataset: wrote manifest=%s windows=%d", manifestPath, len(manifest.Windows))
	return nil
}

func buildRegressionBacktestConfigFromSuiteWindow(window mlRegressionSuiteWindow, profilePath string) (backtest.RunConfig, config.TradingConfig, error) {
	start, _, err := parseCLIBacktestTime(window.Start)
	if err != nil {
		return backtest.RunConfig{}, config.TradingConfig{}, err
	}
	end, endDateOnly, err := parseCLIBacktestTime(window.End)
	if err != nil {
		return backtest.RunConfig{}, config.TradingConfig{}, err
	}
	start, end, err = inferBacktestWindows(start, end, endDateOnly, true)
	if err != nil {
		return backtest.RunConfig{}, config.TradingConfig{}, err
	}
	return buildRegressionBacktestConfig(start, end, profilePath, window.DebugSymbols)
}

func resolveMLDatasetWindows(
	suitePath string,
	startDate string,
	endDate string,
	windowDays int,
	stepDays int,
	idPrefix string,
	purpose string,
	windowIDs []string,
	maxWindows int,
) (string, []mlRegressionSuiteWindow, string, error) {
	hasSuite := suitePath != ""
	hasCalendar := startDate != "" || endDate != ""
	switch {
	case hasSuite && hasCalendar:
		return "", nil, "", fmt.Errorf("use either suite mode or start/end calendar mode, not both")
	case !hasSuite && !hasCalendar:
		return "", nil, "", fmt.Errorf("either suite or start/end is required")
	}

	if hasSuite {
		suite, err := loadMLRegressionSuite(suitePath)
		if err != nil {
			return "", nil, "", err
		}
		windows := filterMLRegressionWindows(suite.Windows, windowIDs)
		if maxWindows > 0 && len(windows) > maxWindows {
			windows = append([]mlRegressionSuiteWindow(nil), windows[:maxWindows]...)
		}
		return "suite", windows, suitePath, nil
	}

	windows, err := buildCalendarMLDatasetWindows(startDate, endDate, windowDays, stepDays, idPrefix, purpose)
	if err != nil {
		return "", nil, "", err
	}
	if len(windowIDs) > 0 {
		windows = filterMLRegressionWindows(windows, windowIDs)
	}
	if maxWindows > 0 && len(windows) > maxWindows {
		windows = append([]mlRegressionSuiteWindow(nil), windows[:maxWindows]...)
	}
	return "calendar", windows, "", nil
}

func buildCalendarMLDatasetWindows(
	startDate string,
	endDate string,
	windowDays int,
	stepDays int,
	idPrefix string,
	purpose string,
) ([]mlRegressionSuiteWindow, error) {
	if startDate == "" || endDate == "" {
		return nil, fmt.Errorf("both start and end are required for calendar mode")
	}
	if windowDays <= 0 {
		return nil, fmt.Errorf("window-days must be > 0")
	}
	if stepDays <= 0 {
		return nil, fmt.Errorf("step-days must be > 0")
	}
	start, err := time.ParseInLocation("2006-01-02", startDate, markethours.Location())
	if err != nil {
		return nil, fmt.Errorf("parse start: %w", err)
	}
	end, err := time.ParseInLocation("2006-01-02", endDate, markethours.Location())
	if err != nil {
		return nil, fmt.Errorf("parse end: %w", err)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("end must be on or after start")
	}

	if idPrefix == "" {
		idPrefix = "window"
	}
	if purpose == "" {
		purpose = "Rolling calendar ML dataset window"
	}

	windows := make([]mlRegressionSuiteWindow, 0, 32)
	for cursor := start; !cursor.After(end); cursor = cursor.AddDate(0, 0, stepDays) {
		if cursor.Weekday() == time.Saturday || cursor.Weekday() == time.Sunday {
			continue
		}
		windowEnd := cursor.AddDate(0, 0, windowDays-1)
		if windowEnd.After(end) {
			windowEnd = end
		}
		window := mlRegressionSuiteWindow{
			ID:      sanitizeMLDatasetID(fmt.Sprintf("%s_%s_%s", idPrefix, cursor.Format("2006-01-02"), windowEnd.Format("2006-01-02"))),
			Kind:    "window",
			Start:   cursor.Format("2006-01-02"),
			End:     windowEnd.Format("2006-01-02"),
			Purpose: purpose,
		}
		windows = append(windows, window)
	}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].Start == windows[j].Start {
			return windows[i].ID < windows[j].ID
		}
		return windows[i].Start < windows[j].Start
	})
	return windows, nil
}

func loadCandidateLabelSummary(path string) (candidateLabelSummary, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return candidateLabelSummary{}, err
	}
	var summary candidateLabelSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return candidateLabelSummary{}, err
	}
	return summary, nil
}

func sanitizeMLDatasetID(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	id = strings.ReplaceAll(id, " ", "_")
	id = strings.ReplaceAll(id, "-", "_")
	return id
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
