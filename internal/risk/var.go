package risk

import (
	"math"
	"sort"
	"sync"
)

// VaRCalculator computes Value-at-Risk and Conditional VaR (Expected Shortfall)
// using either parametric or historical simulation methods.
type VaRCalculator struct {
	mu               sync.RWMutex
	portfolioReturns []float64 // minute-level portfolio returns
	maxReturns       int       // max return observations to keep
	confidenceLevel  float64   // e.g. 0.95
	method           string    // "parametric" or "historical"
}

// NewVaRCalculator creates a VaR calculator.
func NewVaRCalculator(confidenceLevel float64, method string, maxReturns int) *VaRCalculator {
	if confidenceLevel <= 0 || confidenceLevel >= 1 {
		confidenceLevel = 0.95
	}
	if method != "historical" {
		method = "parametric"
	}
	if maxReturns <= 0 {
		maxReturns = 390 // one full trading day of minute bars
	}
	return &VaRCalculator{
		portfolioReturns: make([]float64, 0, maxReturns),
		maxReturns:       maxReturns,
		confidenceLevel:  confidenceLevel,
		method:           method,
	}
}

// AddReturn records a portfolio return observation (e.g., 1-minute return).
func (vc *VaRCalculator) AddReturn(ret float64) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.portfolioReturns = append(vc.portfolioReturns, ret)
	if len(vc.portfolioReturns) > vc.maxReturns {
		vc.portfolioReturns = vc.portfolioReturns[1:]
	}
}

// Returns returns a copy of the stored returns.
func (vc *VaRCalculator) Returns() []float64 {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	out := make([]float64, len(vc.portfolioReturns))
	copy(out, vc.portfolioReturns)
	return out
}

// VaR computes the Value-at-Risk at the configured confidence level.
// Returns a positive number representing the loss threshold.
// For example, VaR = 0.02 means there is a (1-α) probability that the
// loss will not exceed 2% over the horizon.
func (vc *VaRCalculator) VaR() float64 {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	if len(vc.portfolioReturns) < 5 {
		return 0
	}
	if vc.method == "historical" {
		return historicalVaR(vc.portfolioReturns, vc.confidenceLevel)
	}
	return parametricVaR(vc.portfolioReturns, vc.confidenceLevel)
}

// CVaR computes the Conditional VaR (Expected Shortfall) — the expected
// loss given that the loss exceeds VaR.
func (vc *VaRCalculator) CVaR() float64 {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	if len(vc.portfolioReturns) < 5 {
		return 0
	}
	if vc.method == "historical" {
		return historicalCVaR(vc.portfolioReturns, vc.confidenceLevel)
	}
	return parametricCVaR(vc.portfolioReturns, vc.confidenceLevel)
}

// IntraDayVaR computes 1-hour VaR by scaling minute-level VaR.
// Uses last windowBars returns (default 60 for 1 hour of minute bars).
func (vc *VaRCalculator) IntraDayVaR(windowBars int) float64 {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	if windowBars <= 0 {
		windowBars = 60
	}
	n := len(vc.portfolioReturns)
	if n < 5 {
		return 0
	}
	start := n - windowBars
	if start < 0 {
		start = 0
	}
	window := vc.portfolioReturns[start:]
	if len(window) < 5 {
		return 0
	}
	var minuteVaR float64
	if vc.method == "historical" {
		minuteVaR = historicalVaR(window, vc.confidenceLevel)
	} else {
		minuteVaR = parametricVaR(window, vc.confidenceLevel)
	}
	// Scale minute VaR to the window horizon: VaR_h = VaR_1min * sqrt(h)
	return minuteVaR * math.Sqrt(float64(len(window)))
}

// ExceedsDailyLimit checks whether the intraday VaR exceeds the daily budget.
func (vc *VaRCalculator) ExceedsDailyLimit(accountSize, dailyLimitPct float64) bool {
	if accountSize <= 0 || dailyLimitPct <= 0 {
		return false
	}
	dailyVaRLimit := accountSize * dailyLimitPct
	intradayVaR := vc.IntraDayVaR(60)
	return intradayVaR > 0 && (intradayVaR*accountSize) > dailyVaRLimit
}

// CVaRPositionSize computes position size based on CVaR-based risk budgeting.
// riskBudget is the dollar amount of risk to allocate.
// cvarPerUnit is the CVaR per unit of the asset (e.g., CVaR * price).
func CVaRPositionSize(riskBudget, cvarPerUnit float64) int64 {
	if cvarPerUnit <= 0 || riskBudget <= 0 {
		return 0
	}
	size := riskBudget / cvarPerUnit
	return int64(math.Floor(size))
}

