package backtest

import "math"

// ProbabilisticSharpeRatio computes PSR: P(SR > SR_threshold).
// Accounts for skewness and kurtosis of returns.
func ProbabilisticSharpeRatio(observedSR, srThreshold float64, numReturns int, skewness, kurtosis float64) float64 {
	n := float64(numReturns)
	if n < 5 {
		return 0
	}

	// Standard error of Sharpe ratio (Lo 2002, Bailey & Lopez de Prado 2014)
	sr2 := observedSR * observedSR
	numerator := 1.0 - skewness*observedSR + kurtosis/4.0*sr2
	if numerator < 0 {
		numerator = 1.0 // fallback
	}
	se := math.Sqrt(numerator / (n - 1))

	if se == 0 {
		return 0
	}

	z := (observedSR - srThreshold) / se
	return normalCDF(z)
}

// DeflatedSharpeRatio adjusts for multiple testing.
// srThreshold accounts for the number of trials.
func DeflatedSharpeRatio(observedSR float64, numReturns int, skewness, kurtosis float64, numTrials int) float64 {
	if numTrials <= 1 {
		return ProbabilisticSharpeRatio(observedSR, 0, numReturns, skewness, kurtosis)
	}

	// SR threshold from expected maximum of numTrials i.i.d. N(0,1) draws
	euler := 0.5772156649 // Euler-Mascheroni constant
	logN := math.Log(float64(numTrials))
	srThreshold := math.Sqrt(2*logN) - (euler+math.Log(math.Pi/2))/(2*math.Sqrt(2*logN))

	return ProbabilisticSharpeRatio(observedSR, srThreshold, numReturns, skewness, kurtosis)
}

func normalCDF(z float64) float64 {
	return 0.5 * (1 + math.Erf(z/math.Sqrt(2)))
}

// SkewnessKurtosis computes sample skewness and excess kurtosis.
func SkewnessKurtosis(returns []float64) (skewness, kurtosis float64) {
	n := float64(len(returns))
	if n < 3 {
		return 0, 0
	}

	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= n

	var m2, m3, m4 float64
	for _, r := range returns {
		d := r - mean
		m2 += d * d
		m3 += d * d * d
		m4 += d * d * d * d
	}
	m2 /= n
	m3 /= n
	m4 /= n

	if m2 == 0 {
		return 0, 3
	}

	skewness = m3 / math.Pow(m2, 1.5)
	kurtosis = m4/(m2*m2) - 3.0 // excess kurtosis (normal = 0)
	return
}
