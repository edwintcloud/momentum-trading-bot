package backtest

import (
	"math"
	"testing"
)

func TestBonferroniCorrection_KnownValues(t *testing.T) {
	// 5 p-values at alpha=0.05: adjusted = p * 5
	pValues := []float64{0.001, 0.01, 0.03, 0.04, 0.20}
	result := BonferroniCorrection(pValues, 0.05)

	if result.Method != MHTBonferroni {
		t.Fatalf("expected method Bonferroni, got %s", result.Method)
	}
	if result.NTrials != 5 {
		t.Fatalf("expected 5 trials, got %d", result.NTrials)
	}

	// Expected adjusted: 0.005, 0.05, 0.15, 0.20, 1.0
	expected := []float64{0.005, 0.05, 0.15, 0.20, 1.0}
	for i, exp := range expected {
		if math.Abs(result.AdjustedPValues[i]-exp) > 1e-10 {
			t.Errorf("adjusted[%d]: expected %.4f, got %.4f", i, exp, result.AdjustedPValues[i])
		}
	}

	// Only p=0.001 (adj=0.005) should be rejected at alpha=0.05
	expectedRejected := []bool{true, false, false, false, false}
	for i, exp := range expectedRejected {
		if result.Rejected[i] != exp {
			t.Errorf("rejected[%d]: expected %v, got %v", i, exp, result.Rejected[i])
		}
	}
	if result.SignificantCount != 1 {
		t.Errorf("expected 1 significant, got %d", result.SignificantCount)
	}
}

func TestBonferroniCorrection_AllSignificant(t *testing.T) {
	pValues := []float64{0.001, 0.002, 0.003}
	result := BonferroniCorrection(pValues, 0.05)

	// adj: 0.003, 0.006, 0.009 — all < 0.05
	for i, r := range result.Rejected {
		if !r {
			t.Errorf("expected all rejected, but index %d was not", i)
		}
	}
	if result.SignificantCount != 3 {
		t.Errorf("expected 3 significant, got %d", result.SignificantCount)
	}
}

func TestBonferroniCorrection_NoneSignificant(t *testing.T) {
	pValues := []float64{0.10, 0.20, 0.50, 0.80}
	result := BonferroniCorrection(pValues, 0.05)

	for i, r := range result.Rejected {
		if r {
			t.Errorf("expected none rejected, but index %d was", i)
		}
	}
	if result.SignificantCount != 0 {
		t.Errorf("expected 0 significant, got %d", result.SignificantCount)
	}
}

func TestBonferroniCorrection_SingleTest(t *testing.T) {
	pValues := []float64{0.03}
	result := BonferroniCorrection(pValues, 0.05)

	// With 1 test, adjusted = p * 1 = 0.03, which is < 0.05
	if math.Abs(result.AdjustedPValues[0]-0.03) > 1e-10 {
		t.Errorf("expected adjusted p=0.03, got %.4f", result.AdjustedPValues[0])
	}
	if !result.Rejected[0] {
		t.Error("expected single p=0.03 to be rejected at alpha=0.05")
	}
}

func TestBonferroniCorrection_AdjustedCappedAtOne(t *testing.T) {
	pValues := []float64{0.60, 0.80}
	result := BonferroniCorrection(pValues, 0.05)

	// adj: min(0.60*2, 1.0)=1.0, min(0.80*2, 1.0)=1.0
	for i, adj := range result.AdjustedPValues {
		if adj != 1.0 {
			t.Errorf("adjusted[%d]: expected 1.0, got %.4f", i, adj)
		}
	}
}

func TestBonferroniCorrection_Empty(t *testing.T) {
	result := BonferroniCorrection(nil, 0.05)
	if result.NTrials != 0 {
		t.Errorf("expected 0 trials for empty input, got %d", result.NTrials)
	}
	if result.SignificantCount != 0 {
		t.Errorf("expected 0 significant for empty input, got %d", result.SignificantCount)
	}
}

