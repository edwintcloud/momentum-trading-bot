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

func testStrategyConfig() config.TradingConfig {
	cfg := config.DefaultTradingConfig()
	cfg.EntryModelEnabled = false
	cfg.MinFifteenMinuteReturnPct = 0.00
	return cfg
}

func testExecutionReport(symbol string, price float64, quantity int64, filledAt time.Time) domain.ExecutionReport {
	riskPerShare := price * 0.05
	return domain.ExecutionReport{
		Symbol:       symbol,
		Side:         "buy",
		Price:        price,
		Quantity:     quantity,
		StopPrice:    price - riskPerShare,
		RiskPerShare: riskPerShare,
		EntryATR:     price * 0.03,
		SetupType:    "consolidation-breakout",
		FilledAt:     filledAt,
	}
}

func TestStrategyCreatesEntrySignal(t *testing.T) {
	cfg := testStrategyConfig()
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
		FifteenMinuteReturnPct: 5.0,
		VolumeRate:           1.9,
		MinutesSinceOpen:     18,
		Score:                22,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		SetupHigh:             10.05,
		SetupLow:              9.90,
		SetupType:             "opening-range-breakout",
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
	cfg := testStrategyConfig()
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
		FifteenMinuteReturnPct: 3.5,
		PriceVsVWAPPct:       2.0,
		VolumeRate:           1.45,
		MinutesSinceOpen:     18,
		Score:                22,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		SetupHigh:            10.15,
		SetupLow:             9.95,
		SetupType:            "pullback-and-go",
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected pullback-and-go setup to pass, got %s", reason)
	}
}

func TestStrategyRejectsWhenAllFollowThroughSignalsAreWeak(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	candidate := domain.Candidate{
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
		PriceVsVWAPPct:       0.50,
		VolumeRate:           0.45,
		MinutesSinceOpen:     18,
		Score:                18,
		Timestamp:            at,
	}
	_, ok, reason := strat.EvaluateCandidateDetailed(candidate)
	t.Logf("strongSqueeze: %v", strat.isStrongSqueeze(candidate))
	t.Logf("hasTimingConfirmation: %v", strat.hasTimingConfirmation(candidate, strat.isStrongSqueeze(candidate)))
	t.Logf("VolumeRate: %v >= %v", candidate.VolumeRate, strat.config.MinVolumeRate)

	if ok {
		t.Logf("Setup actually passed. Expected it to be blocked.")
	}
	t.Logf("Block reason: %s", reason)
	if ok {
		t.Fatal("expected weak follow-through setup to be blocked")
	}
	if reason != "no-renewed-volume" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsBreakoutWithoutRenewedVolume(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	candidate := domain.Candidate{
		Symbol:                "DRYUP",
		Price:                 6.25,
		Open:                  5.90,
		HighOfDay:             6.30,
		GapPercent:            9.0,
		RelativeVolume:        14.0,
		PriceVsOpenPct:        5.93,
		DistanceFromHighPct:   0.79,
		OneMinuteReturnPct:    0.08,
		ThreeMinuteReturnPct:  2.10,
		VolumeRate:            0.45,
		VolumeLeaderPct:       0.95,
		LeaderRank:            1,
		MinutesSinceOpen:      65,
		ATRPct:                3.20,
		PriceVsVWAPPct:        0.50,
		BreakoutPct:           0.18,
		ConsolidationRangePct: 1.80,
		CloseOffHighPct:       18,
		SetupHigh:             6.24,
		SetupLow:              5.92,
		SetupType:             "vwap-reclaim",
		Score:                 24,
		Timestamp:             at,
	}
	_, ok, reason := strat.EvaluateCandidateDetailed(candidate)
	t.Logf("strongSqueeze: %v", strat.isStrongSqueeze(candidate))
	t.Logf("hasTimingConfirmation: %v", strat.hasTimingConfirmation(candidate, strat.isStrongSqueeze(candidate)))
	t.Logf("VolumeRate: %v >= %v", candidate.VolumeRate, strat.config.MinVolumeRate)
	if ok {
		t.Fatal("expected breakout without renewed volume to be blocked")
	}
	if reason != "no-renewed-volume" {
		t.Logf("Block reason was: %s", reason)
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyAllowsStrongIntradaySqueezeEvenWhenFarFromOpen(t *testing.T) {
	cfg := testStrategyConfig()
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
		FifteenMinuteReturnPct: 6.0,
		VolumeRate:           1.45,
		MinutesSinceOpen:     170,
		SetupLow:             7.30,
		Score:                20,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected strong intraday squeeze to pass, got %s", reason)
	}
}

func TestStrategyAllowsStrongReclaimBelowHigh(t *testing.T) {
	cfg := testStrategyConfig()
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
		FifteenMinuteReturnPct: 4.8,
		VolumeRate:           1.32,
		MinutesSinceOpen:     145,
		SetupLow:             7.20,
		Score:                18,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected strong reclaim setup to pass, got %s", reason)
	}
}

func TestStrategyRejectsStrongSqueezeWithFlatModelPrediction(t *testing.T) {
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
		PriceVsVWAPPct:       0.50,
		VolumeRate:           1.45,
		MinutesSinceOpen:     150,
		Score:                22.0,
		Timestamp:            at,
	})
	if ok {
		t.Fatal("expected flat-model strong squeeze to remain blocked by model gate")
	}
	if reason != "model-threshold" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsSecondaryVolumeSetup(t *testing.T) {
	cfg := testStrategyConfig()
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
		PriceVsVWAPPct:       0.50,
		VolumeRate:           1.45,
		VolumeLeaderPct:      0.001,
		LeaderRank:           56,
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
	cfg := testStrategyConfig()
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
		PriceVsVWAPPct:       0.50,
		VolumeRate:           3.19,
		VolumeLeaderPct:      0.001,
		LeaderRank:           5,
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
	cfg := testStrategyConfig()
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
		FifteenMinuteReturnPct: 5.2,
		VolumeRate:           1.45,
		VolumeLeaderPct:      0.92,
		LeaderRank:           1,
		MinutesSinceOpen:     40,
		Score:                18.5,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		SetupHigh:            5.05,
		SetupLow:             5.00,
		SetupType:            "consolidation-breakout",
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected leader-volume setup to pass, got %s", reason)
	}
}

