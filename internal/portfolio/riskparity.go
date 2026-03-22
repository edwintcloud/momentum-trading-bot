package portfolio

import (
	"math"
	"time"
)

// RiskParityInput holds the inputs for risk parity weight calculation.
type RiskParityInput struct {
	Symbols    []string    // ordered list of asset symbols
	Volatility []float64   // per-asset volatility (σ_i)
}

// RiskParityResult holds the output of risk parity calculation.
type RiskParityResult struct {
	Weights map[string]float64 // target portfolio weights
}

// EWMAVolTracker tracks per-asset EWMA volatility estimates.
type EWMAVolTracker struct {
	Lambda       float64            // decay factor (e.g. 0.94)
	Variances    map[string]float64 // running EWMA variance per symbol
	LastReturns  map[string]float64 // last observed return per symbol
	Initialized  map[string]bool    // whether the tracker has been seeded
}

// NewEWMAVolTracker creates a new EWMA volatility tracker.
func NewEWMAVolTracker(lambda float64) *EWMAVolTracker {
	if lambda <= 0 || lambda >= 1 {
		lambda = 0.94
	}
	return &EWMAVolTracker{
		Lambda:      lambda,
		Variances:   make(map[string]float64),
		LastReturns: make(map[string]float64),
		Initialized: make(map[string]bool),
	}
}

// Update processes a new return observation for a symbol.
func (e *EWMAVolTracker) Update(symbol string, ret float64) {
	if !e.Initialized[symbol] {
		// Seed with the squared return as initial variance
		e.Variances[symbol] = ret * ret
		e.Initialized[symbol] = true
		e.LastReturns[symbol] = ret
		return
	}
	// EWMA variance: σ²_t = λ·σ²_{t-1} + (1-λ)·r²_t
	e.Variances[symbol] = e.Lambda*e.Variances[symbol] + (1-e.Lambda)*ret*ret
	e.LastReturns[symbol] = ret
}

// Volatility returns the current EWMA volatility estimate for a symbol.
func (e *EWMAVolTracker) Volatility(symbol string) float64 {
	v, ok := e.Variances[symbol]
	if !ok || v <= 0 {
		return 0
	}
	return math.Sqrt(v)
}

// RunRiskParity computes volatility-parity weights.
// w_i_raw = 1/σ_i, then normalized so absolute weights sum to 1.
func RunRiskParity(input RiskParityInput) RiskParityResult {
	result := RiskParityResult{Weights: make(map[string]float64)}
	n := len(input.Symbols)
	if n == 0 || len(input.Volatility) != n {
		return result
	}

	rawWeights := make([]float64, n)
	sum := 0.0
	for i := 0; i < n; i++ {
		vol := input.Volatility[i]
		if vol <= 1e-12 {
			// Very low or zero vol — assign a large raw weight (inverse of small number)
			rawWeights[i] = 1e6
		} else {
			rawWeights[i] = 1.0 / vol
		}
		sum += rawWeights[i]
	}

	if sum <= 0 {
		return result
	}

	for i, sym := range input.Symbols {
		result.Weights[sym] = rawWeights[i] / sum
	}
	return result
}

// RiskContribution computes per-asset risk contributions.
// RC_i = w_i × (Σw)_i / sqrt(w'Σw)
func RiskContribution(weights []float64, covMatrix [][]float64) []float64 {
	n := len(weights)
	if n == 0 || len(covMatrix) != n {
		return nil
	}

	// Compute Σw
	sigmaW := matVecMul(covMatrix, weights)

	// Compute portfolio variance w'Σw
	portVar := 0.0
	for i := 0; i < n; i++ {
		portVar += weights[i] * sigmaW[i]
	}

	portVol := math.Sqrt(math.Max(portVar, 0))
	if portVol < 1e-12 {
		return make([]float64, n)
	}

	rc := make([]float64, n)
	for i := 0; i < n; i++ {
		rc[i] = weights[i] * sigmaW[i] / portVol
	}
	return rc
}

// RebalanceChecker determines whether a risk parity rebalance is needed.
type RebalanceChecker struct {
	TargetWeights      map[string]float64
	DeviationThreshold float64
	RebalanceInterval  time.Duration
	LastRebalance      time.Time
}

// NeedsRebalance returns true if any weight has deviated beyond the threshold
// or the time interval has elapsed.
func (r *RebalanceChecker) NeedsRebalance(currentWeights map[string]float64, now time.Time) bool {
	// Time-based trigger
	if !r.LastRebalance.IsZero() && now.Sub(r.LastRebalance) >= r.RebalanceInterval {
		return true
	}

	// Deviation-based trigger
	for sym, target := range r.TargetWeights {
		current, ok := currentWeights[sym]
		if !ok {
			continue
		}
		if target <= 1e-12 {
			continue
		}
		deviation := math.Abs(current-target) / target
		if deviation > r.DeviationThreshold {
			return true
		}
	}
	return false
}
