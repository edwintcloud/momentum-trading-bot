package strategy

import (
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
	// Use a known weekday (Monday March 23, 2026) at 10am ET
	return time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
}

func TestPositionSizingUsesEquity(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 25000
	cfg.RiskPerTradePct = 0.005
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	candidate := domain.Candidate{
		Symbol:    "TEST",
		Direction: domain.DirectionLong,
		Price:     50.0,
		ATR:       1.0,
		Score:     5.0,
		Playbook:  "breakout",
		GapPercent: 5.0,
		RelativeVolume: 3.0,
		PreMarketVolume: 60000,
		Timestamp: ts,
	}

	signal1, ok := s.EvaluateCandidate(candidate)
	if !ok {
		t.Fatal("expected signal for initial candidate")
	}
	qty1 := signal1.Quantity

	// Simulate closing a profitable trade to increase equity
	pm.OpenPosition(domain.ExecutionReport{
		Symbol:       "PROFIT",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        10.0,
		Quantity:     100,
		StopPrice:    9.0,
		RiskPerShare: 1.0,
		FilledAt:     ts,
	})
	pm.ClosePosition(domain.ExecutionReport{
		Symbol:       "PROFIT",
		Side:         domain.SideSell,
		Intent:       domain.IntentClose,
		PositionSide: domain.DirectionLong,
		Price:        20.0,
		Quantity:     100,
		FilledAt:     ts,
	})

	// Current equity should now be 25000 + 1000 = 26000
	equity := pm.CurrentEquity()
	if equity < 25500 {
		t.Fatalf("expected equity > 25500, got %.2f", equity)
	}

	// Second candidate on a different symbol
	candidate2 := candidate
	candidate2.Symbol = "TEST2"
	delete(s.lastEntryAt, "TEST") // reset cooldown
	signal2, ok := s.EvaluateCandidate(candidate2)
	if !ok {
		t.Fatal("expected signal for second candidate")
	}

	if signal2.Quantity <= qty1 {
		t.Errorf("quantity with higher equity (%d) should be > quantity with starting capital (%d)", signal2.Quantity, qty1)
	}
}

func TestRegimeGatingBlocksLongInBearish(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RegimeGatingEnabled = true
	cfg.MinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()
	candidate := domain.Candidate{
		Symbol:          "TEST",
		Direction:       domain.DirectionLong,
		Price:           50.0,
		ATR:             1.0,
		Score:           5.0,
		Playbook:        "breakout",
		MarketRegime:    domain.RegimeBearish,
		RegimeConfidence: 0.8,
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected long entry to be blocked in bearish regime")
	}
}

func TestRegimeGatingBlocksShortInBullish(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RegimeGatingEnabled = true
	cfg.EnableShorts = true
	cfg.MinEntryScore = 0
	cfg.ShortMinEntryScore = 0
	cfg.ConfidenceSizingEnabled = false

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()
	candidate := domain.Candidate{
		Symbol:           "TEST",
		Direction:        domain.DirectionShort,
		Price:            50.0,
		ATR:              1.0,
		Score:            5.0,
		Playbook:         "reversal",
		MarketRegime:     domain.RegimeBullish,
		RegimeConfidence: 0.8,
		GapPercent:       -5.0,
		RelativeVolume:   3.0,
		PreMarketVolume:  60000,
		Timestamp:        ts,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected short entry to be blocked in bullish regime")
	}
}

func TestRegimeGatingMixedRequiresHigherScore(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RegimeGatingEnabled = true
	cfg.MinEntryScore = 2.0
	cfg.RegimeMixedScoreBoost = 1.25
	cfg.ConfidenceSizingEnabled = false

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Score 2.1 passes base threshold (2.0) but fails mixed (2.0*1.25 = 2.5)
	candidate := domain.Candidate{
		Symbol:           "TEST",
		Direction:        domain.DirectionLong,
		Price:            50.0,
		ATR:              1.0,
		Score:            2.1,
		Playbook:         "breakout",
		MarketRegime:     domain.RegimeMixed,
		RegimeConfidence: 0.5,
		GapPercent:       5.0,
		RelativeVolume:   3.0,
		PreMarketVolume:  60000,
		Timestamp:        ts,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected entry with score 2.1 to be blocked in mixed regime (requires 2.5)")
	}

	// Score 3.0 should pass
	candidate.Symbol = "TEST2"
	candidate.Score = 3.0
	_, ok = s.EvaluateCandidate(candidate)
	if !ok {
		t.Error("expected entry with score 3.0 to pass in mixed regime")
	}
}

func TestPlaybookExitsProduceDifferentLevels(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	breakout := s.getPlaybookExitConfig("breakout")
	pullback := s.getPlaybookExitConfig("pullback")
	reversal := s.getPlaybookExitConfig("reversal")

	if breakout.ProfitTargetR == pullback.ProfitTargetR {
		t.Error("breakout and pullback should have different profit targets")
	}
	if breakout.ProfitTargetR == reversal.ProfitTargetR {
		t.Error("breakout and reversal should have different profit targets")
	}
	if pullback.TrailATRMultiplier == breakout.TrailATRMultiplier {
		t.Error("pullback and breakout should have different trail multipliers")
	}
}

func TestStagnationUsesPeakR(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Create a position that reached 0.5R peak but pulled back to 0.1R
	// With playbook stagnation min peak R = 0.3, this should NOT trigger
	pos := domain.Position{
		Symbol:       "TEST",
		Side:         domain.DirectionLong,
		Quantity:     100,
		AvgPrice:     50.0,
		StopPrice:    48.5,
		RiskPerShare: 1.5,
		EntryATR:     1.0,
		Playbook:     "breakout",
		HighestPrice: 50.75, // peaked at 0.5R
		LowestPrice:  49.5,
		OpenedAt:     ts.Add(-20 * time.Minute), // 20 minutes ago
	}

	// Current price = 50.15 -> R = 0.1, but peakR = 0.5
	tick := domain.Tick{
		Symbol:    "TEST",
		Price:     50.15,
		Timestamp: ts,
	}

	reason, shouldExit := s.checkExitConditions(pos, tick)
	if shouldExit && reason == "stagnation" {
		t.Error("should NOT exit for stagnation when peakR (0.5) > stagnation threshold (0.3)")
	}

	// Now test a position that never moved: peakR = 0.1, should trigger stagnation
	pos2 := domain.Position{
		Symbol:       "TEST2",
		Side:         domain.DirectionLong,
		Quantity:     100,
		AvgPrice:     50.0,
		StopPrice:    48.5,
		RiskPerShare: 1.5,
		EntryATR:     1.0,
		Playbook:     "breakout",
		HighestPrice: 50.15, // peakR = 0.1
		LowestPrice:  49.9,
		OpenedAt:     ts.Add(-20 * time.Minute),
	}

	tick2 := domain.Tick{
		Symbol:    "TEST2",
		Price:     50.05,
		Timestamp: ts,
	}

	reason2, shouldExit2 := s.checkExitConditions(pos2, tick2)
	if !shouldExit2 || reason2 != "stagnation" {
		t.Errorf("should exit for stagnation when peakR (0.1) < threshold (0.3), got reason=%q shouldExit=%v", reason2, shouldExit2)
	}
}
