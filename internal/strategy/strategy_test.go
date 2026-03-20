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

func testConfig() config.TradingConfig {
	return config.TradingConfig{
		StartingCapital:          100_000,
		EnableShorts:             false,
		RiskPerTradePct:          0.01,
		DailyLossLimitPct:        0.03,
		MaxTradesPerDay:          8,
		MaxOpenPositions:         4,
		MaxExposurePct:           0.30,
		MaxShortOpenPositions:    1,
		MaxShortExposurePct:      0.15,
		EntryCooldownSec:         45,
		ExitCooldownSec:          5,
		MinEntryScore:            14.0,
		ShortMinEntryScore:       18.0,
		MinOneMinuteReturnPct:    0.05,
		MinThreeMinuteReturnPct:  0.15,
		MinVolumeRate:            1.05,
		MaxPriceVsOpenPct:        50.0,
		BreakoutFailureWindowMin: 15,
		StagnationWindowMin:      30,
		StagnationMinPeakPct:     0.012,
		MinPrice:                 1.0,
		MaxPrice:                 100.0,
		MinRelativeVolume:        1.5,
		EntryATRPercentFallback:  0.02,
		EntryStopATRMultiplier:   1.00,
		MaxRiskATRMultiplier:     4.00,
		BreakEvenHoldMinutes:     5,
		BreakEvenMinR:            0.50,
		TrailActivationR:         0.70,
		TrailATRMultiplier:       1.50,
		TightTrailTriggerR:       1.20,
		TightTrailATRMultiplier:  0.60,
		ProfitTargetR:            1.20,
		FailedBreakoutCutR:       0.05,
		ShortPeakExtensionMinPct: 12.0,
		ShortVWAPBreakMinPct:     -0.75,
		ShortStopATRMultiplier:   1.25,
	}
}

func testStrategyConfig() config.TradingConfig {
	cfg := testConfig()
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

func TestStrategyCreatesShortEntrySignal(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:               "GOAI",
		Direction:            domain.DirectionShort,
		Price:                5.28,
		Open:                 4.70,
		HighOfDay:            6.18,
		GapPercent:           14,
		RelativeVolume:       11.5,
		PriceVsOpenPct:       12.3,
		DistanceFromHighPct:  14.56,
		OneMinuteReturnPct:   -1.6,
		ThreeMinuteReturnPct: -4.3,
		VolumeRate:           1.9,
		VolumeLeaderPct:      0.82,
		LeaderRank:           1,
		MinutesSinceOpen:     150,
		ATR:                  0.31,
		ATRPct:               5.87,
		PriceVsVWAPPct:       -1.4,
		BreakoutPct:          -0.9,
		CloseOffHighPct:      88,
		SetupHigh:            5.84,
		SetupLow:             5.33,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                26,
		Timestamp:            at,
	})
	if !ok {
		t.Fatal("expected short strategy to emit entry signal")
	}
	if signal.Side != domain.SideSell || signal.Intent != domain.IntentOpen || signal.PositionSide != domain.DirectionShort {
		t.Fatalf("unexpected short signal: %+v", signal)
	}
	if signal.StopPrice <= signal.Price {
		t.Fatalf("expected short stop above entry price, got %+v", signal)
	}
}

