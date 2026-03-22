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

func TestTimeOfDayWindowClassification(t *testing.T) {
	loc := markethours.Location()
	// 9:35 ET — within 30 min of open -> TimeWindowOpen
	t1 := time.Date(2026, 3, 23, 9, 35, 0, 0, loc)
	if w := currentTimeWindow(t1); w != TimeWindowOpen {
		t.Errorf("9:35 ET should be Open window, got %d", w)
	}

	// 10:30 ET — 60 min after open -> TimeWindowMorning
	t2 := time.Date(2026, 3, 23, 10, 30, 0, 0, loc)
	if w := currentTimeWindow(t2); w != TimeWindowMorning {
		t.Errorf("10:30 ET should be Morning window, got %d", w)
	}

	// 13:00 ET — midday -> TimeWindowMidDay
	t3 := time.Date(2026, 3, 23, 13, 0, 0, 0, loc)
	if w := currentTimeWindow(t3); w != TimeWindowMidDay {
		t.Errorf("13:00 ET should be MidDay window, got %d", w)
	}

	// 15:30 ET — 30 min before close -> TimeWindowClose
	t4 := time.Date(2026, 3, 23, 15, 30, 0, 0, loc)
	if w := currentTimeWindow(t4); w != TimeWindowClose {
		t.Errorf("15:30 ET should be Close window, got %d", w)
	}
}

func TestTimeOfDayIncreasesScoreThreshold(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.TimeOfDayEnabled = true
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 2.0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	loc := markethours.Location()

	// MidDay candidate with score 2.1 — should be blocked (threshold * 1.15 = 2.3)
	midDayTime := time.Date(2026, 3, 23, 13, 0, 0, 0, loc)
	candidate := domain.Candidate{
		Symbol:          "TEST",
		Direction:       domain.DirectionLong,
		Price:           50.0,
		ATR:             1.0,
		Score:           2.1,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       midDayTime,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected midday candidate with score 2.1 to be blocked (threshold 2.3)")
	}

	// Same candidate at market open — should pass (threshold * 1.0 = 2.0)
	openTime := time.Date(2026, 3, 23, 9, 35, 0, 0, loc)
	candidate.Symbol = "TEST2"
	candidate.Timestamp = openTime
	_, ok = s.EvaluateCandidate(candidate)
	if !ok {
		t.Error("expected open-window candidate with score 2.1 to pass (threshold 2.0)")
	}
}

func TestPartialExitTriggersAtR(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.PartialExitsEnabled = true
	cfg.PartialTrigger1R = 1.0
	cfg.PartialTrigger1Pct = 0.50
	cfg.PartialTrigger2R = 2.0
	cfg.PartialTrigger2Pct = 0.50

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Create a position that reached 1R
	pos := domain.Position{
		Symbol:           "TEST",
		Side:             domain.DirectionLong,
		Quantity:         100,
		OriginalQuantity: 100,
		PartialsExecuted: 0,
		AvgPrice:         50.0,
		StopPrice:        48.5,
		RiskPerShare:     1.5,
		EntryATR:         1.0,
		Playbook:         "breakout",
		HighestPrice:     51.5,
		LowestPrice:      49.5,
		OpenedAt:         ts.Add(-5 * time.Minute),
	}

	// Price at 1R: 50 + 1.5 = 51.5
	tick := domain.Tick{
		Symbol:    "TEST",
		Price:     51.5,
		Timestamp: ts,
	}

	reason, shouldExit := s.checkExitConditions(pos, tick)
	if !shouldExit || reason != "partial-1" {
		t.Errorf("expected partial-1 exit at 1R, got reason=%q shouldExit=%v", reason, shouldExit)
	}

	// After partial 1 executed, check partial 2
	pos.PartialsExecuted = 1
	pos.Quantity = 50
	tick.Price = 53.0 // 2R = 50 + 2*1.5 = 53.0

	reason2, shouldExit2 := s.checkExitConditions(pos, tick)
	if !shouldExit2 || reason2 != "partial-2" {
		t.Errorf("expected partial-2 exit at 2R, got reason=%q shouldExit=%v", reason2, shouldExit2)
	}
}

func TestAdaptiveTrailFactorBullishLong(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.AdaptiveTrailEnabled = true

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	pos := domain.Position{
		Side:         domain.DirectionLong,
		MarketRegime: domain.RegimeBullish,
	}
	factor := s.volRegimeTrailFactor(pos)
	if factor != 1.2 {
		t.Errorf("bullish long trail factor = %.2f, want 1.2", factor)
	}

	pos.Side = domain.DirectionShort
	factor = s.volRegimeTrailFactor(pos)
	if factor != 0.8 {
		t.Errorf("bullish short trail factor = %.2f, want 0.8", factor)
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
