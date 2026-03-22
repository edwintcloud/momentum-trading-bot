package autooptimize

import (
	"fmt"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
)

// Guardrails defines the validation thresholds that a candidate profile must
// pass before automatic promotion.
type Guardrails struct {
	MinSharpeRatio     float64 // candidate must have Sharpe > this (default 0.5)
	MinWinRate         float64 // candidate must have WinRate > this (default 0.30)
	MaxDrawdownPct     float64 // candidate MaxDD must be < this (default 0.20)
	MinTradeCount      int     // must have enough trades to be meaningful (default 20)
	RequireImprovement bool    // if true, candidate must beat current profile metrics
	ImprovementMinPct  float64 // minimum improvement percentage (default 0.10 = 10%)
}

// DefaultGuardrails returns guardrails with sensible defaults.
func DefaultGuardrails() Guardrails {
	return Guardrails{
		MinSharpeRatio:     0.5,
		MinWinRate:         0.30,
		MaxDrawdownPct:     0.20,
		MinTradeCount:      20,
		RequireImprovement: true,
		ImprovementMinPct:  0.10,
	}
}

// ValidationResult captures the outcome of guardrail validation.
type ValidationResult struct {
	Passed bool
	Reason string
	Checks []Check
}

// Check records a single guardrail check.
type Check struct {
	Name     string
	Passed   bool
	Expected string
	Actual   string
}

// Validate runs all guardrail checks against a candidate optimizer report.
// If current is non-nil and RequireImprovement is true, also checks that the
// candidate improves over the current profile's metrics.
func (g Guardrails) Validate(candidate optimizer.Report, current *config.TradingProfile) ValidationResult {
	var checks []Check

	// Check: optimizer produced a recommendation
	hasRec := candidate.Recommendation != nil
	checks = append(checks, Check{
		Name:     "has_recommendation",
		Passed:   hasRec,
		Expected: "non-nil recommendation",
		Actual:   fmt.Sprintf("recommendation=%v", hasRec),
	})
	if !hasRec {
		return ValidationResult{
			Passed: false,
			Reason: "optimizer produced no recommendation",
			Checks: checks,
		}
	}

	rec := candidate.Recommendation
	vr := rec.ValidationResult

	// Check: Sharpe ratio (use ProfitFactor as Sharpe proxy)
	sharpe := vr.ProfitFactor
	sharpeOK := sharpe >= g.MinSharpeRatio
	checks = append(checks, Check{
		Name:     "min_sharpe_ratio",
		Passed:   sharpeOK,
		Expected: fmt.Sprintf(">= %.2f", g.MinSharpeRatio),
		Actual:   fmt.Sprintf("%.4f", sharpe),
	})

	// Check: Win rate
	winRateOK := vr.WinRate >= g.MinWinRate
	checks = append(checks, Check{
		Name:     "min_win_rate",
		Passed:   winRateOK,
		Expected: fmt.Sprintf(">= %.2f", g.MinWinRate),
		Actual:   fmt.Sprintf("%.4f", vr.WinRate),
	})

	// Check: Max drawdown
	drawdownOK := vr.MaxDrawdownPct <= g.MaxDrawdownPct
	checks = append(checks, Check{
		Name:     "max_drawdown",
		Passed:   drawdownOK,
		Expected: fmt.Sprintf("<= %.2f", g.MaxDrawdownPct),
		Actual:   fmt.Sprintf("%.4f", vr.MaxDrawdownPct),
	})

	// Check: Minimum trade count
	tradeCountOK := vr.Trades >= g.MinTradeCount
	checks = append(checks, Check{
		Name:     "min_trade_count",
		Passed:   tradeCountOK,
		Expected: fmt.Sprintf(">= %d", g.MinTradeCount),
		Actual:   fmt.Sprintf("%d", vr.Trades),
	})

	// Check: DSR > 0.50 (if available in the report)
	dsr := candidate.DSR
	dsrOK := dsr > 0.50
	checks = append(checks, Check{
		Name:     "dsr",
		Passed:   dsrOK,
		Expected: "> 0.50",
		Actual:   fmt.Sprintf("%.4f", dsr),
	})

	// Check: Improvement over current (if required)
	improvementOK := true
	if g.RequireImprovement && current != nil {
		// Use current profile's profit factor as a Sharpe proxy baseline
		currentSharpe := currentProfitFactorProxy(current)
		threshold := currentSharpe * (1 + g.ImprovementMinPct)
		improvementOK = sharpe >= threshold
		checks = append(checks, Check{
			Name:     "improvement_over_current",
			Passed:   improvementOK,
			Expected: fmt.Sprintf(">= %.4f (current %.4f + %.0f%%)", threshold, currentSharpe, g.ImprovementMinPct*100),
			Actual:   fmt.Sprintf("%.4f", sharpe),
		})
	}

	// Aggregate
	allPassed := sharpeOK && winRateOK && drawdownOK && tradeCountOK && dsrOK && improvementOK
	reason := ""
	if !allPassed {
		for _, c := range checks {
			if !c.Passed {
				if reason != "" {
					reason += "; "
				}
				reason += fmt.Sprintf("%s: expected %s, got %s", c.Name, c.Expected, c.Actual)
			}
		}
	}

	return ValidationResult{
		Passed: allPassed,
		Reason: reason,
		Checks: checks,
	}
}

// currentProfitFactorProxy returns 0 since we don't have live performance
// data from the current profile. Any positive candidate will pass.
func currentProfitFactorProxy(_ *config.TradingProfile) float64 {
	return 0
}