func TestStrategyCapsShortEntryStopToATRDistance(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableShorts = true
	cfg.ShortStopATRMultiplier = 0.55
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:               "WHLR",
		Direction:            domain.DirectionShort,
		Price:                5.22,
		Open:                 4.80,
		HighOfDay:            10.13,
		GapPercent:           8.2,
		RelativeVolume:       40.0,
		PriceVsOpenPct:       8.75,
		DistanceFromHighPct:  48.47,
		OneMinuteReturnPct:   -8.90,
		ThreeMinuteReturnPct: -14.71,
		VolumeRate:           1.36,
		VolumeLeaderPct:      1.0,
		LeaderRank:           1,
		MinutesSinceOpen:     145,
		ATR:                  0.56,
		ATRPct:               10.74,
		PriceVsVWAPPct:       -8.50,
		BreakoutPct:          -4.74,
		CloseOffHighPct:      88,
		SetupHigh:            6.39,
		SetupLow:             5.48,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                121.34,
		Timestamp:            at,
	})
	if !ok {
		t.Fatal("expected short strategy to emit entry signal")
	}
	if signal.StopPrice != 5.53 {
		t.Fatalf("expected short stop to respect ATR cap, got %+v", signal)
	}
	if signal.RiskPerShare != 0.31 {
		t.Fatalf("expected short risk/share 0.31 after capping stop, got %+v", signal)
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
	cfg := testStrategyConfig()
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
	if reason != "no-renewed-volume" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyBlocksLongsInBearishRegime(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeBearish, Confidence: 0.8})
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
		OneMinuteReturnPct:   0.12,
		ThreeMinuteReturnPct: 0.25,
		VolumeRate:           1.2,
		SetupType:            "consolidation-breakout",
		MinutesSinceOpen:     18,
		VolumeLeaderPct:      0.45,
		LeaderRank:           3,
		Score:                17,
		Timestamp:            inSessionTime(),
	})
	if ok {
		t.Fatal("expected bearish regime to block long setup")
	}
	if reason != "bearish-regime-no-long" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyBlocksBreakoutInRangingRegime(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.7})
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
		OneMinuteReturnPct:   0.12,
		ThreeMinuteReturnPct: 0.25,
		VolumeRate:           1.2,
		SetupType:            "consolidation-breakout",
		MinutesSinceOpen:     18,
		Score:                22,
		Timestamp:            inSessionTime(),
	})
	if ok {
		t.Fatal("expected ranging regime to block breakout-chase long setup")
	}
	if reason != "ranging-regime-breakout-block" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyBlocksHigherLowReclaimInRangingRegime(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.7})
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
		OneMinuteReturnPct:   0.12,
		ThreeMinuteReturnPct: 0.25,
		VolumeRate:           1.2,
		SetupType:            "higher-low-reclaim",
		MinutesSinceOpen:     18,
		Score:                22,
		Timestamp:            inSessionTime(),
	})
	if ok {
		t.Fatal("expected ranging regime to block higher-low reclaim long setup")
	}
	if reason != "ranging-regime-breakout-block" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyAllowsVWAPReclaimInRangingRegime(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.7})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "HUMA",
		Price:                4.20,
		Open:                 4.00,
		HighOfDay:            4.21,
		GapPercent:           21,
		RelativeVolume:       6.4,
		PriceVsOpenPct:       5.0,
		DistanceFromHighPct:  0.24,
		OneMinuteReturnPct:   0.12,
		ThreeMinuteReturnPct: 0.25,
		VolumeRate:           1.2,
		SetupType:            "vwap-reclaim",
		MinutesSinceOpen:     18,
		Score:                22,
		Timestamp:            inSessionTime(),
	})
	if !ok {
		t.Fatalf("expected ranging regime to allow vwap reclaim long setup, got %s", reason)
	}
	if signal.Playbook == "" {
		t.Fatalf("expected ranging regime signal to carry playbook metadata, got %+v", signal)
	}
}

func TestStrategyAllowsLowerHighBreakdownShortInBearishRegime(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeBearish, Confidence: 0.82})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateCandidate(domain.Candidate{
		Symbol:               "WEAK",
		Direction:            domain.DirectionShort,
		Price:                9.92,
		Open:                 10.00,
		HighOfDay:            10.90,
		GapPercent:           4.0,
		RelativeVolume:       12.0,
		PriceVsOpenPct:       -0.8,
		DistanceFromHighPct:  8.99,
		OneMinuteReturnPct:   -2.94,
		ThreeMinuteReturnPct: -2.55,
		VolumeRate:           1.35,
		VolumeLeaderPct:      0.72,
		LeaderRank:           1,
		MinutesSinceOpen:     90,
		ATR:                  0.24,
		ATRPct:               2.42,
		PriceVsVWAPPct:       -1.30,
		BreakoutPct:          -0.80,
		CloseOffHighPct:      80,
		SetupHigh:            10.28,
		SetupLow:             10.00,
		SetupType:            "lower-high-breakdown-short",
		Score:                24,
		Timestamp:            inSessionTime(),
	})
	if !ok {
		t.Fatal("expected bearish regime to allow lower-high short")
	}
	if signal.Playbook != "bearish-breakdown-short" {
		t.Fatalf("expected bearish breakdown playbook, got %+v", signal)
	}
}

