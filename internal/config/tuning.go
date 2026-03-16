package config

import "math"

// TuneTradingConfig applies broker-aware defaults that bias toward conservative
// momentum participation without requiring manual knob tuning.
func TuneTradingConfig(base TradingConfig, equity float64, historicalRateLimitPerMin int) TradingConfig {
	cfg := base
	if equity <= 0 {
		equity = cfg.StartingCapital
	}
	cfg.StartingCapital = round2(equity)

	switch {
	case equity < 25_000:
		cfg.RiskPerTradePct = 0.005
		cfg.DailyLossLimitPct = 0.015
		cfg.MaxTradesPerDay = 6
		cfg.MaxOpenPositions = 2
		cfg.MinPrice = 2.0
	case equity < 100_000:
		cfg.RiskPerTradePct = 0.0075
		cfg.DailyLossLimitPct = 0.020
		cfg.MaxTradesPerDay = 8
		cfg.MaxOpenPositions = 3
		cfg.MinPrice = 1.50
	default:
		cfg.RiskPerTradePct = 0.010
		cfg.DailyLossLimitPct = 0.025
		cfg.MaxTradesPerDay = 8
		cfg.MaxOpenPositions = 4
		cfg.MinPrice = 1.00
	}

	cfg.StopLossPct = 0.07
	cfg.TrailingStopPct = 0.08
	cfg.TrailingStopActivationPct = 0.03
	cfg.EntryCooldownSec = 60
	cfg.ExitCooldownSec = 5
	cfg.EntryModelEnabled = true
	cfg.EntryModelMinPredictedReturnPct = 0.40
	cfg.MinEntryScore = 15.5
	cfg.MinOneMinuteReturnPct = 0.10
	cfg.MinThreeMinuteReturnPct = 0.45
	cfg.MinVolumeRate = 1.05
	cfg.MaxPriceVsOpenPct = 22.0
	cfg.BreakoutFailureWindowMin = 20
	cfg.BreakoutFailureLossPct = 0.0075
	cfg.BreakEvenActivationPct = 0.015
	cfg.BreakEvenFloorPct = 0.001
	cfg.StagnationWindowMin = 35
	cfg.StagnationMinPeakPct = 0.010
	cfg.MinGapPercent = 9.0
	cfg.MinRelativeVolume = 5.0
	cfg.MinPremarketVolume = 400_000
	cfg.ScannerWorkers = 4
	cfg.LimitOrderSlippageDollars = 0.05
	cfg.MaxExposurePct = inferMaxExposurePct(cfg)

	if historicalRateLimitPerMin > 0 {
		budget := int(float64(historicalRateLimitPerMin) * 0.60)
		if budget < 120 {
			budget = 120
		}
		if budget > 2400 {
			budget = 2400
		}
		cfg.HydrationRequestsPerMin = budget
	}

	return cfg
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func inferMaxExposurePct(cfg TradingConfig) float64 {
	assumedStopDistance := math.Max(cfg.StopLossPct, 0.07)
	perPositionExposure := cfg.RiskPerTradePct / assumedStopDistance
	targetFullRiskPositions := 2
	if cfg.MaxOpenPositions > 0 && cfg.MaxOpenPositions < targetFullRiskPositions {
		targetFullRiskPositions = cfg.MaxOpenPositions
	}
	exposure := (perPositionExposure * float64(targetFullRiskPositions)) + 0.05
	if exposure < 0.25 {
		exposure = 0.25
	}
	if exposure > 0.60 {
		exposure = 0.60
	}
	return round2(exposure)
}
