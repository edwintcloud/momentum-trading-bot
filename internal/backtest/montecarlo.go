package backtest

import (
	"math"
	"math/rand"
	"sort"
)

// MonteCarloResult holds simulation statistics from trade resampling.
type MonteCarloResult struct {
	MedianCAGR        float64 `json:"medianCAGR"`
	Percentile5CAGR   float64 `json:"percentile5CAGR"`
	Percentile95CAGR  float64 `json:"percentile95CAGR"`
	MedianMaxDD       float64 `json:"medianMaxDD"`
	Percentile5MaxDD  float64 `json:"percentile5MaxDD"`
	Percentile95MaxDD float64 `json:"percentile95MaxDD"`
	MedianSharpe      float64 `json:"medianSharpe"`
	SharpeCI95Lower   float64 `json:"sharpeCI95Lower"`
	SharpeCI95Upper   float64 `json:"sharpeCI95Upper"`
	NumSimulations    int     `json:"numSimulations"`
}

// TradeResult captures a single trade's PnL for simulation input.
type TradeResult struct {
	PnL float64
}

// RunMonteCarlo resamples the trade sequence N times and computes statistics.
func RunMonteCarlo(trades []TradeResult, startingCapital float64, numSims int, tradingDays int) MonteCarloResult {
	if len(trades) == 0 || numSims == 0 {
		return MonteCarloResult{}
	}

	rng := rand.New(rand.NewSource(42)) // reproducible

	cagrs := make([]float64, numSims)
	maxDDs := make([]float64, numSims)
	sharpes := make([]float64, numSims)

	for sim := 0; sim < numSims; sim++ {
		equity := startingCapital
		hwm := equity
		maxDD := 0.0
		var returns []float64

		for i := 0; i < len(trades); i++ {
			idx := rng.Intn(len(trades))
			pnl := trades[idx].PnL

			priorEquity := equity
			equity += pnl

			if priorEquity > 0 {
				ret := pnl / priorEquity
				returns = append(returns, ret)
			}

			if equity > hwm {
				hwm = equity
			}
			if hwm > 0 {
				dd := (hwm - equity) / hwm
				if dd > maxDD {
					maxDD = dd
				}
			}
		}

		// CAGR = (final/initial)^(252/tradingDays) - 1
		if equity > 0 && startingCapital > 0 && tradingDays > 0 {
			cagrs[sim] = math.Pow(equity/startingCapital, 252.0/float64(tradingDays)) - 1
		}
		maxDDs[sim] = maxDD

		// Sharpe (annualized)
		if len(returns) > 1 {
			mean, stddev := meanStddev(returns)
			if stddev > 0 {
				sharpes[sim] = (mean / stddev) * math.Sqrt(252)
			}
		}
	}

	sort.Float64s(cagrs)
	sort.Float64s(maxDDs)
	sort.Float64s(sharpes)

	return MonteCarloResult{
		MedianCAGR:        percentile(cagrs, 50),
		Percentile5CAGR:   percentile(cagrs, 5),
		Percentile95CAGR:  percentile(cagrs, 95),
		MedianMaxDD:       percentile(maxDDs, 50),
		Percentile5MaxDD:  percentile(maxDDs, 95), // 95th percentile of DD = worst case
		Percentile95MaxDD: percentile(maxDDs, 5),
		MedianSharpe:      percentile(sharpes, 50),
		SharpeCI95Lower:   percentile(sharpes, 2.5),
		SharpeCI95Upper:   percentile(sharpes, 97.5),
		NumSimulations:    numSims,
	}
}

func percentile(sorted []float64, pct float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(pct / 100.0 * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func meanStddev(data []float64) (float64, float64) {
	n := float64(len(data))
	if n < 2 {
		return 0, 0
	}
	var sum, sumSq float64
	for _, v := range data {
		sum += v
		sumSq += v * v
	}
	mean := sum / n
	variance := (sumSq - n*mean*mean) / (n - 1)
	return mean, math.Sqrt(math.Max(0, variance))
}
