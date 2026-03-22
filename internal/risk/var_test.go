package risk

import (
	"math"
	"testing"
)

func TestInverseNormalCDF(t *testing.T) {
	// Known z-scores for common confidence levels
	tests := []struct {
		p        float64
		expected float64
		tol      float64
	}{
		{0.50, 0.0, 1e-6},
		{0.95, 1.6449, 1e-3},
		{0.99, 2.3263, 1e-3},
		{0.975, 1.9600, 1e-3},
		{0.025, -1.9600, 1e-3},
		{0.05, -1.6449, 1e-3},
	}
	for _, tt := range tests {
		got := inverseNormalCDF(tt.p)
		if math.Abs(got-tt.expected) > tt.tol {
			t.Errorf("inverseNormalCDF(%.3f) = %.4f, want %.4f", tt.p, got, tt.expected)
		}
	}
}

func TestNormalPDF(t *testing.T) {
	// PDF at z=0 should be 1/sqrt(2*pi) ≈ 0.3989
	got := normalPDF(0)
	if math.Abs(got-0.3989) > 0.001 {
		t.Errorf("normalPDF(0) = %.4f, want ~0.3989", got)
	}
}

func TestMeanStdDev(t *testing.T) {
	data := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	mu, sigma := meanStdDev(data)
	if math.Abs(mu-5.0) > 0.001 {
		t.Errorf("mean = %.4f, want 5.0", mu)
	}
	if math.Abs(sigma-2.0) > 0.01 {
		t.Errorf("stddev = %.4f, want 2.0", sigma)
	}

	// Empty data
	mu0, sigma0 := meanStdDev(nil)
	if mu0 != 0 || sigma0 != 0 {
		t.Errorf("empty data: got mu=%.4f, sigma=%.4f", mu0, sigma0)
	}
}

func TestParametricVaR(t *testing.T) {
	// Standard normal returns: mean=0, std=0.01 (1% daily vol)
	// VaR at 95% = 0 - (-1.645 * 0.01) = 0.01645
	returns := make([]float64, 1000)
	// Construct returns with known mean=0 and known distribution properties
	// Use uniform spacing to approximate normal with known std
	for i := range returns {
		returns[i] = (float64(i)/1000.0 - 0.5) * 0.02 // range [-0.01, 0.01]
	}
	mu, sigma := meanStdDev(returns)

	var_ := parametricVaR(returns, 0.95)
	expectedVaR := -(mu - inverseNormalCDF(0.95)*sigma)
	if math.Abs(var_-expectedVaR) > 0.001 {
		t.Errorf("parametricVaR = %.6f, expected %.6f", var_, expectedVaR)
	}
}

func TestHistoricalVaR(t *testing.T) {
	// 100 returns: -0.05, -0.04, ..., 0.04
	returns := make([]float64, 100)
	for i := range returns {
		returns[i] = (float64(i) - 50) / 1000.0 // [-0.05, 0.049]
	}

	// At 95% confidence, VaR should be around the 5th percentile
	// 5th percentile: returns[5] = (5-50)/1000 = -0.045
	var_ := historicalVaR(returns, 0.95)
	if var_ <= 0 {
		t.Error("historical VaR should be positive")
	}
	// Should be approximately 0.045
	if math.Abs(var_-0.045) > 0.005 {
		t.Errorf("historicalVaR = %.4f, expected ~0.045", var_)
	}
}

func TestHistoricalCVaR(t *testing.T) {
	returns := make([]float64, 100)
	for i := range returns {
		returns[i] = (float64(i) - 50) / 1000.0 // [-0.05, 0.049]
	}

	cvar := historicalCVaR(returns, 0.95)
	if cvar <= 0 {
		t.Error("historical CVaR should be positive")
	}
	// CVaR should be >= VaR (expected shortfall is worse than threshold)
	var_ := historicalVaR(returns, 0.95)
	if cvar < var_ {
		t.Errorf("CVaR (%.4f) should be >= VaR (%.4f)", cvar, var_)
	}
}