func TestStrategyBlocksRangingParabolicShortBeforeNineAM(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.34})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	timestamp := time.Date(2026, 3, 13, 12, 56, 0, 0, time.UTC) // 08:56 ET
	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "WHLR",
		Direction:            domain.DirectionShort,
		Price:                5.22,
		Open:                 4.80,
		HighOfDay:            10.13,
		GapPercent:           8.2,
		RelativeVolume:       40.0,
		PriceVsOpenPct:       8.75,
		DistanceFromHighPct:  48.47,
		OneMinuteReturnPct:   -8.90,
		ThreeMinuteReturnPct: -14.71,
		VolumeRate:           1.36,
		VolumeLeaderPct:      1.0,
		LeaderRank:           1,
		MinutesSinceOpen:     0,
		ATR:                  0.56,
		ATRPct:               10.74,
		PriceVsVWAPPct:       -8.50,
		BreakoutPct:          -4.74,
		CloseOffHighPct:      88,
		SetupHigh:            6.39,
		SetupLow:             5.48,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                121.34,
		MarketRegime:         domain.MarketRegimeRanging,
		RegimeConfidence:     0.34,
		Timestamp:            timestamp,
	})
	if ok {
		t.Fatal("expected ranging pre-9am short to be blocked")
	}
	if reason != "ranging-short-overnight-block" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyBlocksRangingCapitulationShortBeforeSevenAM(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.34})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	timestamp := time.Date(2026, 3, 13, 10, 39, 0, 0, time.UTC) // 06:39 ET
	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "DXST",
		Direction:            domain.DirectionShort,
		Price:                9.07,
		Open:                 4.20,
		HighOfDay:            14.12,
		GapPercent:           107.08,
		RelativeVolume:       88.56,
		PriceVsOpenPct:       115.95,
		DistanceFromHighPct:  55.68,
		OneMinuteReturnPct:   -12.54,
		ThreeMinuteReturnPct: -16.41,
		VolumeRate:           1.32,
		VolumeLeaderPct:      0.55,
		LeaderRank:           2,
		MinutesSinceOpen:     0,
		ATR:                  1.68,
		ATRPct:               18.49,
		PriceVsVWAPPct:       -9.36,
		BreakoutPct:          -3.20,
		CloseOffHighPct:      88,
		SetupHigh:            10.11,
		SetupLow:             9.35,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                116.63,
		MarketRegime:         domain.MarketRegimeRanging,
		RegimeConfidence:     0.34,
		Timestamp:            timestamp,
	})
	if ok {
		t.Fatal("expected ranging capitulation short before 7am to be blocked")
	}
	if reason != "ranging-short-overnight-block" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyAllowsRangingCapitulationShortAfterSevenAM(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.34})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	timestamp := time.Date(2026, 3, 13, 12, 39, 0, 0, time.UTC) // 08:39 ET
	signal, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "DXST",
		Direction:            domain.DirectionShort,
		Price:                9.07,
		Open:                 4.20,
		HighOfDay:            14.12,
		GapPercent:           107.08,
		RelativeVolume:       88.56,
		PriceVsOpenPct:       115.95,
		DistanceFromHighPct:  55.68,
		OneMinuteReturnPct:   -12.54,
		ThreeMinuteReturnPct: -16.41,
		VolumeRate:           1.32,
		VolumeLeaderPct:      0.55,
		LeaderRank:           2,
		MinutesSinceOpen:     0,
		ATR:                  1.68,
		ATRPct:               18.49,
		PriceVsVWAPPct:       -9.36,
		BreakoutPct:          -3.20,
		CloseOffHighPct:      88,
		SetupHigh:            10.11,
		SetupLow:             9.35,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                116.63,
		MarketRegime:         domain.MarketRegimeRanging,
		RegimeConfidence:     0.34,
		Timestamp:            timestamp,
	})
	if !ok {
		t.Fatalf("expected ranging capitulation short after 7am to be allowed, got %s", reason)
	}
	if signal.Playbook != "ranging-capitulation-short" {
		t.Fatalf("expected capitulation playbook, got %+v", signal)
	}
}

