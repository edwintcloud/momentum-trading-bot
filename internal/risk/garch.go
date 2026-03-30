package risk

import (
	"math"
	"sync"
)

// GARCHForecaster implements GARCH(1,1) volatility forecasting.
//
// Model: σ²_t = ω + α·ε²_{t-1} + β·σ²_{t-1}
// where ω = (1 - α - β) × long_run_variance
//
// Default parameters: α = 0.10, β = 0.85, long_run_var ≈ 0.0004 (~2% daily vol)
type GARCHForecaster struct {
	mu         sync.RWMutex
	alpha      float64 // ARCH coefficient (reaction to shocks)
	beta       float64 // GARCH coefficient (persistence)
	omega      float64 // intercept = (1 - α - β) × long_run_var
	longRunVar float64 // unconditional variance

	// Per-symbol state
	estimates map[string]*garchState
}

type garchState struct {
	lastReturn   float64 // ε_{t-1}
	lastVariance float64 // σ²_{t-1}
	lastPrice    float64
	initialized  bool
	observations int
}

// UpdatePrice feeds a new price observation for a symbol.
func (g *GARCHForecaster) UpdatePrice(symbol string, price float64) {
	if price <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	st, ok := g.estimates[symbol]
	if !ok {
		st = &garchState{lastPrice: price}
		g.estimates[symbol] = st
		return
	}

	if st.lastPrice <= 0 {
		st.lastPrice = price
		return
	}

	// Compute log return
	ret := math.Log(price / st.lastPrice)
	st.lastPrice = price
	st.observations++

	if !st.initialized {
		// Bootstrap: use return squared as initial variance
		st.lastVariance = g.longRunVar
		st.lastReturn = ret
		st.initialized = true
		return
	}

	// GARCH(1,1) update: σ²_t = ω + α·ε²_{t-1} + β·σ²_{t-1}
	newVariance := g.omega + g.alpha*st.lastReturn*st.lastReturn + g.beta*st.lastVariance
	if newVariance < 1e-12 {
		newVariance = g.longRunVar
	}
	st.lastVariance = newVariance
	st.lastReturn = ret
}

// ForecastVariance returns the 1-step-ahead conditional variance for a symbol.
func (g *GARCHForecaster) ForecastVariance(symbol string) float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	st, ok := g.estimates[symbol]
	if !ok || !st.initialized || st.observations < 2 {
		return g.longRunVar
	}
	return g.omega + g.alpha*st.lastReturn*st.lastReturn + g.beta*st.lastVariance
}

// ForecastVolatility returns the 1-step-ahead volatility (std dev) for a symbol.
func (g *GARCHForecaster) ForecastVolatility(symbol string) float64 {
	v := g.ForecastVariance(symbol)
	if v <= 0 {
		return math.Sqrt(g.longRunVar)
	}
	return math.Sqrt(v)
}

// AnnualizedVolatility returns the GARCH forecast annualized from minute bars.
// Annualization factor: sqrt(390 * 252) for minute-to-annual scaling.
func (g *GARCHForecaster) AnnualizedVolatility(symbol string) float64 {
	return g.ForecastVolatility(symbol) * math.Sqrt(390*252)
}

// GetVolatility integrates with VolatilityEstimator interface pattern.
// Returns annualized volatility or long-run default.
func (g *GARCHForecaster) GetVolatility(symbol string) float64 {
	vol := g.AnnualizedVolatility(symbol)
	if vol <= 0 {
		return math.Sqrt(g.longRunVar) * math.Sqrt(390*252)
	}
	return vol
}
