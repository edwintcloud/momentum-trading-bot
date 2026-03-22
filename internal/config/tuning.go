package config


const defaultStartingCapital = 25000.0

// TuneTradingConfig adjusts parameters based on capital and broker PnL.
func TuneTradingConfig(base TradingConfig, equity float64, brokerDayPnL float64) TradingConfig {
	cfg := base
	if equity <= 0 {
		equity = defaultStartingCapital
	}
	cfg.StartingCapital = equity

	// Risk parameters
	if cfg.RiskPerTradePct == 0 {
		cfg.RiskPerTradePct = 0.005
	}
	if cfg.DailyLossLimitPct == 0 {
		cfg.DailyLossLimitPct = 0.02
	}
	if cfg.MaxTradesPerDay == 0 {
		cfg.MaxTradesPerDay = 20
	}
	if cfg.MaxOpenPositions == 0 {
		cfg.MaxOpenPositions = 5
	}
	if cfg.MaxExposurePct == 0 {
		cfg.MaxExposurePct = 0.8
	}
	if cfg.MaxShortOpenPositions == 0 {
		cfg.MaxShortOpenPositions = 3
	}
	if cfg.MaxShortExposurePct == 0 {
		cfg.MaxShortExposurePct = 0.3
	}
	if cfg.EntryCooldownSec == 0 {
		cfg.EntryCooldownSec = 30
	}
	if cfg.ExitCooldownSec == 0 {
		cfg.ExitCooldownSec = 10
	}
	if cfg.MinEntryScore == 0 {
		cfg.MinEntryScore = 2.0
	}
	if cfg.ShortMinEntryScore == 0 {
		cfg.ShortMinEntryScore = 3.0
	}
	if cfg.MinOneMinuteReturnPct == 0 {
		cfg.MinOneMinuteReturnPct = 0.2
	}
	if cfg.MinThreeMinuteReturnPct == 0 {
		cfg.MinThreeMinuteReturnPct = 0.5
	}
	if cfg.MinVolumeRate == 0 {
		cfg.MinVolumeRate = 1.5
	}
	if cfg.BreakoutFailureWindowMin == 0 {
		cfg.BreakoutFailureWindowMin = 5
	}
	if cfg.StagnationWindowMin == 0 {
		cfg.StagnationWindowMin = 15
	}
	// Scanner thresholds — scale with capital
	if cfg.ScannerWorkers == 0 {
		cfg.ScannerWorkers = 4
	}
	if cfg.MinPrice == 0 {
		cfg.MinPrice = 2.0
	}
	if cfg.MaxPrice == 0 {
		cfg.MaxPrice = 200.0
	}
	if cfg.MinGapPercent == 0 {
		cfg.MinGapPercent = 3.0
	}
	if cfg.MinRelativeVolume == 0 {
		cfg.MinRelativeVolume = 2.0
	}
	if cfg.MinPremarketVolume == 0 {
		cfg.MinPremarketVolume = 50000
	}
	if cfg.ScannerMinSetupVolumeRateOffset == 0 {
		cfg.ScannerMinSetupVolumeRateOffset = 0.5
	}
	if cfg.ScannerMinSetupRelativeVolumeExtra == 0 {
		cfg.ScannerMinSetupRelativeVolumeExtra = 0.5
	}
	if cfg.ScannerVWAPTolerancePct == 0 {
		cfg.ScannerVWAPTolerancePct = 2.0
	}
	// Market regime
	if len(cfg.MarketRegimeBenchmarkSymbols) == 0 {
		cfg.MarketRegimeBenchmarkSymbols = []string{"SPY", "QQQ", "IWM"}
	}
	if cfg.MarketRegimeMinBenchmarks == 0 {
		cfg.MarketRegimeMinBenchmarks = 2
	}
	if cfg.MarketRegimeEMAFastPeriod == 0 {
		cfg.MarketRegimeEMAFastPeriod = 9
	}
	if cfg.MarketRegimeEMASlowPeriod == 0 {
		cfg.MarketRegimeEMASlowPeriod = 21
	}
	if cfg.MarketRegimeReturnLookbackMin == 0 {
		cfg.MarketRegimeReturnLookbackMin = 30
	}

	// Hydration
	if cfg.HydrationRequestsPerMin == 0 {
		cfg.HydrationRequestsPerMin = 150
	}
	if cfg.HydrationRetrySec == 0 {
		cfg.HydrationRetrySec = 5
	}
	if cfg.HydrationQueueSize == 0 {
		cfg.HydrationQueueSize = 500
	}

	// Execution
	if cfg.LimitOrderSlippageDollars == 0 {
		cfg.LimitOrderSlippageDollars = 0.05
	}
	if cfg.EntryATRPercentFallback == 0 {
		cfg.EntryATRPercentFallback = 2.0
	}
	if cfg.EntryStopATRMultiplier == 0 {
		cfg.EntryStopATRMultiplier = 1.5
	}
	if cfg.MaxRiskATRMultiplier == 0 {
		cfg.MaxRiskATRMultiplier = 3.0
	}

	// Trade management
	if cfg.BreakEvenMinR == 0 {
		cfg.BreakEvenMinR = 0.5
	}
	if cfg.TrailActivationR == 0 {
		cfg.TrailActivationR = 1.0
	}
	if cfg.TrailATRMultiplier == 0 {
		cfg.TrailATRMultiplier = 2.0
	}
	if cfg.TightTrailTriggerR == 0 {
		cfg.TightTrailTriggerR = 2.5
	}
	if cfg.TightTrailATRMultiplier == 0 {
		cfg.TightTrailATRMultiplier = 1.0
	}
	if cfg.ProfitTargetR == 0 {
		cfg.ProfitTargetR = 4.0
	}
	if cfg.FailedBreakoutCutR == 0 {
		cfg.FailedBreakoutCutR = -0.5
	}
	// Short-specific
	if cfg.ShortPeakExtensionMinPct == 0 {
		cfg.ShortPeakExtensionMinPct = 5.0
	}
	if cfg.ShortVWAPBreakMinPct == 0 {
		cfg.ShortVWAPBreakMinPct = 1.0
	}
	if cfg.ShortStopATRMultiplier == 0 {
		cfg.ShortStopATRMultiplier = 2.0
	}

	// Regime gating defaults
	if !cfg.RegimeGatingEnabled && cfg.RegimeMixedScoreBoost == 0 {
		cfg.RegimeGatingEnabled = true
	}
	if cfg.RegimeMixedScoreBoost == 0 {
		cfg.RegimeMixedScoreBoost = 1.25
	}
	if cfg.RegimeNeutralScoreBoost == 0 {
		cfg.RegimeNeutralScoreBoost = 1.10
	}

	// Confidence sizing defaults
	if !cfg.ConfidenceSizingEnabled && cfg.ConfidenceSizingFloor == 0 {
		cfg.ConfidenceSizingEnabled = true
	}
	if cfg.ConfidenceSizingFloor == 0 {
		cfg.ConfidenceSizingFloor = 0.5
	}

	// Stagnation fix: R-multiple threshold
	if cfg.StagnationMinPeakR == 0 {
		cfg.StagnationMinPeakR = 0.3
	}

	// Playbook exit defaults
	cfg.PlaybookExits = defaultPlaybookExits(cfg.PlaybookExits)

	// Scale position count and limits with capital
	if equity >= 100000 {
		cfg.MaxOpenPositions = intMax(cfg.MaxOpenPositions, 8)
		cfg.MaxTradesPerDay = intMax(cfg.MaxTradesPerDay, 30)
	}

	return cfg
}

