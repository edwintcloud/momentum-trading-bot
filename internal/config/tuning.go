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
		cfg.MinPrice = 5.00
		cfg.MaxExposurePct = 0.25
	case equity < 100_000:
		cfg.RiskPerTradePct = 0.050
		cfg.DailyLossLimitPct = 0.020
		cfg.MaxTradesPerDay = 500
		cfg.MaxOpenPositions = 10
		cfg.MinPrice = 5.00
		cfg.MaxExposurePct = 0.48
	default:
		cfg.RiskPerTradePct = 0.015
		cfg.DailyLossLimitPct = 0.080
		cfg.MaxTradesPerDay = 500
		cfg.MaxOpenPositions = 10
		cfg.MinPrice = 5.00
		cfg.MaxExposurePct = 0.45
	}

	cfg.StopLossPct = 0.03
	cfg.TrailingStopPct = 0.05
	cfg.TrailingStopActivationPct = 0.01
	cfg.EntryCooldownSec = 30
	cfg.ExitCooldownSec = 5
	cfg.EntryModelEnabled = true
	cfg.EntryModelMinPredictedReturnPct = 0.20
	cfg.MinEntryScore = 15.0
	cfg.MinOneMinuteReturnPct = 0.10
	cfg.MaxOneMinuteReturnPct = 3.00
	cfg.MinThreeMinuteReturnPct = 0.20 
	cfg.MinFifteenMinuteReturnPct = 0.00 
	cfg.MinVolumeRate = 3.00
	cfg.MaxPriceVsOpenPct = 30.0
	cfg.MaxPriceVsVWAPPct = 0
	cfg.BreakoutFailureWindowMin = 5
	cfg.BreakoutFailureLossPct = 0.0150
	cfg.BreakEvenActivationPct = 0.015
	cfg.BreakEvenFloorPct = 0.001
	cfg.StagnationWindowMin = 3
	cfg.StagnationMinPeakPct = 0.003
	cfg.MinGapPercent = 0.0
	cfg.MaxPrice = 30.0
	cfg.MaxEntryMinutesSinceOpen = 330
	cfg.MinRelativeVolume = 2.0
	cfg.MinPremarketVolume = 0
	cfg.ScannerWorkers = 4
	cfg.LimitOrderSlippageDollars = 0.03

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
	if exposure > 10.00 {
		exposure = 10.00
	}
	return round2(exposure)
}
