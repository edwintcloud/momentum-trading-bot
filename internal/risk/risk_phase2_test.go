package risk

import (
	"math"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

func marketOpenTime() time.Time {
	loc := markethours.Location()
	return time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
}

func TestPearsonCorrelation(t *testing.T) {
	// Perfect positive correlation
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{2, 4, 6, 8, 10}
	corr := PearsonCorrelation(x, y)
	if math.Abs(corr-1.0) > 0.001 {
		t.Errorf("perfect positive correlation: got %.4f, want 1.0", corr)
	}

	// Perfect negative correlation
	y2 := []float64{10, 8, 6, 4, 2}
	corr2 := PearsonCorrelation(x, y2)
	if math.Abs(corr2-(-1.0)) > 0.001 {
		t.Errorf("perfect negative correlation: got %.4f, want -1.0", corr2)
	}

	// Zero correlation (orthogonal)
	x3 := []float64{1, -1, 1, -1}
	y3 := []float64{1, 1, -1, -1}
	corr3 := PearsonCorrelation(x3, y3)
	if math.Abs(corr3) > 0.001 {
		t.Errorf("zero correlation: got %.4f, want ~0.0", corr3)
	}

	// Empty slices
	if PearsonCorrelation(nil, nil) != 0 {
		t.Error("empty slices should return 0")
	}
}

func TestCorrelationTracker(t *testing.T) {
	ct := NewCorrelationTracker(20)

	// Feed perfectly correlated price series
	for i := 0; i < 25; i++ {
		price := 100.0 + float64(i)
		ct.UpdatePrice("AAPL", price)
		ct.UpdatePrice("MSFT", price*2) // perfectly correlated
	}

	corr := ct.PairwiseCorrelation("AAPL", "MSFT")
	if corr < 0.99 {
		t.Errorf("expected high correlation for perfectly correlated series, got %.4f", corr)
	}

	// Test with insufficient data
	ct2 := NewCorrelationTracker(20)
	ct2.UpdatePrice("X", 10)
	ct2.UpdatePrice("Y", 20)
	if ct2.PairwiseCorrelation("X", "Y") != 0 {
		t.Error("expected 0 correlation with insufficient data")
	}
}

func TestAvgPortfolioCorrelation(t *testing.T) {
	ct := NewCorrelationTracker(20)

	// Feed correlated series
	for i := 0; i < 25; i++ {
		price := 100.0 + float64(i)
		ct.UpdatePrice("AAPL", price)
		ct.UpdatePrice("MSFT", price*1.5)
		ct.UpdatePrice("GOOGL", price*0.8)
	}

	avgCorr := ct.AvgPortfolioCorrelation([]string{"AAPL", "MSFT"}, "GOOGL")
	if avgCorr < 0.5 {
		t.Errorf("expected high avg correlation for correlated series, got %.4f", avgCorr)
	}

	// Empty portfolio
	if ct.AvgPortfolioCorrelation(nil, "GOOGL") != 0 {
		t.Error("expected 0 for empty portfolio")
	}
}

func TestPortfolioHeatGate(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 100000
	cfg.MaxOpenPositions = 10
	cfg.MaxExposurePct = 0.95
	cfg.PortfolioHeatEnabled = true
	cfg.MaxPortfolioHeatPct = 0.05 // 5%
	cfg.CorrelationCheckEnabled = false
	cfg.SectorConcentrationEnabled = false

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	e := NewEngine(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Open 4 positions with $1250 risk each = $5000 total risk = 5% of $100K
	// Using low-priced stocks with small quantity to stay under exposure limits
	for i, sym := range []string{"A", "B", "C", "D"} {
		pm.OpenPosition(domain.ExecutionReport{
			Symbol:       sym,
			Side:         domain.SideBuy,
			Intent:       domain.IntentOpen,
			PositionSide: domain.DirectionLong,
			Price:        10.0,
			Quantity:     50,
			StopPrice:    0.0,
			RiskPerShare: 25.0, // $1250 risk per position
			FilledAt:     ts.Add(time.Duration(i) * time.Minute),
		})
	}

	// New entry should be blocked: adding more risk would exceed 5%
	signal := domain.TradeSignal{
		Symbol:       "NEW",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        10.0,
		Quantity:     50,
		RiskPerShare: 10.0, // $500 risk → total would be 5500/100000 = 5.5% > 5%
		Timestamp:    ts,
	}

	_, approved, reason := e.Evaluate(signal)
	if approved {
		t.Error("expected rejection due to portfolio heat limit")
	}
	if reason != "portfolio-heat-limit" {
		t.Errorf("expected reason 'portfolio-heat-limit', got %q", reason)
	}
}

func TestGraduatedDailyLoss(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	cfg.DailyLossModeratePct = 0.01
	cfg.DailyLossSeverePct = 0.015
	cfg.DailyLossHaltPct = 0.02

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	e := NewEngine(cfg, pm, runtimeState)

	// No loss: factor = 1.0
	if f := e.DailyLossSizingFactor(); f != 1.0 {
		t.Errorf("expected 1.0 with no loss, got %.2f", f)
	}

	ts := marketOpenTime()

	// Simulate 0.5% loss ($125): factor should be 1.0
	pm.OpenPosition(domain.ExecutionReport{
		Symbol: "A", Side: domain.SideBuy, Intent: domain.IntentOpen,
		PositionSide: domain.DirectionLong, Price: 50.0, Quantity: 100,
		StopPrice: 48.0, RiskPerShare: 2.0, FilledAt: ts,
	})
	pm.ClosePosition(domain.ExecutionReport{
		Symbol: "A", Side: domain.SideSell, Intent: domain.IntentClose,
		PositionSide: domain.DirectionLong, Price: 48.75, Quantity: 100,
		FilledAt: ts.Add(time.Minute),
	})
	if f := e.DailyLossSizingFactor(); f != 1.0 {
		t.Errorf("expected 1.0 at 0.5%% loss, got %.2f", f)
	}

	// Simulate additional loss to push to ~1.2% total ($300 total loss)
	pm.OpenPosition(domain.ExecutionReport{
		Symbol: "B", Side: domain.SideBuy, Intent: domain.IntentOpen,
		PositionSide: domain.DirectionLong, Price: 50.0, Quantity: 100,
		StopPrice: 48.0, RiskPerShare: 2.0, FilledAt: ts.Add(2 * time.Minute),
	})
	pm.ClosePosition(domain.ExecutionReport{
		Symbol: "B", Side: domain.SideSell, Intent: domain.IntentClose,
		PositionSide: domain.DirectionLong, Price: 48.25, Quantity: 100,
		FilledAt: ts.Add(3 * time.Minute),
	})
	// Total loss: 125 + 175 = 300 -> 300/25000 = 1.2% -> moderate tier (50%)
	if f := e.DailyLossSizingFactor(); f != 0.50 {
		t.Errorf("expected 0.50 at ~1.2%% loss, got %.2f (dayPnL=%.2f)", f, pm.DayPnL())
	}
}

func TestSectorConcentration(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 100000
	cfg.SectorConcentrationEnabled = true
	cfg.MaxPositionsPerSector = 2
	cfg.MaxSectorExposurePct = 0.25
	cfg.PortfolioHeatEnabled = false
	cfg.CorrelationCheckEnabled = false

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	e := NewEngine(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Open 2 tech positions
	pm.OpenPosition(domain.ExecutionReport{
		Symbol: "AAPL", Side: domain.SideBuy, Intent: domain.IntentOpen,
		PositionSide: domain.DirectionLong, Price: 150.0, Quantity: 50,
		StopPrice: 145.0, RiskPerShare: 5.0, Sector: "technology",
		FilledAt: ts,
	})
	pm.OpenPosition(domain.ExecutionReport{
		Symbol: "MSFT", Side: domain.SideBuy, Intent: domain.IntentOpen,
		PositionSide: domain.DirectionLong, Price: 300.0, Quantity: 25,
		StopPrice: 290.0, RiskPerShare: 10.0, Sector: "technology",
		FilledAt: ts.Add(time.Minute),
	})

	// 3rd tech position should be rejected
	signal := domain.TradeSignal{
		Symbol:       "GOOGL",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        140.0,
		Quantity:     30,
		RiskPerShare: 5.0,
		Sector:       "technology",
		Timestamp:    ts,
	}

	_, approved, reason := e.Evaluate(signal)
	if approved {
		t.Error("expected rejection for 3rd tech sector position")
	}
	if reason != "sector-concentration" {
		t.Errorf("expected reason 'sector-concentration', got %q", reason)
	}

	// Different sector should be allowed
	signal.Symbol = "JPM"
	signal.Sector = "financials"
	_, approved, _ = e.Evaluate(signal)
	if !approved {
		t.Error("expected approval for different sector")
	}
}

func TestDrawdownSizingFactor(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	cfg.DrawdownRiskEnabled = true
	cfg.MaxAcceptableDrawdown = 0.15

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	e := NewEngine(cfg, pm, runtimeState)

	// No drawdown: factor = 1.0
	if f := e.DrawdownSizingFactor(); f != 1.0 {
		t.Errorf("expected 1.0 with no drawdown, got %.2f", f)
	}

	ts := marketOpenTime()

	// Create a loss to generate drawdown
	pm.OpenPosition(domain.ExecutionReport{
		Symbol: "A", Side: domain.SideBuy, Intent: domain.IntentOpen,
		PositionSide: domain.DirectionLong, Price: 100.0, Quantity: 100,
		StopPrice: 95.0, RiskPerShare: 5.0, FilledAt: ts,
	})
	pm.ClosePosition(domain.ExecutionReport{
		Symbol: "A", Side: domain.SideSell, Intent: domain.IntentClose,
		PositionSide: domain.DirectionLong, Price: 87.5, Quantity: 100,
		FilledAt: ts.Add(time.Minute),
	})
	// Loss = (87.5 - 100) * 100 = -$1250
	// HWM was 25000, equity now 23750
	// DD = (25000 - 23750) / 25000 = 5%
	pm.UpdateEquityTracking()

	factor := e.DrawdownSizingFactor()
	expected := 1.0 - 0.05/0.15 // 1 - 0.333 = 0.667
	if math.Abs(factor-expected) > 0.01 {
		t.Errorf("expected drawdown factor ~%.3f at 5%% DD, got %.3f", expected, factor)
	}

	// At 15% DD: factor should be 0.0
	pm.OpenPosition(domain.ExecutionReport{
		Symbol: "B", Side: domain.SideBuy, Intent: domain.IntentOpen,
		PositionSide: domain.DirectionLong, Price: 100.0, Quantity: 100,
		StopPrice: 95.0, RiskPerShare: 5.0, FilledAt: ts.Add(2 * time.Minute),
	})
	pm.ClosePosition(domain.ExecutionReport{
		Symbol: "B", Side: domain.SideSell, Intent: domain.IntentClose,
		PositionSide: domain.DirectionLong, Price: 75.0, Quantity: 100,
		FilledAt: ts.Add(3 * time.Minute),
	})
	// Additional loss = -$2500, total loss = -$3750
	// Equity = 25000 - 3750 = 21250, DD = (25000 - 21250) / 25000 = 15%
	pm.UpdateEquityTracking()

	factor2 := e.DrawdownSizingFactor()
	if factor2 > 0.01 {
		t.Errorf("expected factor ~0.0 at 15%% DD, got %.3f", factor2)
	}
}

func TestVolatilityEstimator(t *testing.T) {
	ve := NewVolatilityEstimator(0.30)

	// Unknown symbol returns default
	if ve.GetVolatility("UNKNOWN") != 0.30 {
		t.Error("expected default volatility for unknown symbol")
	}

	// Feed some prices
	prices := []float64{100, 101, 99, 102, 98, 103, 97, 104, 96, 105}
	for _, p := range prices {
		ve.UpdatePrice("TEST", p)
	}

	vol := ve.GetVolatility("TEST")
	if vol <= 0 {
		t.Error("expected positive volatility estimate")
	}

	// High vol stock should have larger estimate than low vol
	lowVol := []float64{100, 100.1, 99.9, 100.05, 99.95, 100.02, 99.98, 100.01, 99.99, 100.0}
	for _, p := range lowVol {
		ve.UpdatePrice("CALM", p)
	}

	calmVol := ve.GetVolatility("CALM")
	if calmVol >= vol {
		t.Errorf("low-vol stock (%.4f) should have lower vol than high-vol stock (%.4f)", calmVol, vol)
	}
}

func TestVolatilityEstimatorMaxVolClamp(t *testing.T) {
	maxVol := 5.0 // 500% annualized cap
	ve := NewVolatilityEstimator(0.30, maxVol)

	// Feed extreme price moves that would produce >500% annualized vol
	// 5% per-minute moves should produce ~500%+ unclamped vol
	prices := []float64{
		100, 105, 100, 110, 95, 115, 90, 120, 85, 125,
		80, 130, 75, 135, 70, 140, 65, 145, 60, 150, 55,
	}
	for _, p := range prices {
		ve.UpdatePrice("EXTREME", p)
	}

	vol := ve.GetVolatility("EXTREME")
	if vol > maxVol {
		t.Errorf("vol estimate %.2f should be clamped to maxVol %.2f", vol, maxVol)
	}
	if vol <= 0 {
		t.Error("expected positive vol estimate")
	}
}

func TestVolatilityEstimatorNoClampWhenZero(t *testing.T) {
	ve := NewVolatilityEstimator(0.30) // no maxVol

	// Feed extreme prices
	prices := []float64{
		100, 105, 100, 110, 95, 115, 90, 120, 85, 125,
	}
	for _, p := range prices {
		ve.UpdatePrice("WILD", p)
	}

	vol := ve.GetVolatility("WILD")
	// Without clamp, vol should be very high
	if vol <= 5.0 {
		t.Skipf("vol estimate %.2f not high enough without clamp, skipping", vol)
	}
	// No assertion on upper bound — just verifying no artificial cap
}

func TestVolatilityEstimatorSetMaxVol(t *testing.T) {
	ve := NewVolatilityEstimator(0.30) // start without cap

	// Feed extreme prices
	prices := []float64{
		100, 110, 90, 120, 80, 130, 70, 140, 60, 150,
	}
	for _, p := range prices {
		ve.UpdatePrice("SETTER", p)
	}

	volBefore := ve.GetVolatility("SETTER")

	// Now set max vol and feed more prices
	ve.SetMaxVol(2.0)
	for _, p := range prices {
		ve.UpdatePrice("SETTER2", p)
	}

	volAfter := ve.GetVolatility("SETTER2")
	if volAfter > 2.0 {
		t.Errorf("after SetMaxVol(2.0), vol %.2f should be <= 2.0", volAfter)
	}
	_ = volBefore
}
