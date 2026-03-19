package optimizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
)

func TestPriorCompletedWeekEndUsesPreviousFridayBeforeClose(t *testing.T) {
	asOf := time.Date(2026, time.March, 16, 10, 0, 0, 0, marketLocation) // Monday
	got := PriorCompletedWeekEnd(asOf)
	want := time.Date(2026, time.March, 13, 20, 0, 0, 0, marketLocation).UTC()
	if !got.Equal(want) {
		t.Fatalf("expected prior completed Friday close %s, got %s", want, got)
	}
}

func TestPriorCompletedWeekEndUsesSameFridayAfterClose(t *testing.T) {
	asOf := time.Date(2026, time.March, 20, 20, 30, 0, 0, marketLocation)
	got := PriorCompletedWeekEnd(asOf)
	want := time.Date(2026, time.March, 20, 20, 0, 0, 0, marketLocation).UTC()
	if !got.Equal(want) {
		t.Fatalf("expected same Friday close %s, got %s", want, got)
	}
}

func TestBuildCoarseSeedsIsDeterministicAndBounded(t *testing.T) {
	base := config.TuneTradingConfig(config.DefaultTradingConfig(), 25_000, 1_000)
	first := buildCoarseSeeds(base)
	second := buildCoarseSeeds(base)
	if len(first) == 0 {
		t.Fatal("expected coarse seed set")
	}
	if len(first) != len(second) {
		t.Fatalf("expected deterministic seed count, got %d and %d", len(first), len(second))
	}
	for index := range first {
		if first[index].id != second[index].id {
			t.Fatalf("expected deterministic ordering at %d: %q vs %q", index, first[index].id, second[index].id)
		}
		cfg := first[index].config
		if cfg.MinEntryScore < 10 || cfg.MinEntryScore > 20 {
			t.Fatalf("seed %q min entry score out of bounds: %.2f", first[index].id, cfg.MinEntryScore)
		}
		if cfg.RiskPerTradePct < 0.0025 || cfg.RiskPerTradePct > 0.0200 {
			t.Fatalf("seed %q risk per trade out of bounds: %.4f", first[index].id, cfg.RiskPerTradePct)
		}
		if cfg.MaxOpenPositions < 1 || cfg.MaxOpenPositions > 4 {
			t.Fatalf("seed %q max open positions out of bounds: %d", first[index].id, cfg.MaxOpenPositions)
		}
		if !config.IsSupportedStrategyProfile(config.StrategyProfile(first[index].profile)) {
			t.Fatalf("seed %q uses unsupported profile %q", first[index].id, first[index].profile)
		}
	}
}

func TestSliceBarsByWeekPartitionsBarsOnce(t *testing.T) {
	weeks := []WeeklyWindow{
		{
			Label: "2026-03-13",
			Start: time.Date(2026, time.March, 9, 4, 0, 0, 0, marketLocation).UTC(),
			End:   time.Date(2026, time.March, 13, 19, 59, 59, 0, marketLocation).UTC(),
		},
		{
			Label: "2026-03-20",
			Start: time.Date(2026, time.March, 16, 4, 0, 0, 0, marketLocation).UTC(),
			End:   time.Date(2026, time.March, 20, 19, 59, 59, 0, marketLocation).UTC(),
		},
	}
	bars := []backtest.InputBar{
		{Timestamp: time.Date(2026, time.March, 10, 14, 0, 0, 0, time.UTC), Symbol: "AAA"},
		{Timestamp: time.Date(2026, time.March, 17, 14, 0, 0, 0, time.UTC), Symbol: "AAA"},
		{Timestamp: time.Date(2026, time.March, 17, 14, 1, 0, 0, time.UTC), Symbol: "BBB"},
	}

	slices := sliceBarsByWeek(bars, weeks)
	if len(slices) != 2 {
		t.Fatalf("expected two weekly slices, got %d", len(slices))
	}
	if len(slices[0].Bars) != 1 || len(slices[1].Bars) != 2 {
		t.Fatalf("unexpected weekly partition counts: %+v", slices)
	}
	if totalBarsInSlices(slices) != len(bars) {
		t.Fatalf("expected every bar to appear exactly once, got total=%d bars=%d", totalBarsInSlices(slices), len(bars))
	}
}

func TestPromotionRejectReasons(t *testing.T) {
	reasons := promotionRejectReasons(
		PeriodSummary{MaxDrawdownPct: 8.5, WorstWeekPct: -3.2},
		PeriodSummary{ProfitFactor: 1.10, Trades: 12, PositiveWeeksPct: 50},
	)
	if len(reasons) != 5 {
		t.Fatalf("expected all promotion gates to reject, got %v", reasons)
	}
}

