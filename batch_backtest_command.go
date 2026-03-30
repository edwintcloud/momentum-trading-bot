package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

type batchBacktestSummary struct {
	CreatedAt       time.Time                  `json:"createdAt"`
	Start           string                     `json:"start"`
	End             string                     `json:"end"`
	WindowDays      int                        `json:"windowDays"`
	StepDays        int                        `json:"stepDays"`
	ProfilePath     string                     `json:"profilePath,omitempty"`
	ModelPath       string                     `json:"modelPath,omitempty"`
	MLAdvisory      bool                       `json:"mlAdvisory"`
	AdvisoryMinProb float64                    `json:"advisoryMinProb,omitempty"`
	Windows         []batchBacktestWindowRun   `json:"windows"`
	Totals          batchBacktestTotalsSummary `json:"totals"`
}

type batchBacktestWindowRun struct {
	ID             string  `json:"id"`
	Start          string  `json:"start"`
	End            string  `json:"end"`
	ReportPath     string  `json:"reportPath"`
	NetPnL         float64 `json:"netPnL"`
	ROI            float64 `json:"roi"`
	Trades         int     `json:"trades"`
	WinRate        float64 `json:"winRate"`
	MaxDrawdownPct float64 `json:"maxDrawdownPct"`
	ProfitFactor   float64 `json:"profitFactor"`
	EndingEquity   float64 `json:"endingEquity"`
}

type batchBacktestTotalsSummary struct {
	WindowCount       int     `json:"windowCount"`
	TotalNetPnL       float64 `json:"totalNetPnL"`
	AverageROI        float64 `json:"averageROI"`
	WinningWindows    int     `json:"winningWindows"`
	LosingWindows     int     `json:"losingWindows"`
	BestWindowID      string  `json:"bestWindowId,omitempty"`
	BestWindowNetPnL  float64 `json:"bestWindowNetPnL,omitempty"`
	WorstWindowID     string  `json:"worstWindowId,omitempty"`
	WorstWindowNetPnL float64 `json:"worstWindowNetPnL,omitempty"`
}

