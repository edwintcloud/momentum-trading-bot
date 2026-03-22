package backtest

import (
	"context"
	"math"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// WalkForwardConfig controls rolling in-sample / out-of-sample windows.
type WalkForwardConfig struct {
	ISWindowDays  int // in-sample window (default: 60 trading days)
	OOSWindowDays int // out-of-sample window (default: 20 trading days)
	PurgeGapDays  int // gap between IS and OOS to prevent leakage (default: 5)
	StepDays      int // step size for rolling windows (default: 20)
}

// WalkForwardResult aggregates all walk-forward windows.
type WalkForwardResult struct {
	Windows        []WFWindow `json:"windows"`
	OOSSharpe      float64    `json:"oosSharpe"`
	ISSharpe       float64    `json:"isSharpe"`
	Efficiency     float64    `json:"efficiency"`
	OOSTotalReturn float64    `json:"oosTotalReturn"`
}

// WFWindow records a single in-sample/out-of-sample pair.
type WFWindow struct {
	ISStart   time.Time `json:"isStart"`
	ISEnd     time.Time `json:"isEnd"`
	OOSStart  time.Time `json:"oosStart"`
	OOSEnd    time.Time `json:"oosEnd"`
	ISSharpe  float64   `json:"isSharpe"`
	OOSSharpe float64   `json:"oosSharpe"`
	OOSReturn float64   `json:"oosReturn"`
	OOSTrades int       `json:"oosTrades"`
}

// RunWalkForward executes rolling IS/OOS backtests on the bar data.
func RunWalkForward(bars []InputBar, wfCfg WalkForwardConfig, tradingCfg config.TradingConfig) WalkForwardResult {
	if len(bars) == 0 {
		return WalkForwardResult{}
	}

	loc := markethours.Location()

	// Find date range from bars
	minTime := bars[0].Timestamp.In(loc)
	maxTime := bars[len(bars)-1].Timestamp.In(loc)
	totalCalendarDays := int(maxTime.Sub(minTime).Hours() / 24)

	// Convert trading days to approximate calendar days (x 7/5 for weekdays)
	isCalDays := wfCfg.ISWindowDays * 7 / 5
	oosCalDays := wfCfg.OOSWindowDays * 7 / 5
	purgeCalDays := wfCfg.PurgeGapDays * 7 / 5
	stepCalDays := wfCfg.StepDays * 7 / 5

	if isCalDays+purgeCalDays+oosCalDays > totalCalendarDays {
		return WalkForwardResult{}
	}

	var windows []WFWindow
	var allOOSReturns []float64
	var isSharpeSum float64

	for offset := 0; ; offset += stepCalDays {
		isStart := minTime.AddDate(0, 0, offset)
		isEnd := isStart.AddDate(0, 0, isCalDays)
		oosStart := isEnd.AddDate(0, 0, purgeCalDays)
		oosEnd := oosStart.AddDate(0, 0, oosCalDays)

		if oosEnd.After(maxTime) {
			break
		}

		// Extract bars for each window
		isBars := filterBarsByTimeRange(bars, isStart, isEnd)
		oosBars := filterBarsByTimeRange(bars, oosStart, oosEnd)

		if len(isBars) < 10 || len(oosBars) < 5 {
			continue
		}

		// Run backtest on IS window
		isResult, err := Run(context.Background(), tradingCfg, RunConfig{Bars: isBars})
		if err != nil {
			continue
		}

		// Run backtest on OOS window
		oosResult, err := Run(context.Background(), tradingCfg, RunConfig{Bars: oosBars})
		if err != nil {
			continue
		}

		isSharpe := computeSharpeFromResult(isResult)
		oosSharpe := computeSharpeFromResult(oosResult)
		oosReturn := 0.0
		if isResult.StartingCapital > 0 {
			oosReturn = oosResult.NetPnL / oosResult.StartingCapital
		}

		windows = append(windows, WFWindow{
			ISStart:   isStart,
			ISEnd:     isEnd,
			OOSStart:  oosStart,
			OOSEnd:    oosEnd,
			ISSharpe:  isSharpe,
			OOSSharpe: oosSharpe,
			OOSReturn: oosReturn,
			OOSTrades: oosResult.Trades,
		})

		isSharpeSum += isSharpe

		// Collect OOS trade returns for stitched Sharpe
		for _, trade := range oosResult.ClosedTrades {
			if oosResult.StartingCapital > 0 {
				allOOSReturns = append(allOOSReturns, trade.PnL/oosResult.StartingCapital)
			}
		}
	}

	if len(windows) == 0 {
		return WalkForwardResult{}
	}

	// Compute stitched OOS Sharpe from all OOS returns
	oosSharpe := 0.0
	oosTotalReturn := 0.0
	if len(allOOSReturns) > 1 {
		mean, std := meanStddev(allOOSReturns)
		if std > 0 {
			oosSharpe = (mean / std) * math.Sqrt(252)
		}
		for _, r := range allOOSReturns {
			oosTotalReturn += r
		}
	}

	avgISSharpe := isSharpeSum / float64(len(windows))
	efficiency := 0.0
	if avgISSharpe != 0 {
		efficiency = oosSharpe / avgISSharpe
	}

	return WalkForwardResult{
		Windows:        windows,
		OOSSharpe:      oosSharpe,
		ISSharpe:       avgISSharpe,
		Efficiency:     efficiency,
		OOSTotalReturn: oosTotalReturn,
	}
}

func filterBarsByTimeRange(bars []InputBar, start, end time.Time) []InputBar {
	var filtered []InputBar
	for _, b := range bars {
		if !b.Timestamp.Before(start) && b.Timestamp.Before(end) {
			filtered = append(filtered, b)
		}
	}
	return filtered
}

func computeSharpeFromResult(result Result) float64 {
	if len(result.ClosedTrades) < 2 || result.StartingCapital <= 0 {
		return 0
	}
	returns := make([]float64, len(result.ClosedTrades))
	for i, t := range result.ClosedTrades {
		returns[i] = t.PnL / result.StartingCapital
	}
	mean, std := meanStddev(returns)
	if std <= 0 {
		return 0
	}
	return (mean / std) * math.Sqrt(252)
}
