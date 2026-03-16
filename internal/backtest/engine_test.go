package backtest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/strategy"
)

func TestRunExecutesHistoricalReplay(t *testing.T) {
	data := strings.Join([]string{
		"timestamp,symbol,open,high,low,close,volume,prev_close",
		"2026-03-09T09:30:00Z,APVO,10.00,10.10,9.95,10.05,50000,9.80",
		"2026-03-09T09:31:00Z,APVO,10.05,10.10,10.00,10.02,50000,9.80",
		"2026-03-09T09:32:00Z,APVO,10.02,10.08,10.00,10.04,50000,9.80",
		"2026-03-09T09:33:00Z,APVO,10.04,10.09,10.01,10.03,50000,9.80",
		"2026-03-09T09:34:00Z,APVO,10.03,10.07,10.00,10.01,50000,9.80",
		"2026-03-10T08:00:00Z,APVO,11.10,11.30,11.05,11.24,200000,10.01",
		"2026-03-10T08:01:00Z,APVO,11.24,11.48,11.22,11.46,250000,10.01",
		"2026-03-10T08:02:00Z,APVO,11.46,11.72,11.44,11.70,400000,10.01",
		"2026-03-10T09:30:00Z,APVO,11.70,11.86,11.66,11.84,500000,10.01",
		"2026-03-10T09:31:00Z,APVO,11.84,12.12,11.80,12.10,650000,10.01",
		"2026-03-10T09:32:00Z,APVO,12.10,12.35,12.06,12.32,700000,10.01",
		"2026-03-10T09:33:00Z,APVO,12.32,12.42,12.20,12.38,180000,10.01",
		"2026-03-10T09:34:00Z,APVO,12.30,12.32,11.40,11.55,300000,10.01",
	}, "\n")

	path := filepath.Join(t.TempDir(), "bars.csv")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		DataPath: path,
	})
	if err != nil {
		t.Fatalf("expected backtest to complete, got %v", err)
	}
	if result.Diagnostics.BarsLoaded == 0 || result.Diagnostics.BarsInWindow == 0 {
		t.Fatalf("expected replay to process bars, got %+v", result.Diagnostics)
	}
	if result.Diagnostics.EntryCandidates == 0 {
		t.Fatalf("expected scanner to emit at least one candidate, got %+v", result.Diagnostics)
	}
}

func TestRunExecutesHistoricalReplayFromInputBars(t *testing.T) {
	result, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		Bars: []InputBar{
			{Timestamp: time.Date(2026, 3, 9, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 10.00, High: 10.10, Low: 9.95, Close: 10.05, Volume: 50_000, PrevClose: 9.80},
			{Timestamp: time.Date(2026, 3, 9, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 10.05, High: 10.10, Low: 10.00, Close: 10.02, Volume: 50_000, PrevClose: 9.80},
			{Timestamp: time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC), Symbol: "APVO", Open: 11.10, High: 11.30, Low: 11.05, Close: 11.24, Volume: 200_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 8, 1, 0, 0, time.UTC), Symbol: "APVO", Open: 11.24, High: 11.48, Low: 11.22, Close: 11.46, Volume: 250_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 8, 2, 0, 0, time.UTC), Symbol: "APVO", Open: 11.46, High: 11.72, Low: 11.44, Close: 11.70, Volume: 400_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 11.70, High: 11.86, Low: 11.66, Close: 11.84, Volume: 500_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 11.84, High: 12.12, Low: 11.80, Close: 12.10, Volume: 650_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 32, 0, 0, time.UTC), Symbol: "APVO", Open: 12.10, High: 12.35, Low: 12.06, Close: 12.32, Volume: 700_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 34, 0, 0, time.UTC), Symbol: "APVO", Open: 12.30, High: 12.32, Low: 11.40, Close: 11.55, Volume: 300_000, PrevClose: 10.01},
		},
	})
	if err != nil {
		t.Fatalf("expected in-memory bar replay to complete, got %v", err)
	}
	if result.Diagnostics.BarsLoaded == 0 || result.Diagnostics.BarsInWindow == 0 {
		t.Fatalf("expected replay to process bars, got %+v", result.Diagnostics)
	}
	if result.Diagnostics.EntryCandidates == 0 {
		t.Fatalf("expected scanner to emit at least one candidate, got %+v", result.Diagnostics)
	}
}