func TestStrategyAllowsRangingParabolicShortAfterNineAM(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.34})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	timestamp := time.Date(2026, 3, 13, 14, 11, 0, 0, time.UTC) // 09:11 ET
	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "LRHC",
		Direction:            domain.DirectionShort,
		Price:                5.11,
		Open:                 4.34,
		HighOfDay:            7.35,
		GapPercent:           13.3,
		RelativeVolume:       1026.44,
		PriceVsOpenPct:       17.74,
		DistanceFromHighPct:  65.75,
		OneMinuteReturnPct:   -13.83,
		ThreeMinuteReturnPct: -12.80,
		VolumeRate:           0.95,
		VolumeLeaderPct:      0.58,
		LeaderRank:           3,
		MinutesSinceOpen:     0,
		ATR:                  1.21,
		ATRPct:               23.61,
		PriceVsVWAPPct:       -17.12,
		BreakoutPct:          -2.29,
		CloseOffHighPct:      88,
		SetupHigh:            5.52,
		SetupLow:             5.23,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                106.86,
		MarketRegime:         domain.MarketRegimeRanging,
		RegimeConfidence:     0.34,
		Timestamp:            timestamp,
	})
	if !ok {
		t.Fatalf("expected post-9am ranging short to be allowed, got %s", reason)
	}
}

