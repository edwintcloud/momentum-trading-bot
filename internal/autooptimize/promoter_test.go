package autooptimize

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

func TestPromoter_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profiles", "test.json")

	p := &Promoter{ProfilePath: profilePath}
	report := optimizer.Report{
		GeneratedAt: time.Now(),
		AsOf:        time.Now(),
		Recommendation: &optimizer.CandidateResult{
			ProfileName: "baseline_breakout",
			ValidationResult: backtest.Result{
				ProfitFactor: 1.5,
				WinRate:      0.55,
				Trades:       30,
			},
			Config: config.TradingConfig{
				StrategyProfileName:    "baseline_breakout",
				StrategyProfileVersion: "test-v1",
			},
		},
	}

	if err := p.Promote(report); err != nil {
		t.Fatalf("promote failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		t.Fatal("promoted profile file not created")
	}

	// Verify we can load it as a trading profile
	profile, err := config.LoadTradingProfile(profilePath)
	if err != nil {
		t.Fatalf("load promoted profile: %v", err)
	}
	if string(profile.Name) != "baseline_breakout" {
		t.Fatalf("expected profile name baseline_breakout, got %s", profile.Name)
	}
	if profile.Promotion.Status != "auto-promoted" {
		t.Fatalf("expected status auto-promoted, got %s", profile.Promotion.Status)
	}
	if profile.Promotion.ApprovedBy != "auto-optimizer" {
		t.Fatalf("expected approvedBy auto-optimizer, got %s", profile.Promotion.ApprovedBy)
	}
}

func TestPromoter_CreatesBackup(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "test.json")

	// Write an initial file
	if err := os.WriteFile(profilePath, []byte(`{"name":"baseline_breakout","version":"old"}`), 0644); err != nil {
		t.Fatal(err)
	}

	p := &Promoter{ProfilePath: profilePath}
	report := optimizer.Report{
		GeneratedAt: time.Now(),
		AsOf:        time.Now(),
		Recommendation: &optimizer.CandidateResult{
			ProfileName: "baseline_breakout",
			ValidationResult: backtest.Result{
				ProfitFactor: 1.5,
			},
			Config: config.TradingConfig{
				StrategyProfileName:    "baseline_breakout",
				StrategyProfileVersion: "new-v1",
			},
		},
	}

	if err := p.Promote(report); err != nil {
		t.Fatalf("promote failed: %v", err)
	}

	// Check backup exists
	pattern := profilePath + ".backup-*"
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("expected backup file to be created")
	}
}

func TestPromoter_NoRecommendation(t *testing.T) {
	p := &Promoter{ProfilePath: "/tmp/test.json"}
	report := optimizer.Report{}

	err := p.Promote(report)
	if err == nil {
		t.Fatal("expected error for no recommendation")
	}
}