func TestStrategyRejectsParabolicEarlyPremarketSpike(t *testing.T) {
	cfg := testStrategyConfig()
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
	if reason != "early-premarket-banned" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsThinEarlyPremarketSetup(t *testing.T) {
	cfg := testStrategyConfig()
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
	if reason != "early-premarket-banned" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsOpeningParabolicSetup(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := time.Date(2026, 3, 13, 13, 31, 0, 0, time.UTC)

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "REAL",
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

func TestStrategyAllowsReentryAfterLoss(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	book.ApplyExecution(testExecutionReport("DTCK", 3.20, 100, at.Add(-40*time.Minute)))
	book.MarkPriceAt("DTCK", 3.00, at.Add(-14*time.Minute))
	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "DTCK",
		Price:     3.00,
		HighOfDay: 3.25,
		Timestamp: at.Add(-14 * time.Minute),
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
		FifteenMinuteReturnPct: 5.0,
		VolumeRate:           1.50,
		MinutesSinceOpen:     55,
		Score:                19,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		SetupHigh:            10.05,
		SetupLow:             9.90,
		SetupType:            "opening-range-breakout",
		Timestamp:            at.Add(-5 * time.Minute),
	})
	if !ok {
		t.Fatalf("expected reentry after loss to be allowed, got %s", reason)
	}
}

func TestStrategyCapsEntriesPerSymbolPerDay(t *testing.T) {
	cfg := testStrategyConfig()
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
		FifteenMinuteReturnPct: 5.0,
		VolumeRate:           1.50,
		MinutesSinceOpen:     55,
		Score:                19,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		SetupHigh:             10.05,
		SetupLow:              9.90,
		SetupType:             "opening-range-breakout",
	}

	for i := 0; i < 5; i++ {
		next := candidate
		next.Timestamp = at.Add(time.Duration(-150+i*30) * time.Minute)
		if _, ok, reason := strat.EvaluateCandidateDetailed(next); !ok {
			t.Fatalf("expected entry %d to pass, got %s", i+1, reason)
		}
	}

	sixth := candidate
	sixth.Timestamp = at
	_, ok, reason := strat.EvaluateCandidateDetailed(sixth)
	if ok {
		t.Fatal("expected sixth same-day signal for symbol to be blocked")
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
		Symbol:                "WEAK",
		Price:                 4.20,
		Open:                  4.00,
		HighOfDay:             4.21,
		GapPercent:            14,
		RelativeVolume:        5.8,
		PriceVsOpenPct:        5.0,
		DistanceFromHighPct:   0.24,
		OneMinuteReturnPct:    0.12,
		ThreeMinuteReturnPct:  0.52,
		VolumeRate:            1.30,
		VolumeLeaderPct:       0.92,
		LeaderRank:            1,
		ATRPct:                2.40,
		PriceVsVWAPPct:        0.50,
		BreakoutPct:           0.12,
		ConsolidationRangePct: 1.4,
		CloseOffHighPct:       20,
		SetupHigh:             4.18,
		SetupLow:              4.02,
		SetupType:             "vwap-reclaim",
		MinutesSinceOpen:      45,
		Score:                 16,
		Timestamp:             at,
	})
	if ok {
		t.Fatal("expected marginal setup to remain blocked by model gate")
	}
	if reason != "model-threshold" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyRejectsExhaustedMoveFarFromOpen(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:                "FADER",
		Price:                 9.90,
		Open:                  6.00,
		HighOfDay:             10.10,
		GapPercent:            2.0,
		RelativeVolume:        6.2,
		PriceVsOpenPct:        65.0,
		DistanceFromHighPct:   2.02,
		OneMinuteReturnPct:    0.15,
		ThreeMinuteReturnPct:  0.20,
		VolumeRate:            1.25,
		VolumeLeaderPct:       0.95,
		LeaderRank:            1,
		ATRPct:                3.60,
		PriceVsVWAPPct:        0.30,
		BreakoutPct:           -0.12,
		ConsolidationRangePct: 1.6,
		CloseOffHighPct:       42,
		SetupHigh:             10.05,
		SetupLow:              9.40,
		SetupType:             "vwap-reclaim",
		MinutesSinceOpen:      160,
		Score:                 16,
		Timestamp:             at,
	})
	if ok {
		t.Fatal("expected exhausted extended move to be blocked")
	}
	if reason != "too-extended-from-open" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyCreatesExitSignalOnStopLoss(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(testExecutionReport("RKLB", 10, 100, at.Add(-time.Minute)))
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
	cfg := testStrategyConfig()
	cfg.MaxExposurePct = 1.0
	cfg.MaxOpenPositions = 1
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
		SetupLow:             9.90,
		Score:                22,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		Timestamp:            at,
	})
	if !ok {
		t.Fatal("expected strategy to emit entry signal")
	}
	if signal.Quantity != 1000 {
		t.Fatalf("expected quantity scaled by narrower stop, got %d", signal.Quantity)
	}
}