func TestParametricCVaR(t *testing.T) {
	returns := make([]float64, 100)
	for i := range returns {
		returns[i] = (float64(i) - 50) / 1000.0
	}

	cvar := parametricCVaR(returns, 0.95)
	var_ := parametricVaR(returns, 0.95)
	if cvar < var_ {
		t.Errorf("parametric CVaR (%.4f) should be >= VaR (%.4f)", cvar, var_)
	}
}

func TestVaRCalculator(t *testing.T) {
	vc := NewVaRCalculator(0.95, "parametric", 200)

	// No data yet
	if vc.VaR() != 0 {
		t.Error("VaR with no data should be 0")
	}
	if vc.CVaR() != 0 {
		t.Error("CVaR with no data should be 0")
	}

	// Add returns
	for i := 0; i < 100; i++ {
		vc.AddReturn((float64(i) - 50) / 1000.0)
	}

	var_ := vc.VaR()
	if var_ <= 0 {
		t.Error("VaR should be positive with sufficient data")
	}

	cvar := vc.CVaR()
	if cvar <= 0 {
		t.Error("CVaR should be positive with sufficient data")
	}
	if cvar < var_ {
		t.Errorf("CVaR (%.4f) should be >= VaR (%.4f)", cvar, var_)
	}
}

func TestVaRCalculatorHistorical(t *testing.T) {
	vc := NewVaRCalculator(0.95, "historical", 200)

	for i := 0; i < 100; i++ {
		vc.AddReturn((float64(i) - 50) / 1000.0)
	}

	var_ := vc.VaR()
	if var_ <= 0 {
		t.Error("historical VaR should be positive")
	}

	cvar := vc.CVaR()
	if cvar < var_ {
		t.Errorf("historical CVaR (%.4f) should be >= VaR (%.4f)", cvar, var_)
	}
}

func TestIntraDayVaR(t *testing.T) {
	vc := NewVaRCalculator(0.95, "parametric", 390)

	for i := 0; i < 60; i++ {
		vc.AddReturn((float64(i) - 30) / 1000.0)
	}

	intradayVaR := vc.IntraDayVaR(60)
	minuteVaR := vc.VaR()

	// Intraday VaR (scaled) should be larger than single-minute VaR
	if intradayVaR <= minuteVaR {
		t.Errorf("intraday VaR (%.6f) should exceed minute VaR (%.6f)", intradayVaR, minuteVaR)
	}
}

func TestExceedsDailyLimit(t *testing.T) {
	vc := NewVaRCalculator(0.95, "parametric", 390)

	// With no data, should not exceed
	if vc.ExceedsDailyLimit(100000, 0.02) {
		t.Error("should not exceed limit with no data")
	}

	// Feed large negative returns to create high VaR
	for i := 0; i < 100; i++ {
		vc.AddReturn(-0.05) // 5% loss each minute
	}

	// With extremely negative returns, should exceed
	if !vc.ExceedsDailyLimit(100000, 0.02) {
		t.Error("should exceed limit with extreme negative returns")
	}

	// Zero/negative account should never exceed
	if vc.ExceedsDailyLimit(0, 0.02) {
		t.Error("should not exceed limit with zero account")
	}
}

func TestCVaRPositionSize(t *testing.T) {
	// Budget $1000, CVaR per unit $10 → 100 shares
	size := CVaRPositionSize(1000, 10)
	if size != 100 {
		t.Errorf("CVaRPositionSize(1000, 10) = %d, want 100", size)
	}

	// Edge cases
	if CVaRPositionSize(0, 10) != 0 {
		t.Error("zero budget should return 0")
	}
	if CVaRPositionSize(1000, 0) != 0 {
		t.Error("zero CVaR per unit should return 0")
	}
	if CVaRPositionSize(-100, 10) != 0 {
		t.Error("negative budget should return 0")
	}
}

func TestVaRCalculatorReturns(t *testing.T) {
	vc := NewVaRCalculator(0.95, "parametric", 10)
	for i := 0; i < 15; i++ {
		vc.AddReturn(float64(i) * 0.01)
	}
	// Should have kept only the last 10
	ret := vc.Returns()
	if len(ret) != 10 {
		t.Errorf("expected 10 returns, got %d", len(ret))
	}
}