func TestBenjaminiHochbergCorrection_KnownValues(t *testing.T) {
	// Classic BH example from Benjamini & Hochberg (1995)
	pValues := []float64{0.001, 0.008, 0.039, 0.041, 0.042, 0.06, 0.074, 0.205, 0.212, 0.216}
	result := BenjaminiHochbergCorrection(pValues, 0.05)

	if result.Method != MHTBenjaminiHochberg {
		t.Fatalf("expected method BenjaminiHochberg, got %s", result.Method)
	}
	if result.NTrials != 10 {
		t.Fatalf("expected 10 trials, got %d", result.NTrials)
	}

	// BH thresholds: rank/10 * 0.05
	// rank 1: 0.001 ≤ 0.005 → reject
	// rank 2: 0.008 ≤ 0.010 → reject
	// rank 3: 0.039 ≤ 0.015 → NO
	// rank 4: 0.041 ≤ 0.020 → NO
	// rank 5: 0.042 ≤ 0.025 → NO
	// ...
	// BH: reject all up to and including the largest k where p_(k) ≤ k/m * alpha
	// Here k=2 is largest, so reject indices 0,1 (first two p-values)
	if result.SignificantCount != 2 {
		t.Errorf("expected 2 significant, got %d", result.SignificantCount)
	}
	if !result.Rejected[0] || !result.Rejected[1] {
		t.Error("expected first two p-values to be rejected")
	}
	for i := 2; i < 10; i++ {
		if result.Rejected[i] {
			t.Errorf("expected p-value at index %d not to be rejected", i)
		}
	}
}

func TestBenjaminiHochbergCorrection_AllSignificant(t *testing.T) {
	pValues := []float64{0.001, 0.002, 0.003}
	result := BenjaminiHochbergCorrection(pValues, 0.05)

	for i, r := range result.Rejected {
		if !r {
			t.Errorf("expected all rejected, but index %d was not", i)
		}
	}
	if result.SignificantCount != 3 {
		t.Errorf("expected 3 significant, got %d", result.SignificantCount)
	}
}

func TestBenjaminiHochbergCorrection_NoneSignificant(t *testing.T) {
	pValues := []float64{0.30, 0.50, 0.80}
	result := BenjaminiHochbergCorrection(pValues, 0.05)

	for i, r := range result.Rejected {
		if r {
			t.Errorf("expected none rejected, but index %d was", i)
		}
	}
	if result.SignificantCount != 0 {
		t.Errorf("expected 0 significant, got %d", result.SignificantCount)
	}
}

func TestBenjaminiHochbergCorrection_SingleTest(t *testing.T) {
	// Single test: BH reduces to raw comparison
	pValues := []float64{0.03}
	result := BenjaminiHochbergCorrection(pValues, 0.05)

	if !result.Rejected[0] {
		t.Error("expected single p=0.03 to be rejected at alpha=0.05")
	}
	if math.Abs(result.AdjustedPValues[0]-0.03) > 1e-10 {
		t.Errorf("expected adjusted p=0.03 for single test, got %.4f", result.AdjustedPValues[0])
	}
}

func TestBenjaminiHochbergCorrection_Empty(t *testing.T) {
	result := BenjaminiHochbergCorrection(nil, 0.05)
	if result.NTrials != 0 {
		t.Errorf("expected 0 trials for empty input, got %d", result.NTrials)
	}
}

