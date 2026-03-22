package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOptimizerStatus_EmptyDir(t *testing.T) {
	s := &Server{optimizerDir: t.TempDir()}

	status := s.optimizerStatus()
	if status.PendingProfileName != "" {
		t.Errorf("expected empty PendingProfileName, got %q", status.PendingProfileName)
	}
	if !status.LastOptimizerRun.IsZero() {
		t.Errorf("expected zero LastOptimizerRun, got %v", status.LastOptimizerRun)
	}
}

func TestOptimizerStatus_CachesFor60Seconds(t *testing.T) {
	dir := t.TempDir()
	s := &Server{optimizerDir: dir}

	// Write a status file
	statusData := map[string]any{
		"lastRun":  time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC),
		"promoted": true,
	}
	data, _ := json.MarshalIndent(statusData, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "latest-status.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// First call loads from disk
	status1 := s.optimizerStatus()
	if status1.LastOptimizerRun.IsZero() {
		t.Fatal("expected non-zero LastOptimizerRun after first call")
	}

	// Update the file with a new timestamp
	statusData["lastRun"] = time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC)
	data, _ = json.MarshalIndent(statusData, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "latest-status.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Second call should return cached result (same as first)
	status2 := s.optimizerStatus()
	if !status2.LastOptimizerRun.Equal(status1.LastOptimizerRun) {
		t.Errorf("expected cached result: got %v, want %v", status2.LastOptimizerRun, status1.LastOptimizerRun)
	}

	// Simulate cache expiry by backdating cachedArtifactAt
	s.cachedArtifactAt = time.Now().Add(-61 * time.Second)

	// Third call should re-read from disk
	status3 := s.optimizerStatus()
	expected := time.Date(2026, 3, 21, 6, 0, 0, 0, time.UTC)
	if !status3.LastOptimizerRun.Equal(expected) {
		t.Errorf("expected refreshed result: got %v, want %v", status3.LastOptimizerRun, expected)
	}
}

func TestOptimizerStatus_ReadsArtifactFields(t *testing.T) {
	dir := t.TempDir()
	s := &Server{optimizerDir: dir}

	// Write a report JSON in the format LoadArtifactStatus expects
	reportData := map[string]any{
		"generatedAt": time.Date(2026, 3, 20, 6, 0, 0, 0, time.UTC),
		"recommendation": map[string]any{
			"profileName": "high_conviction_breakout",
			"config": map[string]any{
				"strategyProfileVersion": "v3",
			},
		},
	}

	data, _ := json.MarshalIndent(reportData, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "latest-report.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	status := s.optimizerStatus()
	if status.PendingProfileName != "high_conviction_breakout" {
		t.Errorf("PendingProfileName = %q, want %q", status.PendingProfileName, "high_conviction_breakout")
	}
	if status.PendingProfileVersion != "v3" {
		t.Errorf("PendingProfileVersion = %q, want %q", status.PendingProfileVersion, "v3")
	}
}
