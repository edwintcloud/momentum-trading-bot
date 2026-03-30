package backtest

import (
	"math"
	"sort"
)

// MHTMethod identifies the multiple hypothesis testing correction method.
type MHTMethod string

const (
	MHTNone              MHTMethod = "none"
	MHTBonferroni        MHTMethod = "bonferroni"
	MHTBenjaminiHochberg MHTMethod = "benjamini-hochberg"
)

// MHTResult holds the outcome of a multiple hypothesis testing correction.
type MHTResult struct {
	Method           MHTMethod `json:"method"`
	Alpha            float64   `json:"alpha"`
	NTrials          int       `json:"nTrials"`
	OriginalPValues  []float64 `json:"originalPValues"`
	AdjustedPValues  []float64 `json:"adjustedPValues"`
	Rejected         []bool    `json:"rejected"`
	SignificantCount int       `json:"significantCount"`
}

// TrialCounter tracks the total number of parameter combinations evaluated
// during optimization, including ALL trials — not just "promising" ones.
type TrialCounter struct {
	count int
}

// BonferroniCorrection applies the Bonferroni correction to a set of p-values.
//
//	α_adjusted = α / N_tests
//
// Most conservative correction; controls Family-Wise Error Rate (FWER).
// Appropriate when N_tests < 50 and any false discovery is costly.
func BonferroniCorrection(pValues []float64, alpha float64) MHTResult {
	n := len(pValues)
	if n == 0 {
		return MHTResult{
			Method: MHTBonferroni,
			Alpha:  alpha,
		}
	}

	adjusted := make([]float64, n)
	rejected := make([]bool, n)
	sigCount := 0

	for i, p := range pValues {
		// Bonferroni adjusted p-value: min(p * N, 1.0)
		adj := p * float64(n)
		if adj > 1.0 {
			adj = 1.0
		}
		adjusted[i] = adj
		rejected[i] = adj < alpha
		if rejected[i] {
			sigCount++
		}
	}

	return MHTResult{
		Method:           MHTBonferroni,
		Alpha:            alpha,
		NTrials:          n,
		OriginalPValues:  pValues,
		AdjustedPValues:  adjusted,
		Rejected:         rejected,
		SignificantCount: sigCount,
	}
}

// BenjaminiHochbergCorrection applies the Benjamini-Hochberg (BH) procedure.
//
//	Sort p-values: p_(1) ≤ p_(2) ≤ ... ≤ p_(m)
//	BH threshold for rank k: p_(k) ≤ (k/m) × α
//	Reject H_0 for all p_(k) that satisfy the threshold.
//
// Controls False Discovery Rate (FDR), less conservative than Bonferroni.
// Preferred when N_tests > 50.
func BenjaminiHochbergCorrection(pValues []float64, alpha float64) MHTResult {
	n := len(pValues)
	if n == 0 {
		return MHTResult{
			Method: MHTBenjaminiHochberg,
			Alpha:  alpha,
		}
	}

	// Create index-sorted order of p-values (ascending).
	type indexedP struct {
		origIdx int
		pValue  float64
	}
	sorted := make([]indexedP, n)
	for i, p := range pValues {
		sorted[i] = indexedP{origIdx: i, pValue: p}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].pValue < sorted[j].pValue
	})

	// Compute BH-adjusted p-values.
	// Working backwards: adj_p(k) = min(adj_p(k+1), p(k) * m / k)
	adjustedSorted := make([]float64, n)
	m := float64(n)
	for i := n - 1; i >= 0; i-- {
		rank := float64(i + 1)
		adj := sorted[i].pValue * m / rank
		if adj > 1.0 {
			adj = 1.0
		}
		if i < n-1 && adjustedSorted[i+1] < adj {
			adj = adjustedSorted[i+1]
		}
		adjustedSorted[i] = adj
	}

	// Map back to original order.
	adjusted := make([]float64, n)
	rejected := make([]bool, n)
	sigCount := 0
	for i, sp := range sorted {
		adjusted[sp.origIdx] = adjustedSorted[i]
		rejected[sp.origIdx] = adjustedSorted[i] < alpha
		if rejected[sp.origIdx] {
			sigCount++
		}
	}

	return MHTResult{
		Method:           MHTBenjaminiHochberg,
		Alpha:            alpha,
		NTrials:          n,
		OriginalPValues:  pValues,
		AdjustedPValues:  adjusted,
		Rejected:         rejected,
		SignificantCount: sigCount,
	}
}

// ApplyMHTCorrection applies the specified correction method to a set of p-values.
// Returns uncorrected results when method is MHTNone.
func ApplyMHTCorrection(pValues []float64, alpha float64, method MHTMethod) MHTResult {
	switch method {
	case MHTBonferroni:
		return BonferroniCorrection(pValues, alpha)
	case MHTBenjaminiHochberg:
		return BenjaminiHochbergCorrection(pValues, alpha)
	default:
		// No correction — raw comparison against alpha.
		n := len(pValues)
		adjusted := make([]float64, n)
		rejected := make([]bool, n)
		sigCount := 0
		for i, p := range pValues {
			adjusted[i] = p
			rejected[i] = p < alpha
			if rejected[i] {
				sigCount++
			}
		}
		return MHTResult{
			Method:           MHTNone,
			Alpha:            alpha,
			NTrials:          n,
			OriginalPValues:  pValues,
			AdjustedPValues:  adjusted,
			Rejected:         rejected,
			SignificantCount: sigCount,
		}
	}
}

// SharpeRatioPValue computes a two-sided p-value for the null hypothesis that
// the true Sharpe ratio is zero, using the asymptotic distribution from
// Lo (2002) adjusted for skewness and kurtosis (Bailey & López de Prado 2014).
func SharpeRatioPValue(observedSR float64, numReturns int, skewness, kurtosis float64) float64 {
	n := float64(numReturns)
	if n < 5 || observedSR == 0 {
		return 1.0
	}

	sr2 := observedSR * observedSR
	numerator := 1.0 - skewness*observedSR + kurtosis/4.0*sr2
	if numerator < 0 {
		numerator = 1.0
	}
	se := math.Sqrt(numerator / (n - 1))
	if se == 0 {
		return 1.0
	}

	z := math.Abs(observedSR) / se
	// Two-sided p-value.
	return 2.0 * (1.0 - normalCDF(z))
}