func TestBenjaminiHochbergCorrection_UnsortedInput(t *testing.T) {
	// Verify that unsorted input is handled correctly
	pValues := []float64{0.20, 0.001, 0.05, 0.01}
	result := BenjaminiHochbergCorrection(pValues, 0.05)

	// Sorted: 0.001(idx1), 0.01(idx3), 0.05(idx2), 0.20(idx0)
	// BH thresholds: k/4 * 0.05 = 0.0125, 0.025, 0.0375, 0.05
	// rank 1: 0.001 ≤ 0.0125 → reject
	// rank 2: 0.01 ≤ 0.025 → reject
	// rank 3: 0.05 ≤ 0.0375 → NO
	// rank 4: 0.20 ≤ 0.05 → NO
	// Reject first two sorted (original indices 1 and 3)
	if !result.Rejected[1] {
		t.Error("expected index 1 (p=0.001) to be rejected")
	}
	if !result.Rejected[3] {
		t.Error("expected index 3 (p=0.01) to be rejected")
	}
	if result.Rejected[0] {
		t.Error("expected index 0 (p=0.20) not to be rejected")
	}
	if result.Rejected[2] {
		t.Error("expected index 2 (p=0.05) not to be rejected")
	}
	if result.SignificantCount != 2 {
		t.Errorf("expected 2 significant, got %d", result.SignificantCount)
	}
}

func TestBHAdjustedPValues_Monotone(t *testing.T) {
	// BH-adjusted p-values should be monotonically non-decreasing
	// when viewed in sorted-p order.
	pValues := []float64{0.001, 0.01, 0.04, 0.05, 0.50}
	result := BenjaminiHochbergCorrection(pValues, 0.05)

	// Check adjusted p-values are non-decreasing when sorted
	type indexedAdj struct {
		origP float64
		adjP  float64
	}
	pairs := make([]indexedAdj, len(pValues))
	for i := range pValues {
		pairs[i] = indexedAdj{pValues[i], result.AdjustedPValues[i]}
	}
	// Sort by original p-value
	for i := 0; i < len(pairs)-1; i++ {
		for j := i + 1; j < len(pairs); j++ {
			if pairs[i].origP > pairs[j].origP {
				pairs[i], pairs[j] = pairs[j], pairs[i]
			}
		}
	}
	for i := 0; i < len(pairs)-1; i++ {
		if pairs[i].adjP > pairs[i+1].adjP+1e-10 {
			t.Errorf("BH adjusted p-values not monotone: adj[%d]=%.4f > adj[%d]=%.4f",
				i, pairs[i].adjP, i+1, pairs[i+1].adjP)
		}
	}
}

func TestApplyMHTCorrection_MethodRouting(t *testing.T) {
	pValues := []float64{0.01, 0.04}

	bonf := ApplyMHTCorrection(pValues, 0.05, MHTBonferroni)
	if bonf.Method != MHTBonferroni {
		t.Errorf("expected Bonferroni, got %s", bonf.Method)
	}

	bh := ApplyMHTCorrection(pValues, 0.05, MHTBenjaminiHochberg)
	if bh.Method != MHTBenjaminiHochberg {
		t.Errorf("expected BenjaminiHochberg, got %s", bh.Method)
	}

	none := ApplyMHTCorrection(pValues, 0.05, MHTNone)
	if none.Method != MHTNone {
		t.Errorf("expected None, got %s", none.Method)
	}
	// With no correction, both should be significant (0.01 < 0.05, 0.04 < 0.05)
	if none.SignificantCount != 2 {
		t.Errorf("expected 2 significant with no correction, got %d", none.SignificantCount)
	}
}

func TestApplyMHTCorrection_BHLessConservativeThanBonferroni(t *testing.T) {
	// BH should reject at least as many hypotheses as Bonferroni
	pValues := []float64{0.001, 0.008, 0.020, 0.04, 0.06, 0.10}

	bonf := ApplyMHTCorrection(pValues, 0.05, MHTBonferroni)
	bh := ApplyMHTCorrection(pValues, 0.05, MHTBenjaminiHochberg)

	if bh.SignificantCount < bonf.SignificantCount {
		t.Errorf("BH (%d) should reject >= Bonferroni (%d)", bh.SignificantCount, bonf.SignificantCount)
	}
}

