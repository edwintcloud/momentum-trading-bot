package execution

import "math"

// AlmgrenChrissParams holds market impact parameters for a security.
type AlmgrenChrissParams struct {
	ADV        float64 // average daily volume
	Volatility float64 // daily volatility (sigma)
	TempImpact float64 // temporary impact coefficient (eta)
	PermImpact float64 // permanent impact coefficient (gamma)
}

// EstimateImpact estimates execution cost for a given order size.
// Returns estimated impact as a fraction of notional (e.g. 0.005 = 50bps).
func EstimateImpact(orderShares int, price float64, params AlmgrenChrissParams) float64 {
	if params.ADV <= 0 || orderShares <= 0 || price <= 0 {
		return 0
	}

	participationRate := float64(orderShares) / params.ADV

	// Temporary impact: eta * sigma * (shares/ADV)^0.6  (square-root model)
	tempImpact := params.TempImpact * params.Volatility * math.Pow(participationRate, 0.6)

	// Permanent impact: gamma * sigma * (shares/ADV)
	permImpact := params.PermImpact * params.Volatility * participationRate

	return tempImpact + permImpact
}

// EstimateImpactDollars returns estimated cost in dollars.
func EstimateImpactDollars(orderShares int, price float64, params AlmgrenChrissParams) float64 {
	impactPct := EstimateImpact(orderShares, price, params)
	return impactPct * price * float64(orderShares)
}

// DefaultImpactParams returns sensible defaults for a stock.
func DefaultImpactParams(adv float64, dailyVol float64) AlmgrenChrissParams {
	return AlmgrenChrissParams{
		ADV:        adv,
		Volatility: dailyVol,
		TempImpact: 0.1,
		PermImpact: 0.01,
	}
}

// FindMaxQtyWithinImpact binary-searches for the maximum order size
// whose estimated impact is at or below maxImpactPct.
func FindMaxQtyWithinImpact(price float64, params AlmgrenChrissParams, maxImpactPct float64) int {
	if maxImpactPct <= 0 {
		return 0
	}

	lo, hi := 1, int(params.ADV)
	if hi <= 0 {
		return 0
	}

	best := 0
	for lo <= hi {
		mid := (lo + hi) / 2
		impact := EstimateImpact(mid, price, params)
		if impact <= maxImpactPct {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}
