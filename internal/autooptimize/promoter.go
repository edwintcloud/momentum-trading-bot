package autooptimize

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

// Promoter writes a promoted trading profile to disk with atomic file
// operations and backup of the previous profile.
type Promoter struct {
	ProfilePath string
}

// Promote builds a TradingProfile from the optimizer report's recommendation
// and atomically writes it to the configured profile path.
func (p *Promoter) Promote(report optimizer.Report) error {
	if report.Recommendation == nil {
		return fmt.Errorf("promoter: no recommendation in report")
	}

	now := time.Now().In(markethours.Location())
	rec := report.Recommendation

	profile := config.TradingProfile{
		Name:        config.StrategyProfile(rec.ProfileName),
		Version:     rec.Config.StrategyProfileVersion,
		GeneratedAt: report.GeneratedAt,
		AsOf:        report.AsOf,
		Config:      rec.Config,
		Promotion: config.PromotionDecision{
			DeploymentMode: "paper",
			Status:         "auto-promoted",
			ApprovedBy:     "auto-optimizer",
			ApprovedAt:     now,
		},
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("promoter: marshal profile: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(p.ProfilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("promoter: create dir: %w", err)
	}

	// Backup existing profile
	if _, statErr := os.Stat(p.ProfilePath); statErr == nil {
		backupPath := fmt.Sprintf("%s.backup-%s", p.ProfilePath, now.Format("20060102"))
		if copyErr := copyFile(p.ProfilePath, backupPath); copyErr != nil {
			return fmt.Errorf("promoter: backup old profile: %w", copyErr)
		}
	}

	// Atomic write: write to .tmp then rename
	tmpPath := p.ProfilePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("promoter: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, p.ProfilePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("promoter: rename: %w", err)
	}

	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