func defaultPlaybookExits(pe PlaybookExitsConfig) PlaybookExitsConfig {
	if pe.Breakout == (PlaybookExitConfig{}) {
		pe.Breakout = PlaybookExitConfig{
			ProfitTargetR:            4.0,
			FailedBreakoutCutR:       -0.5,
			BreakoutFailureWindowMin: 5,
			StagnationWindowMin:      15,
			StagnationMinPeakR:       0.3,
			TrailActivationR:         1.0,
			TrailATRMultiplier:       2.0,
			TightTrailTriggerR:       2.5,
			TightTrailATRMultiplier:  1.0,
		}
	}
	if pe.Pullback == (PlaybookExitConfig{}) {
		pe.Pullback = PlaybookExitConfig{
			ProfitTargetR:            3.0,
			FailedBreakoutCutR:       -0.3,
			BreakoutFailureWindowMin: 10,
			StagnationWindowMin:      20,
			StagnationMinPeakR:       0.2,
			TrailActivationR:         0.75,
			TrailATRMultiplier:       1.5,
			TightTrailTriggerR:       2.0,
			TightTrailATRMultiplier:  0.8,
		}
	}
	if pe.Continuation == (PlaybookExitConfig{}) {
		pe.Continuation = PlaybookExitConfig{
			ProfitTargetR:            5.0,
			FailedBreakoutCutR:       -0.5,
			BreakoutFailureWindowMin: 5,
			StagnationWindowMin:      15,
			StagnationMinPeakR:       0.3,
			TrailActivationR:         1.0,
			TrailATRMultiplier:       2.5,
			TightTrailTriggerR:       3.0,
			TightTrailATRMultiplier:  1.2,
		}
	}
	if pe.Reversal == (PlaybookExitConfig{}) {
		pe.Reversal = PlaybookExitConfig{
			ProfitTargetR:            2.5,
			FailedBreakoutCutR:       -0.3,
			BreakoutFailureWindowMin: 5,
			StagnationWindowMin:      10,
			StagnationMinPeakR:       0.2,
			TrailActivationR:         0.5,
			TrailATRMultiplier:       1.5,
			TightTrailTriggerR:       1.5,
			TightTrailATRMultiplier:  0.8,
		}
	}
	return pe
}

func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
