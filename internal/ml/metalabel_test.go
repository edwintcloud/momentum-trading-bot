package ml

import (
	"testing"
	"time"
)

func TestTripleBarrierLabeling_ProfitTarget(t *testing.T) {
	// Price rises to hit upper barrier
	bars := []Bar{
		{Timestamp: time.Now(), Close: 100, High: 100, Low: 100},
		{Timestamp: time.Now(), Close: 101, High: 102, Low: 100.5},
		{Timestamp: time.Now(), Close: 103, High: 106, Low: 102}, // High hits 106 = 6% above entry
	}

	label := TripleBarrierLabeling(bars, 0, 0.05, 0.03, 10)
	if label.Label != 1 {
		t.Errorf("expected label +1 (profit), got %d", label.Label)
	}
	if label.Duration != 2 {
		t.Errorf("expected duration 2, got %d", label.Duration)
	}
}

func TestTripleBarrierLabeling_StopLoss(t *testing.T) {
	// Price drops to hit lower barrier
	bars := []Bar{
		{Timestamp: time.Now(), Close: 100, High: 100, Low: 100},
		{Timestamp: time.Now(), Close: 99, High: 100, Low: 99},
		{Timestamp: time.Now(), Close: 96, High: 97, Low: 95}, // Low hits 95 = 5% below entry
	}

	label := TripleBarrierLabeling(bars, 0, 0.10, 0.05, 10)
	if label.Label != -1 {
		t.Errorf("expected label -1 (loss), got %d", label.Label)
	}
}

func TestTripleBarrierLabeling_TimeBarrier(t *testing.T) {
	// Price stays flat — hits time barrier
	bars := make([]Bar, 12)
	for i := range bars {
		bars[i] = Bar{
			Timestamp: time.Now().Add(time.Duration(i) * time.Minute),
			Close:     100,
			High:      100.5,
			Low:       99.5,
		}
	}

	label := TripleBarrierLabeling(bars, 0, 0.10, 0.10, 10)
	if label.Label != 0 {
		t.Errorf("expected label 0 (timeout), got %d", label.Label)
	}
	if label.Duration != 10 {
		t.Errorf("expected duration 10, got %d", label.Duration)
	}
}

func TestTripleBarrierLabeling_InvalidEntry(t *testing.T) {
	bars := []Bar{
		{Close: 100, High: 100, Low: 100},
	}

	// Entry index out of bounds
	label := TripleBarrierLabeling(bars, 5, 0.05, 0.03, 10)
	if label.Label != 0 && label.Duration != 0 {
		t.Error("expected empty label for invalid entry index")
	}

	// Negative entry index
	label2 := TripleBarrierLabeling(bars, -1, 0.05, 0.03, 10)
	if label2.Label != 0 {
		t.Error("expected empty label for negative entry index")
	}
}

func TestMetaLabelSizing_LowProbability(t *testing.T) {
	// Below threshold — should skip trade
	qty := MetaLabelSizing(0.30, 100, 0.40)
	if qty != 0 {
		t.Errorf("expected 0 quantity for low probability, got %d", qty)
	}
}

func TestMetaLabelSizing_HighProbability(t *testing.T) {
	// High probability — should scale up
	qty := MetaLabelSizing(0.80, 100, 0.40)
	if qty <= 100 {
		t.Errorf("expected scaled-up quantity for high probability, got %d", qty)
	}
	if qty > 150 {
		t.Errorf("expected max 1.5x scaling, got %d", qty)
	}
}

func TestMetaLabelSizing_AtThreshold(t *testing.T) {
	// At threshold — should get minimum scaling (0.5x)
	qty := MetaLabelSizing(0.40, 100, 0.40)
	if qty != 50 {
		t.Errorf("at threshold should get 0.5x: expected 50, got %d", qty)
	}
}

func TestMetaLabelSizing_MidProbability(t *testing.T) {
	qty := MetaLabelSizing(0.60, 100, 0.40)
	if qty < 50 || qty > 150 {
		t.Errorf("expected quantity between 50-150, got %d", qty)
	}
}

func TestStubScorer(t *testing.T) {
	scorer := NewStubScorer()
	if scorer.Enabled() {
		t.Error("stub scorer should not be enabled")
	}
	score, err := scorer.Score(ScorerFeatures{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if score != 0.5 {
		t.Errorf("stub scorer should return 0.5, got %f", score)
	}
}

func TestScorerFeaturesToSlice(t *testing.T) {
	f := ScorerFeatures{
		RelativeVolume: 3.0,
		GapPercent:     5.0,
		VolumeRate:     2.0,
	}
	slice := f.ToSlice()
	if len(slice) != 17 {
		t.Errorf("expected 17 features, got %d", len(slice))
	}
	if slice[0] != 3.0 {
		t.Errorf("expected RelativeVolume=3.0, got %f", slice[0])
	}
}