func TestTrialCounter(t *testing.T) {
	tc := NewTrialCounter()
	if tc.Count() != 0 {
		t.Fatalf("expected initial count 0, got %d", tc.Count())
	}

	tc.Inc()
	if tc.Count() != 1 {
		t.Fatalf("expected count 1 after Inc, got %d", tc.Count())
	}

	tc.Add(49)
	if tc.Count() != 50 {
		t.Fatalf("expected count 50 after Add(49), got %d", tc.Count())
	}
}

func TestSharpeRatioPValue_ZeroSR(t *testing.T) {
	p := SharpeRatioPValue(0, 252, 0, 3)
	if p != 1.0 {
		t.Errorf("expected p=1.0 for zero SR, got %.4f", p)
	}
}

func TestSharpeRatioPValue_HighSR(t *testing.T) {
	// High SR with many observations should give small p-value
	p := SharpeRatioPValue(3.0, 252, 0, 3)
	if p > 0.01 {
		t.Errorf("expected very small p-value for SR=3.0, got %.4f", p)
	}
}

func TestSharpeRatioPValue_LowSR(t *testing.T) {
	// Low SR should give large p-value
	p := SharpeRatioPValue(0.1, 50, 0, 3)
	if p < 0.3 {
		t.Errorf("expected large p-value for SR=0.1 with n=50, got %.4f", p)
	}
}

func TestSharpeRatioPValue_FewReturns(t *testing.T) {
	p := SharpeRatioPValue(2.0, 3, 0, 3)
	if p != 1.0 {
		t.Errorf("expected p=1.0 for too few returns, got %.4f", p)
	}
}

func TestSharpeRatioPValue_SymmetricInSign(t *testing.T) {
	// p-value should be the same for +SR and -SR (two-sided)
	pPos := SharpeRatioPValue(1.5, 252, 0, 3)
	pNeg := SharpeRatioPValue(-1.5, 252, 0, 3)
	if math.Abs(pPos-pNeg) > 1e-10 {
		t.Errorf("expected symmetric p-values, got pos=%.6f, neg=%.6f", pPos, pNeg)
	}
}

func TestMHTIntegrationWithDSR(t *testing.T) {
	// End-to-end: compute Sharpe p-values for multiple strategies,
	// then apply MHT correction.
	strategies := []struct {
		sr       float64
		n        int
		skew     float64
		kurtosis float64
	}{
		{2.0, 252, -0.3, 4.0},  // good strategy
		{0.5, 252, 0.0, 3.0},   // mediocre but n is large
		{1.5, 252, -0.5, 5.0},  // decent
		{0.05, 30, 0.0, 3.0},   // weak: very low SR with few observations
		{3.0, 252, 0.0, 3.0},   // strong
	}

	pValues := make([]float64, len(strategies))
	for i, s := range strategies {
		pValues[i] = SharpeRatioPValue(s.sr, s.n, s.skew, s.kurtosis)
	}

	// Without correction, check that the strong strategy has a small p-value
	if pValues[4] > 0.01 {
		t.Errorf("expected strong strategy (SR=3.0) to have small p-value, got %.4f", pValues[4])
	}

	// Apply BH correction
	bh := ApplyMHTCorrection(pValues, 0.05, MHTBenjaminiHochberg)

	// Strong strategies should still be significant after BH
	if !bh.Rejected[0] {
		t.Error("expected SR=2.0 strategy to remain significant after BH correction")
	}
	if !bh.Rejected[4] {
		t.Error("expected SR=3.0 strategy to remain significant after BH correction")
	}

	// Weak strategy should not be significant
	if bh.Rejected[3] {
		t.Error("expected SR=0.2 strategy to NOT be significant after BH correction")
	}

	// Apply Bonferroni — should be more conservative
	bonf := ApplyMHTCorrection(pValues, 0.05, MHTBonferroni)
	if bonf.SignificantCount > bh.SignificantCount {
		t.Errorf("Bonferroni (%d significant) should not exceed BH (%d significant)",
			bonf.SignificantCount, bh.SignificantCount)
	}
}
