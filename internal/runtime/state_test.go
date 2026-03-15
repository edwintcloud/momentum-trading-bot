package runtime

import (
	"testing"
	"time"
)

func TestIsReadyRequiresAllDependenciesHealthy(t *testing.T) {
	state := NewState()
	state.SetDependencyStatus("database", true, "ok")
	state.SetDependencyStatus("alpaca_trading", true, "ok")
	state.SetDependencyStatus("market_data_stream", false, "waiting")

	if state.IsReady() {
		t.Fatal("expected not ready while one dependency is unhealthy")
	}

	state.SetDependencyStatus("market_data_stream", true, "ok")
	if !state.IsReady() {
		t.Fatal("expected ready when all dependencies are healthy")
	}
}

func TestDailyLossStopBlocksEntriesForTheTradingDayOnly(t *testing.T) {
	state := NewState()
	start := time.Now().UTC()
	state.TriggerDailyLossStop(start)

	if reason := state.EntryBlockReasonAt(start.Add(30 * time.Minute)); reason != "daily-loss-limit-day" {
		t.Fatalf("expected daily loss stop to block same-day entries, got %q", reason)
	}

	nextDay := start.Add(24 * time.Hour)
	if reason := state.EntryBlockReasonAt(nextDay); reason != "" {
		t.Fatalf("expected daily loss stop to clear the next trading day, got %q", reason)
	}
}

func TestResumeDoesNotOverrideActiveDailyLossStop(t *testing.T) {
	state := NewState()
	at := time.Now().UTC()
	state.TriggerDailyLossStop(at)

	if resumed := state.Resume(); resumed {
		t.Fatal("expected resume to stay blocked while daily loss stop is active")
	}
}
