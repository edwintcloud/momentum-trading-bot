package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

const (
	DefaultArtifactDir = ".cache/optimizer"
	reportSchemaV1     = "optimizer_report/v1"
)

var marketLocation = mustLoadLocation("America/New_York")

// CandidateScore captures the ordered ranking metrics for optimizer output.
type CandidateScore struct {
	HoldoutMedianWeeklyReturnPct float64 `json:"holdoutMedianWeeklyReturnPct"`
	PositiveWeeksPct             float64 `json:"positiveWeeksPct"`
	HoldoutP25WeeklyReturnPct    float64 `json:"holdoutP25WeeklyReturnPct"`
	ProfitFactor                 float64 `json:"profitFactor"`
	MaxDrawdownPct               float64 `json:"maxDrawdownPct"`
}

// WeeklyWindow defines a completed trading week in America/New_York time.
type WeeklyWindow struct {
	Label string    `json:"label"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// WeeklyPerformance captures the weekly backtest output that feeds ranking.
type WeeklyPerformance struct {
	Label           string    `json:"label"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	NetPnL          float64   `json:"netPnL"`
	LongPnL         float64   `json:"longPnL"`
	ShortPnL        float64   `json:"shortPnL"`
	ReturnPct       float64   `json:"returnPct"`
	ProfitFactor    float64   `json:"profitFactor"`
	MaxDrawdownPct  float64   `json:"maxDrawdownPct"`
	Trades          int       `json:"trades"`
	LongTrades      int       `json:"longTrades"`
	ShortTrades     int       `json:"shortTrades"`
	WinningTrades   int       `json:"winningTrades"`
	LosingTrades    int       `json:"losingTrades"`
	EndingEquity    float64   `json:"endingEquity"`
	RejectReason    string    `json:"rejectReason,omitempty"`
	ClosedTradePnLs []float64 `json:"-"`
}

// PeriodSummary aggregates weekly performance over a time slice.
type PeriodSummary struct {
	Weeks                 int     `json:"weeks"`
	Trades                int     `json:"trades"`
	LongTrades            int     `json:"longTrades"`
	ShortTrades           int     `json:"shortTrades"`
	PositiveWeeks         int     `json:"positiveWeeks"`
	PositiveWeeksPct      float64 `json:"positiveWeeksPct"`
	MedianWeeklyReturnPct float64 `json:"medianWeeklyReturnPct"`
	P25WeeklyReturnPct    float64 `json:"p25WeeklyReturnPct"`
	ProfitFactor          float64 `json:"profitFactor"`
	MaxDrawdownPct        float64 `json:"maxDrawdownPct"`
	WorstWeekPct          float64 `json:"worstWeekPct"`
	LongPnL               float64 `json:"longPnL"`
	ShortPnL              float64 `json:"shortPnL"`
}

// OptimizerCandidate is a single evaluated profile/config combination.
type OptimizerCandidate struct {
	Rank              int                    `json:"rank"`
	CandidateID       string                 `json:"candidateId"`
	Profile           config.StrategyProfile `json:"profile"`
	Config            config.TradingConfig   `json:"config"`
	SearchSummary     PeriodSummary          `json:"searchSummary"`
	ValidationSummary PeriodSummary          `json:"validationSummary"`
	HoldoutSummary    PeriodSummary          `json:"holdoutSummary"`
	ValidationWeeks   []WeeklyPerformance    `json:"validationWeeks"`
	HoldoutWeeks      []WeeklyPerformance    `json:"holdoutWeeks"`
	Score             CandidateScore         `json:"score"`
	RejectReasons     []string               `json:"rejectReasons,omitempty"`
	Promotable        bool                   `json:"promotable"`
}

// OptimizationRun documents the deterministic walk-forward layout.
type OptimizationRun struct {
	SchemaVersion     string         `json:"schemaVersion"`
	GeneratedAt       time.Time      `json:"generatedAt"`
	AsOf              time.Time      `json:"asOf"`
	CompletedWeekEnd  time.Time      `json:"completedWeekEnd"`
	SearchWeeks       []WeeklyWindow `json:"searchWeeks"`
	ValidationWeeks   []WeeklyWindow `json:"validationWeeks"`
	HoldoutWeeks      []WeeklyWindow `json:"holdoutWeeks"`
	CoarseCandidates  int            `json:"coarseCandidates"`
	RefinedCandidates int            `json:"refinedCandidates"`
	Finalists         int            `json:"finalists"`
}

// OptimizationReport is the versioned JSON artifact emitted by the optimizer.
type OptimizationReport struct {
	Run          OptimizationRun      `json:"run"`
	Progress     OptimizationProgress `json:"progress"`
	Candidates   []OptimizerCandidate `json:"candidates"`
	Winner       *OptimizerCandidate  `json:"winner,omitempty"`
	ProfilePath  string               `json:"profilePath,omitempty"`
	GeneratedAt  time.Time            `json:"generatedAt"`
	ArtifactPath string               `json:"artifactPath,omitempty"`
}

// OptimizationProgress captures in-flight optimizer state so the latest report
// file is useful before the run completes.
type OptimizationProgress struct {
	Stage                   string    `json:"stage"`
	Completed               int       `json:"completed"`
	Total                   int       `json:"total"`
	Message                 string    `json:"message,omitempty"`
	UpdatedAt               time.Time `json:"updatedAt"`
	RunStartedAt            time.Time `json:"runStartedAt,omitempty"`
	StageStartedAt          time.Time `json:"stageStartedAt,omitempty"`
	StageElapsedSeconds     int64     `json:"stageElapsedSeconds,omitempty"`
	StageRemainingSeconds   int64     `json:"stageRemainingSeconds,omitempty"`
	StageETA                time.Time `json:"stageEta,omitempty"`
	OverallElapsedSeconds   int64     `json:"overallElapsedSeconds,omitempty"`
	OverallRemainingSeconds int64     `json:"overallRemainingSeconds,omitempty"`
	OverallETA              time.Time `json:"overallEta,omitempty"`
}

// ArtifactStatus powers the operator-visible pending optimizer state.
type ArtifactStatus struct {
	PendingProfileName        string
	PendingProfileVersion     string
	LastOptimizerRun          time.Time
	LastPaperValidationResult string
}

// Params controls an optimization run.
type Params struct {
	BaseConfig      config.TradingConfig
	Bars            []backtest.InputBar
	LoadWeek        func(context.Context, WeeklyWindow) ([]backtest.InputBar, error)
	AsOf            time.Time
	ArtifactDir     string
	SearchWeeks     []WeeklyWindow
	ValidationWeeks []WeeklyWindow
	HoldoutWeeks    []WeeklyWindow
}

type weeklyBarSlice struct {
	Window WeeklyWindow
	Bars   []backtest.InputBar
}

type staticInputBarIterator struct {
	bars []backtest.InputBar
	next int
}

type candidateSeed struct {
	id      string
	profile config.StrategyProfile
	config  config.TradingConfig
}

type progressTracker struct {
	runStartedAt time.Time
	stageOrder   []string
	stages       map[string]*trackedStage
}

type trackedStage struct {
	total      int
	completed  int
	startedAt  time.Time
	finishedAt time.Time
}

type floatKnobSpec struct {
	name string
	grid func(base config.TradingConfig) []float64
	get  func(config.TradingConfig) float64
	set  func(*config.TradingConfig, float64)
}

type intKnobSpec struct {
	name string
	grid func(base config.TradingConfig) []int
	get  func(config.TradingConfig) int
	set  func(*config.TradingConfig, int)
}

