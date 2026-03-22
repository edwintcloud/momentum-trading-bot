package ml

import (
	"math"
	"testing"
)

func TestComputePSI_IdenticalDistributions(t *testing.T) {
	dist := []float64{0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1}
	psi := ComputePSI(dist, dist)
	if math.Abs(psi) > 1e-10 {
		t.Errorf("identical distributions should have PSI=0, got %f", psi)
	}
}

func TestComputePSI_DriftedDistribution(t *testing.T) {
	train := []float64{0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10}
	live := []float64{0.20, 0.20, 0.10, 0.05, 0.05, 0.05, 0.05, 0.10, 0.10, 0.10}

	psi := ComputePSI(train, live)
	if psi <= 0 {
		t.Errorf("drifted distributions should have positive PSI, got %f", psi)
	}
}

func TestComputePSI_MajorDrift(t *testing.T) {
	train := []float64{0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10, 0.10}
	live := []float64{0.50, 0.30, 0.10, 0.02, 0.02, 0.02, 0.01, 0.01, 0.01, 0.01}

	psi := ComputePSI(train, live)
	if psi < 0.2 {
		t.Errorf("major drift should have PSI > 0.2, got %f", psi)
	}
}

func TestComputePSI_EmptyDistributions(t *testing.T) {
	psi := ComputePSI(nil, nil)
	if psi != 0 {
		t.Errorf("empty distributions should return 0, got %f", psi)
	}
}

func TestBinIntoDeciles_UniformData(t *testing.T) {
	// Values uniformly spread from 0 to 10
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(i) / 10.0
	}

	bins := BinIntoDeciles(values, 0, 10)
	if len(bins) != 10 {
		t.Fatalf("expected 10 bins, got %d", len(bins))
	}

	// Each bin should have roughly equal proportion
	for i, b := range bins {
		if b <= 0 {
			t.Errorf("bin %d has non-positive proportion: %f", i, b)
		}
	}
}

func TestBinIntoDeciles_EmptyValues(t *testing.T) {
	bins := BinIntoDeciles(nil, 0, 10)
	if len(bins) != 10 {
		t.Fatal("should return 10 bins even for empty input")
	}
	// Should be uniform fallback
	for _, b := range bins {
		if math.Abs(b-0.1) > 1e-10 {
			t.Errorf("empty input should give uniform bins, got %f", b)
		}
	}
}

func TestDriftDetector_CheckPSI(t *testing.T) {
	trainDist := []float64{0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1}
	dd := NewDriftDetector(trainDist, 0.95)

	// Same distribution — no drift
	psi, drifted := dd.CheckPSI(trainDist, 0.2)
	if drifted {
		t.Error("identical distribution should not trigger drift")
	}
	if psi > 0.01 {
		t.Errorf("expected near-zero PSI, got %f", psi)
	}

	// Shifted distribution — drift
	liveDist := []float64{0.50, 0.20, 0.10, 0.05, 0.03, 0.03, 0.03, 0.03, 0.02, 0.01}
	psi2, drifted2 := dd.CheckPSI(liveDist, 0.2)
	if !drifted2 {
		t.Errorf("shifted distribution should trigger drift, PSI=%f", psi2)
	}
}

func TestDriftDetector_EWMA_Accuracy(t *testing.T) {
	dd := NewDriftDetector(nil, 0.9)

	// Record several correct predictions
	for i := 0; i < 10; i++ {
		dd.UpdateAccuracy(1.0)
	}
	if dd.Accuracy() < 0.9 {
		t.Errorf("expected high accuracy after correct predictions, got %f", dd.Accuracy())
	}

	// Record several incorrect
	for i := 0; i < 20; i++ {
		dd.UpdateAccuracy(0.0)
	}
	if dd.Accuracy() > 0.5 {
		t.Errorf("expected lower accuracy after incorrect predictions, got %f", dd.Accuracy())
	}
}

func TestDriftDetector_RollingSharpe(t *testing.T) {
	dd := NewDriftDetector(nil, 0.95)

	// Not enough data
	if dd.RollingSharpe() != 0 {
		t.Error("expected 0 Sharpe with no data")
	}

	// Add positive returns
	for i := 0; i < 100; i++ {
		dd.RecordReturn(0.01)
	}
	sharpe := dd.RollingSharpe()
	// With constant returns, stddev → 0, but we guard against that
	// All same value → stddev = 0 → Sharpe = 0
	if math.IsNaN(sharpe) || math.IsInf(sharpe, 0) {
		t.Error("Sharpe should not be NaN or Inf")
	}
}

func TestDriftDetector_RollingSharpe_MixedReturns(t *testing.T) {
	dd := NewDriftDetector(nil, 0.95)

	// Add mixed positive returns
	for i := 0; i < 100; i++ {
		r := 0.01 + float64(i%3)*0.005
		dd.RecordReturn(r)
	}
	sharpe := dd.RollingSharpe()
	if sharpe <= 0 {
		t.Errorf("expected positive Sharpe for positive mean returns, got %f", sharpe)
	}
}

func TestDriftDetector_PerformanceDrift(t *testing.T) {
	dd := NewDriftDetector(nil, 0.95)

	// Not enough trades — should not flag drift
	for i := 0; i < 5; i++ {
		dd.RecordReturn(-0.01)
	}
	if dd.CheckPerformanceDrift(0.5) {
		t.Error("should not flag drift with too few trades")
	}

	// Add many negative returns
	for i := 0; i < 100; i++ {
		dd.RecordReturn(-0.02 + float64(i%5)*0.001)
	}
	if !dd.CheckPerformanceDrift(0.5) {
		t.Error("should flag drift for consistently negative returns")
	}
}

func TestConfidenceMultiplier(t *testing.T) {
	if ConfidenceMultiplier(0.1, 0.2) != 1.0 {
		t.Error("below threshold should return 1.0")
	}
	if ConfidenceMultiplier(0.3, 0.2) != 0.5 {
		t.Error("above threshold should return 0.5")
	}
}

func TestDriftDetector_MaxReturnsBuffer(t *testing.T) {
	dd := NewDriftDetector(nil, 0.95)

	// Add more than maxReturns
	for i := 0; i < 600; i++ {
		dd.RecordReturn(0.01)
	}
	if len(dd.recentReturns) > dd.maxReturns {
		t.Errorf("buffer should be capped at %d, got %d", dd.maxReturns, len(dd.recentReturns))
	}
}
