package execution

import (
	"math"
	"testing"
)

func TestEstimateImpact_Basic(t *testing.T) {
	params := AlmgrenChrissParams{
		ADV:        1000000,
		Volatility: 0.02,
		TempImpact: 0.1,
		PermImpact: 0.01,
	}

	// 10,000 shares of a $50 stock against 1M ADV
	impact := EstimateImpact(10000, 50.0, params)
	if impact <= 0 {
		t.Errorf("expected positive impact, got %f", impact)
	}
	if impact > 0.01 {
		t.Errorf("impact for 1%% of ADV should be modest, got %f", impact)
	}
}

func TestEstimateImpact_ZeroInputs(t *testing.T) {
	params := DefaultImpactParams(1000000, 0.02)

	if EstimateImpact(0, 50.0, params) != 0 {
		t.Error("zero shares should give zero impact")
	}
	if EstimateImpact(100, 0, params) != 0 {
		t.Error("zero price should give zero impact")
	}
	if EstimateImpact(100, 50.0, AlmgrenChrissParams{}) != 0 {
		t.Error("zero ADV should give zero impact")
	}
}

func TestEstimateImpact_ScalesWithSize(t *testing.T) {
	params := DefaultImpactParams(1000000, 0.02)

	impactSmall := EstimateImpact(1000, 50.0, params)
	impactLarge := EstimateImpact(100000, 50.0, params)

	if impactLarge <= impactSmall {
		t.Errorf("larger order should have more impact: small=%f, large=%f", impactSmall, impactLarge)
	}
}

func TestEstimateImpactDollars(t *testing.T) {
	params := DefaultImpactParams(1000000, 0.02)
	impactPct := EstimateImpact(10000, 50.0, params)
	impactDollars := EstimateImpactDollars(10000, 50.0, params)

	expected := impactPct * 50.0 * 10000
	if math.Abs(impactDollars-expected) > 1e-6 {
		t.Errorf("EstimateImpactDollars = %f, want %f", impactDollars, expected)
	}
}

func TestDefaultImpactParams(t *testing.T) {
	p := DefaultImpactParams(500000, 0.03)
	if p.ADV != 500000 {
		t.Errorf("ADV = %f, want 500000", p.ADV)
	}
	if p.Volatility != 0.03 {
		t.Errorf("Volatility = %f, want 0.03", p.Volatility)
	}
	if p.TempImpact <= 0 || p.PermImpact <= 0 {
		t.Error("default params should have positive impact coefficients")
	}
}

func TestFindMaxQtyWithinImpact(t *testing.T) {
	params := DefaultImpactParams(1000000, 0.02)

	// 50bps max impact
	maxQty := FindMaxQtyWithinImpact(50.0, params, 0.005)
	if maxQty <= 0 {
		t.Error("should find some acceptable quantity")
	}

	// Verify the returned quantity is within impact budget
	impact := EstimateImpact(maxQty, 50.0, params)
	if impact > 0.005+1e-6 {
		t.Errorf("maxQty=%d has impact %f > 0.005", maxQty, impact)
	}

	// maxQty+1 should exceed the budget (or be at ADV limit)
	if maxQty < int(params.ADV) {
		impactNext := EstimateImpact(maxQty+1, 50.0, params)
		if impactNext < 0.005-1e-6 {
			t.Errorf("maxQty+1=%d has impact %f still under budget", maxQty+1, impactNext)
		}
	}
}

func TestFindMaxQtyWithinImpact_ZeroBudget(t *testing.T) {
	params := DefaultImpactParams(1000000, 0.02)
	maxQty := FindMaxQtyWithinImpact(50.0, params, 0)
	if maxQty != 0 {
		t.Errorf("zero impact budget should return 0, got %d", maxQty)
	}
}