var floatKnobs = []floatKnobSpec{
	{
		name: "MinEntryScore",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.MinEntryScore, 10.0, 20.0, 1.50, 3.00)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.MinEntryScore },
		set: func(cfg *config.TradingConfig, v float64) { cfg.MinEntryScore = round2(v) },
	},
	{
		name: "MinOneMinuteReturnPct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.MinOneMinuteReturnPct, 0.05, 1.20, 0.15, 0.30)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.MinOneMinuteReturnPct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.MinOneMinuteReturnPct = round2(v) },
	},
	{
		name: "MinThreeMinuteReturnPct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.MinThreeMinuteReturnPct, 0.20, 2.25, 0.20, 0.40)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.MinThreeMinuteReturnPct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.MinThreeMinuteReturnPct = round2(v) },
	},
	{
		name: "MinVolumeRate",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.MinVolumeRate, 0.80, 2.40, 0.15, 0.30)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.MinVolumeRate },
		set: func(cfg *config.TradingConfig, v float64) { cfg.MinVolumeRate = round2(v) },
	},
	{
		name: "MaxPriceVsOpenPct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.MaxPriceVsOpenPct, 15.0, 40.0, 2.0, 4.0)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.MaxPriceVsOpenPct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.MaxPriceVsOpenPct = round2(v) },
	},
	{
		name: "ScannerMinPriceVsOpenPctFloor",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ScannerMinPriceVsOpenPctFloor, 1.0, 4.0, 0.25, 0.50)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ScannerMinPriceVsOpenPctFloor },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ScannerMinPriceVsOpenPctFloor = round2(v) },
	},
	{
		name: "ScannerMinSetupRelativeVolumeExtra",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ScannerMinSetupRelativeVolumeExtra, 0.0, 1.0, 0.10, 0.20)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ScannerMinSetupRelativeVolumeExtra },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ScannerMinSetupRelativeVolumeExtra = round2(v) },
	},
	{
		name: "ScannerMinSetupVolumeRateOffset",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ScannerMinSetupVolumeRateOffset, -0.30, 0.15, 0.05, 0.10)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ScannerMinSetupVolumeRateOffset },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ScannerMinSetupVolumeRateOffset = round2(v) },
	},
	{
		name: "ScannerVWAPTolerancePct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ScannerVWAPTolerancePct, -0.50, 0.15, 0.10, 0.20)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ScannerVWAPTolerancePct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ScannerVWAPTolerancePct = round2(v) },
	},
	{
		name: "ScannerConsolidationMaxPct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ScannerConsolidationMaxPct, 3.0, 6.0, 0.25, 0.50)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ScannerConsolidationMaxPct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ScannerConsolidationMaxPct = round2(v) },
	},
	{
		name: "ScannerRenewedVolumeRateMin",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ScannerRenewedVolumeRateMin, 0.85, 1.50, 0.05, 0.10)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ScannerRenewedVolumeRateMin },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ScannerRenewedVolumeRateMin = round2(v) },
	},
	{
		name: "RiskPerTradePct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(clampFloat(base.RiskPerTradePct, 0.0025, 0.0200), 0.0025, 0.0200, 0.0015, 0.0030)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.RiskPerTradePct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.RiskPerTradePct = round4(v) },
	},
	{
		name: "BreakEvenMinR",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.BreakEvenMinR, 0.25, 0.85, 0.05, 0.10)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.BreakEvenMinR },
		set: func(cfg *config.TradingConfig, v float64) { cfg.BreakEvenMinR = round2(v) },
	},
	{
		name: "TrailActivationR",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.TrailActivationR, 0.45, 1.10, 0.05, 0.10)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.TrailActivationR },
		set: func(cfg *config.TradingConfig, v float64) { cfg.TrailActivationR = round2(v) },
	},
	{
		name: "TrailATRMultiplier",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.TrailATRMultiplier, 0.90, 2.10, 0.10, 0.20)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.TrailATRMultiplier },
		set: func(cfg *config.TradingConfig, v float64) { cfg.TrailATRMultiplier = round2(v) },
	},
	{
		name: "TightTrailTriggerR",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.TightTrailTriggerR, 0.85, 1.60, 0.10, 0.20)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.TightTrailTriggerR },
		set: func(cfg *config.TradingConfig, v float64) { cfg.TightTrailTriggerR = round2(v) },
	},
	{
		name: "TightTrailATRMultiplier",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.TightTrailATRMultiplier, 0.40, 1.00, 0.05, 0.10)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.TightTrailATRMultiplier },
		set: func(cfg *config.TradingConfig, v float64) { cfg.TightTrailATRMultiplier = round2(v) },
	},
	{
		name: "ProfitTargetR",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ProfitTargetR, 0.90, 1.60, 0.10, 0.20)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ProfitTargetR },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ProfitTargetR = round2(v) },
	},
	{
		name: "FailedBreakoutCutR",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.FailedBreakoutCutR, 0.03, 0.10, 0.01, 0.02)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.FailedBreakoutCutR },
		set: func(cfg *config.TradingConfig, v float64) { cfg.FailedBreakoutCutR = round2(v) },
	},
	{
		name: "StructureConfirmR",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.StructureConfirmR, 0.00, 0.30, 0.025, 0.05)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.StructureConfirmR },
		set: func(cfg *config.TradingConfig, v float64) { cfg.StructureConfirmR = round2(v) },
	},
	{
		name: "ShortMinEntryScore",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ShortMinEntryScore, 14.0, 28.0, 1.5, 3.0)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ShortMinEntryScore },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ShortMinEntryScore = round2(v) },
	},
	{
		name: "MaxShortExposurePct",
		grid: func(base config.TradingConfig) []float64 {
			maxShortExposure := maxFloat(0.15, base.MaxExposurePct)
			return uniqueFloatGrid(base.MaxShortExposurePct, 0.10, maxShortExposure, 0.03, 0.06)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.MaxShortExposurePct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.MaxShortExposurePct = round2(v) },
	},
	{
		name: "ShortPeakExtensionMinPct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ShortPeakExtensionMinPct, 8.0, 22.0, 1.5, 3.0)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ShortPeakExtensionMinPct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ShortPeakExtensionMinPct = round2(v) },
	},
	{
		name: "ShortVWAPBreakMinPct",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ShortVWAPBreakMinPct, -2.50, -0.20, 0.20, 0.40)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ShortVWAPBreakMinPct },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ShortVWAPBreakMinPct = round2(v) },
	},
	{
		name: "ShortStopATRMultiplier",
		grid: func(base config.TradingConfig) []float64 {
			return uniqueFloatGrid(base.ShortStopATRMultiplier, 0.75, 2.25, 0.15, 0.30)
		},
		get: func(cfg config.TradingConfig) float64 { return cfg.ShortStopATRMultiplier },
		set: func(cfg *config.TradingConfig, v float64) { cfg.ShortStopATRMultiplier = round2(v) },
	},
}

var intKnobs = []intKnobSpec{
	{
		name: "EntryCooldownSec",
		grid: func(base config.TradingConfig) []int { return uniqueIntGrid(base.EntryCooldownSec, 30, 120, 15, 30) },
		get:  func(cfg config.TradingConfig) int { return cfg.EntryCooldownSec },
		set:  func(cfg *config.TradingConfig, v int) { cfg.EntryCooldownSec = v },
	},
	{
		name: "MaxTradesPerDay",
		grid: func(base config.TradingConfig) []int { return uniqueIntGrid(base.MaxTradesPerDay, 4, 20, 2, 4) },
		get:  func(cfg config.TradingConfig) int { return cfg.MaxTradesPerDay },
		set:  func(cfg *config.TradingConfig, v int) { cfg.MaxTradesPerDay = v },
	},
	{
		name: "MaxOpenPositions",
		grid: func(base config.TradingConfig) []int { return uniqueIntGrid(base.MaxOpenPositions, 1, 4, 1, 1) },
		get:  func(cfg config.TradingConfig) int { return cfg.MaxOpenPositions },
		set:  func(cfg *config.TradingConfig, v int) { cfg.MaxOpenPositions = v },
	},
	{
		name: "BreakEvenHoldMinutes",
		grid: func(base config.TradingConfig) []int { return uniqueIntGrid(base.BreakEvenHoldMinutes, 2, 6, 1, 1) },
		get:  func(cfg config.TradingConfig) int { return cfg.BreakEvenHoldMinutes },
		set:  func(cfg *config.TradingConfig, v int) { cfg.BreakEvenHoldMinutes = v },
	},
	{
		name: "MaxShortOpenPositions",
		grid: func(base config.TradingConfig) []int {
			upper := maxInt(1, minInt(3, base.MaxOpenPositions))
			return uniqueIntGrid(base.MaxShortOpenPositions, 1, upper, 1, 1)
		},
		get: func(cfg config.TradingConfig) int { return cfg.MaxShortOpenPositions },
		set: func(cfg *config.TradingConfig, v int) { cfg.MaxShortOpenPositions = v },
	},
}

