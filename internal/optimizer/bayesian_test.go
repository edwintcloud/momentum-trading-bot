package optimizer

import (
	"math"
	"testing"
)

func TestNormalCDF(t *testing.T) {
	// CDF at 0 should be 0.5
	if math.Abs(NormalCDF(0)-0.5) > 1e-10 {
		t.Errorf("NormalCDF(0) = %f, want 0.5", NormalCDF(0))
	}

	// CDF at large positive should be ~1
	if NormalCDF(5) < 0.99 {
		t.Errorf("NormalCDF(5) = %f, want ~1.0", NormalCDF(5))
	}

	// CDF at large negative should be ~0
	if NormalCDF(-5) > 0.01 {
		t.Errorf("NormalCDF(-5) = %f, want ~0.0", NormalCDF(-5))
	}
}

func TestNormalPDF(t *testing.T) {
	// PDF at 0 should be ~0.3989
	expected := 1.0 / math.Sqrt(2*math.Pi)
	if math.Abs(NormalPDF(0)-expected) > 1e-10 {
		t.Errorf("NormalPDF(0) = %f, want %f", NormalPDF(0), expected)
	}
}

func TestExpectedImprovement(t *testing.T) {
	// When predMean > bestScore, EI should be positive
	ei := ExpectedImprovement(1.0, 0.5, 0.5, (1.0-0.5)/0.5)
	if ei <= 0 {
		t.Errorf("EI should be positive when predicted mean > best, got %f", ei)
	}

	// When predMean == bestScore, EI should still be positive (exploration term)
	ei2 := ExpectedImprovement(0.5, 0.5, 0.5, 0)
	if ei2 <= 0 {
		t.Errorf("EI should be positive at best due to exploration, got %f", ei2)
	}

	// Higher sigma should give more EI (more exploration potential)
	ei3 := ExpectedImprovement(0.5, 1.0, 0.5, 0)
	if ei3 <= ei2 {
		t.Errorf("higher sigma should give more EI: sigma=1.0 gave %f, sigma=0.5 gave %f", ei3, ei2)
	}
}

func TestBayesianOptimizerExploration(t *testing.T) {
	ranges := []ParameterRange{
		{Name: "x", Min: 0, Max: 10},
		{Name: "y", Min: 0, Max: 10},
	}

	bo := NewBayesianOptimizer(ranges, 5, 42)

	// First 5 samples should use random exploration
	for i := 0; i < 5; i++ {
		params := bo.SuggestNext()
		if len(params) != 2 {
			t.Fatalf("expected 2 params, got %d", len(params))
		}
		if params[0] < 0 || params[0] > 10 || params[1] < 0 || params[1] > 10 {
			t.Errorf("params out of range: %v", params)
		}
		// Simulate evaluation — quadratic with optimum near (5, 5)
		score := -(params[0]-5)*(params[0]-5) - (params[1]-5)*(params[1]-5)
		bo.AddEvaluation(params, score)
	}

	// After exploration, should use EI
	params := bo.SuggestNext()
	if len(params) != 2 {
		t.Fatalf("expected 2 params after exploration, got %d", len(params))
	}
}

func TestBayesianOptimizerBestObserved(t *testing.T) {
	ranges := []ParameterRange{
		{Name: "x", Min: 0, Max: 10},
	}
	bo := NewBayesianOptimizer(ranges, 5, 42)

	// No evaluations — should return -Inf
	best := bo.BestObserved()
	if !math.IsInf(best, -1) {
		t.Errorf("expected -Inf with no evaluations, got %f", best)
	}

	bo.AddEvaluation([]float64{1.0}, 5.0)
	bo.AddEvaluation([]float64{2.0}, 10.0)
	bo.AddEvaluation([]float64{3.0}, 3.0)

	if bo.BestObserved() != 10.0 {
		t.Errorf("expected best=10.0, got %f", bo.BestObserved())
	}
}

func TestNormalizedDistance(t *testing.T) {
	ranges := []ParameterRange{
		{Name: "x", Min: 0, Max: 10},
		{Name: "y", Min: 0, Max: 20},
	}

	// Same point should be distance 0
	d := normalizedDistance([]float64{5, 10}, []float64{5, 10}, ranges)
	if d != 0 {
		t.Errorf("distance to self should be 0, got %f", d)
	}

	// Opposite corners
	d2 := normalizedDistance([]float64{0, 0}, []float64{10, 20}, ranges)
	expected := math.Sqrt(2.0) // both dimensions are 1.0 in normalized space
	if math.Abs(d2-expected) > 1e-10 {
		t.Errorf("distance across normalized space = %f, want %f", d2, expected)
	}
}

func TestPearsonCorrelation(t *testing.T) {
	// Perfect positive correlation
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{2, 4, 6, 8, 10}
	corr := PearsonCorrelation(x, y)
	if math.Abs(corr-1.0) > 1e-10 {
		t.Errorf("perfect positive correlation: got %f, want 1.0", corr)
	}

	// Perfect negative correlation
	y2 := []float64{10, 8, 6, 4, 2}
	corr2 := PearsonCorrelation(x, y2)
	if math.Abs(corr2-(-1.0)) > 1e-10 {
		t.Errorf("perfect negative correlation: got %f, want -1.0", corr2)
	}

	// Mismatched lengths should return 0
	corr3 := PearsonCorrelation([]float64{1, 2}, []float64{1})
	if corr3 != 0 {
		t.Errorf("mismatched lengths should return 0, got %f", corr3)
	}
}
