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

	cfg.RiskPerTradePct = 0.035
	cfg.DailyLossLimitPct = 0.200
	cfg.MaxTradesPerDay = 30
	cfg.MaxOpenPositions = 5
	cfg.MinPrice = 1.0

	switch {
	case equity < 25_000:
		cfg.RiskPerTradePct = 0.005
		cfg.DailyLossLimitPct = 0.015
		cfg.MaxTradesPerDay = 6
		cfg.MaxOpenPositions = 2
		cfg.MinPrice = 5.0
	case equity < 100_000:
		cfg.RiskPerTradePct = 0.035
		cfg.DailyLossLimitPct = 0.200
		cfg.MaxTradesPerDay = 30
		cfg.MaxOpenPositions = 5
		cfg.MinPrice = 5.0
	default:
		cfg.RiskPerTradePct = 0.015
		cfg.DailyLossLimitPct = 0.080
		cfg.MaxTradesPerDay = 20
		cfg.MaxOpenPositions = 5
		cfg.MinPrice = 5.0
	}

	cfg.StopLossPct = 0.03
	cfg.EntryCooldownSec = 60
	cfg.ExitCooldownSec = 5
	cfg.MinEntryScore = 20.0
	cfg.MinOneMinuteReturnPct = 0.40
	cfg.MinThreeMinuteReturnPct = 1.50
	cfg.MinVolumeRate = 2.00
	cfg.MaxPriceVsOpenPct = 22.0
	cfg.BreakoutFailureWindowMin = 3
	cfg.StagnationWindowMin = 3
	cfg.StagnationMinPeakPct = 0.005
	cfg.MinGapPercent = 6.0
	cfg.MaxPrice = 40.0
	cfg.MinRelativeVolume = 4.0
	cfg.MinPremarketVolume = 1_000_000
	cfg.ScannerWorkers = 4
	cfg.LimitOrderSlippageDollars = 0.02
	cfg.EntryATRPercentFallback = 0.02
	cfg.EntryStopATRMultiplier = 2.00
	cfg.MaxRiskATRMultiplier = 4.00
	cfg.BreakEvenHoldMinutes = 5
	cfg.BreakEvenMinR = 0.50
	cfg.TrailActivationR = 0.60
	cfg.TrailATRMultiplier = 1.25
	cfg.TightTrailTriggerR = 1.00
	cfg.TightTrailATRMultiplier = 0.55
	cfg.ProfitTargetR = 1.20
	cfg.FailedBreakoutCutR = 0.05
	cfg.StructureConfirmR = 0.00
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
	if exposure > 10.00 {
		exposure = 10.00
	}
	return round2(exposure)
}