// Run executes the weekly walk-forward optimizer and writes JSON artifacts.
func Run(ctx context.Context, params Params) (OptimizationReport, *config.TradingProfile, error) {
	if len(params.Bars) == 0 && params.LoadWeek == nil {
		return OptimizationReport{}, nil, fmt.Errorf("optimizer requires historical bars")
	}
	runStartedAt := time.Now().UTC()
	base := config.NormalizeStrategyProfile(params.BaseConfig)
	completedWeekEnd, searchWeeks, validationWeeks, holdoutWeeks, err := resolveRunWindows(params)
	if err != nil {
		return OptimizationReport{}, nil, err
	}

	report := OptimizationReport{
		Run: OptimizationRun{
			SchemaVersion:    reportSchemaV1,
			GeneratedAt:      runStartedAt,
			AsOf:             params.AsOf.UTC(),
			CompletedWeekEnd: completedWeekEnd,
			SearchWeeks:      searchWeeks,
			ValidationWeeks:  validationWeeks,
			HoldoutWeeks:     holdoutWeeks,
		},
		GeneratedAt: runStartedAt,
	}
	tracker := newProgressTracker(runStartedAt)
	tracker.RegisterStage("prepare", 1)
	lastReportWriteAt := time.Time{}
	lastReportWriteStage := ""
	updateProgress := func(stage string, completed, total int, message string) error {
		now := time.Now().UTC()
		report.Progress = tracker.Snapshot(stage, completed, total, message, now)
		log.Printf(
			"Optimizer progress stage=%s completed=%d total=%d stage_elapsed=%s stage_eta=%s overall_elapsed=%s overall_eta=%s %s",
			stage,
			completed,
			total,
			formatDurationCompact(time.Duration(report.Progress.StageElapsedSeconds)*time.Second),
			formatETA(report.Progress.StageETA, report.Progress.StageRemainingSeconds),
			formatDurationCompact(time.Duration(report.Progress.OverallElapsedSeconds)*time.Second),
			formatETA(report.Progress.OverallETA, report.Progress.OverallRemainingSeconds),
			strings.TrimSpace(message),
		)
		shouldWrite := stage != lastReportWriteStage ||
			completed == 0 ||
			completed == total ||
			lastReportWriteAt.IsZero() ||
			now.Sub(lastReportWriteAt) >= time.Second
		if !shouldWrite {
			return nil
		}
		if err := writeReportArtifact(params.ArtifactDir, &report); err != nil {
			return err
		}
		lastReportWriteAt = now
		lastReportWriteStage = stage
		return nil
	}

	loadWeek := params.LoadWeek
	if loadWeek == nil {
		if err := updateProgress("prepare", 0, 1, "partitioning in-memory bars by completed trading week"); err != nil {
			return OptimizationReport{}, nil, err
		}
		partitionWeeks := uniqueWeeklyWindows(append(append(append([]WeeklyWindow(nil), searchWeeks...), validationWeeks...), holdoutWeeks...))
		allWeekSlices := sliceBarsByWeek(params.Bars, partitionWeeks)
		slicesByLabel := make(map[string]weeklyBarSlice, len(allWeekSlices))
		for _, week := range allWeekSlices {
			slicesByLabel[week.Window.Label] = week
		}
		log.Printf(
			"Optimizer dataset weekly_partitions=%d search_bars=%d validation_bars=%d holdout_bars=%d total_bars=%d",
			len(partitionWeeks),
			totalBarsForWindows(searchWeeks, slicesByLabel),
			totalBarsForWindows(validationWeeks, slicesByLabel),
			totalBarsForWindows(holdoutWeeks, slicesByLabel),
			totalBarsInSlices(allWeekSlices),
		)
		loadWeek = func(_ context.Context, window WeeklyWindow) ([]backtest.InputBar, error) {
			if week, ok := slicesByLabel[window.Label]; ok {
				return week.Bars, nil
			}
			return nil, fmt.Errorf("no in-memory bars for week %s", window.Label)
		}
		if err := updateProgress("prepare", 1, 1, "weekly bar partitions ready"); err != nil {
			return OptimizationReport{}, nil, err
		}
	} else {
		if err := updateProgress("prepare", 1, 1, "using incremental week loader"); err != nil {
			return OptimizationReport{}, nil, err
		}
	}

	coarseSeeds := buildCoarseSeeds(base)
	report.Run.CoarseCandidates = len(coarseSeeds)
	tracker.RegisterStage("coarse-search", len(coarseSeeds)*len(searchWeeks))
	if err := updateProgress("coarse-search", 0, len(coarseSeeds)*len(searchWeeks), "evaluating coarse strategy candidates"); err != nil {
		return OptimizationReport{}, nil, err
	}
	searchShortlist, searchCandidates, err := evaluateShortlist(ctx, coarseSeeds, searchWeeks, loadWeek, 25, func(candidate *OptimizerCandidate, summary PeriodSummary) {
		candidate.SearchSummary = summary
	}, func(completed, total int, message string, candidates []OptimizerCandidate) error {
		report.Candidates = candidates
		return updateProgress("coarse-search", completed, total, message)
	})
	if err != nil {
		return OptimizationReport{}, nil, err
	}
	report.Candidates = searchCandidates
	if err := writeReportArtifact(params.ArtifactDir, &report); err != nil {
		return OptimizationReport{}, nil, err
	}

	tracker.RegisterStage("validation-shortlist", len(searchShortlist)*len(validationWeeks))
	if err := updateProgress("validation-shortlist", 0, len(searchShortlist)*len(validationWeeks), "ranking coarse shortlist on validation weeks"); err != nil {
		return OptimizationReport{}, nil, err
	}
	validationAnchors, validationCandidates, err := evaluateShortlist(ctx, searchShortlist, validationWeeks, loadWeek, 10, func(candidate *OptimizerCandidate, summary PeriodSummary) {
		candidate.ValidationSummary = summary
	}, func(completed, total int, message string, candidates []OptimizerCandidate) error {
		report.Candidates = candidates
		return updateProgress("validation-shortlist", completed, total, message)
	})
	if err != nil {
		return OptimizationReport{}, nil, err
	}
	report.Candidates = validationCandidates
	if err := writeReportArtifact(params.ArtifactDir, &report); err != nil {
		return OptimizationReport{}, nil, err
	}

	if len(holdoutWeeks) == 0 {
		finalists := topCandidates(validationCandidates, 10, func(candidate OptimizerCandidate) PeriodSummary {
			return candidate.ValidationSummary
		})
		report.Run.Finalists = len(finalists)
		tracker.RegisterStage("finalize", 1)
		if err := updateProgress("finalize", 1, 1, "writing single-window optimizer artifacts"); err != nil {
			return OptimizationReport{}, nil, err
		}
		finalized := finalizeWithoutHoldout(finalists)
		report.Candidates = finalized
		if len(finalized) > 0 {
			winner := finalized[0]
			report.Winner = &winner
			profile := buildTradingProfile(winner, report.Run)
			if err := writeArtifacts(params.ArtifactDir, &report, &profile); err != nil {
				return OptimizationReport{}, nil, err
			}
			return report, &profile, nil
		}
		if err := writeArtifacts(params.ArtifactDir, &report, nil); err != nil {
			return OptimizationReport{}, nil, err
		}
		return report, nil, nil
	}

	refinedSeeds := buildRefinedSeeds(base, validationAnchors)
	report.Run.RefinedCandidates = len(refinedSeeds)
	tracker.RegisterStage("refinement", len(refinedSeeds)*len(validationWeeks))
	if err := updateProgress("refinement", 0, len(refinedSeeds)*len(validationWeeks), "evaluating refined candidates on validation weeks"); err != nil {
		return OptimizationReport{}, nil, err
	}
	refinedCandidates, err := evaluateAll(ctx, refinedSeeds, validationWeeks, loadWeek, func(candidate *OptimizerCandidate, summary PeriodSummary, weeks []WeeklyPerformance) {
		candidate.ValidationSummary = summary
		candidate.ValidationWeeks = weeks
	}, func(completed, total int, message string, candidates []OptimizerCandidate) error {
		report.Candidates = candidates
		return updateProgress("refinement", completed, total, message)
	})
	if err != nil {
		return OptimizationReport{}, nil, err
	}
	report.Candidates = topCandidates(refinedCandidates, 10, func(candidate OptimizerCandidate) PeriodSummary {
		return candidate.ValidationSummary
	})
	if err := writeReportArtifact(params.ArtifactDir, &report); err != nil {
		return OptimizationReport{}, nil, err
	}

	finalists := topCandidates(refinedCandidates, 10, func(candidate OptimizerCandidate) PeriodSummary {
		return candidate.ValidationSummary
	})
	report.Run.Finalists = len(finalists)
	if len(finalists) == 0 {
		tracker.RegisterStage("finalize", 1)
		if err := updateProgress("finalize", 1, 1, "writing final optimizer artifacts"); err != nil {
			return OptimizationReport{}, nil, err
		}
		if err := writeArtifacts(params.ArtifactDir, &report, nil); err != nil {
			return OptimizationReport{}, nil, err
		}
		return report, nil, nil
	}

	tracker.RegisterStage("holdout", len(finalists)*len(holdoutWeeks))
	if err := updateProgress("holdout", 0, len(finalists)*len(holdoutWeeks), "running untouched holdout weeks on finalists"); err != nil {
		return OptimizationReport{}, nil, err
	}
	finalistSeeds := make([]candidateSeed, 0, len(finalists))
	finalistsByID := make(map[string]OptimizerCandidate, len(finalists))
	for _, finalist := range finalists {
		finalistSeeds = append(finalistSeeds, candidateSeed{
			id:      finalist.CandidateID,
			profile: finalist.Profile,
			config:  finalist.Config,
		})
		finalistsByID[finalist.CandidateID] = finalist
	}
	holdoutCandidates, err := evaluateAll(ctx, finalistSeeds, holdoutWeeks, loadWeek, func(candidate *OptimizerCandidate, summary PeriodSummary, weeks []WeeklyPerformance) {
		candidate.HoldoutSummary = summary
		candidate.HoldoutWeeks = weeks
	}, func(completed, total int, message string, candidates []OptimizerCandidate) error {
		report.Candidates = mergeCandidateProgress(finalistsByID, candidates)
		return updateProgress("holdout", completed, total, message)
	})
	if err != nil {
		return OptimizationReport{}, nil, err
	}
	finalized := make([]OptimizerCandidate, 0, len(holdoutCandidates))
	for _, holdoutCandidate := range holdoutCandidates {
		finalist := finalistsByID[holdoutCandidate.CandidateID]
		finalist.HoldoutSummary = holdoutCandidate.HoldoutSummary
		finalist.HoldoutWeeks = holdoutCandidate.HoldoutWeeks
		holdoutSummary := holdoutCandidate.HoldoutSummary
		holdoutPerf := holdoutCandidate.HoldoutWeeks
		combinedValidationHoldout := append(append([]WeeklyPerformance(nil), finalist.ValidationWeeks...), holdoutPerf...)
		combinedSummary := summarizeWeeks(combinedValidationHoldout)
		finalist.Score = CandidateScore{
			HoldoutMedianWeeklyReturnPct: round2(holdoutSummary.MedianWeeklyReturnPct),
			PositiveWeeksPct:             round2(combinedSummary.PositiveWeeksPct),
			HoldoutP25WeeklyReturnPct:    round2(holdoutSummary.P25WeeklyReturnPct),
			ProfitFactor:                 round2(combinedSummary.ProfitFactor),
			MaxDrawdownPct:               round2(holdoutSummary.MaxDrawdownPct),
		}
		finalist.RejectReasons = promotionRejectReasons(holdoutSummary, combinedSummary)
		finalist.Promotable = len(finalist.RejectReasons) == 0
		finalized = append(finalized, finalist)
	}

	sort.Slice(finalized, func(i, j int) bool {
		return compareFinalCandidates(finalized[i], finalized[j]) < 0
	})
	for index := range finalized {
		finalized[index].Rank = index + 1
	}
	report.Candidates = finalized

	var winnerProfile *config.TradingProfile
	if len(finalized) > 0 {
		winner := finalized[0]
		report.Winner = &winner
		profile := buildTradingProfile(winner, report.Run)
		winnerProfile = &profile
	}

	tracker.RegisterStage("finalize", 1)
	if err := updateProgress("finalize", 1, 1, "writing final optimizer artifacts"); err != nil {
		return OptimizationReport{}, nil, err
	}
	if err := writeArtifacts(params.ArtifactDir, &report, winnerProfile); err != nil {
		return OptimizationReport{}, nil, err
	}
	return report, winnerProfile, nil
}

