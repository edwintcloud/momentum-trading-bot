package backtest

import (
	"math"
	"math/rand"
	"sort"
)

// BootstrapResult records the outcome of a bootstrap significance test.
type BootstrapResult struct {
	PValue      float64 `json:"pValue"`
	CI95Lower   float64 `json:"ci95Lower"`
	CI95Upper   float64 `json:"ci95Upper"`
	Significant bool    `json:"significant"`
	Resamples   int     `json:"resamples"`
}

// BootstrapSignificance tests H0: mean trade return = 0.
// Returns p-value (reject H0 if p < 0.05), and a 95% CI on mean return.
func BootstrapSignificance(tradeReturns []float64, numResamples int) (pValue float64, ci95Lower float64, ci95Upper float64) {
	if len(tradeReturns) < 5 {
		return 1.0, 0, 0
	}

	rng := rand.New(rand.NewSource(42))
	n := len(tradeReturns)

	// Compute observed mean
	observedMean := sliceMean(tradeReturns)

	// Center returns at zero (H0: mean = 0)
	centered := make([]float64, n)
	for i, r := range tradeReturns {
		centered[i] = r - observedMean
	}

	// Bootstrap resamples of centered returns
	var exceedCount int
	for b := 0; b < numResamples; b++ {
		var sum float64
		for i := 0; i < n; i++ {
			idx := rng.Intn(n)
			sum += centered[idx]
		}
		bMean := sum / float64(n)

		if math.Abs(bMean) >= math.Abs(observedMean) {
			exceedCount++
		}
	}

	pValue = float64(exceedCount) / float64(numResamples)

	// Confidence interval on mean return (from non-centered bootstrap)
	bMeansCI := make([]float64, numResamples)
	for b := 0; b < numResamples; b++ {
		var sum float64
		for i := 0; i < n; i++ {
			idx := rng.Intn(n)
			sum += tradeReturns[idx]
		}
		bMeansCI[b] = sum / float64(n)
	}
	sort.Float64s(bMeansCI)
	ci95Lower = percentile(bMeansCI, 2.5)
	ci95Upper = percentile(bMeansCI, 97.5)

	return
}

func sliceMean(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	var sum float64
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}
