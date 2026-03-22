package optimizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

func TestLoadArtifactStatus_NoFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadArtifactStatus(dir)
	if !os.IsNotExist(err) {
		t.Fatalf("expected os.ErrNotExist, got: %v", err)
	}
}

func TestLoadArtifactStatus_ReportOnly(t *testing.T) {
	dir := t.TempDir()
	report := Report{
		GeneratedAt: time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC),
		Recommendation: &CandidateResult{
			ProfileName: "baseline_breakout",
			Config: config.TradingConfig{
				StrategyProfileVersion: "v42",
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "latest-report.json"), report)

	status, err := LoadArtifactStatus(dir)
	if err != nil {
		t.Fatalf("LoadArtifactStatus: %v", err)
	}
	if status.PendingProfileName != "baseline_breakout" {
		t.Errorf("PendingProfileName = %q, want %q", status.PendingProfileName, "baseline_breakout")
	}
	if status.PendingProfileVersion != "v42" {
		t.Errorf("PendingProfileVersion = %q, want %q", status.PendingProfileVersion, "v42")
	}
	if !status.LastOptimizerRun.Equal(report.GeneratedAt) {
		t.Errorf("LastOptimizerRun = %v, want %v", status.LastOptimizerRun, report.GeneratedAt)
	}
}

func TestLoadArtifactStatus_StatusOnly(t *testing.T) {
	dir := t.TempDir()
	statusFile := map[string]any{
		"lastRun":  time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC),
		"promoted": true,
		"reason":   "",
	}
	writeJSON(t, filepath.Join(dir, "latest-status.json"), statusFile)

	status, err := LoadArtifactStatus(dir)
	if err != nil {
		t.Fatalf("LoadArtifactStatus: %v", err)
	}
	if status.LastPaperValidationResult != "promoted" {
		t.Errorf("LastPaperValidationResult = %q, want %q", status.LastPaperValidationResult, "promoted")
	}
	if status.LastOptimizerRun.IsZero() {
		t.Error("expected non-zero LastOptimizerRun")
	}
}

func TestLoadArtifactStatus_MergesPrefersMoreRecent(t *testing.T) {
	dir := t.TempDir()

	// Report with older timestamp
	report := Report{
		GeneratedAt: time.Date(2026, 3, 19, 6, 0, 0, 0, time.UTC),
		Recommendation: &CandidateResult{
			ProfileName: "baseline_breakout",
			Config: config.TradingConfig{
				StrategyProfileVersion: "v1",
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "latest-report.json"), report)

	// Status with newer timestamp
	statusFile := map[string]any{
		"lastRun":  time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC),
		"promoted": false,
		"reason":   "sharpe too low",
	}
	writeJSON(t, filepath.Join(dir, "latest-status.json"), statusFile)

	status, err := LoadArtifactStatus(dir)
	if err != nil {
		t.Fatalf("LoadArtifactStatus: %v", err)
	}

	// Should use status file's more recent time
	expected := time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC)
	if !status.LastOptimizerRun.Equal(expected) {
		t.Errorf("LastOptimizerRun = %v, want %v", status.LastOptimizerRun, expected)
	}

	// Should still have profile info from report
	if status.PendingProfileName != "baseline_breakout" {
		t.Errorf("PendingProfileName = %q, want %q", status.PendingProfileName, "baseline_breakout")
	}

	// Should have rejection reason from status file
	if status.LastPaperValidationResult != "rejected: sharpe too low" {
		t.Errorf("LastPaperValidationResult = %q, want %q", status.LastPaperValidationResult, "rejected: sharpe too low")
	}
}

func TestLoadArtifactStatus_ReportMoreRecentThanStatus(t *testing.T) {
	dir := t.TempDir()

	// Report with newer timestamp
	report := Report{
		GeneratedAt: time.Date(2026, 3, 22, 6, 0, 0, 0, time.UTC),
		Recommendation: &CandidateResult{
			ProfileName: "continuation_breakout",
			Config: config.TradingConfig{
				StrategyProfileVersion: "v5",
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "latest-report.json"), report)

	// Status with older timestamp
	statusFile := map[string]any{
		"lastRun":  time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC),
		"promoted": true,
		"reason":   "",
	}
	writeJSON(t, filepath.Join(dir, "latest-status.json"), statusFile)

	status, err := LoadArtifactStatus(dir)
	if err != nil {
		t.Fatalf("LoadArtifactStatus: %v", err)
	}

	// Report is more recent, so LastOptimizerRun should keep report's time
	if !status.LastOptimizerRun.Equal(report.GeneratedAt) {
		t.Errorf("LastOptimizerRun = %v, want %v", status.LastOptimizerRun, report.GeneratedAt)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
