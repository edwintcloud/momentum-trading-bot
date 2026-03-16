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

func TestStrategyRejectsSecondaryVolumeSetup(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "SECONDARY",
		Price:                5.20,
		Open:                 4.80,
		HighOfDay:            5.22,
		GapPercent:           11.0,
		RelativeVolume:       6.2,
		PriceVsOpenPct:       8.33,
		DistanceFromHighPct:  0.38,
		OneMinuteReturnPct:   0.72,
		ThreeMinuteReturnPct: 1.65,
		VolumeRate:           1.45,
		VolumeLeaderPct:      0.12,
		MinutesSinceOpen:     40,
		Score:                24.0,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected secondary-volume setup to be blocked")
	}
	if reason != "secondary-volume" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsLowLeaderShareEvenWithStrongStats(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "LITX",
		Price:                76.48,
		Open:                 72.00,
		HighOfDay:            76.48,
		GapPercent:           10.0,
		RelativeVolume:       7.7,
		PriceVsOpenPct:       6.22,
		DistanceFromHighPct:  0.0,
		OneMinuteReturnPct:   3.35,
		ThreeMinuteReturnPct: 5.43,
		VolumeRate:           3.19,
		VolumeLeaderPct:      0.02,
		MinutesSinceOpen:     32,
		Score:                70.48,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected low leader-share setup to be blocked")
	}
	if reason != "secondary-volume" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyAllowsLeaderVolumeSetup(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "LEADER",
		Price:                5.20,
		Open:                 4.80,
		HighOfDay:            5.22,
		GapPercent:           11.0,
		RelativeVolume:       6.2,
		PriceVsOpenPct:       8.33,
		DistanceFromHighPct:  0.38,
		OneMinuteReturnPct:   0.28,
		ThreeMinuteReturnPct: 1.10,
		VolumeRate:           1.45,
		VolumeLeaderPct:      0.92,
		MinutesSinceOpen:     40,
		Score:                18.5,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected leader-volume setup to pass, got %s", reason)
	}
}

func TestStrategyRejectsParabolicEarlyPremarketSpike(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := time.Date(2026, 3, 13, 9, 8, 0, 0, time.UTC)

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "OIO",
		Price:                18.52,
		Open:                 12.40,
		HighOfDay:            18.52,
		GapPercent:           3.0,
		RelativeVolume:       9.56,
		PriceVsOpenPct:       49.35,
		DistanceFromHighPct:  0,
		OneMinuteReturnPct:   48.99,
		ThreeMinuteReturnPct: 48.99,
		VolumeRate:           4.49,
		VolumeLeaderPct:      0.95,
		MinutesSinceOpen:     0,
		Score:                471.36,
		Volume:               250_000,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected parabolic early premarket entry to be blocked")
	}
	if reason != "parabolic-spike" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsThinEarlyPremarketSetup(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := time.Date(2026, 3, 13, 9, 20, 0, 0, time.UTC)

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "THIN",
		Price:                3.50,
		Open:                 3.20,
		HighOfDay:            3.52,
		GapPercent:           12.0,
		RelativeVolume:       10.0,
		PriceVsOpenPct:       9.38,
		DistanceFromHighPct:  0.57,
		OneMinuteReturnPct:   0.45,
		ThreeMinuteReturnPct: 1.20,
		VolumeRate:           1.60,
		VolumeLeaderPct:      0.95,
		MinutesSinceOpen:     0,
		Score:                25.0,
		Volume:               300_000,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected thin early premarket setup to be blocked")
	}
	if reason != "thin-premarket" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsOpeningParabolicSetup(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := time.Date(2026, 3, 13, 13, 31, 0, 0, time.UTC)

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "CONL",
		Price:                10.13,
		Open:                 8.80,
		HighOfDay:            10.18,
		GapPercent:           15.0,
		RelativeVolume:       52.0,
		PriceVsOpenPct:       15.11,
		DistanceFromHighPct:  0.49,
		OneMinuteReturnPct:   2.75,
		ThreeMinuteReturnPct: 4.30,
		VolumeRate:           2.10,
		MinutesSinceOpen:     1,
		Score:                48.0,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected opening-session parabolic setup to be blocked")
	}
	if reason != "opening-parabolic" {
		t.Fatalf("unexpected block reason: %s", reason)
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

func TestStrategySizesPremarketEntriesMoreConservatively(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50500)
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:               "BIAF",
		Price:                2.00,
		Open:                 1.70,
		HighOfDay:            2.02,
		GapPercent:           12.0,
		RelativeVolume:       15.0,
		PriceVsOpenPct:       17.65,
		DistanceFromHighPct:  0.99,
		OneMinuteReturnPct:   1.2,
		ThreeMinuteReturnPct: 2.6,
		VolumeRate:           1.8,
		MinutesSinceOpen:     0,
		Score:                28,
		Timestamp:            time.Date(2026, 3, 13, 12, 20, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected conservative premarket setup to pass")
	}
	if signal.Quantity != 2799 {
		t.Fatalf("expected premarket low-priced sizing to be scaled down to 2799 shares, got %d", signal.Quantity)
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
