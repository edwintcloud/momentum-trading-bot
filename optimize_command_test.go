package main

import "testing"

func TestCustomOptimizeWindowParsingSetsOuterEnd(t *testing.T) {
	start, _, err := parseCLIBacktestTime("2026-01-15")
	if err != nil {
		t.Fatalf("parse start: %v", err)
	}

	end := start
	var endDateOnly bool
	end, endDateOnly, err = parseCLIBacktestTime("2026-01-22")
	if err != nil {
		t.Fatalf("parse end: %v", err)
	}
	start, end, err = inferBacktestWindows(start, end, endDateOnly, true)
	if err != nil {
		t.Fatalf("infer window: %v", err)
	}
	if end.IsZero() {
		t.Fatal("expected custom optimize end window to be populated")
	}
	if !end.After(start) {
		t.Fatalf("expected end after start, got start=%s end=%s", start, end)
	}
}
