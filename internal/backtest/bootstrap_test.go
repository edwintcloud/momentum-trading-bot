package backtest

import (
	"testing"
)

func TestBootstrapSignificance_ClearlyProfitable(t *testing.T) {
	// All positive returns → should reject H0 (low p-value)
	returns := make([]float64, 100)
	for i := range returns {
		returns[i] = 0.01 + float64(i)*0.001 // all positive
	}

	pValue, ciLower, ciUpper := BootstrapSignificance(returns, 5000)

	if pValue >= 0.05 {
		t.Errorf("expected significant p-value for clearly profitable trades, got %f", pValue)
	}
	if ciLower <= 0 {
		t.Errorf("expected CI lower > 0 for profitable trades, got %f", ciLower)
	}
	if ciUpper <= ciLower {
		t.Errorf("expected CI upper > lower, got lower=%f upper=%f", ciLower, ciUpper)
	}
}

func TestBootstrapSignificance_ClearlyUnprofitable(t *testing.T) {
	// All negative returns → should reject H0 but CI is below zero
	returns := make([]float64, 100)
	for i := range returns {
		returns[i] = -0.02
	}

	pValue, _, ciUpper := BootstrapSignificance(returns, 5000)

	// With constant values, the p-value should reflect significance
	if pValue >= 0.05 {
		t.Errorf("expected significant p-value for consistently negative returns, got %f", pValue)
	}
	if ciUpper >= 0 {
		t.Errorf("expected CI upper < 0 for clearly unprofitable trades, got %f", ciUpper)
	}
}

func TestBootstrapSignificance_TooFewTrades(t *testing.T) {
	returns := []float64{0.01, 0.02, 0.03}
	pValue, _, _ := BootstrapSignificance(returns, 1000)

	if pValue != 1.0 {
		t.Errorf("expected p-value 1.0 for too few trades, got %f", pValue)
	}
}

func TestBootstrapSignificance_NoisyReturns(t *testing.T) {
	// Zero-mean noisy returns → should NOT reject H0
	returns := make([]float64, 100)
	for i := range returns {
		if i%2 == 0 {
			returns[i] = 0.01
		} else {
			returns[i] = -0.01
		}
	}

	pValue, _, _ := BootstrapSignificance(returns, 5000)

	// Mean is 0, so we should NOT reject H0
	if pValue < 0.05 {
		t.Errorf("expected non-significant p-value for zero-mean returns, got %f", pValue)
	}
}
