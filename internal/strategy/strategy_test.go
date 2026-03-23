package strategy

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/risk"
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

func TestEntryDeadlineBlocksLateEntry(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryDeadlineMinutesAfterOpen = 120 // 2 hours after open
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	loc := markethours.Location()

	// 3 hours after open (11:30+1h = 12:30 ET) — should be blocked
	lateTime := time.Date(2026, 3, 23, 12, 30, 0, 0, loc)
	candidate := domain.Candidate{
		Symbol:          "LATE",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       lateTime,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected entry to be blocked past deadline (180 min > 120 min)")
	}

	// 30 min after open (10:00 ET) — should pass
	earlyTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	candidate.Symbol = "EARLY"
	candidate.Timestamp = earlyTime
	_, ok = s.EvaluateCandidate(candidate)
	if !ok {
		t.Error("expected entry to pass before deadline (30 min < 120 min)")
	}
}

func TestEntryDeadlineDisabledByDefault(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryDeadlineMinutesAfterOpen = 0 // disabled
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	loc := markethours.Location()

	// Late afternoon — should still pass when disabled
	lateTime := time.Date(2026, 3, 23, 15, 0, 0, 0, loc)
	candidate := domain.Candidate{
		Symbol:          "LATE",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       lateTime,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if !ok {
		t.Error("expected entry to pass when deadline is disabled (0)")
	}
}

func TestRiskRewardPreCheckRejectsBadRR(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinRiskRewardRatio = 2.0 // require 2:1 R:R
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Long with price near HOD: reward = HOD - Price = 10.5 - 10.0 = 0.5
	// Risk = ATR * 1.5 = 0.5 * 1.5 = 0.75
	// R:R = 0.5 / 0.75 = 0.67 < 2.0 — should be rejected
	candidate := domain.Candidate{
		Symbol:          "BADRR",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		HighOfDay:       10.5,
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected trade to be rejected when R:R (0.67) < min (2.0)")
	}
}

func TestRiskRewardPreCheckAcceptsGoodRR(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinRiskRewardRatio = 2.0
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Long with HOD well above price: reward = 15.0 - 10.0 = 5.0
	// Risk = ATR * 1.5 = 0.5 * 1.5 = 0.75
	// R:R = 5.0 / 0.75 = 6.67 >= 2.0 — should pass
	candidate := domain.Candidate{
		Symbol:          "GOODRR",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		HighOfDay:       15.0,
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if !ok {
		t.Error("expected trade to pass when R:R (6.67) >= min (2.0)")
	}
}

func TestRiskRewardFallbackToATR(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinRiskRewardRatio = 2.0
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Long where price is at/above HOD — falls back to ATR*2.0 estimate
	// Reward = ATR * 2.0 = 0.5 * 2.0 = 1.0
	// Risk = ATR * 1.5 = 0.75
	// R:R = 1.0 / 0.75 = 1.33 < 2.0 — should be rejected
	candidate := domain.Candidate{
		Symbol:          "ATHOD",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		HighOfDay:       10.0, // price at HOD
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected trade with ATR fallback R:R (1.33) to be rejected when min is 2.0")
	}
}

func TestMidDayScoreMultiplierConfigurable(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.TimeOfDayEnabled = true
	cfg.MidDayScoreMultiplier = 2.0 // very strict midday
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 2.0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	loc := markethours.Location()

	// MidDay candidate with score 3.5 — blocked by 2.0x multiplier (threshold = 4.0)
	midDayTime := time.Date(2026, 3, 23, 13, 0, 0, 0, loc)
	candidate := domain.Candidate{
		Symbol:          "MIDDAY",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		ATR:             0.5,
		Score:           3.5,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       midDayTime,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if ok {
		t.Error("expected midday candidate with score 3.5 to be blocked (threshold 2.0*2.0=4.0)")
	}

	// Score 5.0 should pass even with 2.0x multiplier
	candidate.Symbol = "MIDDAY2"
	candidate.Score = 5.0
	_, ok = s.EvaluateCandidate(candidate)
	if !ok {
		t.Error("expected midday candidate with score 5.0 to pass (threshold 4.0)")
	}
}

func TestMidDayMultiplierDefaultBehavior(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.TimeOfDayEnabled = true
	cfg.MidDayScoreMultiplier = 0 // use hardcoded default (1.15)
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 2.0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	loc := markethours.Location()

	// MidDay candidate with score 2.4 — should pass with default 1.15x (threshold = 2.3)
	midDayTime := time.Date(2026, 3, 23, 13, 0, 0, 0, loc)
	candidate := domain.Candidate{
		Symbol:          "MIDDEF",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		ATR:             0.5,
		Score:           2.4,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       midDayTime,
	}

	_, ok := s.EvaluateCandidate(candidate)
	if !ok {
		t.Error("expected midday candidate with score 2.4 to pass with default 1.15x multiplier (threshold 2.3)")
	}
}

// Diagnostic fix tests

func TestDiagnosticMarketClosed(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	loc := markethours.Location()
	// Premarket time (8:00 AM ET) — market not open
	premarketTime := time.Date(2026, 3, 23, 8, 0, 0, 0, loc)

	candidate := domain.Candidate{
		Symbol:          "PREMARKET",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       premarketTime,
	}

	decision := s.EvaluateCandidateDecision(candidate)
	if decision.Emit {
		t.Fatal("expected premarket candidate to not emit")
	}
	if decision.Reason != "market-closed" {
		t.Errorf("expected reason %q, got %q", "market-closed", decision.Reason)
	}
}

func TestDiagnosticRegimeGated(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RegimeGatingEnabled = true
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()
	candidate := domain.Candidate{
		Symbol:           "GATED",
		Direction:        domain.DirectionLong,
		Price:            50.0,
		ATR:              1.0,
		Score:            5.0,
		Playbook:         "breakout",
		MarketRegime:     domain.RegimeBearish,
		RegimeConfidence: 0.8,
		GapPercent:       5.0,
		RelativeVolume:   3.0,
		PreMarketVolume:  60000,
		Timestamp:        ts,
	}

	decision := s.EvaluateCandidateDecision(candidate)
	if decision.Emit {
		t.Fatal("expected regime-gated candidate to not emit")
	}
	if decision.Reason != "regime-gated" {
		t.Errorf("expected reason %q, got %q", "regime-gated", decision.Reason)
	}
}

func TestDiagnosticPastEntryDeadline(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryDeadlineMinutesAfterOpen = 120
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	loc := markethours.Location()
	// 3 hours after open
	lateTime := time.Date(2026, 3, 23, 12, 30, 0, 0, loc)

	candidate := domain.Candidate{
		Symbol:          "LATE",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       lateTime,
	}

	decision := s.EvaluateCandidateDecision(candidate)
	if decision.Emit {
		t.Fatal("expected past-deadline candidate to not emit")
	}
	if decision.Reason != "past-entry-deadline" {
		t.Errorf("expected reason %q, got %q", "past-entry-deadline", decision.Reason)
	}
}

func TestDiagnosticExistingPosition(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Open a position first
	pm.OpenPosition(domain.ExecutionReport{
		Symbol:       "EXISTING",
		Side:         domain.SideBuy,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionLong,
		Price:        10.0,
		Quantity:     100,
		StopPrice:    9.0,
		RiskPerShare: 1.0,
		FilledAt:     ts,
	})

	candidate := domain.Candidate{
		Symbol:          "EXISTING",
		Direction:       domain.DirectionLong,
		Price:           10.5,
		ATR:             0.5,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       ts.Add(time.Minute),
	}

	decision := s.EvaluateCandidateDecision(candidate)
	if decision.Emit {
		t.Fatal("expected existing-position candidate to not emit")
	}
	if decision.Reason != "existing-position" {
		t.Errorf("expected reason %q, got %q", "existing-position", decision.Reason)
	}
}

func TestDiagnosticLowScore(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 5.0 // high threshold

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	candidate := domain.Candidate{
		Symbol:          "LOWSCORE",
		Direction:       domain.DirectionLong,
		Price:           10.0,
		ATR:             0.5,
		Score:           2.0, // below threshold
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  3.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	decision := s.EvaluateCandidateDecision(candidate)
	if decision.Emit {
		t.Fatal("expected low-score candidate to not emit")
	}
	if decision.Reason != "low-score" {
		t.Errorf("expected reason %q, got %q", "low-score", decision.Reason)
	}
}

func TestPositionSizeFloorBumpsUp(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 77000
	cfg.RiskPerTradePct = 0.005
	cfg.MinPositionNotionalPct = 0.02 // 2% floor = $1540 minimum
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.VolTargetSizingEnabled = true
	cfg.TargetVolPerPosition = 0.02
	cfg.DefaultVolatility = 0.3
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	// Create a vol estimator that produces very high vol (to force small qty)
	volEst := risk.NewVolatilityEstimator(8.0) // 800% default vol
	s := NewStrategy(cfg, pm, runtimeState, volEst)

	ts := marketOpenTime()
	candidate := domain.Candidate{
		Symbol:          "ANNA",
		Direction:       domain.DirectionLong,
		Price:           6.18,
		ATR:             0.23,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  6.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	signal, ok := s.EvaluateCandidate(candidate)
	if !ok {
		t.Fatal("expected signal for ANNA candidate")
	}

	// Floor: 77000 * 0.02 / 6.18 = ceil(249.2) = 250 shares minimum
	minQty := int64(250) // ceil(1540 / 6.18)
	if signal.Quantity < minQty {
		t.Errorf("quantity %d should be >= min floor %d ($1540 notional)", signal.Quantity, minQty)
	}
}

func TestPositionSizeNoFloorWhenDisabled(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 77000
	cfg.RiskPerTradePct = 0.005
	cfg.MinPositionNotionalPct = 0 // disabled
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.VolTargetSizingEnabled = true
	cfg.TargetVolPerPosition = 0.02
	cfg.DefaultVolatility = 0.3
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	volEst := risk.NewVolatilityEstimator(8.0) // very high vol
	s := NewStrategy(cfg, pm, runtimeState, volEst)

	ts := marketOpenTime()
	candidate := domain.Candidate{
		Symbol:          "ANNA",
		Direction:       domain.DirectionLong,
		Price:           6.18,
		ATR:             0.23,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  6.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	signal, ok := s.EvaluateCandidate(candidate)
	if !ok {
		t.Fatal("expected signal")
	}

	// With 800% vol and vol target sizing, qty should be tiny
	// volBasedQty = (77000 * 0.02) / (8.0 * 6.18) = 31 shares
	if signal.Quantity > 50 {
		t.Errorf("without floor, quantity (%d) should be small due to high vol cap", signal.Quantity)
	}
}

func TestFallbackStopLongTriggersWhenStopIsZero(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryATRPercentFallback = 1.5 // 1.5%
	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Position with StopPrice = 0 (simulates broker-seeded without risk metadata)
	pos := domain.Position{
		Symbol:       "NOSTP",
		Side:         domain.DirectionLong,
		Quantity:     100,
		AvgPrice:     10.00,
		StopPrice:    0, // Missing!
		RiskPerShare: 0,
		EntryATR:     0,
		Playbook:     "breakout",
		HighestPrice: 10.05,
		LowestPrice:  9.90,
		OpenedAt:     ts.Add(-5 * time.Minute),
	}

	// fallbackRisk = 10.00 * 1.5 / 100 = 0.15
	// computedStop = 10.00 - 0.15 = 9.85
	// Price at 9.84 should trigger stop-loss-fallback
	tick := domain.Tick{
		Symbol:    "NOSTP",
		Price:     9.84,
		Timestamp: ts,
	}

	reason, shouldExit := s.checkExitConditions(pos, tick)
	if !shouldExit || reason != "stop-loss-fallback" {
		t.Errorf("expected stop-loss-fallback, got reason=%q shouldExit=%v", reason, shouldExit)
	}
}

func TestFallbackStopLongDoesNotTriggerAboveStop(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryATRPercentFallback = 1.5
	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	pos := domain.Position{
		Symbol:       "NOSTP",
		Side:         domain.DirectionLong,
		Quantity:     100,
		AvgPrice:     10.00,
		StopPrice:    0,
		RiskPerShare: 0,
		EntryATR:     0,
		Playbook:     "breakout",
		HighestPrice: 10.10,
		LowestPrice:  9.90,
		OpenedAt:     ts.Add(-5 * time.Minute),
	}

	// Price at 9.90 is above computed stop of 9.85 — should not trigger fallback
	tick := domain.Tick{
		Symbol:    "NOSTP",
		Price:     9.90,
		Timestamp: ts,
	}

	reason, shouldExit := s.checkExitConditions(pos, tick)
	if shouldExit && reason == "stop-loss-fallback" {
		t.Error("should not trigger fallback stop when price is above computed stop")
	}
}

func TestFallbackStopShortTriggersWhenStopIsZero(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryATRPercentFallback = 2.0 // 2%
	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	pos := domain.Position{
		Symbol:       "NOSTP",
		Side:         domain.DirectionShort,
		Quantity:     100,
		AvgPrice:     50.00,
		StopPrice:    0, // Missing!
		RiskPerShare: 0,
		EntryATR:     0,
		Playbook:     "reversal",
		HighestPrice: 50.50,
		LowestPrice:  49.50,
		OpenedAt:     ts.Add(-5 * time.Minute),
	}

	// fallbackRisk = 50.00 * 2.0 / 100 = 1.00
	// computedStop = 50.00 + 1.00 = 51.00
	// Price at 51.10 should trigger stop-loss-fallback
	tick := domain.Tick{
		Symbol:    "NOSTP",
		Price:     51.10,
		Timestamp: ts,
	}

	reason, shouldExit := s.checkExitConditions(pos, tick)
	if !shouldExit || reason != "stop-loss-fallback" {
		t.Errorf("expected stop-loss-fallback for short, got reason=%q shouldExit=%v", reason, shouldExit)
	}
}

func TestFallbackStopUsesDefaultWhenATRPercentZero(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryATRPercentFallback = 0 // Not configured
	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	pos := domain.Position{
		Symbol:       "NOSTP",
		Side:         domain.DirectionLong,
		Quantity:     100,
		AvgPrice:     10.00,
		StopPrice:    0,
		RiskPerShare: 0,
		Playbook:     "breakout",
		HighestPrice: 10.05,
		LowestPrice:  9.90,
		OpenedAt:     ts.Add(-5 * time.Minute),
	}

	// Default fallback: 2% of $10.00 = $0.20
	// computedStop = 10.00 - 0.20 = 9.80
	// Price at 9.79 should trigger
	tick := domain.Tick{
		Symbol:    "NOSTP",
		Price:     9.79,
		Timestamp: ts,
	}

	reason, shouldExit := s.checkExitConditions(pos, tick)
	if !shouldExit || reason != "stop-loss-fallback" {
		t.Errorf("expected stop-loss-fallback with 2%% default, got reason=%q shouldExit=%v", reason, shouldExit)
	}
}

func TestNormalStopStillWorksWhenSet(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	s := NewStrategy(cfg, pm, runtimeState)

	ts := marketOpenTime()

	// Position WITH a proper stop — should use normal stop, not fallback
	pos := domain.Position{
		Symbol:       "HASSTOP",
		Side:         domain.DirectionLong,
		Quantity:     100,
		AvgPrice:     10.00,
		StopPrice:    9.50, // Properly set
		RiskPerShare: 0.50,
		EntryATR:     0.40,
		Playbook:     "breakout",
		HighestPrice: 10.10,
		LowestPrice:  9.60,
		OpenedAt:     ts.Add(-5 * time.Minute),
	}

	tick := domain.Tick{
		Symbol:    "HASSTOP",
		Price:     9.45,
		Timestamp: ts,
	}

	reason, shouldExit := s.checkExitConditions(pos, tick)
	if !shouldExit || reason != "stop-loss" {
		t.Errorf("expected normal stop-loss, got reason=%q shouldExit=%v", reason, shouldExit)
	}
}

func TestVolTargetSizingDisabledNoVolCap(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.StartingCapital = 77000
	cfg.RiskPerTradePct = 0.01
	cfg.VolTargetSizingEnabled = false // disabled for momentum
	cfg.RegimeGatingEnabled = false
	cfg.ConfidenceSizingEnabled = false
	cfg.MinEntryScore = 0

	runtimeState := runtime.NewState()
	pm := portfolio.NewManager(cfg)
	volEst := risk.NewVolatilityEstimator(8.0) // high vol — shouldn't matter
	s := NewStrategy(cfg, pm, runtimeState, volEst)

	ts := marketOpenTime()
	candidate := domain.Candidate{
		Symbol:          "ANNA",
		Direction:       domain.DirectionLong,
		Price:           6.18,
		ATR:             0.23,
		Score:           5.0,
		Playbook:        "breakout",
		GapPercent:      5.0,
		RelativeVolume:  6.0,
		PreMarketVolume: 60000,
		Timestamp:       ts,
	}

	signal, ok := s.EvaluateCandidate(candidate)
	if !ok {
		t.Fatal("expected signal")
	}

	// riskBudget = 77000 * 0.01 = 770
	// riskPerShare = ATR * 1.5 = 0.23 * 1.5 = 0.345
	// quantity = 770 / 0.345 = 2231
	// With vol disabled, should be large
	if signal.Quantity < 1000 {
		t.Errorf("with VolTargetSizing disabled, quantity (%d) should be large", signal.Quantity)
	}
}