// ApplyStrategyProfileDefaults seeds a deterministic starting point per family.
func ApplyStrategyProfileDefaults(base config.TradingConfig, profile config.StrategyProfile) config.TradingConfig {
	cfg := config.NormalizeStrategyProfile(base)
	cfg.StrategyProfileName = string(profile)
	cfg.StrategyProfileVersion = "optimizer-base"

	switch profile {
	case config.StrategyProfileHighConviction:
		cfg.MinEntryScore = round2(maxFloat(cfg.MinEntryScore+1.5, 15.0))
		cfg.MinRelativeVolume = round2(cfg.MinRelativeVolume + 0.40)
		cfg.MinVolumeRate = round2(cfg.MinVolumeRate + 0.15)
		cfg.MinOneMinuteReturnPct = round2(cfg.MinOneMinuteReturnPct + 0.10)
		cfg.MinThreeMinuteReturnPct = round2(cfg.MinThreeMinuteReturnPct + 0.20)
		cfg.MaxOpenPositions = maxInt(1, cfg.MaxOpenPositions-1)
		cfg.MaxTradesPerDay = maxInt(4, cfg.MaxTradesPerDay-4)
		cfg.EntryCooldownSec = maxInt(45, cfg.EntryCooldownSec)
		cfg.RiskPerTradePct = round4(clampFloat(cfg.RiskPerTradePct*0.60, 0.0025, 0.0125))
		cfg.BreakEvenHoldMinutes = maxInt(2, cfg.BreakEvenHoldMinutes-1)
		cfg.BreakEvenMinR = round2(maxFloat(cfg.BreakEvenMinR, 0.45))
		cfg.TrailActivationR = round2(maxFloat(0.60, cfg.TrailActivationR-0.05))
		cfg.TrailATRMultiplier = round2(minFloat(cfg.TrailATRMultiplier, 1.35))
		cfg.TightTrailTriggerR = round2(maxFloat(cfg.TightTrailTriggerR, 1.00))
		cfg.TightTrailATRMultiplier = round2(minFloat(cfg.TightTrailATRMultiplier, 0.60))
		cfg.ProfitTargetR = round2(minFloat(cfg.ProfitTargetR, 1.10))
		cfg.FailedBreakoutCutR = round2(minFloat(cfg.FailedBreakoutCutR, 0.05))
		cfg.StructureConfirmR = round2(maxFloat(cfg.StructureConfirmR, 0.05))
	case config.StrategyProfileContinuation:
		cfg.MinEntryScore = round2(maxFloat(13.0, cfg.MinEntryScore-0.75))
		cfg.MinRelativeVolume = round2(maxFloat(1.60, cfg.MinRelativeVolume))
		cfg.MinVolumeRate = round2(maxFloat(1.15, cfg.MinVolumeRate-0.10))
		cfg.MinOneMinuteReturnPct = round2(maxFloat(0.15, cfg.MinOneMinuteReturnPct-0.10))
		cfg.MinThreeMinuteReturnPct = round2(maxFloat(0.60, cfg.MinThreeMinuteReturnPct-0.15))
		cfg.MaxOpenPositions = minInt(3, maxInt(1, cfg.MaxOpenPositions))
		cfg.MaxTradesPerDay = minInt(18, maxInt(6, cfg.MaxTradesPerDay))
		cfg.EntryCooldownSec = 45
		cfg.RiskPerTradePct = round4(clampFloat(cfg.RiskPerTradePct*0.75, 0.0025, 0.0150))
		cfg.BreakEvenHoldMinutes = minInt(4, maxInt(2, cfg.BreakEvenHoldMinutes))
		cfg.BreakEvenMinR = round2(minFloat(cfg.BreakEvenMinR, 0.45))
		cfg.TrailActivationR = round2(minFloat(cfg.TrailActivationR, 0.60))
		cfg.TrailATRMultiplier = round2(minFloat(cfg.TrailATRMultiplier, 1.25))
		cfg.TightTrailTriggerR = round2(minFloat(cfg.TightTrailTriggerR, 1.05))
		cfg.TightTrailATRMultiplier = round2(minFloat(cfg.TightTrailATRMultiplier, 0.55))
		cfg.ProfitTargetR = round2(minFloat(cfg.ProfitTargetR, 1.05))
		cfg.FailedBreakoutCutR = round2(minFloat(cfg.FailedBreakoutCutR, 0.04))
		cfg.StructureConfirmR = round2(maxFloat(cfg.StructureConfirmR, 0.10))
	default:
		cfg.StrategyProfileName = string(config.StrategyProfileBaseline)
	}

	cfg.MaxExposurePct = inferMaxExposurePct(cfg)
	return cfg
}

// PriorCompletedWeekEnd returns the most recent completed Friday 20:00 ET
// before or at the provided timestamp.
func PriorCompletedWeekEnd(asOf time.Time) time.Time {
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	local := asOf.In(marketLocation)
	offset := (int(local.Weekday()) - int(time.Friday) + 7) % 7
	target := time.Date(local.Year(), local.Month(), local.Day(), 20, 0, 0, 0, marketLocation).AddDate(0, 0, -offset)
	if local.Weekday() == time.Friday && local.Before(target) {
		target = target.AddDate(0, 0, -7)
	}
	return target.UTC()
}

// BuildWeeklyWindows creates count completed trading-week windows ending at the
// supplied Friday close.
func BuildWeeklyWindows(completedWeekEnd time.Time, count int) []WeeklyWindow {
	if count <= 0 {
		return nil
	}
	localEnd := completedWeekEnd.In(marketLocation)
	windows := make([]WeeklyWindow, 0, count)
	for index := count - 1; index >= 0; index-- {
		weekEnd := localEnd.AddDate(0, 0, -7*index)
		weekStart := weekEnd.AddDate(0, 0, -4)
		start := time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 4, 0, 0, 0, marketLocation).UTC()
		end := time.Date(weekEnd.Year(), weekEnd.Month(), weekEnd.Day(), 19, 59, 59, 0, marketLocation).UTC()
		windows = append(windows, WeeklyWindow{
			Label: weekEnd.Format("2006-01-02"),
			Start: start,
			End:   end,
		})
	}
	return windows
}

func resolveRunWindows(params Params) (time.Time, []WeeklyWindow, []WeeklyWindow, []WeeklyWindow, error) {
	if len(params.SearchWeeks) > 0 || len(params.ValidationWeeks) > 0 || len(params.HoldoutWeeks) > 0 {
		searchWeeks := append([]WeeklyWindow(nil), params.SearchWeeks...)
		validationWeeks := append([]WeeklyWindow(nil), params.ValidationWeeks...)
		holdoutWeeks := append([]WeeklyWindow(nil), params.HoldoutWeeks...)
		if len(searchWeeks) == 0 {
			return time.Time{}, nil, nil, nil, fmt.Errorf("optimizer custom mode requires at least one search window")
		}
		if len(validationWeeks) == 0 {
			validationWeeks = append([]WeeklyWindow(nil), searchWeeks...)
		}
		completedWeekEnd := params.AsOf.UTC()
		if completedWeekEnd.IsZero() {
			completedWeekEnd = latestWindowEnd(searchWeeks, validationWeeks, holdoutWeeks)
		}
		if completedWeekEnd.IsZero() {
			return time.Time{}, nil, nil, nil, fmt.Errorf("optimizer custom mode requires a non-zero window end")
		}
		return completedWeekEnd, searchWeeks, validationWeeks, holdoutWeeks, nil
	}

	completedWeekEnd := PriorCompletedWeekEnd(params.AsOf)
	allWeeks := BuildWeeklyWindows(completedWeekEnd, 20)
	if len(allWeeks) != 20 {
		return time.Time{}, nil, nil, nil, fmt.Errorf("expected 20 completed weeks, got %d", len(allWeeks))
	}
	searchWeeks := append([]WeeklyWindow(nil), allWeeks[:12]...)
	validationWeeks := append([]WeeklyWindow(nil), allWeeks[12:16]...)
	holdoutWeeks := append([]WeeklyWindow(nil), allWeeks[16:]...)
	return completedWeekEnd, searchWeeks, validationWeeks, holdoutWeeks, nil
}

func latestWindowEnd(groups ...[]WeeklyWindow) time.Time {
	var latest time.Time
	for _, group := range groups {
		for _, window := range group {
			if window.End.After(latest) {
				latest = window.End
			}
		}
	}
	return latest
}

func uniqueWeeklyWindows(windows []WeeklyWindow) []WeeklyWindow {
	if len(windows) == 0 {
		return nil
	}
	out := make([]WeeklyWindow, 0, len(windows))
	seen := make(map[string]struct{}, len(windows))
	for _, window := range windows {
		if _, exists := seen[window.Label]; exists {
			continue
		}
		seen[window.Label] = struct{}{}
		out = append(out, window)
	}
	return out
}

func totalBarsForWindows(windows []WeeklyWindow, slicesByLabel map[string]weeklyBarSlice) int {
	total := 0
	for _, window := range windows {
		if slice, ok := slicesByLabel[window.Label]; ok {
			total += len(slice.Bars)
		}
	}
	return total
}

