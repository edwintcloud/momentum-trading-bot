package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
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
	ReturnPct       float64   `json:"returnPct"`
	ProfitFactor    float64   `json:"profitFactor"`
	MaxDrawdownPct  float64   `json:"maxDrawdownPct"`
	Trades          int       `json:"trades"`
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
	PositiveWeeks         int     `json:"positiveWeeks"`
	PositiveWeeksPct      float64 `json:"positiveWeeksPct"`
	MedianWeeklyReturnPct float64 `json:"medianWeeklyReturnPct"`
	P25WeeklyReturnPct    float64 `json:"p25WeeklyReturnPct"`
	ProfitFactor          float64 `json:"profitFactor"`
	MaxDrawdownPct        float64 `json:"maxDrawdownPct"`
	WorstWeekPct          float64 `json:"worstWeekPct"`
}

// OptimizerCandidate is a single evaluated profile/config combination.
type OptimizerCandidate struct {
	Rank              int                   `json:"rank"`
	CandidateID       string                `json:"candidateId"`
	Profile           config.StrategyProfile `json:"profile"`
	Config            config.TradingConfig  `json:"config"`
	SearchSummary     PeriodSummary         `json:"searchSummary"`
	ValidationSummary PeriodSummary         `json:"validationSummary"`
	HoldoutSummary    PeriodSummary         `json:"holdoutSummary"`
	ValidationWeeks   []WeeklyPerformance   `json:"validationWeeks"`
	HoldoutWeeks      []WeeklyPerformance   `json:"holdoutWeeks"`
	Score             CandidateScore        `json:"score"`
	RejectReasons     []string              `json:"rejectReasons,omitempty"`
	Promotable        bool                  `json:"promotable"`
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
	Run            OptimizationRun    `json:"run"`
	Candidates     []OptimizerCandidate `json:"candidates"`
	Winner         *OptimizerCandidate `json:"winner,omitempty"`
	ProfilePath    string             `json:"profilePath,omitempty"`
	GeneratedAt    time.Time          `json:"generatedAt"`
	ArtifactPath   string             `json:"artifactPath,omitempty"`
}

// ArtifactStatus powers the operator-visible pending optimizer state.
type ArtifactStatus struct {
	PendingProfileName        string
	PendingProfileVersion     string
	LastOptimizerRun          time.Time
	LastPaperValidationResult string
}

// Params controls a weekly optimization run.
type Params struct {
	BaseConfig  config.TradingConfig
	Bars        []backtest.InputBar
	AsOf        time.Time
	ArtifactDir string
}

type candidateSeed struct {
	id      string
	profile config.StrategyProfile
	config  config.TradingConfig
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
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.MinEntryScore, 10.0, 20.0, 1.50, 3.00) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.MinEntryScore },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.MinEntryScore = round2(v) },
	},
	{
		name: "MinOneMinuteReturnPct",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.MinOneMinuteReturnPct, 0.05, 1.20, 0.15, 0.30) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.MinOneMinuteReturnPct },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.MinOneMinuteReturnPct = round2(v) },
	},
	{
		name: "MinThreeMinuteReturnPct",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.MinThreeMinuteReturnPct, 0.20, 2.25, 0.20, 0.40) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.MinThreeMinuteReturnPct },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.MinThreeMinuteReturnPct = round2(v) },
	},
	{
		name: "MinVolumeRate",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.MinVolumeRate, 0.80, 2.40, 0.15, 0.30) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.MinVolumeRate },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.MinVolumeRate = round2(v) },
	},
	{
		name: "MaxPriceVsOpenPct",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.MaxPriceVsOpenPct, 15.0, 40.0, 2.0, 4.0) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.MaxPriceVsOpenPct },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.MaxPriceVsOpenPct = round2(v) },
	},
	{
		name: "RiskPerTradePct",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(clampFloat(base.RiskPerTradePct, 0.0025, 0.0200), 0.0025, 0.0200, 0.0015, 0.0030) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.RiskPerTradePct },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.RiskPerTradePct = round4(v) },
	},
	{
		name: "BreakEvenMinR",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.BreakEvenMinR, 0.25, 0.85, 0.05, 0.10) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.BreakEvenMinR },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.BreakEvenMinR = round2(v) },
	},
	{
		name: "TrailActivationR",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.TrailActivationR, 0.45, 1.10, 0.05, 0.10) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.TrailActivationR },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.TrailActivationR = round2(v) },
	},
	{
		name: "TrailATRMultiplier",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.TrailATRMultiplier, 0.90, 2.10, 0.10, 0.20) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.TrailATRMultiplier },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.TrailATRMultiplier = round2(v) },
	},
	{
		name: "TightTrailTriggerR",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.TightTrailTriggerR, 0.85, 1.60, 0.10, 0.20) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.TightTrailTriggerR },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.TightTrailTriggerR = round2(v) },
	},
	{
		name: "TightTrailATRMultiplier",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.TightTrailATRMultiplier, 0.40, 1.00, 0.05, 0.10) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.TightTrailATRMultiplier },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.TightTrailATRMultiplier = round2(v) },
	},
	{
		name: "ProfitTargetR",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.ProfitTargetR, 0.90, 1.60, 0.10, 0.20) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.ProfitTargetR },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.ProfitTargetR = round2(v) },
	},
	{
		name: "ProfitTrailActivationR",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.ProfitTrailActivationR, 1.10, 2.25, 0.10, 0.20) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.ProfitTrailActivationR },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.ProfitTrailActivationR = round2(v) },
	},
	{
		name: "ProfitTrailPct",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.ProfitTrailPct, 0.015, 0.050, 0.005, 0.010) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.ProfitTrailPct },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.ProfitTrailPct = round4(v) },
	},
	{
		name: "FailedBreakoutCutR",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.FailedBreakoutCutR, 0.03, 0.10, 0.01, 0.02) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.FailedBreakoutCutR },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.FailedBreakoutCutR = round2(v) },
	},
	{
		name: "StructureConfirmR",
		grid: func(base config.TradingConfig) []float64 { return uniqueFloatGrid(base.StructureConfirmR, 0.00, 0.30, 0.025, 0.05) },
		get:  func(cfg config.TradingConfig) float64 { return cfg.StructureConfirmR },
		set:  func(cfg *config.TradingConfig, v float64) { cfg.StructureConfirmR = round2(v) },
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
}