func TestProgressTrackerEstimatesStageAndOverallETA(t *testing.T) {
	start := time.Date(2026, time.March, 18, 19, 0, 0, 0, time.UTC)
	tracker := newProgressTracker(start)
	tracker.RegisterStage("coarse-search", 100)
	tracker.RegisterStage("validation-shortlist", 40)

	tracker.Snapshot("coarse-search", 0, 100, "starting", start.Add(5*time.Second))
	progress := tracker.Snapshot("coarse-search", 10, 100, "working", start.Add(15*time.Second))

	if got, want := progress.StageRemainingSeconds, int64(90); got != want {
		t.Fatalf("expected stage remaining seconds %d, got %d", want, got)
	}
	if got, want := progress.OverallRemainingSeconds, int64(130); got != want {
		t.Fatalf("expected overall remaining seconds %d, got %d", want, got)
	}
	if progress.StageETA.IsZero() {
		t.Fatal("expected stage ETA to be populated")
	}
	if progress.OverallETA.IsZero() {
		t.Fatal("expected overall ETA to be populated")
	}
}

func TestEvaluateAllProgressIncludesPartialCandidateResults(t *testing.T) {
	asOf := time.Date(2026, time.June, 1, 12, 0, 0, 0, marketLocation)
	windows := BuildWeeklyWindows(PriorCompletedWeekEnd(asOf), 2)
	bars := optimizerFixtureBars(asOf)
	base := config.TuneTradingConfig(config.DefaultTradingConfig(), 25_000, 1_000)
	base.MinEntryScore = 10
	base.MinOneMinuteReturnPct = 0.10
	base.MinThreeMinuteReturnPct = 0.20
	base.MinVolumeRate = 1.05
	base.MaxPriceVsOpenPct = 40
	base.LimitOrderSlippageDollars = 0

	var sawPartial bool
	_, err := evaluateAll(context.Background(), []candidateSeed{{
		id:      "baseline",
		profile: config.StrategyProfileBaseline,
		config:  base,
	}}, windows, func(_ context.Context, window WeeklyWindow) ([]backtest.InputBar, error) {
		filtered := make([]backtest.InputBar, 0)
		for _, bar := range bars {
			if bar.Timestamp.Before(window.Start) || bar.Timestamp.After(window.End) {
				continue
			}
			filtered = append(filtered, bar)
		}
		return filtered, nil
	}, func(candidate *OptimizerCandidate, summary PeriodSummary, weeks []WeeklyPerformance) {
		candidate.SearchSummary = summary
		candidate.ValidationWeeks = weeks
	}, func(completed, total int, _ string, candidates []OptimizerCandidate) error {
		if completed > 0 && total == 2 && len(candidates) == 1 && candidates[0].SearchSummary.Weeks > 0 {
			sawPartial = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected evaluateAll to succeed, got %v", err)
	}
	if !sawPartial {
		t.Fatal("expected progress callback to receive partial candidate results")
	}
}

func TestRunEmitsArtifactsAndWinner(t *testing.T) {
	asOf := time.Date(2026, time.June, 1, 12, 0, 0, 0, marketLocation)
	bars := optimizerFixtureBars(asOf)
	dir := t.TempDir()
	baseConfig := config.TuneTradingConfig(config.DefaultTradingConfig(), 25_000, 1_000)
	baseConfig.MinEntryScore = 10
	baseConfig.MinOneMinuteReturnPct = 0.10
	baseConfig.MinThreeMinuteReturnPct = 0.20
	baseConfig.MinVolumeRate = 1.05
	baseConfig.MaxPriceVsOpenPct = 40
	baseConfig.LimitOrderSlippageDollars = 0
	sanity, err := backtest.Run(context.Background(), baseConfig, backtest.RunConfig{
		Bars:  bars,
		Start: BuildWeeklyWindows(PriorCompletedWeekEnd(asOf), 20)[0].Start,
		End:   BuildWeeklyWindows(PriorCompletedWeekEnd(asOf), 20)[0].End,
	})
	if err != nil {
		t.Fatalf("expected weekly sanity backtest to succeed, got %v", err)
	}
	if sanity.Diagnostics.EntryCandidates == 0 {
		t.Fatalf("expected fixture bars to produce entry candidates, got %+v", sanity.Diagnostics)
	}
	if sanity.Diagnostics.EntrySignals == 0 || sanity.Diagnostics.EntryRiskApproved == 0 {
		t.Fatalf("expected fixture bars to produce entry signals, got %+v", sanity.Diagnostics)
	}

	report, profile, err := Run(context.Background(), Params{
		BaseConfig:  baseConfig,
		Bars:        bars,
		AsOf:        asOf,
		ArtifactDir: dir,
	})
	if err != nil {
		t.Fatalf("expected optimizer run to succeed, got %v", err)
	}
	if report.Run.Finalists == 0 {
		t.Fatal("expected finalists in optimizer report")
	}
	if report.Winner == nil || profile == nil {
		t.Fatalf("expected promotable winner and trading profile, got winner=%+v candidates=%+v", report.Winner, report.Candidates)
	}
	if profile.Promotion.DeploymentMode != "paper" {
		t.Fatalf("expected winner to default to paper deployment, got %q", profile.Promotion.DeploymentMode)
	}
	if _, err := os.Stat(filepath.Join(dir, "latest-report.json")); err != nil {
		t.Fatalf("expected latest report artifact, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "latest-candidate-profile.json")); err != nil {
		t.Fatalf("expected latest candidate profile artifact, got %v", err)
	}
}

func TestRunSupportsSingleWindowMode(t *testing.T) {
	asOf := time.Date(2026, time.June, 1, 12, 0, 0, 0, marketLocation)
	bars := optimizerFixtureBars(asOf)
	dir := t.TempDir()
	window := BuildWeeklyWindows(PriorCompletedWeekEnd(asOf), 1)[0]
	baseConfig := config.TuneTradingConfig(config.DefaultTradingConfig(), 25_000, 1_000)
	baseConfig.MinEntryScore = 10
	baseConfig.MinOneMinuteReturnPct = 0.10
	baseConfig.MinThreeMinuteReturnPct = 0.20
	baseConfig.MinVolumeRate = 1.05
	baseConfig.MaxPriceVsOpenPct = 40
	baseConfig.LimitOrderSlippageDollars = 0

	report, profile, err := Run(context.Background(), Params{
		BaseConfig:      baseConfig,
		Bars:            bars,
		AsOf:            window.End,
		ArtifactDir:     dir,
		SearchWeeks:     []WeeklyWindow{window},
		ValidationWeeks: []WeeklyWindow{window},
	})
	if err != nil {
		t.Fatalf("expected single-window optimizer run to succeed, got %v", err)
	}
	if len(report.Run.SearchWeeks) != 1 || len(report.Run.ValidationWeeks) != 1 || len(report.Run.HoldoutWeeks) != 0 {
		t.Fatalf("unexpected single-window partitions: %+v", report.Run)
	}
	if report.Winner == nil || profile == nil {
		t.Fatalf("expected single-window winner and profile, got winner=%+v", report.Winner)
	}
	if report.Winner.Promotable {
		t.Fatalf("expected single-window winner to remain non-promotable without holdout, got %+v", report.Winner)
	}
	if profile.Promotion.Status != "blocked-research-gates" {
		t.Fatalf("expected single-window profile to stay blocked for promotion, got %+v", profile.Promotion)
	}
}

func optimizerFixtureBars(asOf time.Time) []backtest.InputBar {
	weeks := BuildWeeklyWindows(PriorCompletedWeekEnd(asOf), 20)
	bars := make([]backtest.InputBar, 0, len(weeks)*5*9)
	prevClose := 10.0
	for _, week := range weeks {
		weekStart := week.Start.In(marketLocation)
		for offset := 0; offset < 5; offset++ {
			day := weekStart.AddDate(0, 0, offset)
			bars = append(bars, dailyFixtureBars(day, prevClose)...)
			prevClose += 0.02
		}
	}
	return bars
}

func dailyFixtureBars(day time.Time, prevClose float64) []backtest.InputBar {
	entries := []struct {
		hour   int
		minute int
		open   float64
		high   float64
		low    float64
		close  float64
		volume int64
	}{
		{4, 0, prevClose * 1.10, prevClose * 1.12, prevClose * 1.09, prevClose * 1.115, 220_000},
		{4, 1, prevClose * 1.115, prevClose * 1.145, prevClose * 1.11, prevClose * 1.14, 300_000},
		{4, 2, prevClose * 1.14, prevClose * 1.17, prevClose * 1.135, prevClose * 1.165, 420_000},
		{9, 30, prevClose * 1.165, prevClose * 1.19, prevClose * 1.16, prevClose * 1.185, 520_000},
		{9, 31, prevClose * 1.185, prevClose * 1.23, prevClose * 1.18, prevClose * 1.225, 780_000},
		{9, 32, prevClose * 1.225, prevClose * 1.235, prevClose * 1.19, prevClose * 1.205, 720_000},
		{9, 33, prevClose * 1.205, prevClose * 1.275, prevClose * 1.20, prevClose * 1.265, 860_000},
		{9, 34, prevClose * 1.265, prevClose * 1.315, prevClose * 1.255, prevClose * 1.305, 640_000},
		{9, 35, prevClose * 1.305, prevClose * 1.33, prevClose * 1.275, prevClose * 1.285, 360_000},
		{9, 36, prevClose * 1.285, prevClose * 1.29, prevClose * 1.24, prevClose * 1.25, 260_000},
	}

	out := make([]backtest.InputBar, 0, len(entries))
	for _, entry := range entries {
		timestamp := time.Date(day.Year(), day.Month(), day.Day(), entry.hour, entry.minute, 0, 0, marketLocation).UTC()
		out = append(out, backtest.InputBar{
			Timestamp: timestamp,
			Symbol:    "APVO",
			Open:      round2(entry.open),
			High:      round2(entry.high),
			Low:       round2(entry.low),
			Close:     round2(entry.close),
			Volume:    entry.volume,
			PrevClose: round2(prevClose),
		})
	}
	return out
}
