package optimizer

import (
	"math"
	"sort"
)

// SensitivityResult contains first-order sensitivity indices per parameter.
type SensitivityResult struct {
	Parameters []ParameterSensitivity `json:"parameters"`
}

// ParameterSensitivity captures how much a single parameter affects the objective.
type ParameterSensitivity struct {
	Name          string  `json:"name"`
	FirstOrderIdx float64 `json:"firstOrderIdx"`
	TotalIdx      float64 `json:"totalIdx"`
}

// Evaluation records a single optimizer trial for sensitivity analysis.
type Evaluation struct {
	ParamValues []float64
	Score       float64
}

// ComputeSensitivity analyzes which parameters most affect the objective function.
func ComputeSensitivity(evaluations []Evaluation, paramNames []string) SensitivityResult {
	if len(evaluations) < 20 || len(paramNames) == 0 {
		return SensitivityResult{}
	}

	scores := make([]float64, len(evaluations))
	for i, e := range evaluations {
		scores[i] = e.Score
	}

	totalVar := variance(scores)
	if totalVar == 0 {
		return SensitivityResult{}
	}

	result := SensitivityResult{}
	for pIdx, pName := range paramNames {
		if pIdx >= len(evaluations[0].ParamValues) {
			continue
		}

		paramValues := make([]float64, len(evaluations))
		for i, e := range evaluations {
			if pIdx < len(e.ParamValues) {
				paramValues[i] = e.ParamValues[pIdx]
			}
		}

		// First-order: correlation^2 ≈ fraction of variance explained
		corr := PearsonCorrelation(paramValues, scores)
		firstOrder := corr * corr

		// Total index: also consider non-linear effects via binning
		totalIdx := computeTotalSensitivity(paramValues, scores, totalVar)

		result.Parameters = append(result.Parameters, ParameterSensitivity{
			Name:          pName,
			FirstOrderIdx: firstOrder,
			TotalIdx:      totalIdx,
		})
	}

	return result
}

// PearsonCorrelation computes the Pearson correlation coefficient between x and y.
func PearsonCorrelation(x, y []float64) float64 {
	n := len(x)
	if n != len(y) || n < 2 {
		return 0
	}

	var sumX, sumY, sumXY, sumX2, sumY2 float64
	for i := 0; i < n; i++ {
		sumX += x[i]
		sumY += y[i]
		sumXY += x[i] * y[i]
		sumX2 += x[i] * x[i]
		sumY2 += y[i] * y[i]
	}

	nf := float64(n)
	denom := math.Sqrt((nf*sumX2 - sumX*sumX) * (nf*sumY2 - sumY*sumY))
	if denom == 0 {
		return 0
	}

	return (nf*sumXY - sumX*sumY) / denom
}

func computeTotalSensitivity(paramValues, scores []float64, totalVar float64) float64 {
	if totalVar == 0 || len(paramValues) == 0 {
		return 0
	}

	// Bin the parameter values into 10 quantile bins
	numBins := 10
	if len(paramValues) < numBins*3 {
		numBins = len(paramValues) / 3
	}
	if numBins < 2 {
		return 0
	}

	type indexedVal struct {
		idx int
		val float64
	}
	indexed := make([]indexedVal, len(paramValues))
	for i, v := range paramValues {
		indexed[i] = indexedVal{idx: i, val: v}
	}
	sort.Slice(indexed, func(i, j int) bool {
		return indexed[i].val < indexed[j].val
	})

	binSize := len(indexed) / numBins
	if binSize < 1 {
		binSize = 1
	}

	// Compute E[Var(Y|X)] — expected conditional variance
	var conditionalVarSum float64
	var totalCount float64
	for b := 0; b < numBins; b++ {
		start := b * binSize
		end := start + binSize
		if b == numBins-1 {
			end = len(indexed)
		}
		if start >= len(indexed) {
			break
		}

		binScores := make([]float64, 0, end-start)
		for i := start; i < end && i < len(indexed); i++ {
			binScores = append(binScores, scores[indexed[i].idx])
		}

		if len(binScores) < 2 {
			continue
		}

		binVar := variance(binScores)
		conditionalVarSum += binVar * float64(len(binScores))
		totalCount += float64(len(binScores))
	}

	if totalCount == 0 {
		return 0
	}

	expectedConditionalVar := conditionalVarSum / totalCount
	// First-order index via binning: 1 - E[Var(Y|X)] / Var(Y)
	firstOrderBin := 1.0 - expectedConditionalVar/totalVar
	if firstOrderBin < 0 {
		firstOrderBin = 0
	}

	return math.Min(firstOrderBin, 1.0)
}

func variance(data []float64) float64 {
	n := float64(len(data))
	if n < 2 {
		return 0
	}
	var sum, sumSq float64
	for _, v := range data {
		sum += v
		sumSq += v * v
	}
	mean := sum / n
	return sumSq/n - mean*mean
}
