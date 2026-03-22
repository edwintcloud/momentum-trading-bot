package backtest

import (
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/regime"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
	"github.com/edwintcloud/momentum-trading-bot/internal/strategy"
	"github.com/edwintcloud/momentum-trading-bot/internal/telemetry"
)

// Result holds the outcome of a backtest run.
type Result struct {
	StartDate       string                   `json:"startDate"`
	EndDate         string                   `json:"endDate"`
	TotalBars       int                      `json:"totalBars"`
	TotalTrades     int                      `json:"totalTrades"`
	WinRate         float64                  `json:"winRate"`
	ProfitFactor    float64                  `json:"profitFactor"`
	NetPnL          float64                  `json:"netPnL"`
	MaxDrawdown     float64                  `json:"maxDrawdown"`
	SharpeRatio     float64                  `json:"sharpeRatio"`
	SortinoRatio    float64                  `json:"sortinoRatio"`
	AvgRMultiple    float64                  `json:"avgRMultiple"`
	AvgWin          float64                  `json:"avgWin"`
	AvgLoss         float64                  `json:"avgLoss"`
	LargestWin      float64                  `json:"largestWin"`
	LargestLoss     float64                  `json:"largestLoss"`
	Trades          []domain.ClosedTrade     `json:"trades"`
	DailyPnL        []DailyPnLEntry          `json:"dailyPnl"`
	Performance     domain.PerformanceMetrics `json:"performance"`
}

// DailyPnLEntry tracks daily PnL for the equity curve.
type DailyPnLEntry struct {
	Date string  `json:"date"`
	PnL  float64 `json:"pnl"`
}

// Engine replays historical bars through the trading pipeline.
type Engine struct {
	cfg config.TradingConfig
}

// NewEngine creates a backtest engine.
func NewEngine(cfg config.TradingConfig) *Engine {
	return &Engine{cfg: cfg}
}

// Run executes a backtest on the provided bars.
func (e *Engine) Run(bars []domain.Tick) Result {
	// Sort bars by timestamp
	sort.Slice(bars, func(i, j int) bool {
		return bars[i].Timestamp.Before(bars[j].Timestamp)
	})

	if len(bars) == 0 {
		return Result{}
	}

	// Initialize components
	logger := telemetry.NewLogger(nil)
	runtimeState := runtime.NewState(logger)
	runtimeState.SetReady(true)
	portfolioMgr := portfolio.NewManager(e.cfg, logger)
	scannerInst := scanner.NewScanner(e.cfg, runtimeState)
	strategyInst := strategy.NewStrategy(e.cfg, portfolioMgr, runtimeState)
	regimeTracker := regime.NewTracker(e.cfg, runtimeState)

	log.Printf("backtest: replaying %d bars from %s to %s",
		len(bars), bars[0].Timestamp.Format("2006-01-02"), bars[len(bars)-1].Timestamp.Format("2006-01-02"))

	// Replay bars
	for _, tick := range bars {
		// Update regime
		if regimeTracker.IsBenchmark(tick.Symbol) {
			regimeTracker.UpdateTick(tick)
		}

		// Evaluate exits for open positions
		exitSignal, shouldExit := strategyInst.EvaluateExit(tick)
		if shouldExit {
			report := simulatedFill(exitSignal)
			portfolioMgr.ClosePosition(report)
		}

		// Scanner evaluation
		candidate, ok := scannerInst.Evaluate(tick)
		if !ok {
			continue
		}

		// Strategy evaluation
		entrySignal, shouldEnter := strategyInst.EvaluateCandidate(candidate)
		if !shouldEnter {
			continue
		}

		// Simulate fill
		report := simulatedFill(entrySignal)
		if domain.IsOpeningIntent(entrySignal.Intent) {
			portfolioMgr.OpenPosition(report)
		}
	}

	// Close any remaining positions at last price
	closedTrades := portfolioMgr.GetClosedTrades()

	return e.computeResult(bars, closedTrades)
}