// parametricVaR computes VaR assuming normally distributed returns.
// VaR = μ - z_α × σ (returned as positive loss)
func parametricVaR(returns []float64, confidence float64) float64 {
	mu, sigma := meanStdDev(returns)
	if sigma <= 0 {
		return 0
	}
	z := inverseNormalCDF(confidence)
	var_ := -(mu - z*sigma) // negative because VaR is a loss
	if var_ < 0 {
		return 0
	}
	return var_
}

// parametricCVaR computes CVaR for normal distribution.
// CVaR = -μ + σ × φ(z_α) / (1 - α)
func parametricCVaR(returns []float64, confidence float64) float64 {
	mu, sigma := meanStdDev(returns)
	if sigma <= 0 {
		return 0
	}
	z := inverseNormalCDF(confidence)
	phi := normalPDF(z)
	cvar := -mu + sigma*phi/(1-confidence)
	if cvar < 0 {
		return 0
	}
	return cvar
}

// historicalVaR computes VaR as the α-quantile of the return distribution.
func historicalVaR(returns []float64, confidence float64) float64 {
	sorted := make([]float64, len(returns))
	copy(sorted, returns)
	sort.Float64s(sorted)
	idx := int(math.Floor((1 - confidence) * float64(len(sorted))))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	var_ := -sorted[idx]
	if var_ < 0 {
		return 0
	}
	return var_
}

// historicalCVaR computes CVaR as the mean of losses beyond VaR.
func historicalCVaR(returns []float64, confidence float64) float64 {
	sorted := make([]float64, len(returns))
	copy(sorted, returns)
	sort.Float64s(sorted)
	cutoff := int(math.Floor((1 - confidence) * float64(len(sorted))))
	if cutoff <= 0 {
		cutoff = 1
	}
	if cutoff > len(sorted) {
		cutoff = len(sorted)
	}
	var sum float64
	for i := 0; i < cutoff; i++ {
		sum += sorted[i]
	}
	cvar := -(sum / float64(cutoff))
	if cvar < 0 {
		return 0
	}
	return cvar
}

// meanStdDev computes the mean and population standard deviation of a slice.
func meanStdDev(data []float64) (float64, float64) {
	n := float64(len(data))
	if n == 0 {
		return 0, 0
	}
	var sum, sumSq float64
	for _, v := range data {
		sum += v
		sumSq += v * v
	}
	mu := sum / n
	variance := sumSq/n - mu*mu
	if variance < 0 {
		variance = 0
	}
	return mu, math.Sqrt(variance)
}

// inverseNormalCDF approximates the inverse of the standard normal CDF
// using the rational approximation by Peter Acklam.
func inverseNormalCDF(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}
	if p == 0.5 {
		return 0
	}

	// Coefficients for the rational approximation
	const (
		a1 = -3.969683028665376e+01
		a2 = 2.209460984245205e+02
		a3 = -2.759285104469687e+02
		a4 = 1.383577518672690e+02
		a5 = -3.066479806614716e+01
		a6 = 2.506628277459239e+00

		b1 = -5.447609879822406e+01
		b2 = 1.615858368580409e+02
		b3 = -1.556989798598866e+02
		b4 = 6.680131188771972e+01
		b5 = -1.328068155288572e+01

		c1 = -7.784894002430293e-03
		c2 = -3.223964580411365e-01
		c3 = -2.400758277161838e+00
		c4 = -2.549732539343734e+00
		c5 = 4.374664141464968e+00
		c6 = 2.938163982698783e+00

		d1 = 7.784695709041462e-03
		d2 = 3.224671290700398e-01
		d3 = 2.445134137142996e+00
		d4 = 3.754408661907416e+00
	)

	const (
		pLow  = 0.02425
		pHigh = 1 - pLow
	)

	var q, r float64

	if p < pLow {
		// Rational approximation for lower region
		q = math.Sqrt(-2 * math.Log(p))
		return (((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) /
			((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	} else if p <= pHigh {
		// Rational approximation for central region
		q = p - 0.5
		r = q * q
		return (((((a1*r+a2)*r+a3)*r+a4)*r+a5)*r + a6) * q /
			(((((b1*r+b2)*r+b3)*r+b4)*r+b5)*r + 1)
	} else {
		// Rational approximation for upper region
		q = math.Sqrt(-2 * math.Log(1-p))
		return -(((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) /
			((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	}
}

// normalPDF computes the standard normal probability density function.
func normalPDF(x float64) float64 {
	return math.Exp(-0.5*x*x) / math.Sqrt(2*math.Pi)
}
