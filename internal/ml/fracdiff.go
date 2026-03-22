package ml

import "math"

// FracDiffWeights computes fractional differentiation weights using the recursive
// formula: w_k = -w_{k-1} * (d - k + 1) / k, starting with w_0 = 1.
// Weights are truncated when |w_k| < threshold.
func FracDiffWeights(d float64, maxLen int, threshold float64) []float64 {
	if maxLen <= 0 {
		return nil
	}
	if threshold <= 0 {
		threshold = 1e-5
	}
	weights := make([]float64, 0, maxLen)
	weights = append(weights, 1.0)

	for k := 1; k < maxLen; k++ {
		w := -weights[k-1] * (d - float64(k) + 1) / float64(k)
		if math.Abs(w) < threshold {
			break
		}
		weights = append(weights, w)
	}
	return weights
}

// FracDiff applies fractional differentiation of order d to a price series.
// It convolves the series with the fractional weights to produce a stationary
// series that preserves memory. Returns nil if the series is too short.
func FracDiff(series []float64, d float64, threshold float64) []float64 {
	if len(series) == 0 {
		return nil
	}
	weights := FracDiffWeights(d, len(series), threshold)
	if len(weights) == 0 {
		return nil
	}

	n := len(series)
	wLen := len(weights)
	result := make([]float64, 0, n-wLen+1)

	for i := wLen - 1; i < n; i++ {
		val := 0.0
		for j := 0; j < wLen; j++ {
			val += weights[j] * series[i-j]
		}
		result = append(result, val)
	}
	return result
}

// VarianceRatio computes a simplified stationarity test.
// It compares variance of sub-segments to overall variance.
// A ratio near 1.0 suggests stationarity; ratios significantly different
// indicate non-stationarity. Returns ratio in [0, 2+].
func VarianceRatio(series []float64, lag int) float64 {
	n := len(series)
	if n < lag*2 || lag <= 0 {
		return 0
	}

	// Compute 1-period returns
	returns1 := make([]float64, n-1)
	for i := 1; i < n; i++ {
		returns1[i-1] = series[i] - series[i-1]
	}

	// Compute lag-period returns
	returnsK := make([]float64, n-lag)
	for i := lag; i < n; i++ {
		returnsK[i-lag] = series[i] - series[i-lag]
	}

	var1 := variance(returns1)
	varK := variance(returnsK)

	if var1 == 0 {
		return 0
	}
	return varK / (float64(lag) * var1)
}

// FindMinD searches for the minimum fractional differentiation order d
// in [minD, maxD] such that the series becomes approximately stationary.
// Stationarity is assessed via variance ratio test — a ratio near 1.0
// indicates stationarity. Step controls the search granularity.
func FindMinD(series []float64, minD, maxD, step float64) float64 {
	if len(series) < 10 {
		return minD
	}
	if step <= 0 {
		step = 0.05
	}

	bestD := maxD
	stationarityThreshold := 0.3 // VR within [0.7, 1.3] is "stationary enough"

	for d := minD; d <= maxD; d += step {
		diffed := FracDiff(series, d, 1e-5)
		if len(diffed) < 10 {
			continue
		}
		vr := VarianceRatio(diffed, 5)
		if math.Abs(vr-1.0) < stationarityThreshold {
			bestD = d
			break
		}
	}
	return bestD
}

// variance computes sample variance of a float64 slice.
func variance(data []float64) float64 {
	n := len(data)
	if n < 2 {
		return 0
	}
	mean := 0.0
	for _, v := range data {
		mean += v
	}
	mean /= float64(n)

	sum := 0.0
	for _, v := range data {
		diff := v - mean
		sum += diff * diff
	}
	return sum / float64(n-1)
}
