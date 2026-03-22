package autooptimize

import (
	"context"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

func TestScheduler_RunOnce_NoRecommendation(t *testing.T) {
	sched := &Scheduler{
		ProfilePath:  "profiles/default.json",
		OptimizerDir: t.TempDir(),
		Schedule:     "weekly",
		Guardrails:   Guardrails{MinSharpeRatio: 0.5, MinWinRate: 0.30, MaxDrawdownPct: 0.20, MinTradeCount: 20},
		Notifier:     Notifier{}, // zero-value is a no-op (no token/chatID)
		RunOptimizer: func(ctx context.Context, asOf time.Time, outDir string) (optimizer.Report, error) {
			return optimizer.Report{}, nil // no recommendation
		},
	}

	err := sched.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
}

func TestScheduler_RunOnce_Promoted(t *testing.T) {
	tmpDir := t.TempDir()
	profilePath := tmpDir + "/default.json"

	// Write a minimal profile so the promoter can back it up
	if err := writeMinimalProfile(profilePath); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	sched := &Scheduler{
		ProfilePath:  profilePath,
		OptimizerDir: tmpDir,
		Schedule:     "weekly",
		Guardrails: Guardrails{
			MinSharpeRatio: 0.5,
			MinWinRate:     0.30,
			MaxDrawdownPct: 0.20,
			MinTradeCount:  20,
		},
		Notifier: Notifier{},
		RunOptimizer: func(ctx context.Context, asOf time.Time, outDir string) (optimizer.Report, error) {
			return optimizer.Report{
				DSR: 0.8,
				Recommendation: &optimizer.CandidateResult{
					ProfileName: "baseline_breakout",
					ValidationResult: backtest.Result{
						ProfitFactor:   1.5,
						WinRate:        0.45,
						MaxDrawdownPct: 0.10,
						Trades:         50,
					},
					Config: config.TradingConfig{
						StrategyProfileName:    "baseline_breakout",
						StrategyProfileVersion: "v2",
					},
				},
			}, nil
		},
	}

	err := sched.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
}

func TestScheduler_RunOnce_Rejected(t *testing.T) {
	tmpDir := t.TempDir()

	sched := &Scheduler{
		ProfilePath:  tmpDir + "/default.json",
		OptimizerDir: tmpDir,
		Schedule:     "weekly",
		Guardrails: Guardrails{
			MinSharpeRatio: 2.0, // very high — will reject
			MinWinRate:     0.30,
			MaxDrawdownPct: 0.20,
			MinTradeCount:  20,
		},
		Notifier: Notifier{},
		RunOptimizer: func(ctx context.Context, asOf time.Time, outDir string) (optimizer.Report, error) {
			return optimizer.Report{
				DSR: 0.8,
				Recommendation: &optimizer.CandidateResult{
					ProfileName: "baseline_breakout",
					ValidationResult: backtest.Result{
						ProfitFactor:   0.8, // below min sharpe of 2.0
						WinRate:        0.45,
						MaxDrawdownPct: 0.10,
						Trades:         50,
					},
					Config: config.TradingConfig{
						StrategyProfileName:    "baseline_breakout",
						StrategyProfileVersion: "v2",
					},
				},
			}, nil
		},
	}

	err := sched.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}

	// Verify status file was written with rejection
	status, loadErr := optimizer.LoadArtifactStatus(tmpDir)
	if loadErr != nil {
		t.Fatalf("LoadArtifactStatus: %v", loadErr)
	}
	if status.LastPaperValidationResult == "" {
		t.Error("expected non-empty paper validation result after rejection")
	}
}

func TestScheduler_RunOnce_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	sched := &Scheduler{
		ProfilePath:  "profiles/default.json",
		OptimizerDir: t.TempDir(),
		Schedule:     "weekly",
		Guardrails:   Guardrails{},
		Notifier:     Notifier{},
		RunOptimizer: func(ctx context.Context, asOf time.Time, outDir string) (optimizer.Report, error) {
			return optimizer.Report{}, ctx.Err()
		},
	}

	err := sched.RunOnce(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func writeMinimalProfile(path string) error {
	profile := config.TradingProfile{
		Name:    "baseline_breakout",
		Version: "v1",
		Config:  config.DefaultTradingConfig(),
	}
	return config.SaveTradingProfile(path, profile)
}