func TestRunFallsBackWhenTrainingSamplesAreUnavailable(t *testing.T) {
	result, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		Bars: []InputBar{
			{Timestamp: time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC), Symbol: "TEST", Open: 5, High: 5.01, Low: 4.99, Close: 5, Volume: 1000},
			{Timestamp: time.Date(2026, 3, 10, 14, 1, 0, 0, time.UTC), Symbol: "TEST", Open: 5, High: 5.01, Low: 4.99, Close: 5, Volume: 1000},
		},
		Start:      time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 3, 10, 14, 1, 0, 0, time.UTC),
		TrainStart: time.Date(2026, 3, 9, 14, 0, 0, 0, time.UTC),
		TrainEnd:   time.Date(2026, 3, 9, 14, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expected fallback to seeded model, got %v", err)
	}
	if result.ModelTrainingWarning == "" {
		t.Fatalf("expected training warning when samples are unavailable, got %+v", result)
	}
}

func TestRunMarksOpenPositionsToMarketAtBacktestEnd(t *testing.T) {
	result, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		Bars: []InputBar{
			{Timestamp: time.Date(2026, 3, 9, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 10.00, High: 10.10, Low: 9.95, Close: 10.05, Volume: 50_000, PrevClose: 9.80},
			{Timestamp: time.Date(2026, 3, 9, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 10.05, High: 10.10, Low: 10.00, Close: 10.02, Volume: 50_000, PrevClose: 9.80},
			{Timestamp: time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC), Symbol: "APVO", Open: 11.10, High: 11.30, Low: 11.05, Close: 11.24, Volume: 200_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 8, 1, 0, 0, time.UTC), Symbol: "APVO", Open: 11.24, High: 11.48, Low: 11.22, Close: 11.46, Volume: 250_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 8, 2, 0, 0, time.UTC), Symbol: "APVO", Open: 11.46, High: 11.72, Low: 11.44, Close: 11.70, Volume: 400_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 11.70, High: 11.86, Low: 11.66, Close: 11.84, Volume: 500_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 11.84, High: 12.12, Low: 11.80, Close: 12.10, Volume: 650_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 32, 0, 0, time.UTC), Symbol: "APVO", Open: 12.10, High: 12.35, Low: 12.06, Close: 12.32, Volume: 700_000, PrevClose: 10.01},
		},
		End: time.Date(2026, 3, 10, 9, 32, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("expected mark-to-market backtest to complete, got %v", err)
	}
	if result.NetPnL != round2(result.RealizedPnL+result.UnrealizedPnL) {
		t.Fatalf("expected net pnl to reconcile realized + unrealized, got %+v", result)
	}
}

func TestTrainingTargetPenalizesFailedBreakouts(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	start := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)
	candidate := domain.Candidate{
		Symbol:                "TEST",
		Price:                 10.00,
		Open:                  9.70,
		HighOfDay:             10.05,
		GapPercent:            12.0,
		RelativeVolume:        8.0,
		Volume:                500_000,
		PriceVsOpenPct:        3.09,
		DistanceFromHighPct:   0.50,
		OneMinuteReturnPct:    1.2,
		ThreeMinuteReturnPct:  2.4,
		VolumeRate:            1.8,
		VolumeLeaderPct:       0.65,
		MinutesSinceOpen:      30,
		ATR:                   0.40,
		ATRPct:                4.0,
		PriceVsVWAPPct:        0.8,
		BreakoutPct:           0.4,
		ConsolidationRangePct: 2.2,
		PullbackDepthPct:      1.6,
		CloseOffHighPct:       18,
		SetupHigh:             9.96,
		SetupLow:              9.70,
		SetupType:             "consolidation-breakout",
		Score:                 26,
		Timestamp:             start,
	}
	plan, ok, reason := strategy.BuildEntryPlan(candidate)
	if !ok {
		t.Fatalf("expected valid entry plan, got %s", reason)
	}

	goodRecords := []record{
		{bar: bar{Timestamp: start, Symbol: "TEST", Open: 9.95, High: 10.05, Low: 9.92, Close: 10.00}, candidate: candidate, hasCandidate: true},
		{bar: bar{Timestamp: start.Add(time.Minute), Symbol: "TEST", Open: 10.01, High: 10.70, Low: 9.98, Close: 10.55}, tick: domain.Tick{Symbol: "TEST", Price: 10.55, BarOpen: 10.01, BarHigh: 10.70, BarLow: 9.98, Timestamp: start.Add(time.Minute)}},
		{bar: bar{Timestamp: start.Add(2 * time.Minute), Symbol: "TEST", Open: 10.56, High: 11.10, Low: 10.40, Close: 10.95}, tick: domain.Tick{Symbol: "TEST", Price: 10.95, BarOpen: 10.56, BarHigh: 11.10, BarLow: 10.40, Timestamp: start.Add(2 * time.Minute)}},
	}
	badRecords := []record{
		{bar: bar{Timestamp: start, Symbol: "TEST", Open: 9.95, High: 10.05, Low: 9.92, Close: 10.00}, candidate: candidate, hasCandidate: true},
		{bar: bar{Timestamp: start.Add(time.Minute), Symbol: "TEST", Open: 10.01, High: 10.12, Low: 9.55, Close: 9.62}, tick: domain.Tick{Symbol: "TEST", Price: 9.62, BarOpen: 10.01, BarHigh: 10.12, BarLow: 9.55, Timestamp: start.Add(time.Minute)}},
		{bar: bar{Timestamp: start.Add(2 * time.Minute), Symbol: "TEST", Open: 9.60, High: 9.68, Low: 9.35, Close: 9.40}, tick: domain.Tick{Symbol: "TEST", Price: 9.40, BarOpen: 9.60, BarHigh: 9.68, BarLow: 9.35, Timestamp: start.Add(2 * time.Minute)}},
	}

	good, ok := trainingTarget(cfg, goodRecords[0], []int{0, 1, 2}, 0, goodRecords, RunConfig{
		LabelLookaheadBars: 2,
		TrainStart:         start,
		TrainEnd:           start.Add(2 * time.Minute),
	}, plan)
	if !ok {
		t.Fatal("expected good continuation training sample to produce a target")
	}
	bad, ok := trainingTarget(cfg, badRecords[0], []int{0, 1, 2}, 0, badRecords, RunConfig{
		LabelLookaheadBars: 2,
		TrainStart:         start,
		TrainEnd:           start.Add(2 * time.Minute),
	}, plan)
	if !ok {
		t.Fatal("expected failed breakout training sample to produce a target")
	}

	if good <= bad {
		t.Fatalf("expected continuation target %.2fR to exceed failed breakout target %.2fR", good, bad)
	}
	if bad >= 0 {
		t.Fatalf("expected failed breakout target to be negative, got %.2fR", bad)
	}
}

