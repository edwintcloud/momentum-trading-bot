package portfolio

import (
	"math"
)

// FactorNeutralInput holds the inputs for factor-neutral portfolio construction.
type FactorNeutralInput struct {
	Symbols           []string           // ordered list of asset symbols
	Weights           map[string]float64 // current portfolio weights
	Betas             map[string]float64 // per-symbol beta vs benchmark
	MaxNetBeta        float64            // maximum allowed |net_beta|
	PortfolioNotional float64            // total portfolio notional value
	BenchmarkPrice    float64            // current price of hedging instrument (e.g. SPY)
}

// FactorNeutralResult holds the output of factor-neutral adjustment.
type FactorNeutralResult struct {
	AdjustedWeights map[string]float64 // adjusted portfolio weights
	NetBeta         float64            // portfolio net beta after adjustment
	HedgeShares     float64            // number of benchmark shares to trade for hedging
	NeedsHedge      bool               // whether a hedge is recommended
}

// ComputeRollingBeta computes the beta of an asset vs a benchmark using
// Cov(asset, benchmark) / Var(benchmark).
// assetReturns and benchmarkReturns must have the same length.
func ComputeRollingBeta(assetReturns, benchmarkReturns []float64) float64 {
	n := len(assetReturns)
	if n < 2 || len(benchmarkReturns) != n {
		return 1.0 // default beta = 1
	}

	// Compute means
	assetMean := 0.0
	benchMean := 0.0
	for i := 0; i < n; i++ {
		assetMean += assetReturns[i]
		benchMean += benchmarkReturns[i]
	}
	assetMean /= float64(n)
	benchMean /= float64(n)

	// Compute covariance and benchmark variance
	cov := 0.0
	benchVar := 0.0
	for i := 0; i < n; i++ {
		ad := assetReturns[i] - assetMean
		bd := benchmarkReturns[i] - benchMean
		cov += ad * bd
		benchVar += bd * bd
	}

	if benchVar < 1e-12 {
		return 1.0 // cannot compute, return market beta
	}

	return cov / benchVar
}

// NetBeta computes the portfolio's net beta: Σ(w_i × beta_i).
func NetBeta(weights map[string]float64, betas map[string]float64) float64 {
	netBeta := 0.0
	for sym, w := range weights {
		beta, ok := betas[sym]
		if !ok {
			beta = 1.0 // assume market beta if unknown
		}
		netBeta += w * beta
	}
	return netBeta
}

// RunFactorNeutral adjusts portfolio weights to bring net beta within bounds
// and computes the required hedge position.
func RunFactorNeutral(input FactorNeutralInput) FactorNeutralResult {
	result := FactorNeutralResult{
		AdjustedWeights: make(map[string]float64),
	}

	if len(input.Weights) == 0 {
		return result
	}

	// Copy weights
	for sym, w := range input.Weights {
		result.AdjustedWeights[sym] = w
	}

	// Compute net beta
	result.NetBeta = NetBeta(input.Weights, input.Betas)

	maxBeta := input.MaxNetBeta
	if maxBeta <= 0 {
		maxBeta = 0.3
	}

	// If within bounds, no adjustment needed
	if math.Abs(result.NetBeta) <= maxBeta {
		return result
	}

	result.NeedsHedge = true

	// Compute hedge shares: hedge_shares = -net_beta × portfolio_notional / spy_price
	if input.BenchmarkPrice > 0 && input.PortfolioNotional > 0 {
		result.HedgeShares = -result.NetBeta * input.PortfolioNotional / input.BenchmarkPrice
	}

	// Also adjust weights proportionally to reduce net beta
	// Scale all weights so that |net_beta| ≤ maxBeta
	if math.Abs(result.NetBeta) > 1e-12 {
		scale := maxBeta / math.Abs(result.NetBeta)
		if scale < 1.0 {
			for sym, w := range result.AdjustedWeights {
				result.AdjustedWeights[sym] = w * scale
			}
			result.NetBeta *= scale
		}
	}

	return result
}
