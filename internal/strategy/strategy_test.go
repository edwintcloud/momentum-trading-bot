package strategy

import (
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func inSessionTime() time.Time {
	return time.Date(2026, 3, 13, 14, 0, 0, 0, time.UTC)
}

func TestStrategyCreatesEntrySignal(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:               "HUMA",
		Price:                4.20,
		Open:                 4.00,
		HighOfDay:            4.21,
		GapPercent:           21,
		RelativeVolume:       6.4,
		PriceVsOpenPct:       5.0,
		DistanceFromHighPct:  0.24,
		OneMinuteReturnPct:   0.8,
		ThreeMinuteReturnPct: 1.7,
		VolumeRate:           1.9,
		MinutesSinceOpen:     18,
		Score:                22,
		Timestamp:            at,
	})
	if !ok {
		t.Fatal("expected strategy to emit entry signal")
	}
	if signal.Side != "buy" {
		t.Fatalf("unexpected side: %s", signal.Side)
	}
	if signal.Quantity <= 0 {
		t.Fatal("expected positive quantity")
	}
}

func TestStrategyAllowsPullbackAndGoWhenBroaderFollowThroughIsStrong(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "HUMA",
		Price:                4.20,
		Open:                 4.00,
		HighOfDay:            4.21,
		GapPercent:           21,
		RelativeVolume:       7.2,
		PriceVsOpenPct:       5.0,
		DistanceFromHighPct:  0.24,
		OneMinuteReturnPct:   0.03,
		ThreeMinuteReturnPct: 1.10,
		VolumeRate:           1.45,
		MinutesSinceOpen:     18,
		Score:                22,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected pullback-and-go setup to pass, got %s", reason)
	}
}

func TestStrategyRejectsWhenAllFollowThroughSignalsAreWeak(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "HUMA",
		Price:                4.20,
		Open:                 4.00,
		HighOfDay:            4.23,
		GapPercent:           21,
		RelativeVolume:       5.4,
		PriceVsOpenPct:       5.0,
		DistanceFromHighPct:  0.71,
		OneMinuteReturnPct:   0.02,
		ThreeMinuteReturnPct: 0.10,
		VolumeRate:           0.95,
		MinutesSinceOpen:     18,
		Score:                18,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected weak follow-through setup to be blocked")
	}
	if reason != "weak-follow-through" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyAllowsStrongIntradaySqueezeEvenWhenFarFromOpen(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "SQUEEZE",
		Price:                7.45,
		Open:                 5.40,
		HighOfDay:            7.48,
		GapPercent:           2.1,
		RelativeVolume:       9.5,
		PriceVsOpenPct:       37.96,
		DistanceFromHighPct:  0.40,
		OneMinuteReturnPct:   0.35,
		ThreeMinuteReturnPct: 1.40,
		VolumeRate:           1.45,
		MinutesSinceOpen:     170,
		Score:                20,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected strong intraday squeeze to pass, got %s", reason)
	}
}

func TestStrategyAllowsStrongReclaimBelowHigh(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.EntryModelEnabled = false
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "RECLAIM",
		Price:                7.35,
		Open:                 6.10,
		HighOfDay:            7.48,
		GapPercent:           3.0,
		RelativeVolume:       8.4,
		PriceVsOpenPct:       20.49,
		DistanceFromHighPct:  1.77,
		OneMinuteReturnPct:   0.28,
		ThreeMinuteReturnPct: 1.05,
		VolumeRate:           1.32,
		MinutesSinceOpen:     145,
		Score:                18,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected strong reclaim setup to pass, got %s", reason)
	}
}

func TestStrategyAllowsStrongSqueezeWithFlatModelPrediction(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	strat.SetEntryModel(LinearModel{Name: "flat-test", Weights: map[string]float64{}})
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "SQUEEZE",
		Price:                7.35,
		Open:                 5.40,
		HighOfDay:            7.48,
		GapPercent:           2.0,
		RelativeVolume:       9.0,
		PriceVsOpenPct:       36.11,
		DistanceFromHighPct:  1.77,
		OneMinuteReturnPct:   0.22,
		ThreeMinuteReturnPct: 1.10,
		VolumeRate:           1.45,
		MinutesSinceOpen:     150,
		Score:                19.5,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected strong squeeze to survive soft model gate, got %s", reason)
	}
}