func TestTrainingCorpusCacheRoundTrip(t *testing.T) {
	originalRoot := trainingCorpusCacheRoot
	trainingCorpusCacheRoot = filepath.Join(t.TempDir(), "training-corpus")
	defer func() {
		trainingCorpusCacheRoot = originalRoot
	}()

	expected := trainingCorpus{
		candidateTimestamps: []time.Time{
			time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC),
		},
		rows: []trainingRow{
			{
				candidateAt: time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC),
				availableAt: time.Date(2026, 3, 10, 14, 5, 0, 0, time.UTC),
				sample: strategy.TrainingSample{
					Candidate: domain.Candidate{
						Symbol:    "TEST",
						Price:     5.25,
						SetupType: "consolidation-breakout",
					},
					ForwardReturnPct: 1.25,
				},
			},
		},
	}

	if err := saveTrainingCorpusCache("cache-key", expected); err != nil {
		t.Fatalf("expected training corpus cache save to succeed, got %v", err)
	}

	loaded, ok, err := loadTrainingCorpusCache("cache-key")
	if err != nil {
		t.Fatalf("expected training corpus cache load to succeed, got %v", err)
	}
	if !ok {
		t.Fatal("expected training corpus cache hit")
	}
	if !reflect.DeepEqual(expected, loaded) {
		t.Fatalf("expected cached corpus %+v, got %+v", expected, loaded)
	}
}

func TestTrainingCorpusCacheKeyIncludesTradingConfig(t *testing.T) {
	cfgA := config.DefaultTradingConfig()
	cfgB := config.DefaultTradingConfig()
	cfgB.MinRelativeVolume = cfgA.MinRelativeVolume + 1

	runCfg := RunConfig{
		Start:              time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC),
		End:                time.Date(2026, 3, 12, 20, 0, 0, 0, time.UTC),
		TrainStart:         time.Date(2026, 3, 9, 14, 0, 0, 0, time.UTC),
		TrainEnd:           time.Date(2026, 3, 9, 20, 0, 0, 0, time.UTC),
		LabelLookaheadBars: 24,
	}
	records := []record{
		{bar: bar{Timestamp: time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC), Symbol: "TEST"}},
		{bar: bar{Timestamp: time.Date(2026, 3, 12, 20, 0, 0, 0, time.UTC), Symbol: "TEST"}},
	}
	symbolIndices := map[string][]int{"TEST": []int{0, 1}}

	keyA := trainingCorpusCacheKey(cfgA, runCfg, records, symbolIndices)
	keyB := trainingCorpusCacheKey(cfgB, runCfg, records, symbolIndices)
	if keyA == keyB {
		t.Fatal("expected training corpus cache key to change when trading config changes")
	}
}
