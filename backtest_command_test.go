package main

import (
	"testing"
	"time"
)

func TestInferBacktestWindowsDefaultsTrainingWindowBeforeStart(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	gotStart, gotEnd, trainStart, trainEnd, err := inferBacktestWindows(start, end, true)
	if err != nil {
		t.Fatalf("expected windows to infer, got %v", err)
	}
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("unexpected backtest window: %v %v", gotStart, gotEnd)
	}
	if !trainEnd.Equal(start.Add(-time.Minute)) {
		t.Fatalf("expected training to end one minute before backtest start, got %v", trainEnd)
	}
	if !trainStart.Equal(trainEnd.Add(-end.Sub(start))) {
		t.Fatalf("expected matching lookback duration, got %v", trainStart)
	}
}

func TestInferBacktestWindowsDefaultsEndToNow(t *testing.T) {
	start := time.Now().UTC().Add(-2 * time.Hour)
	_, end, _, _, err := inferBacktestWindows(start, time.Time{}, true)
	if err != nil {
		t.Fatalf("expected end time to default, got %v", err)
	}
	if end.Before(start) {
		t.Fatalf("expected inferred end to be after start, got %v", end)
	}
}
