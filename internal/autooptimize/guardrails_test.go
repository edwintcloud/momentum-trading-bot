package autooptimize

import (
	"testing"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

func makeReport(sharpe, winRate, maxDD float64, trades int, dsr float64) optimizer.Report {
	return optimizer.Report{
		DSR: dsr,
		Recommendation: &optimizer.CandidateResult{
			ProfileName: "baseline_breakout",
			ValidationResult: backtest.Result{
				ProfitFactor:   sharpe,
				WinRate:        winRate,
				MaxDrawdownPct: maxDD,
				Trades:         trades,
			},
			Config: config.TradingConfig{
				StrategyProfileName:    "baseline_breakout",
				StrategyProfileVersion: "test-v1",
			},
		},
	}
}

func TestGuardrails_AllPass(t *testing.T) {
	g := DefaultGuardrails()
	g.RequireImprovement = false // skip improvement check for simplicity

	report := makeReport(1.5, 0.55, 0.10, 30, 0.75)
	result := g.Validate(report, nil)

	if !result.Passed {
		t.Fatalf("expected all checks to pass, got reason: %s", result.Reason)
	}
	if len(result.Checks) == 0 {
		t.Fatal("expected checks to be populated")
	}
}

func TestGuardrails_NoRecommendation(t *testing.T) {
	g := DefaultGuardrails()
	report := optimizer.Report{}
	result := g.Validate(report, nil)

	if result.Passed {
		t.Fatal("expected failure for no recommendation")
	}
	if result.Reason != "optimizer produced no recommendation" {
		t.Fatalf("unexpected reason: %s", result.Reason)
	}
}

func TestGuardrails_FailSharpe(t *testing.T) {
	g := DefaultGuardrails()
	g.RequireImprovement = false

	report := makeReport(0.3, 0.55, 0.10, 30, 0.75) // sharpe=0.3 < 0.5
	result := g.Validate(report, nil)

	if result.Passed {
		t.Fatal("expected failure for low Sharpe")
	}
	found := false
	for _, c := range result.Checks {
		if c.Name == "min_sharpe_ratio" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected min_sharpe_ratio check to fail")
	}
}

func TestGuardrails_FailWinRate(t *testing.T) {
	g := DefaultGuardrails()
	g.RequireImprovement = false

	report := makeReport(1.5, 0.20, 0.10, 30, 0.75) // winRate=0.20 < 0.30
	result := g.Validate(report, nil)

	if result.Passed {
		t.Fatal("expected failure for low win rate")
	}
	found := false
	for _, c := range result.Checks {
		if c.Name == "min_win_rate" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected min_win_rate check to fail")
	}
}

func TestGuardrails_FailDrawdown(t *testing.T) {
	g := DefaultGuardrails()
	g.RequireImprovement = false

	report := makeReport(1.5, 0.55, 0.30, 30, 0.75) // maxDD=0.30 > 0.20
	result := g.Validate(report, nil)

	if result.Passed {
		t.Fatal("expected failure for high drawdown")
	}
	found := false
	for _, c := range result.Checks {
		if c.Name == "max_drawdown" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected max_drawdown check to fail")
	}
}

func TestGuardrails_FailTradeCount(t *testing.T) {
	g := DefaultGuardrails()
	g.RequireImprovement = false

	report := makeReport(1.5, 0.55, 0.10, 5, 0.75) // trades=5 < 20
	result := g.Validate(report, nil)

	if result.Passed {
		t.Fatal("expected failure for low trade count")
	}
	found := false
	for _, c := range result.Checks {
		if c.Name == "min_trade_count" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected min_trade_count check to fail")
	}
}

func TestGuardrails_FailDSR(t *testing.T) {
	g := DefaultGuardrails()
	g.RequireImprovement = false

	report := makeReport(1.5, 0.55, 0.10, 30, 0.30) // dsr=0.30 < 0.50
	result := g.Validate(report, nil)

	if result.Passed {
		t.Fatal("expected failure for low DSR")
	}
	found := false
	for _, c := range result.Checks {
		if c.Name == "dsr" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected dsr check to fail")
	}
}

func TestGuardrails_ImprovementSkippedWhenNoCurrentProfile(t *testing.T) {
	g := DefaultGuardrails()
	g.RequireImprovement = true

	report := makeReport(1.5, 0.55, 0.10, 30, 0.75)
	result := g.Validate(report, nil) // nil current profile

	if !result.Passed {
		t.Fatalf("expected pass when no current profile: %s", result.Reason)
	}
}
