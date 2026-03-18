package main

import (
	"testing"
	"time"
)

func TestInferBacktestWindowsReturnsRequestedWindow(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)

	gotStart, gotEnd, err := inferBacktestWindows(start, end, false, true)
	if err != nil {
		t.Fatalf("expected windows to infer, got %v", err)
	}
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("unexpected backtest window: %v %v", gotStart, gotEnd)
	}
}

func TestInferBacktestWindowsDefaultsEndToNow(t *testing.T) {
	start := time.Now().UTC().Add(-2 * time.Hour)
	_, end, err := inferBacktestWindows(start, time.Time{}, false, true)
	if err != nil {
		t.Fatalf("expected end time to default, got %v", err)
	}
	if end.Before(start) {
		t.Fatalf("expected inferred end to be after start, got %v", end)
	}
}

func TestInferBacktestWindowsTreatsDateOnlyStartAsMarketDayStart(t *testing.T) {
	start, dateOnly, err := parseCLIBacktestTime("2026-03-13")
	if err != nil {
		t.Fatalf("expected date-only parse to succeed, got %v", err)
	}
	if !dateOnly {
		t.Fatal("expected date-only input to be marked as date-only")
	}
	end := time.Date(2026, 3, 13, 18, 0, 0, 0, time.UTC)

	gotStart, gotEnd, err := inferBacktestWindows(start, end, false, true)
	if err != nil {
		t.Fatalf("expected date-only start normalization, got %v", err)
	}
	expectedStart := time.Date(2026, 3, 13, 4, 0, 0, 0, time.UTC)
	if !gotStart.Equal(expectedStart) {
		t.Fatalf("expected market-day start %v, got %v", expectedStart, gotStart)
	}
	if !gotEnd.Equal(end) {
		t.Fatalf("expected explicit end to remain unchanged, got %v", gotEnd)
	}
}

func TestParseCLIBacktestTimeMarksDateOnlyInput(t *testing.T) {
	parsed, dateOnly, err := parseCLIBacktestTime("2026-03-13")
	if err != nil {
		t.Fatalf("expected parse success, got %v", err)
	}
	if !dateOnly {
		t.Fatal("expected date-only input to be marked as date-only")
	}
	expected := time.Date(2026, 3, 13, 4, 0, 0, 0, time.UTC)
	if !parsed.Equal(expected) {
		t.Fatalf("expected date-only parse to preserve market-day midnight, got %v", parsed)
	}
}
