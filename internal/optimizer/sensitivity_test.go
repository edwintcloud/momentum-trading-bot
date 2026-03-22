package optimizer

import (
	"math"
	"math/rand"
	"testing"
)

func newTestRNG() *rand.Rand {
	return rand.New(rand.NewSource(42))
}

func TestPearsonCorrelation_PerfectPositive(t *testing.T) {
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{2, 4, 6, 8, 10}

	corr := PearsonCorrelation(x, y)

	if math.Abs(corr-1.0) > 0.001 {
		t.Errorf("expected correlation 1.0 for perfect positive, got %f", corr)
	}
}

func TestPearsonCorrelation_PerfectNegative(t *testing.T) {
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{10, 8, 6, 4, 2}

	corr := PearsonCorrelation(x, y)

	if math.Abs(corr-(-1.0)) > 0.001 {
		t.Errorf("expected correlation -1.0, got %f", corr)
	}
}

func TestPearsonCorrelation_Uncorrelated(t *testing.T) {
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{5, 1, 3, 5, 1}

	corr := PearsonCorrelation(x, y)

	// Not perfectly zero but should be low
	if math.Abs(corr) > 0.5 {
		t.Errorf("expected low correlation for uncorrelated data, got %f", corr)
	}
}

func TestPearsonCorrelation_TooFew(t *testing.T) {
	x := []float64{1}
	y := []float64{2}

	corr := PearsonCorrelation(x, y)
	if corr != 0 {
		t.Errorf("expected 0 for too few points, got %f", corr)
	}
}

func TestPearsonCorrelation_MismatchedLength(t *testing.T) {
	x := []float64{1, 2}
	y := []float64{1, 2, 3}

	corr := PearsonCorrelation(x, y)
	if corr != 0 {
		t.Errorf("expected 0 for mismatched lengths, got %f", corr)
	}
}

func TestComputeSensitivity_DominantParameter(t *testing.T) {
	// Create evaluations where param 0 determines the score
	evals := make([]Evaluation, 100)
	for i := range evals {
		p0 := float64(i) / 100.0
		p1 := float64(99-i) / 100.0 // inversely correlated noise
		evals[i] = Evaluation{
			ParamValues: []float64{p0, p1},
			Score:       p0 * 2, // score is purely a function of p0
		}
	}

	result := ComputeSensitivity(evals, []string{"dominant", "noise"})

	if len(result.Parameters) != 2 {
		t.Fatalf("expected 2 parameters, got %d", len(result.Parameters))
	}

	// dominant param should have high first-order index
	if result.Parameters[0].FirstOrderIdx < 0.9 {
		t.Errorf("expected dominant param first-order > 0.9, got %f", result.Parameters[0].FirstOrderIdx)
	}
}

func TestComputeSensitivity_TooFewEvals(t *testing.T) {
	evals := make([]Evaluation, 10)
	for i := range evals {
		evals[i] = Evaluation{ParamValues: []float64{float64(i)}, Score: float64(i)}
	}

	result := ComputeSensitivity(evals, []string{"param"})
	if len(result.Parameters) != 0 {
		t.Errorf("expected empty result for too few evaluations, got %d params", len(result.Parameters))
	}
}

func TestLatinHypercubeSample(t *testing.T) {
	params := []ParameterRange{
		{Name: "a", Min: 0, Max: 10},
		{Name: "b", Min: -1, Max: 1},
	}

	rng := newTestRNG()
	samples := LatinHypercubeSample(params, 100, rng)

	if len(samples) != 100 {
		t.Fatalf("expected 100 samples, got %d", len(samples))
	}

	// Verify all values are within bounds
	for i, s := range samples {
		if s[0] < 0 || s[0] > 10 {
			t.Errorf("sample %d param 0 out of bounds: %f", i, s[0])
		}
		if s[1] < -1 || s[1] > 1 {
			t.Errorf("sample %d param 1 out of bounds: %f", i, s[1])
		}
	}

	// Verify LHS property: each interval [k/N, (k+1)/N] has exactly one sample
	// Test for first parameter
	bins := make([]int, 100)
	for _, s := range samples {
		binIdx := int(s[0] / 10.0 * 100)
		if binIdx >= 100 {
			binIdx = 99
		}
		bins[binIdx]++
	}

	// Each bin should have exactly 1 sample (LHS property)
	for i, count := range bins {
		if count != 1 {
			t.Errorf("LHS bin %d has %d samples, expected 1", i, count)
		}
	}
}
