package backtest

import (
	"math"
	"testing"
)

func TestRunMonteCarlo_EmptyTrades(t *testing.T) {
	result := RunMonteCarlo(nil, 25000, 100, 252)
	if result.NumSimulations != 0 {
		t.Errorf("expected 0 simulations for empty trades, got %d", result.NumSimulations)
	}
}

func TestRunMonteCarlo_AllWinners(t *testing.T) {
	trades := make([]TradeResult, 50)
	for i := range trades {
		trades[i] = TradeResult{PnL: 100}
	}

	result := RunMonteCarlo(trades, 25000, 1000, 252)

	if result.NumSimulations != 1000 {
		t.Errorf("expected 1000 simulations, got %d", result.NumSimulations)
	}
	if result.MedianCAGR <= 0 {
		t.Errorf("expected positive median CAGR for all-winning trades, got %f", result.MedianCAGR)
	}
	if result.Percentile5CAGR > result.MedianCAGR {
		t.Errorf("5th percentile CAGR (%.4f) should be <= median (%.4f)", result.Percentile5CAGR, result.MedianCAGR)
	}
	if result.Percentile95CAGR < result.MedianCAGR {
		t.Errorf("95th percentile CAGR (%.4f) should be >= median (%.4f)", result.Percentile95CAGR, result.MedianCAGR)
	}
	if result.MedianSharpe <= 0 {
		t.Errorf("expected positive Sharpe for all-winning trades, got %f", result.MedianSharpe)
	}
}

func TestRunMonteCarlo_MixedTrades(t *testing.T) {
	trades := []TradeResult{
		{PnL: 200}, {PnL: -50}, {PnL: 150}, {PnL: -100},
		{PnL: 300}, {PnL: -75}, {PnL: 100}, {PnL: -25},
		{PnL: 250}, {PnL: -60},
	}

	result := RunMonteCarlo(trades, 25000, 5000, 60)

	if result.NumSimulations != 5000 {
		t.Errorf("expected 5000 simulations, got %d", result.NumSimulations)
	}
	// Sharpe CI should bracket the median
	if result.SharpeCI95Lower > result.MedianSharpe {
		t.Errorf("Sharpe CI lower (%.4f) should be <= median (%.4f)", result.SharpeCI95Lower, result.MedianSharpe)
	}
	if result.SharpeCI95Upper < result.MedianSharpe {
		t.Errorf("Sharpe CI upper (%.4f) should be >= median (%.4f)", result.SharpeCI95Upper, result.MedianSharpe)
	}
}

func TestPercentile(t *testing.T) {
	data := make([]float64, 100)
	for i := range data {
		data[i] = float64(i + 1)
	}

	p50 := percentile(data, 50)
	if math.Abs(p50-50.5) > 1.0 {
		t.Errorf("expected percentile(50) near 50.5, got %f", p50)
	}

	p5 := percentile(data, 5)
	if p5 > 10 {
		t.Errorf("expected percentile(5) near 5, got %f", p5)
	}

	p95 := percentile(data, 95)
	if p95 < 90 {
		t.Errorf("expected percentile(95) near 95, got %f", p95)
	}
}

func TestPercentile_Empty(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("expected 0 for empty slice, got %f", got)
	}
}

func TestMeanStddev(t *testing.T) {
	data := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	mean, std := meanStddev(data)

	if math.Abs(mean-5.0) > 0.01 {
		t.Errorf("expected mean 5.0, got %f", mean)
	}
	if math.Abs(std-2.0) > 0.1 {
		t.Errorf("expected std ~2.0, got %f", std)
	}
}