func TestStrategyBlocksWeakRangingParabolicShortAfterOpen(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.34})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	timestamp := time.Date(2026, 3, 13, 14, 34, 0, 0, time.UTC) // 09:34 ET
	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "PRFX",
		Direction:            domain.DirectionShort,
		Price:                4.46,
		Open:                 4.98,
		HighOfDay:            5.47,
		GapPercent:           7.0,
		RelativeVolume:       2546.22,
		PriceVsOpenPct:       -10.44,
		DistanceFromHighPct:  22.65,
		OneMinuteReturnPct:   -4.70,
		ThreeMinuteReturnPct: -7.85,
		VolumeRate:           0.50,
		VolumeLeaderPct:      0.38,
		LeaderRank:           2,
		MinutesSinceOpen:     0,
		ATR:                  0.23,
		ATRPct:               5.06,
		PriceVsVWAPPct:       -4.87,
		BreakoutPct:          -1.33,
		CloseOffHighPct:      88,
		SetupHigh:            4.61,
		SetupLow:             4.48,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                106.98,
		MarketRegime:         domain.MarketRegimeRanging,
		RegimeConfidence:     0.34,
		Timestamp:            timestamp,
	})
	if ok {
		t.Fatal("expected weak ranging short to be blocked")
	}
	if reason != "ranging-short-needs-stronger-breakdown" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyBlocksSecondRangingParabolicShortSameDay(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.34})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	timestamp := time.Date(2026, 3, 13, 14, 11, 0, 0, time.UTC) // 09:11 ET
	first := domain.Candidate{
		Symbol:               "WHLR",
		Direction:            domain.DirectionShort,
		Price:                5.41,
		Open:                 4.80,
		HighOfDay:            9.36,
		GapPercent:           8.2,
		RelativeVolume:       998.68,
		PriceVsOpenPct:       12.71,
		DistanceFromHighPct:  38.74,
		OneMinuteReturnPct:   -5.12,
		ThreeMinuteReturnPct: -6.14,
		VolumeRate:           0.58,
		VolumeLeaderPct:      1.0,
		LeaderRank:           1,
		MinutesSinceOpen:     0,
		ATR:                  0.25,
		ATRPct:               4.35,
		PriceVsVWAPPct:       -6.12,
		BreakoutPct:          -5.02,
		CloseOffHighPct:      88,
		SetupHigh:            6.20,
		SetupLow:             5.77,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                99.51,
		MarketRegime:         domain.MarketRegimeRanging,
		RegimeConfidence:     0.34,
		Timestamp:            timestamp,
	}
	if _, ok, reason := strat.EvaluateCandidateDetailed(first); !ok {
		t.Fatalf("expected first ranging short to be allowed, got %s", reason)
	}
	second := first
	second.Price = 5.70
	second.Timestamp = timestamp.Add(3 * time.Minute)
	_, ok, reason := strat.EvaluateCandidateDetailed(second)
	if ok {
		t.Fatal("expected second ranging short on same symbol/day to be blocked")
	}
	if reason != "ranging-short-one-shot" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyBlocksLowATRRangingParabolicShort(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	runtimeState.SetMarketRegime(domain.MarketRegimeSnapshot{Regime: domain.MarketRegimeRanging, Confidence: 0.34})
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	timestamp := time.Date(2026, 3, 13, 14, 9, 0, 0, time.UTC) // 09:09 ET
	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
		Symbol:               "FULC",
		Direction:            domain.DirectionShort,
		Price:                13.81,
		Open:                 12.94,
		HighOfDay:            17.11,
		GapPercent:           10.0,
		RelativeVolume:       48.26,
		PriceVsOpenPct:       6.72,
		DistanceFromHighPct:  31.26,
		OneMinuteReturnPct:   -3.70,
		ThreeMinuteReturnPct: -4.96,
		VolumeRate:           2.26,
		VolumeLeaderPct:      0.21,
		LeaderRank:           3,
		ATR:                  0.22,
		ATRPct:               1.57,
		PriceVsVWAPPct:       -2.87,
		BreakoutPct:          -5.03,
		CloseOffHighPct:      88,
		SetupHigh:            14.33,
		SetupLow:             13.90,
		SetupType:            "parabolic-failed-reclaim-short",
		Score:                102.17,
		MarketRegime:         domain.MarketRegimeRanging,
		RegimeConfidence:     0.34,
		Timestamp:            timestamp,
	})
	if ok {
		t.Fatal("expected low-ATR ranging short to be blocked")
	}
	if reason != "ranging-short-needs-volatility" {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestStrategyTradeConfigTightensRangingPlaybookAndWidensTrendPlaybook(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableMarketRegime = true
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)

	ranging := strat.tradeConfigForPosition(domain.Position{Playbook: "ranging-reclaim-long"})
	trend := strat.tradeConfigForPosition(domain.Position{Playbook: "bearish-trend-short"})
	capitulation := strat.tradeConfigForPosition(domain.Position{Playbook: "ranging-capitulation-short"})

	if ranging.StagnationWindowMin >= cfg.StagnationWindowMin || ranging.TrailATRMultiplier >= cfg.TrailATRMultiplier {
		t.Fatalf("expected ranging playbook to tighten exits, got %+v", ranging)
	}
	if trend.StagnationWindowMin <= cfg.StagnationWindowMin || trend.TrailATRMultiplier <= cfg.TrailATRMultiplier {
		t.Fatalf("expected trend playbook to widen exits, got %+v", trend)
	}
	if capitulation.StagnationWindowMin <= cfg.StagnationWindowMin || capitulation.TrailATRMultiplier <= cfg.TrailATRMultiplier {
		t.Fatalf("expected capitulation short playbook to widen exits, got %+v", capitulation)
	}
}

func TestStrategyRejectsBreakoutWithoutRenewedVolume(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	_, ok, reason := strat.EvaluateCandidateDetailed(domain.Candidate{
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
		VolumeRate:            0.72,
		VolumeLeaderPct:       0.95,
		LeaderRank:            1,
		MinutesSinceOpen:      65,
		ATRPct:                3.20,
		PriceVsVWAPPct:        0.55,
		BreakoutPct:           0.18,
		ConsolidationRangePct: 1.80,
		CloseOffHighPct:       18,
		SetupHigh:             6.24,
		SetupLow:              5.92,
		SetupType:             "vwap-reclaim",
		Score:                 24,
		Timestamp:             at,
	})
	if ok {
		t.Fatal("expected breakout without renewed volume to be blocked")
	}
	if reason != "no-renewed-volume" {
		t.Fatalf("unexpected block reason: %s", reason)
	}
}