func (e *Engine) computeResult(bars []domain.Tick, trades []domain.ClosedTrade) Result {
	result := Result{
		TotalBars:  len(bars),
		TotalTrades: len(trades),
		Trades:     trades,
	}

	if len(bars) > 0 {
		result.StartDate = bars[0].Timestamp.Format("2006-01-02")
		result.EndDate = bars[len(bars)-1].Timestamp.Format("2006-01-02")
	}

	if len(trades) == 0 {
		return result
	}

	var wins, losses int
	var totalWin, totalLoss float64
	var totalR float64
	var dailyPnL = make(map[string]float64)
	var pnlSeries []float64

	for _, t := range trades {
		day := t.ClosedAt.Format("2006-01-02")
		dailyPnL[day] += t.PnL
		pnlSeries = append(pnlSeries, t.PnL)
		totalR += t.RMultiple
		result.NetPnL += t.PnL

		if t.PnL >= 0 {
			wins++
			totalWin += t.PnL
			if t.PnL > result.LargestWin {
				result.LargestWin = t.PnL
			}
		} else {
			losses++
			totalLoss += math.Abs(t.PnL)
			if t.PnL < result.LargestLoss {
				result.LargestLoss = t.PnL
			}
		}
	}

	n := float64(len(trades))
	result.WinRate = float64(wins) / n
	if losses > 0 {
		result.AvgLoss = totalLoss / float64(losses)
	}
	if wins > 0 {
		result.AvgWin = totalWin / float64(wins)
	}
	if totalLoss > 0 {
		result.ProfitFactor = totalWin / totalLoss
	}
	result.AvgRMultiple = totalR / n

	// Daily PnL entries
	for day, pnl := range dailyPnL {
		result.DailyPnL = append(result.DailyPnL, DailyPnLEntry{Date: day, PnL: pnl})
	}
	sort.Slice(result.DailyPnL, func(i, j int) bool {
		return result.DailyPnL[i].Date < result.DailyPnL[j].Date
	})

	// Max drawdown
	peak := 0.0
	cumulative := 0.0
	for _, pnl := range pnlSeries {
		cumulative += pnl
		if cumulative > peak {
			peak = cumulative
		}
		dd := peak - cumulative
		if dd > result.MaxDrawdown {
			result.MaxDrawdown = dd
		}
	}

	// Sharpe and Sortino (annualized, assuming 252 trading days)
	if len(pnlSeries) > 1 {
		mean := result.NetPnL / n
		var sumSq, sumDownSq float64
		for _, pnl := range pnlSeries {
			diff := pnl - mean
			sumSq += diff * diff
			if pnl < 0 {
				sumDownSq += pnl * pnl
			}
		}
		stdDev := math.Sqrt(sumSq / (n - 1))
		downDev := math.Sqrt(sumDownSq / (n - 1))
		if stdDev > 0 {
			result.SharpeRatio = (mean / stdDev) * math.Sqrt(252)
		}
		if downDev > 0 {
			result.SortinoRatio = (mean / downDev) * math.Sqrt(252)
		}
	}

	result.Performance = domain.PerformanceMetrics{
		TotalTrades:  len(trades),
		WinRate:      result.WinRate,
		AvgWin:       result.AvgWin,
		AvgLoss:      result.AvgLoss,
		ProfitFactor: result.ProfitFactor,
		SharpeRatio:  result.SharpeRatio,
		SortinoRatio: result.SortinoRatio,
		MaxDrawdown:  result.MaxDrawdown,
		AvgRMultiple: result.AvgRMultiple,
		LargestWin:   result.LargestWin,
		LargestLoss:  result.LargestLoss,
	}

	return result
}

func simulatedFill(signal domain.TradeSignal) domain.ExecutionReport {
	return domain.ExecutionReport{
		Symbol:           signal.Symbol,
		Side:             signal.Side,
		Intent:           signal.Intent,
		PositionSide:     signal.PositionSide,
		Price:            signal.Price,
		Quantity:         signal.Quantity,
		StopPrice:        signal.StopPrice,
		RiskPerShare:     signal.RiskPerShare,
		EntryATR:         signal.EntryATR,
		SetupType:        signal.SetupType,
		Reason:           signal.Reason,
		MarketRegime:     signal.MarketRegime,
		RegimeConfidence: signal.RegimeConfidence,
		Playbook:         signal.Playbook,
		BrokerOrderID:    fmt.Sprintf("sim-%d", time.Now().UnixNano()),
		BrokerStatus:     "filled",
		FilledAt:         signal.Timestamp,
	}
}