func TestStrategyBlocksImmediateReentryAfterLoss(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "DTCK",
		Side:     "buy",
		Price:    3.20,
		Quantity: 100,
		FilledAt: at.Add(-40 * time.Minute),
	})
	book.MarkPriceAt("DTCK", 3.00, at.Add(-29*time.Minute))
	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "DTCK",
		Price:     3.00,
		HighOfDay: 3.25,
		Timestamp: at.Add(-29 * time.Minute),
	})
	if !ok || signal.Reason != "stop-loss" {
		t.Fatalf("expected setup loss to register stop-loss, got %+v ok=%t", signal, ok)
	}
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "DTCK",
		Side:     "sell",
		Price:    signal.Price,
		Quantity: signal.Quantity,
		Reason:   signal.Reason,
		FilledAt: signal.Timestamp,
	})

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "DTCK",
		Price:                3.18,
		Open:                 2.90,
		HighOfDay:            3.20,
		GapPercent:           9.5,
		RelativeVolume:       10.5,
		PriceVsOpenPct:       9.66,
		DistanceFromHighPct:  0.63,
		OneMinuteReturnPct:   0.42,
		ThreeMinuteReturnPct: 1.20,
		VolumeRate:           1.50,
		MinutesSinceOpen:     55,
		Score:                19,
		Timestamp:            at.Add(-10 * time.Minute),
	})
	if ok {
		t.Fatal("expected immediate reentry after loss to be blocked")
	}
	if reason != "post-loss-cooldown" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyCapsEntriesPerSymbolPerDay(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	candidate := domain.Candidate{
		Symbol:               "DTCK",
		Price:                3.18,
		Open:                 2.90,
		HighOfDay:            3.20,
		GapPercent:           9.5,
		RelativeVolume:       10.5,
		PriceVsOpenPct:       9.66,
		DistanceFromHighPct:  0.63,
		OneMinuteReturnPct:   0.42,
		ThreeMinuteReturnPct: 1.20,
		VolumeRate:           1.50,
		MinutesSinceOpen:     55,
		Score:                19,
	}

	for i, ts := range []time.Time{at.Add(-3 * time.Hour), at.Add(-2 * time.Hour)} {
		next := candidate
		next.Timestamp = ts
		if _, ok, reason := strat.EvaluateCandidateDetailed(next); !ok {
			t.Fatalf("expected entry %d to pass, got %s", i+1, reason)
		}
	}

	third := candidate
	third.Timestamp = at
	_, ok, reason := strat.EvaluateCandidateDetailed(third)
	if ok {
		t.Fatal("expected third same-day signal for symbol to be blocked")
	}
	if reason != "symbol-daily-cap" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyBlocksWeakSetupWithFlatModelPrediction(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	strat.SetEntryModel(LinearModel{Name: "flat-test", Weights: map[string]float64{}})
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "WEAK",
		Price:                4.20,
		Open:                 4.00,
		HighOfDay:            4.21,
		GapPercent:           14,
		RelativeVolume:       5.8,
		PriceVsOpenPct:       5.0,
		DistanceFromHighPct:  0.24,
		OneMinuteReturnPct:   0.12,
		ThreeMinuteReturnPct: 0.52,
		VolumeRate:           1.05,
		MinutesSinceOpen:     45,
		Score:                14,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected marginal setup to remain blocked by model gate")
	}
	if reason != "model-threshold" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsExhaustedMoveFarFromOpen(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "FADER",
		Price:                9.90,
		Open:                 6.00,
		HighOfDay:            10.10,
		GapPercent:           2.0,
		RelativeVolume:       6.2,
		PriceVsOpenPct:       65.0,
		DistanceFromHighPct:  2.02,
		OneMinuteReturnPct:   0.02,
		ThreeMinuteReturnPct: 0.20,
		VolumeRate:           1.01,
		MinutesSinceOpen:     160,
		Score:                14,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected exhausted extended move to be blocked")
	}
	if reason != "too-extended-from-open" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyCreatesExitSignalOnStopLoss(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "RKLB",
		Side:     "buy",
		Price:    10,
		Quantity: 100,
		FilledAt: at.Add(-time.Minute),
	})
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     9.40,
		HighOfDay: 10.50,
		Timestamp: at,
	})
	if !ok {
		t.Fatal("expected stop-loss exit")
	}
	if signal.Side != "sell" || signal.Reason != "stop-loss" {
		t.Fatalf("unexpected exit signal: %+v", signal)
	}
}

