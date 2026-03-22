package regime

import (
	"math"
	"testing"
)

func TestGaussianPDF(t *testing.T) {
	// Standard normal at x=0 should be ~0.3989
	val := gaussianPDF(0, 0, 1)
	expected := 1.0 / math.Sqrt(2*math.Pi)
	if math.Abs(val-expected) > 1e-6 {
		t.Errorf("gaussianPDF(0,0,1) = %f, want %f", val, expected)
	}

	// Zero stddev should return 0
	if gaussianPDF(0, 0, 0) != 0 {
		t.Error("gaussianPDF with zero stddev should return 0")
	}

	// Negative stddev should return 0
	if gaussianPDF(0, 0, -1) != 0 {
		t.Error("gaussianPDF with negative stddev should return 0")
	}
}

func TestNewHMMRegimeDetector(t *testing.T) {
	h := NewHMMRegimeDetector()
	if h.numStates != 2 {
		t.Errorf("expected 2 states, got %d", h.numStates)
	}
	if len(h.forwardProbs) != 2 {
		t.Errorf("expected 2 forward probs, got %d", len(h.forwardProbs))
	}
	if math.Abs(h.forwardProbs[0]+h.forwardProbs[1]-1.0) > 1e-10 {
		t.Error("forward probs should sum to 1")
	}
}

func TestHMMForwardAlgorithm(t *testing.T) {
	h := NewHMMRegimeDetector()

	// Feed positive returns — should push toward bullish
	for i := 0; i < 50; i++ {
		h.Update(0.002) // +20bps per bar (strongly positive)
	}

	regime, conf := h.CurrentRegime()
	if regime != "bullish" {
		t.Errorf("expected bullish regime after positive returns, got %s", regime)
	}
	if conf < 0.7 {
		t.Errorf("expected confidence > 0.7, got %f", conf)
	}

	// Reset and feed strongly negative returns — should push toward bearish
	h.Reset()
	for i := 0; i < 200; i++ {
		h.Update(-0.02) // strongly negative, well into bear emission territory
	}

	regime, conf = h.CurrentRegime()
	if regime != "bearish" {
		t.Errorf("expected bearish regime after negative returns, got %s (conf=%f)", regime, conf)
	}
	if conf < 0.5 {
		t.Errorf("expected confidence > 0.5, got %f", conf)
	}
}

func TestHMMProbabilitiesSumToOne(t *testing.T) {
	h := NewHMMRegimeDetector()

	// After each update, probabilities should still sum to ~1
	observations := []float64{0.001, -0.002, 0.003, -0.001, 0.0005, -0.004}
	for _, obs := range observations {
		h.Update(obs)
		probs := h.ForwardProbs()
		sum := 0.0
		for _, p := range probs {
			sum += p
		}
		if math.Abs(sum-1.0) > 1e-10 {
			t.Errorf("forward probs sum to %f after observation %f, want 1.0", sum, obs)
		}
	}
}

func TestHMMReturnHistoryCap(t *testing.T) {
	h := NewHMMRegimeDetector()

	// Feed more than 1000 observations
	for i := 0; i < 1100; i++ {
		h.Update(0.001)
	}

	h.mu.RLock()
	histLen := len(h.returnHistory)
	h.mu.RUnlock()

	if histLen > 1000 {
		t.Errorf("return history should be capped at 1000, got %d", histLen)
	}
}
