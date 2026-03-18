package main

import (
	"strings"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
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

func TestBacktestSummaryLinesAreHumanReadable(t *testing.T) {
	start := time.Date(2026, 3, 13, 13, 30, 0, 0, time.UTC)
	end := time.Date(2026, 3, 13, 20, 0, 0, 0, time.UTC)
	lines := backtestSummaryLines(start, end, backtest.Result{
		Trades:              12,
		Wins:                7,
		Losses:              5,
		WinRate:             58.33,
		ProfitFactor:        1.84,
		AvgWinPnL:           132.45,
		AvgLossPnL:          -74.12,
		AvgWinR:             1.42,
		AvgLossR:            -0.71,
		AvgMFER:             1.88,
		AvgMAER:             0.62,
		TrailingStopExitPct: 41.67,
		AvgTimeToStopMin:    18.5,
		RealizedPnL:         554.21,
		UnrealizedPnL:       -12.34,
		NetPnL:              541.87,
		EndingEquity:        100541.87,
		OpenPositionsAtEnd:  1,
		MaxDrawdownPct:      3.42,
	})

	if len(lines) != 6 {
		t.Fatalf("expected 6 summary lines, got %d", len(lines))
	}
	joined := strings.Join(lines, "\n")
	for _, fragment := range []string{
		"Backtest Summary",
		"PnL          net=+541.87 realized=+554.21 unrealized=-12.34 ending_equity=+100541.87 max_drawdown=3.42%",
		"Trades       total=12 wins=7 losses=5 win_rate=58.33% profit_factor=1.84 open_positions=1",
		"Avg PnL/R    avg_win=+132.45 avg_loss=-74.12 avg_win_r=1.42 avg_loss_r=-0.71",
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("expected summary to contain %q, got:\n%s", fragment, joined)
		}
	}
}
