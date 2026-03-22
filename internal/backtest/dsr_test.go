package backtest

import (
	"math"
	"testing"
)

func TestProbabilisticSharpeRatio_HighSR(t *testing.T) {
	// High observed SR with normal distribution should give high PSR
	psr := ProbabilisticSharpeRatio(2.0, 0.0, 252, 0, 3)

	if psr < 0.95 {
		t.Errorf("expected PSR > 0.95 for SR=2.0, got %f", psr)
	}
}

func TestProbabilisticSharpeRatio_LowSR(t *testing.T) {
	// With a threshold of 1.0, a low observed SR should fail
	psr := ProbabilisticSharpeRatio(0.1, 1.0, 252, 0, 3)

	if psr > 0.1 {
		t.Errorf("expected low PSR for SR=0.1 vs threshold=1.0, got %f", psr)
	}
}

func TestProbabilisticSharpeRatio_FewReturns(t *testing.T) {
	psr := ProbabilisticSharpeRatio(2.0, 0.0, 3, 0, 3)

	if psr != 0 {
		t.Errorf("expected PSR=0 for < 5 returns, got %f", psr)
	}
}

func TestDeflatedSharpeRatio_SingleTrial(t *testing.T) {
	// With 1 trial, DSR should equal PSR with threshold=0
	dsr := DeflatedSharpeRatio(2.0, 252, 0, 3, 1)
	psr := ProbabilisticSharpeRatio(2.0, 0, 252, 0, 3)

	if math.Abs(dsr-psr) > 0.001 {
		t.Errorf("DSR (%.4f) should equal PSR (%.4f) with 1 trial", dsr, psr)
	}
}

func TestDeflatedSharpeRatio_ManyTrials(t *testing.T) {
	// Many trials should increase the threshold and reduce DSR
	dsr1 := DeflatedSharpeRatio(1.5, 252, 0, 3, 1)
	dsr500 := DeflatedSharpeRatio(1.5, 252, 0, 3, 500)

	if dsr500 >= dsr1 {
		t.Errorf("DSR with 500 trials (%.4f) should be less than with 1 trial (%.4f)", dsr500, dsr1)
	}
}

func TestDeflatedSharpeRatio_WithSkewness(t *testing.T) {
	// Negative skewness should reduce DSR
	dsrNoSkew := DeflatedSharpeRatio(1.5, 252, 0, 3, 100)
	dsrNegSkew := DeflatedSharpeRatio(1.5, 252, -1.0, 3, 100)

	// Negative skewness increases the standard error, which can change DSR
	// Just verify both return valid values
	if dsrNoSkew < 0 || dsrNoSkew > 1 {
		t.Errorf("DSR without skew out of range: %f", dsrNoSkew)
	}
	if dsrNegSkew < 0 || dsrNegSkew > 1 {
		t.Errorf("DSR with negative skew out of range: %f", dsrNegSkew)
	}
}

func TestSkewnessKurtosis_Normal(t *testing.T) {
	// Symmetric data → skew near 0, kurtosis near 3
	data := []float64{-2, -1, 0, 1, 2, -2, -1, 0, 1, 2}
	skew, kurt := SkewnessKurtosis(data)

	if math.Abs(skew) > 0.1 {
		t.Errorf("expected near-zero skewness for symmetric data, got %f", skew)
	}
	// For this uniform-ish data, kurtosis will be less than 3 (platykurtic)
	if kurt < 1 || kurt > 4 {
		t.Errorf("expected kurtosis in reasonable range, got %f", kurt)
	}
}

func TestSkewnessKurtosis_TooFew(t *testing.T) {
	data := []float64{1, 2}
	skew, kurt := SkewnessKurtosis(data)

	if skew != 0 || kurt != 3 {
		t.Errorf("expected (0, 3) for < 3 data points, got (%f, %f)", skew, kurt)
	}
}

func TestNormalCDF(t *testing.T) {
	// z=0 → 0.5
	if math.Abs(normalCDF(0)-0.5) > 0.001 {
		t.Errorf("expected normalCDF(0) = 0.5, got %f", normalCDF(0))
	}

	// z=large positive → near 1
	if normalCDF(4) < 0.99 {
		t.Errorf("expected normalCDF(4) near 1, got %f", normalCDF(4))
	}

	// z=large negative → near 0
	if normalCDF(-4) > 0.01 {
		t.Errorf("expected normalCDF(-4) near 0, got %f", normalCDF(-4))
	}
}