// LoadArtifactStatus reads the latest optimizer artifacts for dashboard status.
func LoadArtifactStatus(artifactDir string) (ArtifactStatus, error) {
	status := ArtifactStatus{}
	artifactDir = artifactDirOrDefault(artifactDir)

	reportPath := filepath.Join(artifactDir, "latest-report.json")
	reportRaw, err := os.ReadFile(reportPath)
	if err == nil {
		var report OptimizationReport
		if err := json.Unmarshal(reportRaw, &report); err != nil {
			return ArtifactStatus{}, fmt.Errorf("decode optimizer report %s: %w", reportPath, err)
		}
		status.LastOptimizerRun = report.GeneratedAt
	} else if !os.IsNotExist(err) {
		return ArtifactStatus{}, err
	}

	profilePath := filepath.Join(artifactDir, "latest-candidate-profile.json")
	profileRaw, err := os.ReadFile(profilePath)
	if err == nil {
		var profile config.TradingProfile
		if err := json.Unmarshal(profileRaw, &profile); err != nil {
			return ArtifactStatus{}, fmt.Errorf("decode trading profile %s: %w", profilePath, err)
		}
		status.PendingProfileName = string(profile.Name)
		status.PendingProfileVersion = profile.Version
		status.LastPaperValidationResult = profile.Promotion.LastPaperValidationResult
		if status.LastPaperValidationResult == "" {
			status.LastPaperValidationResult = "pending-paper-validation"
		}
	} else if !os.IsNotExist(err) {
		return ArtifactStatus{}, err
	}

	return status, nil
}

