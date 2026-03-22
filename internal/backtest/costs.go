package backtest

import "math"

// TransactionCosts computes all execution costs for a trade.
type TransactionCosts struct {
	Commission float64 `json:"commission"`
	SECFee     float64 `json:"secFee"`
	TAFFee     float64 `json:"tafFee"`
	SpreadCost float64 `json:"spreadCost"`
	TotalCost  float64 `json:"totalCost"`
}

// ComputeTransactionCosts calculates commission, SEC fee, TAF fee, and spread cost.
func ComputeTransactionCosts(price float64, quantity int, side string, spreadBps float64, commissionPerShare float64) TransactionCosts {
	notional := price * float64(quantity)
	shares := float64(quantity)

	tc := TransactionCosts{}

	// Commission per share
	tc.Commission = shares * commissionPerShare

	// SEC fee: sell side only, $27.80 per $1,000,000
	if side == "sell" {
		tc.SECFee = notional * 27.80 / 1_000_000
	}

	// TAF fee: sell side only, $0.000166 per share, capped at $8.30
	if side == "sell" {
		taf := shares * 0.000166
		tc.TAFFee = math.Min(taf, 8.30)
	}

	// Spread cost: half the bid-ask spread
	if spreadBps > 0 {
		tc.SpreadCost = notional * (spreadBps / 10000.0) / 2
	}

	tc.TotalCost = tc.Commission + tc.SECFee + tc.TAFFee + tc.SpreadCost
	return tc
}
