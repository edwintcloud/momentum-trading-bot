package ml

import (
	"math"
	"testing"
)

func TestEqualWeightBlend_Basic(t *testing.T) {
	signals := []Signal{
		{Name: "a", Value: 0.5},
		{Name: "b", Value: -0.5},
		{Name: "c", Value: 1.0},
	}

	result := EqualWeightBlend(signals)
	expected := (0.5 - 0.5 + 1.0) / 3.0
	if math.Abs(result.CombinedSignal-expected) > 1e-10 {
		t.Errorf("expected %.4f, got %.4f", expected, result.CombinedSignal)
	}
	if result.Method != "equal" {
		t.Errorf("expected method 'equal', got %s", result.Method)
	}
	if result.SignalCount != 3 {
		t.Errorf("expected 3 signals, got %d", result.SignalCount)
	}
}

func TestEqualWeightBlend_Empty(t *testing.T) {
	result := EqualWeightBlend(nil)
	if result.CombinedSignal != 0 {
		t.Errorf("expected 0 for empty signals, got %f", result.CombinedSignal)
	}
}

func TestIRWeightedBlend_Basic(t *testing.T) {
	signals := []Signal{
		{Name: "a", Value: 1.0},
		{Name: "b", Value: -1.0},
	}
	// Give higher weight to signal a
	weights := []float64{3.0, 1.0}

	result := IRWeightedBlend(signals, weights)
	// Normalized: a=0.75, b=0.25
	expected := 0.75*1.0 + 0.25*(-1.0)
	if math.Abs(result.CombinedSignal-expected) > 1e-10 {
		t.Errorf("expected %.4f, got %.4f", expected, result.CombinedSignal)
	}
}

func TestIRWeightedBlend_FallbackToEqual(t *testing.T) {
	signals := []Signal{
		{Name: "a", Value: 0.5},
		{Name: "b", Value: 0.5},
	}
	// Wrong length → falls back to equal weight
	result := IRWeightedBlend(signals, []float64{1.0})
	if result.Method != "equal" {
		t.Error("should fall back to equal weight on mismatch")
	}
}

func TestIRWeightedBlend_ZeroWeights(t *testing.T) {
	signals := []Signal{{Name: "a", Value: 1.0}}
	result := IRWeightedBlend(signals, []float64{0.0})
	if result.Method != "equal" {
		t.Error("should fall back to equal weight for zero weights")
	}
}

func TestRegimeConditionalBlend_Bullish(t *testing.T) {
	signals := []Signal{
		{Name: "momentum", Value: 0.8},
		{Name: "mean_reversion", Value: 0.3},
	}

	result := RegimeConditionalBlend(signals, "bullish", 0.9)
	// Momentum gets 1.5x weight, mean-reversion 0.5x in bullish
	if result.CombinedSignal <= 0 {
		t.Errorf("bullish regime with positive signals should produce positive result, got %f", result.CombinedSignal)
	}
}

func TestRegimeConditionalBlend_Bearish(t *testing.T) {
	signals := []Signal{
		{Name: "momentum", Value: 0.5},
		{Name: "mean_reversion", Value: 0.8},
	}

	result := RegimeConditionalBlend(signals, "bearish", 0.8)
	// Mean-reversion gets higher weight in bearish
	if result.CombinedSignal <= 0 {
		t.Error("bearish regime with positive signals should still be positive")
	}
}

func TestRegimeConditionalBlend_Ranging(t *testing.T) {
	signals := []Signal{
		{Name: "momentum", Value: 1.0},
		{Name: "mean_reversion", Value: 1.0},
	}

	result := RegimeConditionalBlend(signals, "ranging", 0.5)
	// Ranging reduces all signals by 0.5x
	if result.CombinedSignal >= 1.0 {
		t.Errorf("ranging regime should reduce signals, got %f", result.CombinedSignal)
	}
	if result.CombinedSignal <= 0 {
		t.Errorf("positive signals should still be positive in ranging, got %f", result.CombinedSignal)
	}
}

