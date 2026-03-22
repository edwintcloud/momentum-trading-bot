package ml

import (
	"math"
	"testing"
)

func TestFracDiffWeights_BasicProperties(t *testing.T) {
	weights := FracDiffWeights(0.4, 100, 1e-5)
	if len(weights) == 0 {
		t.Fatal("expected non-empty weights")
	}
	// First weight is always 1.0
	if weights[0] != 1.0 {
		t.Errorf("first weight should be 1.0, got %f", weights[0])
	}
	// Weights should decay in magnitude
	for i := 1; i < len(weights); i++ {
		if math.Abs(weights[i]) > math.Abs(weights[i-1]) {
			t.Errorf("weight %d (%.6f) has larger magnitude than weight %d (%.6f)",
				i, weights[i], i-1, weights[i-1])
		}
	}
}

func TestFracDiffWeights_D0IsNoOp(t *testing.T) {
	weights := FracDiffWeights(0.0, 100, 1e-5)
	// d=0 means w_1 = -w_0 * (0-1+1)/1 = 0, so only w_0=1
	if len(weights) != 1 {
		t.Errorf("d=0 should produce only one weight, got %d", len(weights))
	}
}

func TestFracDiffWeights_D1IsFirstDiff(t *testing.T) {
	weights := FracDiffWeights(1.0, 10, 1e-10)
	// d=1 → w_0=1, w_1=-1, w_2=0, ...
	if len(weights) < 2 {
		t.Fatal("expected at least 2 weights for d=1")
	}
	if math.Abs(weights[0]-1.0) > 1e-10 {
		t.Errorf("w_0 should be 1.0, got %f", weights[0])
	}
	if math.Abs(weights[1]-(-1.0)) > 1e-10 {
		t.Errorf("w_1 should be -1.0, got %f", weights[1])
	}
}

func TestFracDiff_ShortSeries(t *testing.T) {
	result := FracDiff(nil, 0.4, 1e-5)
	if result != nil {
		t.Error("expected nil for empty series")
	}
}

func TestFracDiff_PriceSeriesProducesOutput(t *testing.T) {
	// Synthetic trending price series
	series := make([]float64, 100)
	for i := range series {
		series[i] = 100 + float64(i)*0.5
	}

	result := FracDiff(series, 0.4, 1e-5)
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
	// Result should be shorter than input (by weight length - 1)
	if len(result) >= len(series) {
		t.Error("result should be shorter than input")
	}
}

func TestFracDiff_D0PreservesOriginal(t *testing.T) {
	series := []float64{10, 20, 30, 40, 50}
	result := FracDiff(series, 0.0, 1e-10)
	// d=0 with single weight [1] just returns the original series
	if len(result) != len(series) {
		t.Errorf("d=0 should preserve length, got %d vs %d", len(result), len(series))
	}
	for i, v := range result {
		if math.Abs(v-series[i]) > 1e-10 {
			t.Errorf("d=0 value[%d] should match original: got %f, want %f", i, v, series[i])
		}
	}
}

func TestVarianceRatio_StationarySeries(t *testing.T) {
	// White noise-like series should have VR near 1.0
	series := make([]float64, 200)
	for i := range series {
		// Simple mean-reverting: alternate around 100
		series[i] = 100 + float64(i%2)*0.1
	}
	vr := VarianceRatio(series, 5)
	// For a near-constant series, VR should be defined
	if vr < 0 {
		t.Errorf("variance ratio should be non-negative, got %f", vr)
	}
}

func TestVarianceRatio_TooShort(t *testing.T) {
	vr := VarianceRatio([]float64{1, 2, 3}, 5)
	if vr != 0 {
		t.Errorf("expected 0 for too-short series, got %f", vr)
	}
}

func TestFindMinD_ReturnsInRange(t *testing.T) {
	series := make([]float64, 200)
	for i := range series {
		series[i] = 100 + float64(i)*0.3 + math.Sin(float64(i)/10)*2
	}

	d := FindMinD(series, 0.1, 0.8, 0.05)
	if d < 0.1 || d > 0.8 {
		t.Errorf("d should be in [0.1, 0.8], got %f", d)
	}
}

func TestFindMinD_ShortSeries(t *testing.T) {
	d := FindMinD([]float64{1, 2, 3}, 0.3, 0.5, 0.05)
	if d != 0.3 {
		t.Errorf("short series should return minD, got %f", d)
	}
}
