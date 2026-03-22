package backtest

import (
	"math"
	"testing"
)

func TestComputeTransactionCosts_Buy(t *testing.T) {
	tc := ComputeTransactionCosts(50.0, 100, "buy", 10.0, 0.005)

	// Commission: 100 * 0.005 = $0.50
	if math.Abs(tc.Commission-0.50) > 0.01 {
		t.Errorf("expected commission $0.50, got $%.4f", tc.Commission)
	}

	// No SEC fee on buy
	if tc.SECFee != 0 {
		t.Errorf("expected no SEC fee on buy, got $%.4f", tc.SECFee)
	}

	// No TAF fee on buy
	if tc.TAFFee != 0 {
		t.Errorf("expected no TAF fee on buy, got $%.4f", tc.TAFFee)
	}

	// Spread: $50 * 100 * 10/10000 / 2 = $2.50
	if math.Abs(tc.SpreadCost-2.50) > 0.01 {
		t.Errorf("expected spread cost $2.50, got $%.4f", tc.SpreadCost)
	}

	// Total = 0.50 + 0 + 0 + 2.50 = $3.00
	if math.Abs(tc.TotalCost-3.00) > 0.01 {
		t.Errorf("expected total cost $3.00, got $%.4f", tc.TotalCost)
	}
}

func TestComputeTransactionCosts_Sell(t *testing.T) {
	tc := ComputeTransactionCosts(100.0, 200, "sell", 10.0, 0.005)

	// Commission: 200 * 0.005 = $1.00
	if math.Abs(tc.Commission-1.00) > 0.01 {
		t.Errorf("expected commission $1.00, got $%.4f", tc.Commission)
	}

	// SEC fee: $20,000 * 27.80 / 1,000,000 = $0.556
	expectedSEC := 20000.0 * 27.80 / 1_000_000
	if math.Abs(tc.SECFee-expectedSEC) > 0.01 {
		t.Errorf("expected SEC fee $%.4f, got $%.4f", expectedSEC, tc.SECFee)
	}

	// TAF fee: 200 * 0.000166 = $0.0332
	expectedTAF := 200.0 * 0.000166
	if math.Abs(tc.TAFFee-expectedTAF) > 0.001 {
		t.Errorf("expected TAF fee $%.4f, got $%.4f", expectedTAF, tc.TAFFee)
	}

	// Spread: $100 * 200 * 10/10000 / 2 = $10.00
	if math.Abs(tc.SpreadCost-10.00) > 0.01 {
		t.Errorf("expected spread cost $10.00, got $%.4f", tc.SpreadCost)
	}
}

func TestComputeTransactionCosts_TAFCap(t *testing.T) {
	// Very large order to trigger TAF cap
	tc := ComputeTransactionCosts(10.0, 100000, "sell", 0, 0)

	// TAF: 100000 * 0.000166 = $16.60, capped at $8.30
	if math.Abs(tc.TAFFee-8.30) > 0.01 {
		t.Errorf("expected TAF fee capped at $8.30, got $%.4f", tc.TAFFee)
	}
}

func TestComputeTransactionCosts_ZeroSpread(t *testing.T) {
	tc := ComputeTransactionCosts(50.0, 100, "buy", 0, 0.005)

	if tc.SpreadCost != 0 {
		t.Errorf("expected zero spread cost, got $%.4f", tc.SpreadCost)
	}
}
