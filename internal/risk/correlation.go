package risk

import (
	"math"
	"sync"
)

// CorrelationTracker computes rolling pairwise correlations between symbols.
type CorrelationTracker struct {
	mu           sync.RWMutex
	priceHistory map[string][]float64 // symbol -> recent prices
	windowSize   int
}

// NewCorrelationTracker creates a tracker with the given rolling window size.
func NewCorrelationTracker(windowSize int) *CorrelationTracker {
	return &CorrelationTracker{
		priceHistory: make(map[string][]float64),
		windowSize:   windowSize,
	}
}

// UpdatePrice adds a price observation for a symbol.
func (ct *CorrelationTracker) UpdatePrice(symbol string, price float64) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	history := ct.priceHistory[symbol]
	history = append(history, price)
	if len(history) > ct.windowSize+1 {
		history = history[1:]
	}
	ct.priceHistory[symbol] = history
}

// returns computes log returns from price history.
func (ct *CorrelationTracker) returns(symbol string) []float64 {
	prices := ct.priceHistory[symbol]
	if len(prices) < 2 {
		return nil
	}
	rets := make([]float64, len(prices)-1)
	for i := 1; i < len(prices); i++ {
		if prices[i-1] != 0 {
			rets[i-1] = (prices[i] - prices[i-1]) / prices[i-1]
		}
	}
	return rets
}

// PairwiseCorrelation computes Pearson correlation between two symbols.
func (ct *CorrelationTracker) PairwiseCorrelation(sym1, sym2 string) float64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	r1 := ct.returns(sym1)
	r2 := ct.returns(sym2)

	n := len(r1)
	if len(r2) < n {
		n = len(r2)
	}
	if n < 5 {
		return 0 // assume uncorrelated if insufficient data
	}

	// Truncate to same length (most recent)
	r1 = r1[len(r1)-n:]
	r2 = r2[len(r2)-n:]

	return PearsonCorrelation(r1, r2)
}

// AvgPortfolioCorrelation computes average absolute pairwise correlation
// between newSymbol and each existing position.
func (ct *CorrelationTracker) AvgPortfolioCorrelation(existingSymbols []string, newSymbol string) float64 {
	if len(existingSymbols) == 0 {
		return 0
	}

	var totalCorr float64
	var count int

	for _, sym := range existingSymbols {
		corr := ct.PairwiseCorrelation(sym, newSymbol)
		totalCorr += math.Abs(corr)
		count++
	}

	if count == 0 {
		return 0
	}
	return totalCorr / float64(count)
}

// PearsonCorrelation computes the Pearson correlation coefficient between two slices.
func PearsonCorrelation(x, y []float64) float64 {
	n := float64(len(x))
	if n == 0 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2, sumY2 float64
	for i := range x {
		sumX += x[i]
		sumY += y[i]
		sumXY += x[i] * y[i]
		sumX2 += x[i] * x[i]
		sumY2 += y[i] * y[i]
	}
	num := n*sumXY - sumX*sumY
	den := math.Sqrt((n*sumX2 - sumX*sumX) * (n*sumY2 - sumY*sumY))
	if den == 0 {
		return 0
	}
	return num / den
}