func TestStrategySizesPremarketEntriesMoreConservatively(t *testing.T) {
	cfg := testStrategyConfig()
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
		LeaderRank:           1,
		MinutesSinceOpen:     0,
		SetupLow:             1.96,
		Score:                28,
		ATR:                  0.50,
		ATRPct:               5.0,
		BreakoutPct:          0.10,
		Timestamp:            time.Date(2026, 3, 13, 12, 20, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected conservative premarket setup to pass")
	}
	if signal.Quantity != 1875 {
		t.Fatalf("expected premarket ATR-sized quantity to be scaled down to 1875 shares, got %d", signal.Quantity)
	}
}

func TestStrategyUsesHardProfitTargetOnMassiveSpike(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(testExecutionReport("RKLB", 10, 100, at.Add(-3*time.Minute)))
	book.MarkPriceAt("RKLB", 10.00, at.Add(-2*time.Minute))
	strat := NewStrategy(cfg, book, runtimeState)

	// Spike to 16.00 (12.0R if R is 0.50)
	spikeTick := domain.Tick{
		Symbol:    "RKLB",
		Price:     16.00,
		HighOfDay: 16.00,
		Timestamp: at,
	}
	signal, ok := strat.evaluateExit(spikeTick)
	if !ok || signal.Reason != "profit-target" {
		t.Fatalf("expected profit-target exit immediately, got %+v", signal)
	}
}

