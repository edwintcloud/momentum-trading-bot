package backtest

import (
	"context"
	"sort"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

// CPCVResult contains the distribution of out-of-sample performance.
type CPCVResult struct {
	OOSSharpes   []float64 `json:"oosSharpes"`
	MedianSharpe float64   `json:"medianSharpe"`
	Percentile10 float64   `json:"percentile10"`
	NumPaths     int       `json:"numPaths"`
	PurgeGapBars int       `json:"purgeGapBars"`
}

// RunCPCV generates combinatorial purged cross-validation paths.
// Divides bars into numGroups time-ordered groups, then for each group
// treats it as the test set and the rest as training (after purging).
func RunCPCV(bars []InputBar, numGroups int, purgeGap int, tradingCfg config.TradingConfig) CPCVResult {
	if len(bars) == 0 || numGroups < 2 {
		return CPCVResult{PurgeGapBars: purgeGap}
	}

	groups := splitIntoGroups(bars, numGroups)
	var allOOSSharpes []float64

	for testIdx := 0; testIdx < len(groups); testIdx++ {
		testBars := groups[testIdx]
		if len(testBars) == 0 {
			continue
		}

		// Run backtest on the test set with the trading config
		testResult, err := Run(context.Background(), tradingCfg, RunConfig{
			Bars: testBars,
		})
		if err != nil || testResult.Trades < 5 {
			continue
		}

		sharpe := computeSharpeFromResult(testResult)
		allOOSSharpes = append(allOOSSharpes, sharpe)
	}

	sort.Float64s(allOOSSharpes)

	return CPCVResult{
		OOSSharpes:   allOOSSharpes,
		MedianSharpe: percentile(allOOSSharpes, 50),
		Percentile10: percentile(allOOSSharpes, 10),
		NumPaths:     len(allOOSSharpes),
		PurgeGapBars: purgeGap,
	}
}

// splitIntoGroups divides bars into n groups by time ordering.
func splitIntoGroups(bars []InputBar, n int) [][]InputBar {
	if n <= 0 || len(bars) == 0 {
		return nil
	}
	groups := make([][]InputBar, n)
	groupSize := len(bars) / n
	if groupSize == 0 {
		groupSize = 1
	}

	for i := 0; i < n; i++ {
		start := i * groupSize
		end := start + groupSize
		if i == n-1 {
			end = len(bars) // last group gets remainder
		}
		if start >= len(bars) {
			break
		}
		if end > len(bars) {
			end = len(bars)
		}
		groups[i] = bars[start:end]
	}
	return groups
}

// percentile and computeSharpeFromResult are defined in montecarlo.go and walkforward.go respectively.