func TestRegimeConditionalBlend_Empty(t *testing.T) {
	result := RegimeConditionalBlend(nil, "bullish", 0.9)
	if result.CombinedSignal != 0 {
		t.Errorf("expected 0 for empty signals, got %f", result.CombinedSignal)
	}
}

func TestBlendSignals_Dispatch(t *testing.T) {
	signals := []Signal{{Name: "a", Value: 0.5}, {Name: "b", Value: 0.5}}

	// Equal
	r1 := BlendSignals("equal", signals, nil, "", 0)
	if r1.Method != "equal" {
		t.Error("should use equal method")
	}

	// IR weighted
	r2 := BlendSignals("ir_weighted", signals, []float64{1, 1}, "", 0)
	if r2.Method != "ir_weighted" {
		t.Error("should use ir_weighted method")
	}

	// Regime conditional
	r3 := BlendSignals("regime_conditional", signals, nil, "bullish", 0.8)
	if r3.Method != "regime_conditional" {
		t.Error("should use regime_conditional method")
	}

	// Unknown → default to equal
	r4 := BlendSignals("unknown", signals, nil, "", 0)
	if r4.Method != "equal" {
		t.Error("unknown method should default to equal")
	}
}

func TestPairwiseCorrelation_PerfectCorrelation(t *testing.T) {
	s1 := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	s2 := []float64{2, 4, 6, 8, 10, 12, 14, 16, 18, 20}

	corr := PairwiseCorrelation([][]float64{s1, s2})
	if math.Abs(corr-1.0) > 0.01 {
		t.Errorf("perfectly correlated signals should have correlation ~1.0, got %f", corr)
	}
}

func TestPairwiseCorrelation_Uncorrelated(t *testing.T) {
	s1 := []float64{1, -1, 1, -1, 1, -1, 1, -1, 1, -1}
	s2 := []float64{1, 1, -1, -1, 1, 1, -1, -1, 1, 1}

	corr := PairwiseCorrelation([][]float64{s1, s2})
	if corr > 0.3 {
		t.Errorf("uncorrelated signals should have low correlation, got %f", corr)
	}
}

func TestPairwiseCorrelation_Single(t *testing.T) {
	corr := PairwiseCorrelation([][]float64{{1, 2, 3}})
	if corr != 0 {
		t.Errorf("single signal should return 0 correlation, got %f", corr)
	}
}

func TestPairwiseCorrelation_TooShort(t *testing.T) {
	corr := PairwiseCorrelation([][]float64{{1}, {2}})
	if corr != 0 {
		t.Errorf("too-short series should return 0, got %f", corr)
	}
}

func TestDiversityCheck(t *testing.T) {
	// Highly correlated
	s1 := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	s2 := []float64{2, 4, 6, 8, 10, 12, 14, 16, 18, 20}
	if DiversityCheck([][]float64{s1, s2}, 0.6) {
		t.Error("highly correlated signals should fail diversity check")
	}

	// Uncorrelated
	s3 := []float64{1, -1, 1, -1, 1, -1, 1, -1, 1, -1}
	s4 := []float64{1, 1, -1, -1, 1, 1, -1, -1, 1, 1}
	if !DiversityCheck([][]float64{s3, s4}, 0.6) {
		t.Error("uncorrelated signals should pass diversity check")
	}
}

func TestPearsonCorrelation_Perfect(t *testing.T) {
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{2, 4, 6, 8, 10}
	corr := pearsonCorrelation(x, y)
	if math.Abs(corr-1.0) > 1e-10 {
		t.Errorf("expected perfect correlation, got %f", corr)
	}
}

func TestPearsonCorrelation_NegativePerfect(t *testing.T) {
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{10, 8, 6, 4, 2}
	corr := pearsonCorrelation(x, y)
	if math.Abs(corr-(-1.0)) > 1e-10 {
		t.Errorf("expected -1.0 correlation, got %f", corr)
	}
}

func TestPearsonCorrelation_Empty(t *testing.T) {
	corr := pearsonCorrelation(nil, nil)
	if corr != 0 {
		t.Errorf("expected 0 for empty, got %f", corr)
	}
}
