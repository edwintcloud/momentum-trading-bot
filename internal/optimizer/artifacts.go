package optimizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// DefaultArtifactDir is the default output directory for optimizer artifacts.
const DefaultArtifactDir = ".cache/optimizer"

// ArtifactStatus summarizes the latest optimizer run metadata.
type ArtifactStatus struct {
	PendingProfileName          string    `json:"pendingProfileName"`
	PendingProfileVersion       string    `json:"pendingProfileVersion"`
	LastOptimizerRun            time.Time `json:"lastOptimizerRun"`
	LastPaperValidationResult   string    `json:"lastPaperValidationResult"`
}

// LoadArtifactStatus reads the latest optimizer run metadata from the given directory.
// It checks both latest-report.json and latest-status.json, using whichever has a
// more recent timestamp. The status file also provides paper validation results.
func LoadArtifactStatus(dir string) (ArtifactStatus, error) {
	var status ArtifactStatus
	var found bool

	// Try latest-report.json first
	reportPath := filepath.Join(dir, "latest-report.json")
	if raw, err := os.ReadFile(reportPath); err == nil {
		var report Report
		if err := json.Unmarshal(raw, &report); err == nil {
			status.LastOptimizerRun = report.GeneratedAt
			if report.Recommendation != nil {
				status.PendingProfileName = report.Recommendation.ProfileName
				status.PendingProfileVersion = report.Recommendation.Config.StrategyProfileVersion
			}
			found = true
		}
	}

	// Check latest-status.json — use its lastRun if more recent
	statusPath := filepath.Join(dir, "latest-status.json")
	if raw, err := os.ReadFile(statusPath); err == nil {
		var statusFile struct {
			LastRun  time.Time `json:"lastRun"`
			Promoted bool      `json:"promoted"`
			Reason   string    `json:"reason"`
		}
		if err := json.Unmarshal(raw, &statusFile); err == nil {
			if statusFile.LastRun.After(status.LastOptimizerRun) {
				status.LastOptimizerRun = statusFile.LastRun
			}
			if statusFile.Promoted {
				status.LastPaperValidationResult = "promoted"
			} else if statusFile.Reason != "" {
				status.LastPaperValidationResult = "rejected: " + statusFile.Reason
			}
			found = true
		}
	}

	if !found {
		return ArtifactStatus{}, os.ErrNotExist
	}
	return status, nil
}