func TestStrategyUsesBrokerSeedMetadataForTimeBasedExits(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	strat := NewStrategy(cfg, book, runtimeState)
	at := inSessionTime()

	book.SeedPosition(domain.Position{
		Symbol:           "SEEDED",
		Quantity:         100,
		AvgPrice:         10.00,
		StopPrice:        9.50,
		InitialStopPrice: 9.50,
		RiskPerShare:     0.50,
		EntryATR:         0.50,
		SetupType:        "consolidation-breakout",
		LastPrice:        10.05,
		HighestPrice:     10.12,
		MarketValue:      1005,
		UnrealizedPnL:    5,
		BrokerSeeded:     true,
		OpenedAt:         at,
		UpdatedAt:        at,
	})

	seededSignal, seededOK, seededReason := strat.EvaluateExitDetailed(domain.Tick{
		Symbol:    "SEEDED",
		Price:     10.04,
		BarOpen:   10.06,
		BarHigh:   10.12,
		BarLow:    9.90,
		Open:      10.00,
		HighOfDay: 10.12,
		Timestamp: at.Add(10 * time.Minute),
	})
	if !seededOK {
		t.Fatalf("expected broker-seeded position to use timing metadata, got %s", seededReason)
	}
	if seededSignal.Reason != "failed-breakout" {
		t.Fatalf("expected seeded position to be eligible for failed-breakout, got %+v", seededSignal)
	}

	freshBook := portfolio.NewManager(cfg, runtimeState)
	freshStrat := NewStrategy(cfg, freshBook, runtimeState)
	freshBook.ApplyExecution(domain.ExecutionReport{
		Symbol:       "FRESH",
		Side:         "buy",
		Price:        10.00,
		Quantity:     100,
		StopPrice:    9.50,
		RiskPerShare: 0.50,
		EntryATR:     0.50,
		SetupType:    "consolidation-breakout",
		FilledAt:     at,
	})

	freshSignal, freshOK, freshReason := freshStrat.EvaluateExitDetailed(domain.Tick{
		Symbol:    "FRESH",
		Price:     10.04,
		BarOpen:   10.06,
		BarHigh:   10.12,
		BarLow:    9.90,
		Open:      10.00,
		HighOfDay: 10.12,
		Timestamp: at.Add(10 * time.Minute),
	})
	if freshOK {
		t.Fatalf("expected fresh position to remain inside the grace period, got %+v", freshSignal)
	}
	if freshReason != "hold" {
		t.Fatalf("unexpected fresh position reason: %s", freshReason)
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
		VolumeRate:           1.45,
		MinutesSinceOpen:     170,
		SetupLow:             6.80,
		Score:                20,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected strong intraday squeeze to pass, got %s", reason)
	}
}