func TestStrategyExitsFailedBreakoutBeforeFullStop(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(testExecutionReport("RKLB", 10, 100, at.Add(-25*time.Minute)))
	book.MarkPriceAt("RKLB", 10.05, at.Add(-15*time.Minute))
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     9.49,
		BarOpen:   9.70,
		BarHigh:   9.72,
		BarLow:    8.50,
		HighOfDay: 11.50,
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
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(testExecutionReport("RKLB", 10, 100, at.Add(-10*time.Minute)))
	book.MarkPriceAt("RKLB", 10.50, at.Add(-4*time.Minute))
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "RKLB",
		Price:     9.99,
		HighOfDay: 10.50,
		Timestamp: at,
	})
	if !ok {
		t.Fatal("expected trailing protection exit after confirmation")
	}
	if signal.Reason != "trailing-stop" {
		t.Fatalf("expected trailing-stop reason, got %+v", signal)
	}
}

func TestStrategyBlocksEntriesOutsideTradableSession(t *testing.T) {
	cfg := testStrategyConfig()
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

func TestStrategyDynamicReallocationOpportunitySwap(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.MaxOpenPositions = 2
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	// Fill two positions so we are at capacity
	book.ApplyExecution(testExecutionReport("STAG", 5.0, 100, at.Add(-10*time.Minute))) // Holder, doing nothing
	book.ApplyExecution(testExecutionReport("WINR", 10.0, 100, at.Add(-10*time.Minute))) // Winner, doing well
	
	// Mark latest prices. STAG is weak (0.1R), WINR is strong (2.0R)
	book.MarkPriceAt("STAG", 5.02, at)
	book.MarkPriceAt("WINR", 11.00, at)

	// Here comes a massive A+ setup that we want, but we are full
	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "STAR",
		Price:                8.20,
		Open:                 8.00,
		HighOfDay:            8.21,
		GapPercent:           31,
		RelativeVolume:       12.4,
		PriceVsOpenPct:       15.0,
		DistanceFromHighPct:  0.10,
		OneMinuteReturnPct:   1.8,
		ThreeMinuteReturnPct: 4.7,
		FifteenMinuteReturnPct: 8.0,
		VolumeRate:           3.9,
		MinutesSinceOpen:     30,
		Score:                22, // Exceptional score overrides
		ATR:                  0.80,
		ATRPct:               5.0,
		BreakoutPct:          0.50,
		SetupHigh:             8.10,
		SetupLow:              7.90,
		SetupType:             "consolidation-breakout",
		Timestamp:            at,
	})

	if ok {
		t.Fatal("expected candidate to be blocked due to capacity")
	}
	if reason != "reallocation-swap-pending" {
		t.Fatalf("unexpected block reason, wanted reallocation-swap-pending: %s", reason)
	}

	// The flag should be set for the weaker position (STAG)
	if !strat.reallocationTargets["STAG"] {
		t.Fatal("expected STAG to be flagged for reallocation swap targets")
	}

	// When STAG ticks next, it should be liquidated immediately to free capacity
	exitSignal, exitOk := strat.evaluateExit(domain.Tick{
		Symbol:    "STAG",
		Price:     5.01,
		BarOpen:   5.05,
		BarHigh:   5.05,
		BarLow:    5.00,
		HighOfDay: 5.50,
		Timestamp: at.Add(1 * time.Second),
	})

	if !exitOk {
		t.Fatal("expected STAG to emit an exit signal")
	}
	if exitSignal.Reason != "opportunity-reallocation" {
		t.Fatalf("expected opportunity-reallocation reason, got: %s", exitSignal.Reason)
	}

	// Winner continues unbothered
	winnerExitSignal, winnerExitOk := strat.evaluateExit(domain.Tick{
		Symbol:    "WINR",
		Price:     10.90,
		HighOfDay: 11.50,
		Timestamp: at.Add(1 * time.Second),
	})

	if winnerExitOk {
		t.Fatalf("expected WINR to hold its position, but got exit: %s", winnerExitSignal.Reason)
	}
}
