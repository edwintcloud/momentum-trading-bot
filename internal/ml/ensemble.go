package ml

import (
	"math"
)

// Signal represents a named signal with a value in [-1, 1].
type Signal struct {
	Name  string
	Value float64
}

// EnsembleResult is the output of signal combination.
type EnsembleResult struct {
	CombinedSignal float64
	Method         string
	SignalCount    int
}

// EqualWeightBlend computes combined_signal = (1/n) × Σ signal_i.
func EqualWeightBlend(signals []Signal) EnsembleResult {
	if len(signals) == 0 {
		return EnsembleResult{Method: "equal"}
	}

	sum := 0.0
	for _, s := range signals {
		sum += s.Value
	}
	return EnsembleResult{
		CombinedSignal: sum / float64(len(signals)),
		Method:         "equal",
		SignalCount:    len(signals),
	}
}

// IRWeightedBlend computes combined_signal = Σ w_i × signal_i,
// where w_i ∝ IR_i (information ratio). IR is approximated as
// meanReturn / stdReturn for each signal's history.
func IRWeightedBlend(signals []Signal, irWeights []float64) EnsembleResult {
	if len(signals) == 0 {
		return EnsembleResult{Method: "ir_weighted"}
	}
	if len(irWeights) != len(signals) {
		// Fallback to equal weight
		return EqualWeightBlend(signals)
	}

	// Normalize weights to sum to 1
	totalW := 0.0
	for _, w := range irWeights {
		totalW += math.Abs(w)
	}
	if totalW == 0 {
		return EqualWeightBlend(signals)
	}

	sum := 0.0
	for i, s := range signals {
		normalizedW := math.Abs(irWeights[i]) / totalW
		sum += normalizedW * s.Value
	}
	return EnsembleResult{
		CombinedSignal: sum,
		Method:         "ir_weighted",
		SignalCount:    len(signals),
	}
}

// RegimeConditionalBlend routes signals through regime-appropriate weighting.
// In bullish low-vol: emphasize momentum signals.
// In high-vol: emphasize mean-reversion signals.
// In undefined/ranging: reduce all signals.
func RegimeConditionalBlend(signals []Signal, regime string, regimeConfidence float64) EnsembleResult {
	if len(signals) == 0 {
		return EnsembleResult{Method: "regime_conditional"}
	}

	weights := make(map[string]float64)

	switch regime {
	case "bullish":
		// Emphasize momentum signals
		for _, s := range signals {
			switch s.Name {
			case "momentum", "ml_score", "breakout":
				weights[s.Name] = 1.5
			case "mean_reversion":
				weights[s.Name] = 0.5
			default:
				weights[s.Name] = 1.0
			}
		}
	case "bearish":
		// Emphasize mean-reversion, reduce momentum
		for _, s := range signals {
			switch s.Name {
			case "momentum", "breakout":
				weights[s.Name] = 0.5
			case "mean_reversion":
				weights[s.Name] = 1.5
			default:
				weights[s.Name] = 1.0
			}
		}
	default:
		// Ranging/undefined: reduce everything
		for _, s := range signals {
			weights[s.Name] = 0.5
		}
	}

	// Apply regime confidence as a global scaling factor
	confidenceScale := math.Max(regimeConfidence, 0.3)

	totalW := 0.0
	for _, s := range signals {
		totalW += weights[s.Name]
	}
	if totalW == 0 {
		return EnsembleResult{Method: "regime_conditional", SignalCount: len(signals)}
	}

	sum := 0.0
	for _, s := range signals {
		w := weights[s.Name] / totalW
		sum += w * s.Value * confidenceScale
	}

	return EnsembleResult{
		CombinedSignal: sum,
		Method:         "regime_conditional",
		SignalCount:    len(signals),
	}
}

// BlendSignals dispatches to the appropriate blending method based on config.
func BlendSignals(method string, signals []Signal, irWeights []float64, regime string, regimeConfidence float64) EnsembleResult {
	switch method {
	case "ir_weighted":
		return IRWeightedBlend(signals, irWeights)
	case "regime_conditional":
		return RegimeConditionalBlend(signals, regime, regimeConfidence)
	default:
		return EqualWeightBlend(signals)
	}
}

// PairwiseCorrelation computes the average pairwise correlation of signal
// history columns. Each column in history represents one signal's time series.
// Returns 0 if insufficient data.
func PairwiseCorrelation(history [][]float64) float64 {
	n := len(history)
	if n < 2 {
		return 0
	}
	// Each history[i] is a time series of signal values
	rows := len(history[0])
	if rows < 3 {
		return 0
	}

	pairs := 0
	totalCorr := 0.0

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if len(history[i]) != rows || len(history[j]) != rows {
				continue
			}
			corr := pearsonCorrelation(history[i], history[j])
			totalCorr += math.Abs(corr)
			pairs++
		}
	}

	if pairs == 0 {
		return 0
	}
	return totalCorr / float64(pairs)
}

// DiversityCheck returns true if the ensemble has sufficient diversity
// (average pairwise correlation below threshold).
func DiversityCheck(history [][]float64, threshold float64) bool {
	avgCorr := PairwiseCorrelation(history)
	return avgCorr < threshold
}

// pearsonCorrelation computes the Pearson correlation between two series.
func pearsonCorrelation(x, y []float64) float64 {
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
