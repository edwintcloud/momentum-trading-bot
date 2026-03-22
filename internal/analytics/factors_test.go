package analytics

import (
	"math"
	"testing"
)

func TestDecomposeReturns_KnownLinearRelationship(t *testing.T) {
	n := 100
	stratReturns := make([]float64, n)
	mktReturns := make([]float64, n)
	momReturns := make([]float64, n)
	sizeReturns := make([]float64, n)

	// Generate: stratReturn = 0.001 + 1.5*mkt + 0.5*mom + 0.2*size
	for i := 0; i < n; i++ {
		mktReturns[i] = float64(i-50) * 0.001
		momReturns[i] = float64(i%20-10) * 0.0005
		sizeReturns[i] = float64(i%30-15) * 0.0003
		stratReturns[i] = 0.001 + 1.5*mktReturns[i] + 0.5*momReturns[i] + 0.2*sizeReturns[i]
	}

	decomp := DecomposeReturns(stratReturns, mktReturns, momReturns, sizeReturns)

	if math.Abs(decomp.Alpha-0.001) > 1e-6 {
		t.Errorf("alpha = %f, want ~0.001", decomp.Alpha)
	}
	if math.Abs(decomp.BetaMarket-1.5) > 1e-6 {
		t.Errorf("beta_market = %f, want ~1.5", decomp.BetaMarket)
	}
	if math.Abs(decomp.BetaMomentum-0.5) > 1e-6 {
		t.Errorf("beta_momentum = %f, want ~0.5", decomp.BetaMomentum)
	}
	if math.Abs(decomp.BetaSize-0.2) > 1e-6 {
		t.Errorf("beta_size = %f, want ~0.2", decomp.BetaSize)
	}
	if decomp.RSquared < 0.99 {
		t.Errorf("R^2 = %f, want ~1.0 for perfect linear data", decomp.RSquared)
	}
}

func TestDecomposeReturns_TooFewObservations(t *testing.T) {
	short := make([]float64, 10)
	decomp := DecomposeReturns(short, short, short, short)
	if decomp.Alpha != 0 && decomp.BetaMarket != 0 {
		t.Error("expected zero decomposition for too few observations")
	}
}

func TestDecomposeReturns_MismatchedLengths(t *testing.T) {
	a := make([]float64, 30)
	b := make([]float64, 25)
	decomp := DecomposeReturns(a, b, a, a)
	if decomp.RSquared != 0 {
		t.Error("expected zero decomposition for mismatched lengths")
	}
}

func TestSolveLinearSystem4_Identity(t *testing.T) {
	// Solve Ix = b where I is identity
	A := [4][4]float64{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
	}
	b := [4]float64{1, 2, 3, 4}
	x := solveLinearSystem4(A, b)
	if x == nil {
		t.Fatal("expected non-nil solution")
	}
	for i := 0; i < 4; i++ {
		if math.Abs(x[i]-b[i]) > 1e-10 {
			t.Errorf("x[%d] = %f, want %f", i, x[i], b[i])
		}
	}
}

func TestSolveLinearSystem4_Singular(t *testing.T) {
	// Singular matrix (row of zeros)
	A := [4][4]float64{
		{1, 0, 0, 0},
		{0, 0, 0, 0},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
	}
	b := [4]float64{1, 2, 3, 4}
	x := solveLinearSystem4(A, b)
	if x != nil {
		t.Error("expected nil for singular matrix")
	}
}