func buildCoarseSeeds(base config.TradingConfig) []candidateSeed {
	byID := make(map[string]candidateSeed)
	profiles := []config.StrategyProfile{
		config.StrategyProfileBaseline,
		config.StrategyProfileHighConviction,
		config.StrategyProfileContinuation,
	}
	for _, profile := range profiles {
		profileBase := ApplyStrategyProfileDefaults(base, profile)
		addSeed(byID, candidateSeed{
			id:      fmt.Sprintf("%s-base", profile),
			profile: profile,
			config:  normalizeCandidateConfig(profileBase),
		})
		for _, knob := range floatKnobs {
			grid := knob.grid(profileBase)
			for _, idx := range []int{0, len(grid) / 2, len(grid) - 1} {
				cfg := profileBase
				knob.set(&cfg, grid[idx])
				cfg = normalizeCandidateConfig(cfg)
				addSeed(byID, candidateSeed{
					id:      fmt.Sprintf("%s-%s-%d", profile, knob.name, idx),
					profile: profile,
					config:  cfg,
				})
			}
		}
		for _, knob := range intKnobs {
			grid := knob.grid(profileBase)
			for _, idx := range []int{0, len(grid) / 2, len(grid) - 1} {
				cfg := profileBase
				knob.set(&cfg, grid[idx])
				cfg = normalizeCandidateConfig(cfg)
				addSeed(byID, candidateSeed{
					id:      fmt.Sprintf("%s-%s-%d", profile, knob.name, idx),
					profile: profile,
					config:  cfg,
				})
			}
		}
	}

	out := make([]candidateSeed, 0, len(byID))
	for _, seed := range byID {
		out = append(out, seed)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func buildRefinedSeeds(base config.TradingConfig, anchors []candidateSeed) []candidateSeed {
	byID := make(map[string]candidateSeed)
	for _, anchor := range anchors {
		addSeed(byID, candidateSeed{
			id:      anchor.id,
			profile: anchor.profile,
			config:  anchor.config,
		})
		profileBase := ApplyStrategyProfileDefaults(base, anchor.profile)
		for _, knob := range floatKnobs {
			grid := knob.grid(profileBase)
			current := nearestFloatIndex(grid, knob.get(anchor.config))
			for _, neighbor := range []int{current - 1, current + 1} {
				if neighbor < 0 || neighbor >= len(grid) {
					continue
				}
				cfg := anchor.config
				knob.set(&cfg, grid[neighbor])
				cfg = normalizeCandidateConfig(cfg)
				addSeed(byID, candidateSeed{
					id:      fmt.Sprintf("%s-%s-neighbor-%d", anchor.id, knob.name, neighbor),
					profile: anchor.profile,
					config:  cfg,
				})
			}
		}
		for _, knob := range intKnobs {
			grid := knob.grid(profileBase)
			current := nearestIntIndex(grid, knob.get(anchor.config))
			for _, neighbor := range []int{current - 1, current + 1} {
				if neighbor < 0 || neighbor >= len(grid) {
					continue
				}
				cfg := anchor.config
				knob.set(&cfg, grid[neighbor])
				cfg = normalizeCandidateConfig(cfg)
				addSeed(byID, candidateSeed{
					id:      fmt.Sprintf("%s-%s-neighbor-%d", anchor.id, knob.name, neighbor),
					profile: anchor.profile,
					config:  cfg,
				})
			}
		}
	}

	out := make([]candidateSeed, 0, len(byID))
	for _, seed := range byID {
		out = append(out, seed)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func evaluateShortlist(
	ctx context.Context,
	seeds []candidateSeed,
	windows []WeeklyWindow,
	loadWeek func(context.Context, WeeklyWindow) ([]backtest.InputBar, error),
	limit int,
	assign func(*OptimizerCandidate, PeriodSummary),
	onProgress func(completed, total int, message string, candidates []OptimizerCandidate) error,
) ([]candidateSeed, []OptimizerCandidate, error) {
	candidates, err := evaluateAll(ctx, seeds, windows, loadWeek, func(candidate *OptimizerCandidate, summary PeriodSummary, _ []WeeklyPerformance) {
		assign(candidate, summary)
	}, onProgress)
	if err != nil {
		return nil, nil, err
	}
	top := topCandidates(candidates, limit, func(candidate OptimizerCandidate) PeriodSummary {
		if len(candidate.ValidationWeeks) > 0 {
			return candidate.ValidationSummary
		}
		return candidate.SearchSummary
	})
	out := make([]candidateSeed, 0, len(top))
	for _, candidate := range top {
		out = append(out, candidateSeed{
			id:      candidate.CandidateID,
			profile: candidate.Profile,
			config:  candidate.Config,
		})
	}
	return out, candidates, nil
}

func evaluateAll(
	ctx context.Context,
	seeds []candidateSeed,
	windows []WeeklyWindow,
	loadWeek func(context.Context, WeeklyWindow) ([]backtest.InputBar, error),
	assign func(*OptimizerCandidate, PeriodSummary, []WeeklyPerformance),
	onProgress func(completed, total int, message string, candidates []OptimizerCandidate) error,
) ([]OptimizerCandidate, error) {
	performanceByCandidate := make([][]WeeklyPerformance, len(seeds))
	results := make([]OptimizerCandidate, len(seeds))
	for index, seed := range seeds {
		results[index] = OptimizerCandidate{
			CandidateID: seed.id,
			Profile:     seed.profile,
			Config:      seed.config,
		}
	}
	total := len(seeds) * len(windows)
	completed := 0

	for weekIndex, window := range windows {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		bars, err := loadWeek(ctx, window)
		if err != nil {
			return nil, fmt.Errorf("load week %s: %w", window.Label, err)
		}
		if onProgress != nil {
			if err := onProgress(completed, total, fmt.Sprintf("loaded week=%s bars=%d week_index=%d/%d", window.Label, len(bars), weekIndex+1, len(windows)), results); err != nil {
				return nil, err
			}
		}
		for candidateIndex, seed := range seeds {
			performance, err := evaluateSingleWeek(ctx, seed.config, window, bars)
			if err != nil {
				return nil, err
			}
			performanceByCandidate[candidateIndex] = append(performanceByCandidate[candidateIndex], performance)
			candidate := results[candidateIndex]
			assign(&candidate, summarizeWeeks(performanceByCandidate[candidateIndex]), performanceByCandidate[candidateIndex])
			results[candidateIndex] = candidate
			completed++
			if onProgress != nil {
				message := fmt.Sprintf(
					"week=%s candidate=%s candidate_index=%d/%d week_index=%d/%d",
					window.Label,
					seed.id,
					candidateIndex+1,
					len(seeds),
					weekIndex+1,
					len(windows),
				)
				if err := onProgress(completed, total, message, results); err != nil {
					return nil, err
				}
			}
		}
	}
	return results, nil
}

func evaluateWeeks(
	ctx context.Context,
	cfg config.TradingConfig,
	windows []WeeklyWindow,
	loadWeek func(context.Context, WeeklyWindow) ([]backtest.InputBar, error),
) (PeriodSummary, []WeeklyPerformance, error) {
	out := make([]WeeklyPerformance, 0, len(windows))
	for _, window := range windows {
		select {
		case <-ctx.Done():
			return PeriodSummary{}, nil, ctx.Err()
		default:
		}
		bars, err := loadWeek(ctx, window)
		if err != nil {
			return PeriodSummary{}, nil, fmt.Errorf("load week %s: %w", window.Label, err)
		}
		performance, err := evaluateSingleWeek(ctx, cfg, window, bars)
		if err != nil {
			return PeriodSummary{}, nil, err
		}
		out = append(out, performance)
	}
	return summarizeWeeks(out), out, nil
}

func evaluateSingleWeek(ctx context.Context, cfg config.TradingConfig, window WeeklyWindow, bars []backtest.InputBar) (WeeklyPerformance, error) {
	if len(bars) == 0 {
		return WeeklyPerformance{
			Label:        window.Label,
			Start:        window.Start,
			End:          window.End,
			EndingEquity: round2(cfg.StartingCapital),
		}, nil
	}
	result, err := backtest.Run(ctx, cfg, backtest.RunConfig{
		Iterator: &staticInputBarIterator{bars: bars},
	})
	if err != nil {
		return WeeklyPerformance{}, fmt.Errorf("evaluate week %s: %w", window.Label, err)
	}
	performance := WeeklyPerformance{
		Label:          window.Label,
		Start:          window.Start,
		End:            window.End,
		NetPnL:         round2(result.NetPnL),
		ReturnPct:      round2(percentReturn(result.NetPnL, result.StartingCapital)),
		ProfitFactor:   round2(result.ProfitFactor),
		MaxDrawdownPct: round2(result.MaxDrawdownPct),
		Trades:         result.Trades,
		WinningTrades:  result.Wins,
		LosingTrades:   result.Losses,
		EndingEquity:   round2(result.EndingEquity),
	}
	for _, trade := range result.ClosedTrades {
		if domain.IsShort(trade.Side) {
			performance.ShortTrades++
			performance.ShortPnL += trade.PnL
		} else {
			performance.LongTrades++
			performance.LongPnL += trade.PnL
		}
		performance.ClosedTradePnLs = append(performance.ClosedTradePnLs, trade.PnL)
	}
	performance.LongPnL = round2(performance.LongPnL)
	performance.ShortPnL = round2(performance.ShortPnL)
	return performance, nil
}

func summarizeWeeks(weeks []WeeklyPerformance) PeriodSummary {
	if len(weeks) == 0 {
		return PeriodSummary{}
	}
	returns := make([]float64, 0, len(weeks))
	positiveWeeks := 0
	trades := 0
	longTrades := 0
	shortTrades := 0
	maxDrawdown := 0.0
	worstWeek := math.MaxFloat64
	grossWins := 0.0
	grossLosses := 0.0
	longPnL := 0.0
	shortPnL := 0.0
	for _, week := range weeks {
		returns = append(returns, week.ReturnPct)
		trades += week.Trades
		longTrades += week.LongTrades
		shortTrades += week.ShortTrades
		longPnL += week.LongPnL
		shortPnL += week.ShortPnL
		if week.ReturnPct > 0 {
			positiveWeeks++
		}
		if week.MaxDrawdownPct > maxDrawdown {
			maxDrawdown = week.MaxDrawdownPct
		}
		if week.ReturnPct < worstWeek {
			worstWeek = week.ReturnPct
		}
		for _, pnl := range week.ClosedTradePnLs {
			if pnl >= 0 {
				grossWins += pnl
			} else {
				grossLosses += math.Abs(pnl)
			}
		}
	}
	profitFactor := 0.0
	if grossLosses > 0 {
		profitFactor = grossWins / grossLosses
	}
	sort.Float64s(returns)
	return PeriodSummary{
		Weeks:                 len(weeks),
		Trades:                trades,
		LongTrades:            longTrades,
		ShortTrades:           shortTrades,
		PositiveWeeks:         positiveWeeks,
		PositiveWeeksPct:      round2((float64(positiveWeeks) / float64(len(weeks))) * 100),
		MedianWeeklyReturnPct: round2(percentile(returns, 0.50)),
		P25WeeklyReturnPct:    round2(percentile(returns, 0.25)),
		ProfitFactor:          round2(profitFactor),
		MaxDrawdownPct:        round2(maxDrawdown),
		WorstWeekPct:          round2(worstWeek),
		LongPnL:               round2(longPnL),
		ShortPnL:              round2(shortPnL),
	}
}

func topCandidates(candidates []OptimizerCandidate, limit int, summary func(OptimizerCandidate) PeriodSummary) []OptimizerCandidate {
	sort.Slice(candidates, func(i, j int) bool {
		return compareSummary(summary(candidates[i]), summary(candidates[j])) < 0
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]OptimizerCandidate, len(candidates))
	copy(out, candidates)
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

func finalizeWithoutHoldout(candidates []OptimizerCandidate) []OptimizerCandidate {
	finalized := make([]OptimizerCandidate, len(candidates))
	copy(finalized, candidates)
	for index := range finalized {
		summary := finalized[index].ValidationSummary
		finalized[index].Score = CandidateScore{
			HoldoutMedianWeeklyReturnPct: round2(summary.MedianWeeklyReturnPct),
			PositiveWeeksPct:             round2(summary.PositiveWeeksPct),
			HoldoutP25WeeklyReturnPct:    round2(summary.P25WeeklyReturnPct),
			ProfitFactor:                 round2(summary.ProfitFactor),
			MaxDrawdownPct:               round2(summary.MaxDrawdownPct),
		}
		finalized[index].RejectReasons = append(finalized[index].RejectReasons, "single-window-no-holdout")
		finalized[index].Promotable = false
	}
	sort.Slice(finalized, func(i, j int) bool {
		return compareFinalCandidates(finalized[i], finalized[j]) < 0
	})
	for index := range finalized {
		finalized[index].Rank = index + 1
	}
	return finalized
}

func compareSummary(a, b PeriodSummary) int {
	switch {
	case a.MedianWeeklyReturnPct > b.MedianWeeklyReturnPct:
		return -1
	case a.MedianWeeklyReturnPct < b.MedianWeeklyReturnPct:
		return 1
	case a.PositiveWeeksPct > b.PositiveWeeksPct:
		return -1
	case a.PositiveWeeksPct < b.PositiveWeeksPct:
		return 1
	case a.P25WeeklyReturnPct > b.P25WeeklyReturnPct:
		return -1
	case a.P25WeeklyReturnPct < b.P25WeeklyReturnPct:
		return 1
	case a.ProfitFactor > b.ProfitFactor:
		return -1
	case a.ProfitFactor < b.ProfitFactor:
		return 1
	case a.MaxDrawdownPct < b.MaxDrawdownPct:
		return -1
	case a.MaxDrawdownPct > b.MaxDrawdownPct:
		return 1
	default:
		return 0
	}
}

func compareFinalCandidates(a, b OptimizerCandidate) int {
	switch {
	case a.Promotable && !b.Promotable:
		return -1
	case !a.Promotable && b.Promotable:
		return 1
	case a.Score.HoldoutMedianWeeklyReturnPct > b.Score.HoldoutMedianWeeklyReturnPct:
		return -1
	case a.Score.HoldoutMedianWeeklyReturnPct < b.Score.HoldoutMedianWeeklyReturnPct:
		return 1
	case a.Score.PositiveWeeksPct > b.Score.PositiveWeeksPct:
		return -1
	case a.Score.PositiveWeeksPct < b.Score.PositiveWeeksPct:
		return 1
	case a.Score.HoldoutP25WeeklyReturnPct > b.Score.HoldoutP25WeeklyReturnPct:
		return -1
	case a.Score.HoldoutP25WeeklyReturnPct < b.Score.HoldoutP25WeeklyReturnPct:
		return 1
	case a.Score.ProfitFactor > b.Score.ProfitFactor:
		return -1
	case a.Score.ProfitFactor < b.Score.ProfitFactor:
		return 1
	case a.Score.MaxDrawdownPct < b.Score.MaxDrawdownPct:
		return -1
	case a.Score.MaxDrawdownPct > b.Score.MaxDrawdownPct:
		return 1
	default:
		return strings.Compare(a.CandidateID, b.CandidateID)
	}
}

func mergeCandidateProgress(base map[string]OptimizerCandidate, partial []OptimizerCandidate) []OptimizerCandidate {
	merged := make([]OptimizerCandidate, 0, len(partial))
	for _, candidate := range partial {
		if existing, ok := base[candidate.CandidateID]; ok {
			existing.HoldoutSummary = candidate.HoldoutSummary
			existing.HoldoutWeeks = candidate.HoldoutWeeks
			merged = append(merged, existing)
			continue
		}
		merged = append(merged, candidate)
	}
	return merged
}

func newProgressTracker(runStartedAt time.Time) *progressTracker {
	return &progressTracker{
		runStartedAt: runStartedAt,
		stageOrder:   make([]string, 0, 6),
		stages:       make(map[string]*trackedStage, 6),
	}
}

func (t *progressTracker) RegisterStage(stage string, total int) {
	if strings.TrimSpace(stage) == "" {
		return
	}
	tracked, ok := t.stages[stage]
	if !ok {
		tracked = &trackedStage{}
		t.stages[stage] = tracked
		t.stageOrder = append(t.stageOrder, stage)
	}
	if total > 0 {
		tracked.total = total
	}
}

func (t *progressTracker) Snapshot(stage string, completed, total int, message string, now time.Time) OptimizationProgress {
	t.RegisterStage(stage, total)
	tracked := t.stages[stage]
	if tracked.startedAt.IsZero() {
		tracked.startedAt = now
	}
	if total > 0 {
		tracked.total = total
	}
	if completed < 0 {
		completed = 0
	}
	if tracked.total > 0 && completed > tracked.total {
		completed = tracked.total
	}
	tracked.completed = completed
	if tracked.total > 0 && completed >= tracked.total {
		tracked.finishedAt = now
	}

	stageElapsed := now.Sub(tracked.startedAt)
	stageRemaining := t.estimateStageRemaining(stage, now)
	overallElapsed := now.Sub(t.runStartedAt)
	overallRemaining := t.estimateOverallRemaining(stage, now)

	progress := OptimizationProgress{
		Stage:                 stage,
		Completed:             completed,
		Total:                 tracked.total,
		Message:               message,
		UpdatedAt:             now,
		RunStartedAt:          t.runStartedAt,
		StageStartedAt:        tracked.startedAt,
		StageElapsedSeconds:   int64(stageElapsed.Round(time.Second) / time.Second),
		OverallElapsedSeconds: int64(overallElapsed.Round(time.Second) / time.Second),
	}
	if stageRemaining > 0 {
		progress.StageRemainingSeconds = int64(stageRemaining.Round(time.Second) / time.Second)
		progress.StageETA = now.Add(stageRemaining)
	}
	if overallRemaining > 0 {
		progress.OverallRemainingSeconds = int64(overallRemaining.Round(time.Second) / time.Second)
		progress.OverallETA = now.Add(overallRemaining)
	}
	return progress
}

func (t *progressTracker) estimateStageRemaining(stage string, now time.Time) time.Duration {
	tracked, ok := t.stages[stage]
	if !ok || tracked.total <= 0 || tracked.completed <= 0 || tracked.completed >= tracked.total || tracked.startedAt.IsZero() {
		return 0
	}
	elapsed := now.Sub(tracked.startedAt)
	if elapsed <= 0 {
		return 0
	}
	avgPerUnit := elapsed / time.Duration(tracked.completed)
	return avgPerUnit * time.Duration(tracked.total-tracked.completed)
}

func (t *progressTracker) estimateOverallRemaining(currentStage string, now time.Time) time.Duration {
	aggregateAvg := t.aggregateAveragePerUnit(now)
	if aggregateAvg <= 0 {
		return 0
	}
	var remaining time.Duration
	for _, stage := range t.stageOrder {
		tracked := t.stages[stage]
		if tracked == nil || tracked.total <= 0 || tracked.completed >= tracked.total {
			continue
		}
		unitsRemaining := tracked.total - tracked.completed
		if unitsRemaining <= 0 {
			continue
		}
		if stage == currentStage {
			if stageRemaining := t.estimateStageRemaining(stage, now); stageRemaining > 0 {
				remaining += stageRemaining
				continue
			}
		}
		remaining += aggregateAvg * time.Duration(unitsRemaining)
	}
	return remaining
}

func (t *progressTracker) aggregateAveragePerUnit(now time.Time) time.Duration {
	var totalDuration time.Duration
	totalUnits := 0
	for _, stage := range t.stageOrder {
		tracked := t.stages[stage]
		if tracked == nil || tracked.total <= 1 || tracked.completed <= 0 || tracked.startedAt.IsZero() {
			continue
		}
		elapsedEnd := now
		if !tracked.finishedAt.IsZero() && tracked.completed >= tracked.total {
			elapsedEnd = tracked.finishedAt
		}
		elapsed := elapsedEnd.Sub(tracked.startedAt)
		if elapsed <= 0 {
			continue
		}
		totalDuration += elapsed
		totalUnits += tracked.completed
	}
	if totalUnits == 0 {
		return 0
	}
	return totalDuration / time.Duration(totalUnits)
}

func formatETA(eta time.Time, remainingSeconds int64) string {
	if eta.IsZero() || remainingSeconds <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%s (%s)", eta.In(marketLocation).Format(time.RFC3339), formatDurationCompact(time.Duration(remainingSeconds)*time.Second))
}

func formatDurationCompact(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	d -= minutes * time.Minute
	seconds := d / time.Second
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func (it *staticInputBarIterator) Next() (backtest.InputBar, bool, error) {
	if it.next >= len(it.bars) {
		return backtest.InputBar{}, false, nil
	}
	item := it.bars[it.next]
	it.next++
	return item, true, nil
}

func (it *staticInputBarIterator) Close() error {
	return nil
}

func sliceBarsByWeek(bars []backtest.InputBar, windows []WeeklyWindow) []weeklyBarSlice {
	out := make([]weeklyBarSlice, len(windows))
	for index, window := range windows {
		out[index] = weeklyBarSlice{
			Window: window,
			Bars:   make([]backtest.InputBar, 0, 4096),
		}
	}
	if len(windows) == 0 || len(bars) == 0 {
		return out
	}
	windowIndex := 0
	for _, bar := range bars {
		for windowIndex < len(windows) && bar.Timestamp.After(windows[windowIndex].End) {
			windowIndex++
		}
		if windowIndex >= len(windows) {
			break
		}
		window := windows[windowIndex]
		if bar.Timestamp.Before(window.Start) {
			continue
		}
		out[windowIndex].Bars = append(out[windowIndex].Bars, bar)
	}
	return out
}

func totalBarsInSlices(weeks []weeklyBarSlice) int {
	total := 0
	for _, week := range weeks {
		total += len(week.Bars)
	}
	return total
}

func promotionRejectReasons(holdoutSummary, combinedSummary PeriodSummary) []string {
	var reasons []string
	if holdoutSummary.MaxDrawdownPct > 8.0 {
		reasons = append(reasons, "holdout-max-drawdown")
	}
	if holdoutSummary.WorstWeekPct < -3.0 {
		reasons = append(reasons, "holdout-bad-week")
	}
	if combinedSummary.ProfitFactor < 1.25 {
		reasons = append(reasons, "profit-factor")
	}
	if combinedSummary.Trades < 20 {
		reasons = append(reasons, "min-trades")
	}
	if combinedSummary.PositiveWeeksPct < 60.0 {
		reasons = append(reasons, "positive-weeks")
	}
	return reasons
}

func buildTradingProfile(candidate OptimizerCandidate, run OptimizationRun) config.TradingProfile {
	version := buildProfileVersion(candidate.Profile, run.CompletedWeekEnd)
	cfg := candidate.Config
	cfg.StrategyProfileName = string(candidate.Profile)
	cfg.StrategyProfileVersion = version
	promotion := config.PromotionDecision{
		DeploymentMode:            "paper",
		Status:                    "pending-paper-validation",
		LastPaperValidationResult: "pending-paper-validation",
		Notes:                     "Deploy to paper for one full trading week, then require operator approval before live promotion.",
	}
	if !candidate.Promotable {
		promotion.Status = "blocked-research-gates"
		promotion.LastPaperValidationResult = "research-gates-failed"
		promotion.Notes = "Research gates failed; do not promote without manual override and additional review."
	}
	return config.TradingProfile{
		Name:        candidate.Profile,
		Version:     version,
		GeneratedAt: run.GeneratedAt,
		AsOf:        run.AsOf,
		Config:      cfg,
		Promotion:   promotion,
	}
}

func writeArtifacts(artifactDir string, report *OptimizationReport, winnerProfile *config.TradingProfile) error {
	if err := writeReportArtifact(artifactDir, report); err != nil {
		return err
	}
	if err := writeProfileArtifact(artifactDir, report, winnerProfile); err != nil {
		return err
	}
	if winnerProfile != nil {
		return writeReportArtifact(artifactDir, report)
	}
	return nil
}

func writeReportArtifact(artifactDir string, report *OptimizationReport) error {
	artifactDir = artifactDirOrDefault(artifactDir)
	reportDir := filepath.Join(artifactDir, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return err
	}
	reportName := fmt.Sprintf("%s-%s.json", report.Run.CompletedWeekEnd.In(marketLocation).Format("20060102"), report.Run.SchemaVersion[:15])
	reportPath := filepath.Join(reportDir, reportName)
	report.ArtifactPath = reportPath
	if err := writeJSON(reportPath, report); err != nil {
		return err
	}
	return writeJSON(filepath.Join(artifactDir, "latest-report.json"), report)
}

func writeProfileArtifact(artifactDir string, report *OptimizationReport, winnerProfile *config.TradingProfile) error {
	artifactDir = artifactDirOrDefault(artifactDir)
	if winnerProfile != nil {
		profileDir := filepath.Join(artifactDir, "profiles")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			return err
		}
		profilePath := filepath.Join(profileDir, winnerProfile.Version+".json")
		winnerProfile.SourceReportPath = report.ArtifactPath
		report.ProfilePath = profilePath
		if err := writeJSON(profilePath, winnerProfile); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(artifactDir, "latest-candidate-profile.json"), winnerProfile); err != nil {
			return err
		}
	} else {
		_ = os.Remove(filepath.Join(artifactDir, "latest-candidate-profile.json"))
	}
	return nil
}

func normalizeCandidateConfig(cfg config.TradingConfig) config.TradingConfig {
	cfg.StrategyProfileName = string(config.StrategyProfile(cfg.StrategyProfileName))
	if strings.TrimSpace(cfg.StrategyProfileName) == "" {
		cfg.StrategyProfileName = string(config.StrategyProfileBaseline)
	}
	cfg.MaxTradesPerDay = maxInt(cfg.MaxTradesPerDay, cfg.MaxOpenPositions)
	cfg.MaxOpenPositions = clampInt(cfg.MaxOpenPositions, 1, 4)
	cfg.MaxShortOpenPositions = clampInt(cfg.MaxShortOpenPositions, 1, maxInt(1, cfg.MaxOpenPositions))
	cfg.MaxTradesPerDay = clampInt(cfg.MaxTradesPerDay, 4, 20)
	cfg.EntryCooldownSec = clampInt(cfg.EntryCooldownSec, 30, 120)
	cfg.BreakEvenHoldMinutes = clampInt(cfg.BreakEvenHoldMinutes, 2, 6)
	cfg.MinEntryScore = clampFloat(cfg.MinEntryScore, 10.0, 20.0)
	cfg.ShortMinEntryScore = clampFloat(cfg.ShortMinEntryScore, 14.0, 28.0)
	cfg.MinOneMinuteReturnPct = clampFloat(cfg.MinOneMinuteReturnPct, 0.05, 1.20)
	cfg.MinThreeMinuteReturnPct = clampFloat(cfg.MinThreeMinuteReturnPct, 0.20, 2.25)
	cfg.MinVolumeRate = clampFloat(cfg.MinVolumeRate, 0.80, 2.40)
	cfg.MaxPriceVsOpenPct = clampFloat(cfg.MaxPriceVsOpenPct, 15.0, 40.0)
	cfg.ScannerMinPriceVsOpenPctFloor = clampFloat(cfg.ScannerMinPriceVsOpenPctFloor, 1.0, 4.0)
	cfg.ScannerMinPriceVsOpenGapMultiplier = clampFloat(cfg.ScannerMinPriceVsOpenGapMultiplier, 0.10, 0.50)
	cfg.ScannerMinSetupVolumeRateOffset = clampFloat(cfg.ScannerMinSetupVolumeRateOffset, -0.30, 0.15)
	cfg.ScannerMinSetupRelativeVolumeExtra = clampFloat(cfg.ScannerMinSetupRelativeVolumeExtra, 0.0, 1.0)
	cfg.ScannerVWAPTolerancePct = clampFloat(cfg.ScannerVWAPTolerancePct, -0.50, 0.15)
	cfg.ScannerConsolidationATRMultiplier = clampFloat(cfg.ScannerConsolidationATRMultiplier, 1.0, 2.5)
	cfg.ScannerConsolidationMaxPct = clampFloat(cfg.ScannerConsolidationMaxPct, 3.0, 6.0)
	cfg.ScannerPullbackDepthMinATRMultiplier = clampFloat(cfg.ScannerPullbackDepthMinATRMultiplier, 0.10, 1.0)
	cfg.ScannerPullbackDepthMinPct = clampFloat(cfg.ScannerPullbackDepthMinPct, 0.10, 2.0)
	cfg.ScannerPullbackDepthMaxATRMultiplier = clampFloat(cfg.ScannerPullbackDepthMaxATRMultiplier, 1.0, 3.5)
	cfg.ScannerPullbackDepthMaxPct = clampFloat(cfg.ScannerPullbackDepthMaxPct, 4.0, 12.0)
	cfg.ScannerRenewedVolumeRateMin = clampFloat(cfg.ScannerRenewedVolumeRateMin, 0.85, 1.50)
	cfg.RiskPerTradePct = clampFloat(cfg.RiskPerTradePct, 0.0025, 0.0200)
	cfg.BreakEvenMinR = clampFloat(cfg.BreakEvenMinR, 0.25, 0.85)
	cfg.TrailActivationR = clampFloat(cfg.TrailActivationR, 0.45, 1.10)
	cfg.TrailATRMultiplier = clampFloat(cfg.TrailATRMultiplier, 0.90, 2.10)
	cfg.TightTrailTriggerR = clampFloat(cfg.TightTrailTriggerR, 0.85, 1.60)
	cfg.TightTrailATRMultiplier = clampFloat(cfg.TightTrailATRMultiplier, 0.40, 1.00)
	cfg.ProfitTargetR = clampFloat(cfg.ProfitTargetR, 0.90, 1.60)
	cfg.FailedBreakoutCutR = clampFloat(cfg.FailedBreakoutCutR, 0.03, 0.10)
	cfg.StructureConfirmR = clampFloat(cfg.StructureConfirmR, 0.00, 0.30)
	cfg.MaxShortExposurePct = clampFloat(cfg.MaxShortExposurePct, 0.10, cfg.MaxExposurePct)
	cfg.ShortPeakExtensionMinPct = clampFloat(cfg.ShortPeakExtensionMinPct, 8.0, 22.0)
	cfg.ShortVWAPBreakMinPct = clampFloat(cfg.ShortVWAPBreakMinPct, -2.50, -0.20)
	cfg.ShortStopATRMultiplier = clampFloat(cfg.ShortStopATRMultiplier, 0.75, 2.25)
	if cfg.TightTrailTriggerR < cfg.TrailActivationR+0.20 {
		cfg.TightTrailTriggerR = round2(cfg.TrailActivationR + 0.20)
	}
	cfg.MaxExposurePct = inferMaxExposurePct(cfg)
	return cfg
}

func buildProfileVersion(profile config.StrategyProfile, completedWeekEnd time.Time) string {
	return fmt.Sprintf("%s-%s", completedWeekEnd.In(marketLocation).Format("20060102"), profile)
}

func artifactDirOrDefault(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return DefaultArtifactDir
	}
	return strings.TrimSpace(dir)
}

func addSeed(target map[string]candidateSeed, seed candidateSeed) {
	target[seedSignature(seed)] = seed
}

func seedSignature(seed candidateSeed) string {
	cfg := seed.config
	return strings.Join([]string{
		string(seed.profile),
		fmt.Sprintf("%.2f", cfg.MinEntryScore),
		fmt.Sprintf("%.2f", cfg.MinOneMinuteReturnPct),
		fmt.Sprintf("%.2f", cfg.MinThreeMinuteReturnPct),
		fmt.Sprintf("%.2f", cfg.MinVolumeRate),
		fmt.Sprintf("%.2f", cfg.MaxPriceVsOpenPct),
		fmt.Sprintf("%d", cfg.EntryCooldownSec),
		fmt.Sprintf("%.4f", cfg.RiskPerTradePct),
		fmt.Sprintf("%d", cfg.MaxTradesPerDay),
		fmt.Sprintf("%d", cfg.MaxOpenPositions),
		fmt.Sprintf("%d", cfg.BreakEvenHoldMinutes),
		fmt.Sprintf("%.2f", cfg.BreakEvenMinR),
		fmt.Sprintf("%.2f", cfg.TrailActivationR),
		fmt.Sprintf("%.2f", cfg.TrailATRMultiplier),
		fmt.Sprintf("%.2f", cfg.TightTrailTriggerR),
		fmt.Sprintf("%.2f", cfg.TightTrailATRMultiplier),
		fmt.Sprintf("%.2f", cfg.ProfitTargetR),
		fmt.Sprintf("%.2f", cfg.FailedBreakoutCutR),
		fmt.Sprintf("%.2f", cfg.StructureConfirmR),
		fmt.Sprintf("%.2f", cfg.ShortMinEntryScore),
		fmt.Sprintf("%d", cfg.MaxShortOpenPositions),
		fmt.Sprintf("%.2f", cfg.MaxShortExposurePct),
		fmt.Sprintf("%.2f", cfg.ShortPeakExtensionMinPct),
		fmt.Sprintf("%.2f", cfg.ShortVWAPBreakMinPct),
		fmt.Sprintf("%.2f", cfg.ShortStopATRMultiplier),
	}, "|")
}

func percentReturn(netPnL, startingCapital float64) float64 {
	if startingCapital <= 0 {
		return 0
	}
	return (netPnL / startingCapital) * 100
}

func percentile(sortedValues []float64, q float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	if q <= 0 {
		return sortedValues[0]
	}
	if q >= 1 {
		return sortedValues[len(sortedValues)-1]
	}
	position := q * float64(len(sortedValues)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sortedValues[lower]
	}
	weight := position - float64(lower)
	return sortedValues[lower] + (sortedValues[upper]-sortedValues[lower])*weight
}

func inferMaxExposurePct(cfg config.TradingConfig) float64 {
	assumedStopDistance := math.Max(cfg.StopLossPct, 0.07)
	perPositionExposure := cfg.RiskPerTradePct / assumedStopDistance
	targetFullRiskPositions := 2
	if cfg.MaxOpenPositions > 0 && cfg.MaxOpenPositions < targetFullRiskPositions {
		targetFullRiskPositions = cfg.MaxOpenPositions
	}
	exposure := (perPositionExposure * float64(targetFullRiskPositions)) + 0.05
	if exposure < 0.25 {
		exposure = 0.25
	}
	if exposure > 1.0 {
		exposure = 1.0
	}
	return round2(exposure)
}

func uniqueFloatGrid(base, minValue, maxValue, smallStep, largeStep float64) []float64 {
	return dedupeFloatSlice([]float64{
		round2(clampFloat(base-largeStep, minValue, maxValue)),
		round2(clampFloat(base-smallStep, minValue, maxValue)),
		round2(clampFloat(base, minValue, maxValue)),
		round2(clampFloat(base+smallStep, minValue, maxValue)),
		round2(clampFloat(base+largeStep, minValue, maxValue)),
	})
}

func uniqueIntGrid(base, minValue, maxValue, smallStep, largeStep int) []int {
	return dedupeIntSlice([]int{
		clampInt(base-largeStep, minValue, maxValue),
		clampInt(base-smallStep, minValue, maxValue),
		clampInt(base, minValue, maxValue),
		clampInt(base+smallStep, minValue, maxValue),
		clampInt(base+largeStep, minValue, maxValue),
	})
}

func dedupeFloatSlice(values []float64) []float64 {
	seen := make(map[float64]struct{}, len(values))
	out := make([]float64, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Float64s(out)
	return out
}

func dedupeIntSlice(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func nearestFloatIndex(values []float64, target float64) int {
	bestIndex := 0
	bestDistance := math.MaxFloat64
	for index, value := range values {
		distance := math.Abs(value - target)
		if distance < bestDistance {
			bestDistance = distance
			bestIndex = index
		}
	}
	return bestIndex
}

func nearestIntIndex(values []int, target int) int {
	bestIndex := 0
	bestDistance := math.MaxInt
	for index, value := range values {
		distance := absInt(value - target)
		if distance < bestDistance {
			bestDistance = distance
			bestIndex = index
		}
	}
	return bestIndex
}

func writeJSON(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func round4(value float64) float64 {
	return math.Round(value*10_000) / 10_000
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
