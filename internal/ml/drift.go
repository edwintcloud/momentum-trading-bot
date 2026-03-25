package ml

import (
	"math"
	"sync"
)

// DriftDetector monitors concept drift via PSI and performance metrics.
type DriftDetector struct {
	mu sync.Mutex

	// Training distribution: proportion per decile bin
	trainDistribution []float64

	// Rolling prediction accuracy (EWMA)
	ewmaAccuracy float64
	ewmaLambda   float64
	initialized  bool

	// Rolling Sharpe tracking for ML-driven trades
	recentReturns []float64
	maxReturns    int
}

// NewDriftDetector creates a detector with the given training distribution
// (decile proportions, length 10) and EWMA decay factor.
func NewDriftDetector(trainDist []float64, ewmaLambda float64) *DriftDetector {
	if ewmaLambda <= 0 || ewmaLambda >= 1 {
		ewmaLambda = 0.95
	}
	dist := make([]float64, len(trainDist))
	copy(dist, trainDist)
	return &DriftDetector{
		trainDistribution: dist,
		ewmaLambda:        ewmaLambda,
		maxReturns:        500,
	}
}

// ComputePSI calculates the Population Stability Index between two distributions.
// Both slices should represent proportions per bin (e.g., deciles) and sum to ~1.
// PSI = Σ (p_i - q_i) × ln(p_i / q_i)
// PSI > 0.2 → significant drift
func ComputePSI(trainDist, liveDist []float64) float64 {
	if len(trainDist) == 0 || len(liveDist) == 0 {
		return 0
	}
	n := len(trainDist)
	if len(liveDist) < n {
		n = len(liveDist)
	}

	psi := 0.0
	for i := 0; i < n; i++ {
		p := math.Max(trainDist[i], 1e-8) // avoid division by zero
		q := math.Max(liveDist[i], 1e-8)
		psi += (p - q) * math.Log(p/q)
	}
	return psi
}

// BinIntoDeciles converts raw feature values into decile proportions.
// It sorts values into 10 equal-width bins based on the provided min/max range.
func BinIntoDeciles(values []float64, minVal, maxVal float64) []float64 {
	bins := make([]float64, 10)
	if len(values) == 0 || maxVal <= minVal {
		// uniform fallback
		for i := range bins {
			bins[i] = 0.1
		}
		return bins
	}

	binWidth := (maxVal - minVal) / 10.0
	for _, v := range values {
		idx := int((v - minVal) / binWidth)
		if idx < 0 {
			idx = 0
		}
		if idx >= 10 {
			idx = 9
		}
		bins[idx]++
	}

	// Normalize to proportions
	total := float64(len(values))
	for i := range bins {
		bins[i] /= total
		if bins[i] == 0 {
			bins[i] = 1e-8 // avoid zero proportions
		}
	}
	return bins
}

// CheckPSI evaluates drift between training and live feature distributions.
// Returns (psi value, needs retrain).
func (d *DriftDetector) CheckPSI(liveDistribution []float64, threshold float64) (float64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.trainDistribution) == 0 {
		return 0, false
	}

	psi := ComputePSI(d.trainDistribution, liveDistribution)
	return psi, psi > threshold
}

// UpdateAccuracy updates the EWMA prediction accuracy tracker.
// correct should be 1.0 for a correct prediction, 0.0 for incorrect.
func (d *DriftDetector) UpdateAccuracy(correct float64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.initialized {
		d.ewmaAccuracy = correct
		d.initialized = true
		return
	}
	d.ewmaAccuracy = d.ewmaLambda*d.ewmaAccuracy + (1-d.ewmaLambda)*correct
}

// Accuracy returns the current EWMA prediction accuracy.
func (d *DriftDetector) Accuracy() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ewmaAccuracy
}

// RecordReturn records an ML-driven trade return for rolling Sharpe computation.
func (d *DriftDetector) RecordReturn(ret float64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.recentReturns = append(d.recentReturns, ret)
	if len(d.recentReturns) > d.maxReturns {
		d.recentReturns = d.recentReturns[1:]
	}
}

// RollingSharpe computes the annualized Sharpe ratio of recent ML-driven trades.
// Returns 0 if insufficient data.
func (d *DriftDetector) RollingSharpe() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.recentReturns) < 10 {
		return 0
	}

	mean := 0.0
	for _, r := range d.recentReturns {
		mean += r
	}
	mean /= float64(len(d.recentReturns))

	sumSq := 0.0
	for _, r := range d.recentReturns {
		diff := r - mean
		sumSq += diff * diff
	}
	stddev := math.Sqrt(sumSq / float64(len(d.recentReturns)-1))

	if stddev == 0 {
		return 0
	}

	// Annualize assuming ~252 trading days, ~390 bars/day for intraday
	return (mean / stddev) * math.Sqrt(252)
}

// CheckPerformanceDrift returns true if the rolling Sharpe of ML trades
// has fallen below the baseline threshold, indicating the model should
// be disabled or retrained.
func (d *DriftDetector) CheckPerformanceDrift(baselineThreshold float64) bool {
	d.mu.Lock()
	n := len(d.recentReturns)
	d.mu.Unlock()
	sharpe := d.RollingSharpe()
	return n >= 30 && sharpe < baselineThreshold
}

// ConfidenceMultiplier returns a multiplier for model confidence based on
// drift status. Returns 1.0 if no drift, 0.5 if PSI drift detected.
func ConfidenceMultiplier(psi, threshold float64) float64 {
	if psi > threshold {
		return 0.5
	}
	return 1.0
}