// Run executes the weekly walk-forward optimizer and writes JSON artifacts.
func Run(ctx context.Context, params Params) (OptimizationReport, *config.TradingProfile, error) {
	if len(params.Bars) == 0 {
		return OptimizationReport{}, nil, fmt.Errorf("optimizer requires historical bars")
	}
	base := config.NormalizeStrategyProfile(params.BaseConfig)
	completedWeekEnd := PriorCompletedWeekEnd(params.AsOf)
	allWeeks := BuildWeeklyWindows(completedWeekEnd, 20)
	if len(allWeeks) != 20 {
		return OptimizationReport{}, nil, fmt.Errorf("expected 20 completed weeks, got %d", len(allWeeks))
	}

	searchWeeks := append([]WeeklyWindow(nil), allWeeks[:12]...)
	validationWeeks := append([]WeeklyWindow(nil), allWeeks[12:16]...)
	holdoutWeeks := append([]WeeklyWindow(nil), allWeeks[16:]...)

	report := OptimizationReport{
		Run: OptimizationRun{
			SchemaVersion:    reportSchemaV1,
			GeneratedAt:      time.Now().UTC(),
			AsOf:             params.AsOf.UTC(),
			CompletedWeekEnd: completedWeekEnd,
			SearchWeeks:      searchWeeks,
			ValidationWeeks:  validationWeeks,
			HoldoutWeeks:     holdoutWeeks,
		},
		GeneratedAt: time.Now().UTC(),
	}

	coarseSeeds := buildCoarseSeeds(base)
	report.Run.CoarseCandidates = len(coarseSeeds)
	searchShortlist, err := evaluateShortlist(ctx, coarseSeeds, params.Bars, searchWeeks, 25, func(candidate *OptimizerCandidate, summary PeriodSummary) {
		candidate.SearchSummary = summary
	})
	if err != nil {
		return OptimizationReport{}, nil, err
	}

	validationAnchors, err := evaluateShortlist(ctx, searchShortlist, params.Bars, validationWeeks, 10, func(candidate *OptimizerCandidate, summary PeriodSummary) {
		candidate.ValidationSummary = summary
	})
	if err != nil {
		return OptimizationReport{}, nil, err
	}

	refinedSeeds := buildRefinedSeeds(base, validationAnchors)
	report.Run.RefinedCandidates = len(refinedSeeds)
	refinedCandidates, err := evaluateAll(ctx, refinedSeeds, params.Bars, validationWeeks, func(candidate *OptimizerCandidate, summary PeriodSummary, weeks []WeeklyPerformance) {
		candidate.ValidationSummary = summary
		candidate.ValidationWeeks = weeks
	})
	if err != nil {
		return OptimizationReport{}, nil, err
	}

	finalists := topCandidates(refinedCandidates, 10, func(candidate OptimizerCandidate) PeriodSummary {
		return candidate.ValidationSummary
	})
	report.Run.Finalists = len(finalists)
	if len(finalists) == 0 {
		if err := writeArtifacts(params.ArtifactDir, &report, nil); err != nil {
			return OptimizationReport{}, nil, err
		}
		return report, nil, nil
	}

	finalized := make([]OptimizerCandidate, 0, len(finalists))
	for _, finalist := range finalists {
		select {
		case <-ctx.Done():
			return OptimizationReport{}, nil, ctx.Err()
		default:
		}
		holdoutSummary, holdoutPerf, err := evaluateWeeks(ctx, finalist.Config, params.Bars, holdoutWeeks)
		if err != nil {
			return OptimizationReport{}, nil, err
		}
		finalist.HoldoutSummary = holdoutSummary
		finalist.HoldoutWeeks = holdoutPerf
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
		cfg.ProfitTrailActivationR = round2(maxFloat(cfg.ProfitTrailActivationR, cfg.ProfitTargetR+0.20))
		cfg.ProfitTrailPct = round4(minFloat(cfg.ProfitTrailPct, 0.025))
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
		cfg.ProfitTrailActivationR = round2(minFloat(maxFloat(cfg.ProfitTrailActivationR, cfg.ProfitTargetR+0.10), 1.35))
		cfg.ProfitTrailPct = round4(minFloat(cfg.ProfitTrailPct, 0.0225))
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

func evaluateShortlist(ctx context.Context, seeds []candidateSeed, bars []backtest.InputBar, windows []WeeklyWindow, limit int, assign func(*OptimizerCandidate, PeriodSummary)) ([]candidateSeed, error) {
	candidates, err := evaluateAll(ctx, seeds, bars, windows, func(candidate *OptimizerCandidate, summary PeriodSummary, _ []WeeklyPerformance) {
		assign(candidate, summary)
	})
	if err != nil {
		return nil, err
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
	return out, nil
}

func evaluateAll(ctx context.Context, seeds []candidateSeed, bars []backtest.InputBar, windows []WeeklyWindow, assign func(*OptimizerCandidate, PeriodSummary, []WeeklyPerformance)) ([]OptimizerCandidate, error) {
	results := make([]OptimizerCandidate, 0, len(seeds))
	for _, seed := range seeds {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		summary, perf, err := evaluateWeeks(ctx, seed.config, bars, windows)
		if err != nil {
			return nil, err
		}
		candidate := OptimizerCandidate{
			CandidateID: seed.id,
			Profile:     seed.profile,
			Config:      seed.config,
		}
		assign(&candidate, summary, perf)
		results = append(results, candidate)
	}
	return results, nil
}

func evaluateWeeks(ctx context.Context, cfg config.TradingConfig, bars []backtest.InputBar, windows []WeeklyWindow) (PeriodSummary, []WeeklyPerformance, error) {
	weeks := make([]WeeklyPerformance, 0, len(windows))
	for _, window := range windows {
		select {
		case <-ctx.Done():
			return PeriodSummary{}, nil, ctx.Err()
		default:
		}
		result, err := backtest.Run(ctx, cfg, backtest.RunConfig{
			Bars:  bars,
			Start: window.Start,
			End:   window.End,
		})
		if err != nil {
			return PeriodSummary{}, nil, fmt.Errorf("evaluate week %s: %w", window.Label, err)
		}
		week := WeeklyPerformance{
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
			week.ClosedTradePnLs = append(week.ClosedTradePnLs, trade.PnL)
		}
		weeks = append(weeks, week)
	}
	return summarizeWeeks(weeks), weeks, nil
}

func summarizeWeeks(weeks []WeeklyPerformance) PeriodSummary {
	if len(weeks) == 0 {
		return PeriodSummary{}
	}
	returns := make([]float64, 0, len(weeks))
	positiveWeeks := 0
	trades := 0
	maxDrawdown := 0.0
	worstWeek := math.MaxFloat64
	grossWins := 0.0
	grossLosses := 0.0
	for _, week := range weeks {
		returns = append(returns, week.ReturnPct)
		trades += week.Trades
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
		PositiveWeeks:         positiveWeeks,
		PositiveWeeksPct:      round2((float64(positiveWeeks) / float64(len(weeks))) * 100),
		MedianWeeklyReturnPct: round2(percentile(returns, 0.50)),
		P25WeeklyReturnPct:    round2(percentile(returns, 0.25)),
		ProfitFactor:          round2(profitFactor),
		MaxDrawdownPct:        round2(maxDrawdown),
		WorstWeekPct:          round2(worstWeek),
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
	artifactDir = artifactDirOrDefault(artifactDir)
	reportDir := filepath.Join(artifactDir, "reports")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return err
	}
	reportName := fmt.Sprintf("%s-%s.json", report.Run.CompletedWeekEnd.In(marketLocation).Format("20060102"), report.Run.SchemaVersion[:15])
	reportPath := filepath.Join(reportDir, reportName)
	report.ArtifactPath = reportPath
	if winnerProfile != nil {
		profileDir := filepath.Join(artifactDir, "profiles")
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			return err
		}
		profilePath := filepath.Join(profileDir, winnerProfile.Version+".json")
		winnerProfile.SourceReportPath = reportPath
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
	if err := writeJSON(reportPath, report); err != nil {
		return err
	}
	return writeJSON(filepath.Join(artifactDir, "latest-report.json"), report)
}

func normalizeCandidateConfig(cfg config.TradingConfig) config.TradingConfig {
	cfg.StrategyProfileName = string(config.StrategyProfile(cfg.StrategyProfileName))
	if strings.TrimSpace(cfg.StrategyProfileName) == "" {
		cfg.StrategyProfileName = string(config.StrategyProfileBaseline)
	}
	cfg.MaxTradesPerDay = maxInt(cfg.MaxTradesPerDay, cfg.MaxOpenPositions)
	cfg.MaxOpenPositions = clampInt(cfg.MaxOpenPositions, 1, 4)
	cfg.MaxTradesPerDay = clampInt(cfg.MaxTradesPerDay, 4, 20)
	cfg.EntryCooldownSec = clampInt(cfg.EntryCooldownSec, 30, 120)
	cfg.BreakEvenHoldMinutes = clampInt(cfg.BreakEvenHoldMinutes, 2, 6)
	cfg.MinEntryScore = clampFloat(cfg.MinEntryScore, 10.0, 20.0)
	cfg.MinOneMinuteReturnPct = clampFloat(cfg.MinOneMinuteReturnPct, 0.05, 1.20)
	cfg.MinThreeMinuteReturnPct = clampFloat(cfg.MinThreeMinuteReturnPct, 0.20, 2.25)
	cfg.MinVolumeRate = clampFloat(cfg.MinVolumeRate, 0.80, 2.40)
	cfg.MaxPriceVsOpenPct = clampFloat(cfg.MaxPriceVsOpenPct, 15.0, 40.0)
	cfg.RiskPerTradePct = clampFloat(cfg.RiskPerTradePct, 0.0025, 0.0200)
	cfg.BreakEvenMinR = clampFloat(cfg.BreakEvenMinR, 0.25, 0.85)
	cfg.TrailActivationR = clampFloat(cfg.TrailActivationR, 0.45, 1.10)
	cfg.TrailATRMultiplier = clampFloat(cfg.TrailATRMultiplier, 0.90, 2.10)
	cfg.TightTrailTriggerR = clampFloat(cfg.TightTrailTriggerR, 0.85, 1.60)
	cfg.TightTrailATRMultiplier = clampFloat(cfg.TightTrailATRMultiplier, 0.40, 1.00)
	cfg.ProfitTargetR = clampFloat(cfg.ProfitTargetR, 0.90, 1.60)
	cfg.ProfitTrailActivationR = clampFloat(cfg.ProfitTrailActivationR, maxFloat(1.10, cfg.ProfitTargetR), 2.25)
	cfg.ProfitTrailPct = clampFloat(cfg.ProfitTrailPct, 0.015, 0.050)
	cfg.FailedBreakoutCutR = clampFloat(cfg.FailedBreakoutCutR, 0.03, 0.10)
	cfg.StructureConfirmR = clampFloat(cfg.StructureConfirmR, 0.00, 0.30)
	if cfg.TightTrailTriggerR < cfg.TrailActivationR+0.20 {
		cfg.TightTrailTriggerR = round2(cfg.TrailActivationR + 0.20)
	}
	if cfg.ProfitTrailActivationR < cfg.ProfitTargetR {
		cfg.ProfitTrailActivationR = cfg.ProfitTargetR
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
		fmt.Sprintf("%.2f", cfg.ProfitTrailActivationR),
		fmt.Sprintf("%.4f", cfg.ProfitTrailPct),
		fmt.Sprintf("%.2f", cfg.FailedBreakoutCutR),
		fmt.Sprintf("%.2f", cfg.StructureConfirmR),
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
	if exposure > 10.0 {
		exposure = 10.0
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