func runBatchBacktest(args []string) error {
	flags := flag.NewFlagSet("batch-backtest", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	startDate := flags.String("start", "", "Required batch start date in YYYY-MM-DD format")
	endDate := flags.String("end", "", "Required batch end date in YYYY-MM-DD format")
	windowDays := flags.Int("window-days", 7, "Calendar window length in days")
	stepDays := flags.Int("step-days", 7, "Calendar step length in days")
	maxWindows := flags.Int("max-windows", 0, "Optional cap on number of windows; 0 means no limit")
	idPrefix := flags.String("id-prefix", "batch", "Window id prefix")
	outDir := flags.String("out-dir", filepath.Join(".cache", "backtest", "batch"), "Output directory for per-window reports and summary artifacts")
	csvOut := flags.String("csv-out", "", "Optional CSV summary path; defaults to out-dir/summary.csv")
	summaryOut := flags.String("summary-out", "", "Optional JSON summary path; defaults to out-dir/summary.json")
	profilePath := flags.String("profile", "", "Optional trading profile path override")
	debugSymbols := flags.String("debug", "", "Optional comma-separated debug symbols applied to each window")
	mlModelPath := flags.String("ml-model", "", "Optional ML model artifact path or directory")
	mlThreshold := flags.Float64("ml-threshold", 0, "Optional ML scoring threshold override")
	mlAdvisory := flags.Bool("ml-advisory", false, "Enable ML advisory mode")
	mlAdvisoryMinProb := flags.Float64("ml-advisory-min-prob", 0, "Optional ML advisory minimum probability override")
	mlAdvisoryMaxVetos := flags.Int("ml-advisory-max-vetos", 0, "Optional ML advisory max daily vetos override")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*startDate) == "" || strings.TrimSpace(*endDate) == "" {
		return fmt.Errorf("start and end are required")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}

	windows, err := buildCalendarMLDatasetWindows(
		strings.TrimSpace(*startDate),
		strings.TrimSpace(*endDate),
		*windowDays,
		*stepDays,
		strings.TrimSpace(*idPrefix),
		"Batch backtest window",
	)
	if err != nil {
		return err
	}
	if *maxWindows > 0 && len(windows) > *maxWindows {
		windows = windows[:*maxWindows]
	}
	if len(windows) == 0 {
		return fmt.Errorf("no batch windows selected")
	}

	resolvedProfilePath := strings.TrimSpace(*profilePath)
	if resolvedProfilePath == "" {
		resolvedProfilePath = config.ResolveTradingProfilePath(os.Getenv("TRADING_PROFILE_PATH"))
	}
	debugList := splitCSV(*debugSymbols)

	summary := batchBacktestSummary{
		CreatedAt:       time.Now().In(markethours.Location()),
		Start:           strings.TrimSpace(*startDate),
		End:             strings.TrimSpace(*endDate),
		WindowDays:      *windowDays,
		StepDays:        *stepDays,
		ProfilePath:     resolvedProfilePath,
		ModelPath:       strings.TrimSpace(*mlModelPath),
		MLAdvisory:      *mlAdvisory,
		AdvisoryMinProb: *mlAdvisoryMinProb,
		Windows:         make([]batchBacktestWindowRun, 0, len(windows)),
	}

	for idx, window := range windows {
		window.DebugSymbols = append([]string(nil), debugList...)
		log.Printf(
			"batch-backtest: running window=%d/%d id=%s %s..%s",
			idx+1,
			len(windows),
			window.ID,
			window.Start,
			window.End,
		)
		run, err := runBatchBacktestWindow(window, resolvedProfilePath, *outDir, batchBacktestWindowOptions{
			MLModelPath:        strings.TrimSpace(*mlModelPath),
			MLThreshold:        *mlThreshold,
			MLAdvisory:         *mlAdvisory,
			MLAdvisoryMinProb:  *mlAdvisoryMinProb,
			MLAdvisoryMaxVetos: *mlAdvisoryMaxVetos,
		})
		if err != nil {
			return err
		}
		summary.Windows = append(summary.Windows, run)
	}

	summary.Totals = summarizeBatchBacktestWindows(summary.Windows)

	csvPath := strings.TrimSpace(*csvOut)
	if csvPath == "" {
		csvPath = filepath.Join(*outDir, "summary.csv")
	}
	if err := writeBatchBacktestCSV(csvPath, summary.Windows); err != nil {
		return err
	}

	summaryPath := strings.TrimSpace(*summaryOut)
	if summaryPath == "" {
		summaryPath = filepath.Join(*outDir, "summary.json")
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(summaryPath, data, 0o644); err != nil {
		return err
	}
	log.Printf(
		"batch-backtest: wrote summary=%s csv=%s windows=%d total_net=%s avg_roi=%.2f%%",
		summaryPath,
		csvPath,
		len(summary.Windows),
		formatMoney(summary.Totals.TotalNetPnL),
		summary.Totals.AverageROI,
	)
	return nil
}

type batchBacktestWindowOptions struct {
	MLModelPath        string
	MLThreshold        float64
	MLAdvisory         bool
	MLAdvisoryMinProb  float64
	MLAdvisoryMaxVetos int
}

func runBatchBacktestWindow(
	window mlRegressionSuiteWindow,
	profilePath string,
	outDir string,
	opts batchBacktestWindowOptions,
) (batchBacktestWindowRun, error) {
	runCfg, cfg, err := buildRegressionBacktestConfigFromSuiteWindow(window, profilePath)
	if err != nil {
		return batchBacktestWindowRun{}, err
	}

	if opts.MLModelPath != "" {
		cfg.MLScoringEnabled = true
		cfg.MLModelPath = opts.MLModelPath
	}
	if opts.MLThreshold > 0 {
		cfg.MLScoringEnabled = true
		cfg.MLScoringThreshold = opts.MLThreshold
	}
	if opts.MLAdvisory {
		cfg.MLScoringEnabled = true
		cfg.MLAdvisoryEnabled = true
	}
	if opts.MLAdvisoryMinProb > 0 {
		cfg.MLScoringEnabled = true
		cfg.MLAdvisoryEnabled = true
		cfg.MLAdvisoryMinProb = opts.MLAdvisoryMinProb
	}
	if opts.MLAdvisoryMaxVetos > 0 {
		cfg.MLAdvisoryEnabled = true
		cfg.MLAdvisoryMaxVetosPerDay = opts.MLAdvisoryMaxVetos
	}

	result, err := backtest.Run(context.Background(), cfg, runCfg)
	if err != nil {
		return batchBacktestWindowRun{}, err
	}

	reportPath := filepath.Join(outDir, window.ID+".json")
	if err := writeBacktestReport(reportPath, result); err != nil {
		return batchBacktestWindowRun{}, err
	}

	actualStart := result.EndingEquity - result.NetPnL
	return batchBacktestWindowRun{
		ID:             window.ID,
		Start:          window.Start,
		End:            window.End,
		ReportPath:     reportPath,
		NetPnL:         result.NetPnL,
		ROI:            safeROIPct(result.NetPnL, actualStart),
		Trades:         result.Trades,
		WinRate:        result.WinRate,
		MaxDrawdownPct: result.MaxDrawdownPct,
		ProfitFactor:   result.ProfitFactor,
		EndingEquity:   result.EndingEquity,
	}, nil
}

func summarizeBatchBacktestWindows(windows []batchBacktestWindowRun) batchBacktestTotalsSummary {
	summary := batchBacktestTotalsSummary{
		WindowCount: len(windows),
	}
	if len(windows) == 0 {
		return summary
	}

	totalROI := 0.0
	best := windows[0]
	worst := windows[0]
	for _, window := range windows {
		summary.TotalNetPnL += window.NetPnL
		totalROI += window.ROI
		switch {
		case window.NetPnL > 0:
			summary.WinningWindows++
		case window.NetPnL < 0:
			summary.LosingWindows++
		}
		if window.NetPnL > best.NetPnL {
			best = window
		}
		if window.NetPnL < worst.NetPnL {
			worst = window
		}
	}
	summary.AverageROI = totalROI / float64(len(windows))
	summary.BestWindowID = best.ID
	summary.BestWindowNetPnL = best.NetPnL
	summary.WorstWindowID = worst.ID
	summary.WorstWindowNetPnL = worst.NetPnL
	return summary
}

func writeBatchBacktestCSV(path string, windows []batchBacktestWindowRun) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{
		"Window ID",
		"Week Start",
		"Week End",
		"Net Profit",
		"Profit Percentage",
		"Trades",
		"Win Rate",
		"Max Drawdown Pct",
		"Profit Factor",
		"Report Path",
	}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, window := range windows {
		row := []string{
			window.ID,
			window.Start,
			window.End,
			strconv.FormatFloat(roundBatchBacktest2(window.NetPnL), 'f', 2, 64),
			strconv.FormatFloat(roundBatchBacktest2(window.ROI), 'f', 2, 64),
			strconv.Itoa(window.Trades),
			strconv.FormatFloat(roundBatchBacktest2(window.WinRate), 'f', 2, 64),
			strconv.FormatFloat(roundBatchBacktest2(window.MaxDrawdownPct), 'f', 2, 64),
			strconv.FormatFloat(roundBatchBacktest2(window.ProfitFactor), 'f', 2, 64),
			window.ReportPath,
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	return writer.Error()
}

func roundBatchBacktest2(value float64) float64 {
	return math.Round(value*100) / 100
}