func TestStrategyAllowsStrongReclaimBelowHigh(t *testing.T) {
	cfg := testStrategyConfig()
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
		SetupLow:             6.90,
		Score:                18,
		Timestamp:            at,
	})
	if !ok {
		t.Fatalf("expected strong reclaim setup to pass, got %s", reason)
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
		VolumeRate:           1.45,
		VolumeLeaderPct:      0.04,
		LeaderRank:           6,
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
		VolumeRate:           3.19,
		VolumeLeaderPct:      0.02,
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
		VolumeRate:           1.45,
		VolumeLeaderPct:      0.92,
		LeaderRank:           1,
		MinutesSinceOpen:     40,
		Score:                18.5,
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
	if reason != "parabolic-spike" {
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
	if reason != "thin-premarket" {
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
		Symbol:               "TSLA",
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
		VolumeRate:           1.50,
		MinutesSinceOpen:     55,
		Score:                19,
		Timestamp:            at.Add(-5 * time.Minute),
	})
	if ok {
		t.Fatal("expected immediate reentry after loss to be blocked")
	}
	if reason != "symbol-loss-lockout" {
		t.Fatalf("unexpected block reason: %s", reason)
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
		VolumeRate:           1.50,
		MinutesSinceOpen:     55,
		SetupLow:             3.00,
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

func TestStrategyUsesCashValueForSizing(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50500)
	book.SyncBrokerCash(50000)
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
	if signal.Quantity != 1062 {
		t.Fatalf("expected quantity scaled by narrower stop, got %d", signal.Quantity)
	}
}

func TestStrategyCapsSizingToAvailableCash(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.MaxExposurePct = 1.0
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50500)
	book.SyncBrokerCash(5000)
	at := inSessionTime()
	book.SeedPosition(domain.Position{
		Symbol:      "SEEDED",
		Quantity:    4500,
		AvgPrice:    10,
		LastPrice:   10,
		MarketValue: 45000,
		OpenedAt:    at.Add(-time.Hour),
		UpdatedAt:   at.Add(-time.Hour),
	})
	strat := NewStrategy(cfg, book, runtimeState)

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
	if signal.Quantity != 500 {
		t.Fatalf("expected quantity capped by remaining cash, got %d", signal.Quantity)
	}
}

func TestStrategySizesPremarketEntriesMoreConservatively(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	book.SyncBrokerAccount(50000, 50500)
	book.SyncBrokerCash(50000)
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
		SetupLow:             1.85,
		Score:                28,
		Timestamp:            time.Date(2026, 3, 13, 12, 20, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected conservative premarket setup to pass")
	}
	if signal.Quantity != 1833 {
		t.Fatalf("expected premarket ATR-sized quantity to be scaled down to 1833 shares, got %d", signal.Quantity)
	}
}

func TestStrategyDoesNotExitLongOnPureSpikeWithoutRetrace(t *testing.T) {
	cfg := testStrategyConfig()
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(testExecutionReport("RKLB", 10, 100, at.Add(-3*time.Minute)))
	book.MarkPriceAt("RKLB", 10.00, at.Add(-2*time.Minute))
	strat := NewStrategy(cfg, book, runtimeState)

	// Spike to 11.50 (3.0R if R is 0.50)
	spikeTick := domain.Tick{
		Symbol:    "RKLB",
		Price:     11.50,
		HighOfDay: 11.50,
		Timestamp: at,
	}
	signal, ok := strat.evaluateExit(spikeTick)
	if ok {
		t.Fatalf("expected strategy to hold until a stop or retrace is hit, got %+v", signal)
	}
}

func TestStrategyCoversShortWhenStopBreaks(t *testing.T) {
	cfg := testStrategyConfig()
	cfg.EnableShorts = true
	runtimeState := runtime.NewState()
	book := portfolio.NewManager(cfg, runtimeState)
	at := inSessionTime()
	book.ApplyExecution(domain.ExecutionReport{
		Symbol:       "GOAI",
		Side:         domain.SideSell,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionShort,
		Price:        5.30,
		Quantity:     100,
		StopPrice:    5.62,
		RiskPerShare: 0.32,
		EntryATR:     0.24,
		SetupType:    "parabolic-failed-reclaim-short",
		FilledAt:     at.Add(-10 * time.Minute),
	})
	strat := NewStrategy(cfg, book, runtimeState)

	signal, ok := strat.evaluateExit(domain.Tick{
		Symbol:    "GOAI",
		Price:     5.66,
		BarOpen:   5.40,
		BarHigh:   5.68,
		BarLow:    5.34,
		HighOfDay: 6.18,
		Timestamp: at,
	})
	if !ok {
		t.Fatal("expected short stop exit")
	}
	if signal.Side != domain.SideBuy || signal.Intent != domain.IntentClose || signal.PositionSide != domain.DirectionShort {
		t.Fatalf("unexpected short exit signal: %+v", signal)
	}
	if signal.Reason != "stop-loss" && signal.Reason != "trailing-stop" && signal.Reason != "break-even-stop" {
		t.Fatalf("expected stop-like short exit, got %+v", signal)
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
		BarLow:    9.45,
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