func TestStrategyUsesEffectiveCapitalForSizing(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50500)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:               "HUMA",
		Price:                10.00,
		Open:                 9.60,
		HighOfDay:            10.05,
		GapPercent:           21,
		RelativeVolume:       6.4,
		PriceVsOpenPct:       4.17,
		DistanceFromHighPct:  0.50,
		OneMinuteReturnPct:   0.7,
		ThreeMinuteReturnPct: 1.5,
		VolumeRate:           2.1,
		MinutesSinceOpen:     12,
		Score:                22,
		Timestamp:            at,
	})
	if !ok {
		t.Fatal("expected strategy to emit entry signal")
	}
	if signal.Quantity != 1000 {
		t.Fatalf("expected quantity 1000 using broker equity sizing, got %d", signal.Quantity)
	}
}

func TestStrategyUsesTrailingStopInsteadOfImmediateProfitTarget(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "RKLB",
		Side:     "buy",
		Price:    10,
		Quantity: 100,
		FilledAt: at.Add(-3 * time.Minute),
	})
	book.MarkPriceAt("RKLB", 11.50, at.Add(-2*time.Minute))
	strat := NewStrategy(cfg, book, runtimeState)

	if signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     11.20,
		HighOfDay: 11.50,
		Timestamp: at.Add(-time.Minute),
	}); ok {
		t.Fatalf("expected no immediate take-profit exit, got %+v", signal)
	}

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     10.70,
		HighOfDay: 11.50,
		Timestamp: at,
	})
	if !ok {
		t.Fatal("expected trailing-stop exit after pullback from the high")
	}
	if signal.Reason != "trailing-stop" {
		t.Fatalf("expected trailing-stop reason, got %+v", signal)
	}
}

func TestStrategyExitsFailedBreakoutBeforeFullStop(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "RKLB",
		Side:     "buy",
		Price:    10,
		Quantity: 100,
		FilledAt: at.Add(-13 * time.Minute),
	})
	book.MarkPriceAt("RKLB", 10.05, at.Add(-8*time.Minute))
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     9.90,
		HighOfDay: 10.10,
		Timestamp: at,
	})
	if !ok {
		t.Fatal("expected failed-breakout exit")
	}
	if signal.Reason != "failed-breakout" {
		t.Fatalf("expected failed-breakout reason, got %+v", signal)
	}
}

func TestStrategyProtectsNearBreakEvenAfterInitialPop(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:   "RKLB",
		Side:     "buy",
		Price:    10,
		Quantity: 100,
		FilledAt: at.Add(-10 * time.Minute),
	})
	book.MarkPriceAt("RKLB", 10.25, at.Add(-4*time.Minute))
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     10.01,
		HighOfDay: 10.25,
		Timestamp: at,
	})
	if !ok {
		t.Fatal("expected break-even protection exit")
	}
	if signal.Reason != "break-even-stop" {
		t.Fatalf("expected break-even-stop reason, got %+v", signal)
	}
}

func TestStrategyBlocksEntriesOutsideTradableSession(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "HUMA",
		Price:                4.20,
		Open:                 4.00,
		HighOfDay:            4.21,
		GapPercent:           21,
		RelativeVolume:       6.4,
		PriceVsOpenPct:       5.0,
		DistanceFromHighPct:  0.24,
		OneMinuteReturnPct:   0.8,
		ThreeMinuteReturnPct: 1.7,
		VolumeRate:           1.9,
		MinutesSinceOpen:     18,
		Score:                22,
		Timestamp:            time.Date(2026, 3, 13, 6, 30, 0, 0, time.UTC),
	})
	if ok {
		t.Fatal("expected outside-session candidate to be blocked")
	}
	if reason != "outside-session" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}
