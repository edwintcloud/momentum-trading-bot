package config

import "math"

const defaultStartingCapital = 100_000

// TuneTradingConfig applies broker-aware defaults that bias toward conservative
// momentum participation without requiring manual knob tuning.
func TuneTradingConfig(base TradingConfig, capital float64, historicalRateLimitPerMin int) TradingConfig {
	cfg := base
	if capital <= 0 {
		capital = cfg.StartingCapital
	}
	cfg.StartingCapital = round2(capital)

	cfg.EnableMarketRegime = false
	cfg.EnableShorts = true
	cfg.RiskPerTradePct = inferRiskPerTradePct(capital)
	cfg.DailyLossLimitPct = 0.2
	cfg.MaxTradesPerDay = 20
	cfg.MaxOpenPositions = inferMaxOpenPositions(capital)
	cfg.StopLossPct = 0.05
	cfg.MaxShortOpenPositions = inferMaxShortOpenPositions(cfg.MaxOpenPositions)
	cfg.MaxShortExposurePct = inferMaxShortExposurePct(cfg.MaxExposurePct)
	cfg.EntryCooldownSec = 60
	cfg.ExitCooldownSec = 5
	cfg.MinEntryScore = 18
	cfg.ShortMinEntryScore = 20
	cfg.MinOneMinuteReturnPct = 0.4
	cfg.MinThreeMinuteReturnPct = 0.8
	cfg.MinVolumeRate = 1.8
	cfg.MaxPriceVsOpenPct = 30
	cfg.BreakoutFailureWindowMin = 10
	cfg.StagnationWindowMin = 3
	cfg.StagnationMinPeakPct = 0.005
	cfg.ScannerWorkers = 4
	cfg.MinPrice = 3.5
	cfg.MaxPrice = 20
	cfg.MinGapPercent = 1
	cfg.MinRelativeVolume = 4
	cfg.MinPremarketVolume = 60_000
	cfg.ScannerMinPriceVsOpenPctFloor = 2.50
	cfg.ScannerMinPriceVsOpenGapMultiplier = 0.25
	cfg.ScannerMinSetupVolumeRateOffset = -0.05
	cfg.ScannerMinSetupRelativeVolumeExtra = 0.25
	cfg.ScannerVWAPTolerancePct = -0.10
	cfg.ScannerConsolidationATRMultiplier = 1.75
	cfg.ScannerConsolidationMaxPct = 4.50
	cfg.ScannerPullbackDepthMinATRMultiplier = 0.35
	cfg.ScannerPullbackDepthMinPct = 0.40
	cfg.ScannerPullbackDepthMaxATRMultiplier = 2.40
	cfg.ScannerPullbackDepthMaxPct = 8.00
	cfg.ScannerRenewedVolumeRateMin = 1.05
	cfg.MarketRegimeBenchmarkSymbols = []string{"SPY", "QQQ", "IWM"}
	cfg.MarketRegimeMinBenchmarks = 2
	cfg.MarketRegimeEMAFastPeriod = 20
	cfg.MarketRegimeEMASlowPeriod = 60
	cfg.MarketRegimeReturnLookbackMin = 30
	cfg.HydrationRequestsPerMin = 120
	cfg.HydrationRetrySec = 300
	cfg.HydrationQueueSize = 512
	cfg.LimitOrderSlippageDollars = 0.02
	cfg.EntryATRPercentFallback = 0.02
	cfg.EntryStopATRMultiplier = 2
	cfg.MaxRiskATRMultiplier = 4
	cfg.BreakEvenHoldMinutes = 5
	cfg.BreakEvenMinR = 0.5
	cfg.TrailActivationR = 0.7
	cfg.TrailATRMultiplier = 1.25
	cfg.TightTrailTriggerR = 1.2
	cfg.TightTrailATRMultiplier = 0.55
	cfg.ProfitTargetR = 2
	cfg.FailedBreakoutCutR = 0.05
	cfg.StructureConfirmR = 0
	cfg.ShortPeakExtensionMinPct = 12
	cfg.ShortVWAPBreakMinPct = -0.75
	cfg.ShortStopATRMultiplier = 1.25
	cfg.MinFloat = 500_000
	cfg.ShortMinFloat = 5_000_000
	cfg.FloatRotationScoreWeight = 3.0
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
	if exposure > 1.00 {
		exposure = 1.00
	}
	return round2(exposure)
}

func inferRiskPerTradePct(capital float64) float64 {
	switch {
	case capital <= 25_000:
		return 0.005
	case capital >= 100_000:
		return 0.015
	default:
		return 0.01
	}
}

func inferMaxOpenPositions(capital float64) int {
	switch {
	case capital <= 25_000:
		return 2
	case capital >= 100_000:
		return 4
	default:
		return 3
	}
}

func inferMaxShortOpenPositions(maxOpenPositions int) int {
	switch {
	case maxOpenPositions <= 1:
		return 1
	case maxOpenPositions >= 4:
		return 2
	default:
		return 1
	}
}

func inferMaxShortExposurePct(maxExposurePct float64) float64 {
	if maxExposurePct <= 0 {
		return 0.20
	}
	shortExposure := round2(maxExposurePct * 0.45)
	if shortExposure < 0.15 {
		shortExposure = 0.15
	}
	if shortExposure > 0.40 {
		shortExposure = 0.40
	}
	return shortExposure
}
