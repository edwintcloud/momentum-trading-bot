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
func LoadArtifactStatus(dir string) (ArtifactStatus, error) {
	path := filepath.Join(dir, "latest-report.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return ArtifactStatus{}, err
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return ArtifactStatus{}, err
	}
	status := ArtifactStatus{
		LastOptimizerRun: report.GeneratedAt,
	}
	if report.Recommendation != nil {
		status.PendingProfileName = report.Recommendation.ProfileName
		status.PendingProfileVersion = report.Recommendation.Config.StrategyProfileVersion
	}
	return status, nil
}
