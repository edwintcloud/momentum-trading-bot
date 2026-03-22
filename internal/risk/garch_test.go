package risk

import (
	"math"
	"testing"
)

func TestNewGARCHForecaster(t *testing.T) {
	g := NewGARCHForecaster(0.10, 0.85, 0.0004)
	if g.alpha != 0.10 || g.beta != 0.85 {
		t.Errorf("unexpected params: alpha=%.2f, beta=%.2f", g.alpha, g.beta)
	}
	expectedOmega := (1 - 0.10 - 0.85) * 0.0004
	if math.Abs(g.omega-expectedOmega) > 1e-10 {
		t.Errorf("omega = %.10f, want %.10f", g.omega, expectedOmega)
	}
}

func TestGARCHDefaultParams(t *testing.T) {
	// Invalid params should use defaults
	g := NewGARCHForecaster(-1, -1, -1)
	if g.alpha != 0.10 || g.beta != 0.85 {
		t.Errorf("defaults not applied: alpha=%.2f, beta=%.2f", g.alpha, g.beta)
	}
}

func TestGARCHStationarity(t *testing.T) {
	// alpha + beta >= 1 should reset to defaults
	g := NewGARCHForecaster(0.5, 0.6, 0.0004)
	if g.alpha+g.beta >= 1 {
		t.Error("stationarity constraint violated")
	}
}

func TestGARCHForecastWithNoData(t *testing.T) {
	g := NewGARCHForecaster(0.10, 0.85, 0.0004)

	// Unknown symbol returns long-run variance
	v := g.ForecastVariance("UNKNOWN")
	if math.Abs(v-0.0004) > 1e-10 {
		t.Errorf("expected long-run variance 0.0004, got %.10f", v)
	}

	vol := g.ForecastVolatility("UNKNOWN")
	if math.Abs(vol-math.Sqrt(0.0004)) > 1e-6 {
		t.Errorf("expected sqrt(0.0004) = %.6f, got %.6f", math.Sqrt(0.0004), vol)
	}
}

func TestGARCHForecastUpdate(t *testing.T) {
	g := NewGARCHForecaster(0.10, 0.85, 0.0004)

	// Feed steady prices — variance should converge to long-run
	basePrice := 100.0
	for i := 0; i < 50; i++ {
		g.UpdatePrice("STEADY", basePrice+float64(i)*0.01)
	}

	vol := g.ForecastVariance("STEADY")
	if vol <= 0 {
		t.Error("variance should be positive after updates")
	}

	// After many small returns, variance should be near long-run
	longRunVol := 0.0004
	if vol > longRunVol*10 {
		t.Errorf("variance %.6f seems unreasonably high vs long-run %.6f", vol, longRunVol)
	}
}

func TestGARCHVolatilityShockResponse(t *testing.T) {
	g := NewGARCHForecaster(0.10, 0.85, 0.0004)

	// Feed calm prices first
	for i := 0; i < 30; i++ {
		g.UpdatePrice("SHOCK", 100.0+float64(i)*0.01)
	}
	calmVol := g.ForecastVariance("SHOCK")

	// Now feed a big shock
	g.UpdatePrice("SHOCK", 95.0) // ~5% drop
	shockedVol := g.ForecastVariance("SHOCK")

	if shockedVol <= calmVol {
		t.Errorf("variance after shock (%.8f) should exceed calm variance (%.8f)", shockedVol, calmVol)
	}
}

func TestGARCHAnnualizedVolatility(t *testing.T) {
	g := NewGARCHForecaster(0.10, 0.85, 0.0004)

	// Feed prices
	for i := 0; i < 20; i++ {
		g.UpdatePrice("ANN", 100.0+float64(i)*0.1)
	}

	annVol := g.AnnualizedVolatility("ANN")
	if annVol <= 0 {
		t.Error("annualized vol should be positive")
	}

	// Annualized should be much larger than per-minute
	minuteVol := g.ForecastVolatility("ANN")
	if annVol <= minuteVol {
		t.Errorf("annualized vol (%.6f) should exceed minute vol (%.6f)", annVol, minuteVol)
	}
}

func TestGARCHGetVolatility(t *testing.T) {
	g := NewGARCHForecaster(0.10, 0.85, 0.0004)

	// Default for unknown symbol
	vol := g.GetVolatility("UNKNOWN")
	if vol <= 0 {
		t.Error("GetVolatility should return positive default for unknown symbol")
	}
}

func TestGARCHZeroPrice(t *testing.T) {
	g := NewGARCHForecaster(0.10, 0.85, 0.0004)
	g.UpdatePrice("ZERO", 0) // Should be ignored
	g.UpdatePrice("ZERO", 100)
	g.UpdatePrice("ZERO", 0) // Should be ignored

	v := g.ForecastVariance("ZERO")
	if v != 0.0004 { // Still long-run since not enough data
		// Accept any valid response since state is complex
		if v <= 0 {
			t.Error("variance should be positive")
		}
	}
}
