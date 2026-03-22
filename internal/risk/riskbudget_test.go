package risk

import (
	"math"
	"testing"
)

func TestVolScalar(t *testing.T) {
	tests := []struct {
		name       string
		targetVol  float64
		realizedVol float64
		expected   float64
	}{
		{"equal vol", 0.10, 0.10, 1.0},
		{"half vol", 0.10, 0.05, 2.0},       // capped at 2.0
		{"double vol", 0.10, 0.20, 0.50},
		{"very high vol", 0.10, 0.50, 0.25},  // floored at 0.25
		{"very low vol", 0.10, 0.03, 2.0},    // capped at 2.0
		{"zero target", 0, 0.10, 1.0},
		{"zero realized", 0.10, 0, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VolScalar(tt.targetVol, tt.realizedVol)
			if math.Abs(got-tt.expected) > 0.01 {
				t.Errorf("VolScalar(%.2f, %.2f) = %.4f, want %.4f", tt.targetVol, tt.realizedVol, got, tt.expected)
			}
		})
	}
}

func TestVolTargetedPositionSize(t *testing.T) {
	// Budget $1000, volScalar 1.0, stock vol 0.30, price $50
	// size = (1000 * 1.0) / (0.30 * 50) = 66.67 → 66 shares
	size := VolTargetedPositionSize(1000, 1.0, 0.30, 50)
	if size != 66 {
		t.Errorf("VolTargetedPositionSize = %d, want 66", size)
	}

	// With volScalar 2.0 (low vol environment)
	size2 := VolTargetedPositionSize(1000, 2.0, 0.30, 50)
	if size2 != 133 {
		t.Errorf("VolTargetedPositionSize with scalar 2.0 = %d, want 133", size2)
	}

	// Edge cases
	if VolTargetedPositionSize(0, 1.0, 0.30, 50) != 0 {
		t.Error("zero budget should return 0")
	}
	if VolTargetedPositionSize(1000, 1.0, 0, 50) != 0 {
		t.Error("zero vol should return 0")
	}
	if VolTargetedPositionSize(1000, 1.0, 0.30, 0) != 0 {
		t.Error("zero price should return 0")
	}
}

func TestRiskBudgetManagerIntradayVol(t *testing.T) {
	rb := NewRiskBudgetManager(0.10, 0.01)

	// No returns
	if rb.IntradayRealizedVol(30) != 0 {
		t.Error("should return 0 with no data")
	}

	// Add returns
	for i := 0; i < 40; i++ {
		rb.AddReturn((float64(i) - 20) / 1000.0)
	}

	vol := rb.IntradayRealizedVol(30)
	if vol <= 0 {
		t.Error("intraday vol should be positive with data")
	}
}

func TestRiskBudgetManagerBarRiskLimit(t *testing.T) {
	rb := NewRiskBudgetManager(0.10, 0.01)

	// Full day: daily_budget = 100000 * 0.01 = 1000, remaining_ratio = 390/390 = 1.0
	limit := rb.BarRiskLimit(100000, 390, 390)
	if math.Abs(limit-1000) > 0.01 {
		t.Errorf("full day bar risk limit = %.2f, want 1000", limit)
	}

	// Half day: remaining_ratio = 195/390 = 0.5
	limit2 := rb.BarRiskLimit(100000, 195, 390)
	if math.Abs(limit2-500) > 0.01 {
		t.Errorf("half day bar risk limit = %.2f, want 500", limit2)
	}

	// Near close: remaining_ratio = 10/390 ≈ 0.0256
	limit3 := rb.BarRiskLimit(100000, 10, 390)
	expected := 1000.0 * 10.0 / 390.0
	if math.Abs(limit3-expected) > 0.1 {
		t.Errorf("near close bar risk limit = %.2f, want %.2f", limit3, expected)
	}

	// Edge cases
	if rb.BarRiskLimit(0, 390, 390) != 0 {
		t.Error("zero account should return 0")
	}
	if rb.BarRiskLimit(100000, 0, 390) != 0 {
		t.Error("zero remaining should return 0")
	}
}

func TestRiskBudgetManagerMaxPosition(t *testing.T) {
	rb := NewRiskBudgetManager(0.10, 0.01)

	// Add returns to have intraday vol
	for i := 0; i < 40; i++ {
		rb.AddReturn((float64(i) - 20) / 1000.0)
	}

	intradayVol := rb.IntradayRealizedVol(30)
	if intradayVol <= 0 {
		t.Fatal("need positive intraday vol for this test")
	}

	maxPos := rb.MaxPositionFromBudget(100000, 390, 390, intradayVol, 50)
	if maxPos <= 0 {
		t.Error("max position should be positive with valid inputs")
	}

	// With half the time remaining, max position should be smaller
	maxPosHalf := rb.MaxPositionFromBudget(100000, 195, 390, intradayVol, 50)
	if maxPosHalf >= maxPos {
		t.Errorf("half day position (%d) should be <= full day position (%d)", maxPosHalf, maxPos)
	}

	// Edge cases
	if rb.MaxPositionFromBudget(100000, 390, 390, 0, 50) != 0 {
		t.Error("zero vol should return 0")
	}
	if rb.MaxPositionFromBudget(100000, 390, 390, intradayVol, 0) != 0 {
		t.Error("zero price should return 0")
	}
}

func TestRiskBudgetManagerResetDay(t *testing.T) {
	rb := NewRiskBudgetManager(0.10, 0.01)

	for i := 0; i < 50; i++ {
		rb.AddReturn((float64(i) - 25) / 1000.0) // varying returns
	}
	if rb.IntradayRealizedVol(30) == 0 {
		t.Error("should have non-zero vol before reset")
	}

	rb.ResetDay()
	if rb.IntradayRealizedVol(30) != 0 {
		t.Error("should have zero vol after reset")
	}
}

func TestRiskBudgetManagerMaxReturns(t *testing.T) {
	rb := NewRiskBudgetManager(0.10, 0.01)

	// Add more than maxReturns (390)
	for i := 0; i < 500; i++ {
		rb.AddReturn(float64(i) * 0.001)
	}

	// Should have capped at 390
	rb.mu.RLock()
	n := len(rb.intradayReturns)
	rb.mu.RUnlock()
	if n != 390 {
		t.Errorf("expected 390 returns, got %d", n)
	}
}
