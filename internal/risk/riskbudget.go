package risk

import (
	"math"
	"sync"
)

// RiskBudgetManager implements dynamic risk budgeting based on realized volatility.
//
// Volatility-targeted sizing:
//
//	position_size_i = (risk_budget × vol_scalar) / (stock_vol_i × price_i)
//	vol_scalar = target_vol / realized_vol
//
// Intraday dynamic budgeting:
//
//	bar_risk_limit = daily_risk_budget / remaining_bars_ratio
//	max_position = bar_risk_limit / (intraday_vol × price)
type RiskBudgetManager struct {
	mu                 sync.RWMutex
	targetVolAnnual    float64 // target portfolio volatility (annualized)
	dailyRiskBudgetPct float64 // daily risk budget as fraction of account

	// Intraday return tracking for realized vol
	intradayReturns []float64
	maxReturns      int
}

// NewRiskBudgetManager creates a dynamic risk budget manager.
func NewRiskBudgetManager(targetVolAnnual, dailyRiskBudgetPct float64) *RiskBudgetManager {
	if targetVolAnnual <= 0 {
		targetVolAnnual = 0.10
	}
	if dailyRiskBudgetPct <= 0 {
		dailyRiskBudgetPct = 0.01
	}
	return &RiskBudgetManager{
		targetVolAnnual:    targetVolAnnual,
		dailyRiskBudgetPct: dailyRiskBudgetPct,
		intradayReturns:    make([]float64, 0, 390),
		maxReturns:         390,
	}
}

// AddReturn records a 1-minute portfolio return for intraday vol computation.
func (rb *RiskBudgetManager) AddReturn(ret float64) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.intradayReturns = append(rb.intradayReturns, ret)
	if len(rb.intradayReturns) > rb.maxReturns {
		rb.intradayReturns = rb.intradayReturns[1:]
	}
}

// ResetDay clears intraday returns for a new trading day.
func (rb *RiskBudgetManager) ResetDay() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.intradayReturns = rb.intradayReturns[:0]
}

// VolScalar computes the volatility scalar: target_vol / realized_vol.
// realizedVol is the annualized realized volatility of the portfolio.
// Returns a multiplier to scale position sizes. Clamped to [0.25, 2.0].
func VolScalar(targetVolAnnual, realizedVolAnnual float64) float64 {
	if realizedVolAnnual <= 0 || targetVolAnnual <= 0 {
		return 1.0
	}
	scalar := targetVolAnnual / realizedVolAnnual
	if scalar < 0.25 {
		scalar = 0.25
	}
	if scalar > 2.0 {
		scalar = 2.0
	}
	return scalar
}

// VolTargetedPositionSize computes a position size using volatility targeting.
//
//	riskBudget: dollar risk budget for the position
//	volScalar: target_vol / realized_vol
//	stockVol: annualized volatility of the individual stock
//	price: current stock price
func VolTargetedPositionSize(riskBudget, volScalar, stockVol, price float64) int64 {
	if stockVol <= 0 || price <= 0 || riskBudget <= 0 {
		return 0
	}
	size := (riskBudget * volScalar) / (stockVol * price)
	qty := int64(math.Floor(size))
	if qty < 0 {
		return 0
	}
	return qty
}

// IntradayRealizedVol computes intraday realized volatility from the last
// windowBars minute returns, annualized to daily scale.
// Annualization: std(1min_returns) × sqrt(390)
func (rb *RiskBudgetManager) IntradayRealizedVol(windowBars int) float64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if windowBars <= 0 {
		windowBars = 30
	}
	n := len(rb.intradayReturns)
	if n < 5 {
		return 0
	}
	start := n - windowBars
	if start < 0 {
		start = 0
	}
	window := rb.intradayReturns[start:]
	if len(window) < 5 {
		return 0
	}
	_, sigma := meanStdDev(window)
	// Annualize from minute to daily: sqrt(390 minutes per day)
	return sigma * math.Sqrt(390)
}

// BarRiskLimit computes the per-bar risk limit for intraday budgeting.
//
//	accountSize: current account equity
//	remainingBars: minutes left in the trading session
//	totalBars: total minutes in the session (390)
func (rb *RiskBudgetManager) BarRiskLimit(accountSize float64, remainingBars, totalBars int) float64 {
	if accountSize <= 0 || remainingBars <= 0 || totalBars <= 0 {
		return 0
	}
	dailyBudget := accountSize * rb.dailyRiskBudgetPct
	remainingRatio := float64(remainingBars) / float64(totalBars)
	if remainingRatio <= 0 {
		return 0
	}
	return dailyBudget * remainingRatio
}

// MaxPositionFromBudget computes the maximum position size from the intraday
// risk budget at the current bar.
//
//	accountSize: current account equity
//	remainingBars: minutes left in session
//	totalBars: total minutes in session (390)
//	intradayVol: intraday realized volatility (daily scale)
//	price: current stock price
func (rb *RiskBudgetManager) MaxPositionFromBudget(accountSize float64, remainingBars, totalBars int, intradayVol, price float64) int64 {
	if intradayVol <= 0 || price <= 0 {
		return 0
	}
	barLimit := rb.BarRiskLimit(accountSize, remainingBars, totalBars)
	if barLimit <= 0 {
		return 0
	}
	maxPos := barLimit / (intradayVol * price)
	qty := int64(math.Floor(maxPos))
	if qty < 0 {
		return 0
	}
	return qty
}
