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
	// Market regime — enable by default for regime-aware trading
	if !cfg.EnableMarketRegime {
		cfg.EnableMarketRegime = true
	}
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

	// Phase 2: Portfolio heat defaults
	if !cfg.PortfolioHeatEnabled && cfg.MaxPortfolioHeatPct == 0 {
		cfg.PortfolioHeatEnabled = true
	}
	if cfg.MaxPortfolioHeatPct == 0 {
		cfg.MaxPortfolioHeatPct = 0.05
	}
	if cfg.PortfolioHeatAlertPct == 0 {
		cfg.PortfolioHeatAlertPct = 0.03
	}

	// Phase 2: Graduated daily loss defaults
	if cfg.DailyLossModeratePct == 0 {
		cfg.DailyLossModeratePct = 0.01
	}
	if cfg.DailyLossSeverePct == 0 {
		cfg.DailyLossSeverePct = 0.015
	}
	if cfg.DailyLossHaltPct == 0 {
		cfg.DailyLossHaltPct = 0.02
	}

	// Phase 2: Sector concentration defaults
	if !cfg.SectorConcentrationEnabled && cfg.MaxPositionsPerSector == 0 {
		cfg.SectorConcentrationEnabled = true
	}
	if cfg.MaxPositionsPerSector == 0 {
		cfg.MaxPositionsPerSector = 2
	}
	if cfg.MaxSectorExposurePct == 0 {
		cfg.MaxSectorExposurePct = 0.25
	}

	// Phase 2: Correlation defaults
	if !cfg.CorrelationCheckEnabled && cfg.MaxAvgCorrelation == 0 {
		cfg.CorrelationCheckEnabled = true
	}
	if cfg.CorrelationWindowSize == 0 {
		cfg.CorrelationWindowSize = 20
	}
	if cfg.MaxAvgCorrelation == 0 {
		cfg.MaxAvgCorrelation = 0.70
	}

	// Phase 2: Kelly sizing defaults
	if !cfg.KellySizingEnabled && cfg.KellyFraction == 0 {
		cfg.KellySizingEnabled = true
	}
	if cfg.KellyWindowSize == 0 {
		cfg.KellyWindowSize = 200
	}
	if cfg.KellyMinTrades == 0 {
		cfg.KellyMinTrades = 30
	}
	if cfg.KellyFraction == 0 {
		cfg.KellyFraction = 0.25
	}
	if cfg.MaxKellyRiskPct == 0 {
		cfg.MaxKellyRiskPct = 0.02
	}

	// Phase 2: Volatility sizing defaults
	if !cfg.VolTargetSizingEnabled && cfg.TargetVolPerPosition == 0 {
		cfg.VolTargetSizingEnabled = true
	}
	if cfg.TargetVolPerPosition == 0 {
		cfg.TargetVolPerPosition = 0.02
	}
	if cfg.DefaultVolatility == 0 {
		cfg.DefaultVolatility = 0.30
	}

	// Phase 2: Drawdown risk defaults
	if !cfg.DrawdownRiskEnabled && cfg.MaxAcceptableDrawdown == 0 {
		cfg.DrawdownRiskEnabled = true
	}
	if cfg.MaxAcceptableDrawdown == 0 {
		cfg.MaxAcceptableDrawdown = 0.15
	}

	// Phase 3: RSI filter defaults
	if !cfg.RSIFilterEnabled && cfg.RSIOverboughtThreshold == 0 {
		cfg.RSIFilterEnabled = true
	}
	if cfg.RSIOverboughtThreshold == 0 {
		cfg.RSIOverboughtThreshold = 80.0
	}
	if cfg.RSIOversoldThreshold == 0 {
		cfg.RSIOversoldThreshold = 20.0
	}

	// Phase 3: Time-of-day defaults
	if !cfg.TimeOfDayEnabled && cfg.PartialTrigger1R == 0 {
		cfg.TimeOfDayEnabled = true
	}

	// Phase 3: Partial exit defaults
	if !cfg.PartialExitsEnabled && cfg.PartialTrigger1R == 0 {
		cfg.PartialExitsEnabled = true
	}
	if cfg.PartialTrigger1R == 0 {
		cfg.PartialTrigger1R = 1.0
	}
	if cfg.PartialTrigger1Pct == 0 {
		cfg.PartialTrigger1Pct = 0.50
	}
	if cfg.PartialTrigger2R == 0 {
		cfg.PartialTrigger2R = 2.0
	}
	if cfg.PartialTrigger2Pct == 0 {
		cfg.PartialTrigger2Pct = 0.50
	}
	if !cfg.MoveStopAfterPartial {
		cfg.MoveStopAfterPartial = true
	}

	// Phase 3: Adaptive trailing stop defaults
	if !cfg.AdaptiveTrailEnabled && cfg.MeanReversionMaxADX == 0 {
		cfg.AdaptiveTrailEnabled = true
	}

	// Phase 3: Mean-reversion overlay defaults
	if !cfg.MeanReversionEnabled && cfg.MeanReversionMaxADX == 0 {
		cfg.MeanReversionEnabled = true
	}
	if cfg.MeanReversionMaxADX == 0 {
		cfg.MeanReversionMaxADX = 20.0
	}
	if cfg.BollingerPeriod == 0 {
		cfg.BollingerPeriod = 20
	}
	if cfg.BollingerK == 0 {
		cfg.BollingerK = 2.0
	}

	// Phase 3: Percentage-based slippage defaults
	if cfg.SlippageLiquidBps == 0 {
		cfg.SlippageLiquidBps = 5.0
	}
	if cfg.SlippageMidBps == 0 {
		cfg.SlippageMidBps = 10.0
	}
	if cfg.SlippageIlliquidBps == 0 {
		cfg.SlippageIlliquidBps = 20.0
	}

	// Phase 4: Monte Carlo defaults
	if !cfg.MonteCarloEnabled && cfg.MonteCarloSims == 0 {
		cfg.MonteCarloEnabled = true
	}
	if cfg.MonteCarloSims == 0 {
		cfg.MonteCarloSims = 10000
	}

	// Phase 4: Transaction cost defaults
	if !cfg.TransactionCostsEnabled && cfg.CommissionPerShare == 0 {
		cfg.TransactionCostsEnabled = true
	}
	if cfg.CommissionPerShare == 0 {
		cfg.CommissionPerShare = 0.005
	}
	if cfg.DefaultSpreadBps == 0 {
		cfg.DefaultSpreadBps = 10.0
	}

	// Phase 4: Bootstrap defaults
	if !cfg.BootstrapEnabled && cfg.BootstrapResamples == 0 {
		cfg.BootstrapEnabled = true
	}
	if cfg.BootstrapResamples == 0 {
		cfg.BootstrapResamples = 10000
	}

	// Phase 4: Optimizer defaults
	if cfg.OptimizerSamples == 0 {
		cfg.OptimizerSamples = 500
	}
	if !cfg.OptimizerUseLHS && cfg.OptimizerSamples > 0 {
		cfg.OptimizerUseLHS = true
	}
	if !cfg.OptimizerTimeSplit && cfg.OptimizerSamples > 0 {
		cfg.OptimizerTimeSplit = true
	}

	// Phase 4: Walk-forward defaults
	if !cfg.WalkForwardEnabled && cfg.WFISWindowDays == 0 {
		cfg.WalkForwardEnabled = true
	}
	if cfg.WFISWindowDays == 0 {
		cfg.WFISWindowDays = 60
	}
	if cfg.WFOOSWindowDays == 0 {
		cfg.WFOOSWindowDays = 20
	}
	if cfg.WFPurgeGapDays == 0 {
		cfg.WFPurgeGapDays = 5
	}
	if cfg.WFStepDays == 0 {
		cfg.WFStepDays = 20
	}

	// Backtest fixes: entry throttle and ATR minimum defaults
	if cfg.MaxEntriesPerMinute == 0 {
		cfg.MaxEntriesPerMinute = 3
	}
	if cfg.MinATRBars == 0 {
		cfg.MinATRBars = 5
	}

	// Phase 5: HMM regime detection defaults
	if !cfg.HMMRegimeEnabled && cfg.HMMConfidenceMin == 0 {
		cfg.HMMRegimeEnabled = true
	}
	if cfg.HMMConfidenceMin == 0 {
		cfg.HMMConfidenceMin = 0.70
	}

	// Phase 5: Bayesian optimization defaults
	if !cfg.BayesianOptEnabled && cfg.BayesianExploration == 0 {
		cfg.BayesianOptEnabled = true
	}
	if cfg.BayesianExploration == 0 {
		cfg.BayesianExploration = 20
	}

	// Phase 5: Factor analysis defaults
	if !cfg.FactorAnalysisEnabled && cfg.CPCVGroups == 0 {
		cfg.FactorAnalysisEnabled = true
	}

	// Phase 5: Impact model defaults
	if !cfg.ImpactModelEnabled && cfg.MaxAcceptableImpactPct == 0 {
		cfg.ImpactModelEnabled = true
	}
	if cfg.MaxAcceptableImpactPct == 0 {
		cfg.MaxAcceptableImpactPct = 0.005
	}

	// Phase 5: ML scoring defaults (disabled until model trained)
	if cfg.MLScoreWeight == 0 {
		cfg.MLScoreWeight = 0.50
	}

	// Phase 5: Meta-labeling defaults (disabled until model trained)
	if cfg.MetaLabelMinProb == 0 {
		cfg.MetaLabelMinProb = 0.40
	}

	// Phase 5: CPCV defaults
	if !cfg.CPCVEnabled && cfg.CPCVGroups == 0 {
		cfg.CPCVEnabled = true
	}
	if cfg.CPCVGroups == 0 {
		cfg.CPCVGroups = 6
	}
	if cfg.CPCVPurgeGap == 0 {
		cfg.CPCVPurgeGap = 60
	}

	// Statistical validation: MHT correction defaults (disabled by default)
	if cfg.MHTCorrectionMethod == "" {
		cfg.MHTCorrectionMethod = "none"
	}
	if cfg.MHTAlpha == 0 {
		cfg.MHTAlpha = 0.05
	}

	// Risk enhancements: VaR/CVaR defaults (disabled by default)
	if cfg.VaRConfidenceLevel == 0 {
		cfg.VaRConfidenceLevel = 0.95
	}
	if cfg.VaRDailyLimitPct == 0 {
		cfg.VaRDailyLimitPct = 0.02
	}
	if cfg.VaRMethod == "" {
		cfg.VaRMethod = "parametric"
	}

	// Risk enhancements: GARCH defaults (disabled by default)
	if cfg.GARCHAlpha == 0 {
		cfg.GARCHAlpha = 0.10
	}
	if cfg.GARCHBeta == 0 {
		cfg.GARCHBeta = 0.85
	}
	if cfg.GARCHLongRunVar == 0 {
		cfg.GARCHLongRunVar = 0.0004 // ~2% daily vol squared
	}

	// Risk enhancements: Dynamic risk budget defaults (disabled by default)
	if cfg.TargetVolAnnualized == 0 {
		cfg.TargetVolAnnualized = 0.10
	}
	if cfg.DailyRiskBudgetPct == 0 {
		cfg.DailyRiskBudgetPct = 0.01
	}

	// Alpha signals: OFI defaults (disabled by default)
	if cfg.OFIWindowBars == 0 {
		cfg.OFIWindowBars = 60
	}
	if cfg.OFIThresholdSigma == 0 {
		cfg.OFIThresholdSigma = 3.0
	}
	if cfg.OFIPersistenceMin == 0 {
		cfg.OFIPersistenceMin = 3
	}

	// Alpha signals: VPIN defaults (disabled by default)
	if cfg.VPINBucketDivisor == 0 {
		cfg.VPINBucketDivisor = 50
	}
	if cfg.VPINLookbackBuckets == 0 {
		cfg.VPINLookbackBuckets = 50
	}
	if cfg.VPINHighThreshold == 0 {
		cfg.VPINHighThreshold = 0.7
	}
	if cfg.VPINLowThreshold == 0 {
		cfg.VPINLowThreshold = 0.3
	}

	// Alpha signals: OBV Divergence defaults (disabled by default)
	if cfg.OBVLookbackBars == 0 {
		cfg.OBVLookbackBars = 20
	}

	// Alpha signals: Dollar Bars defaults (disabled by default)
	if cfg.DollarBarThreshold == 0 {
		cfg.DollarBarThreshold = 500000
	}

	// Alpha signals: Volume Bars defaults (disabled by default)
	if cfg.VolumeBarThreshold == 0 {
		cfg.VolumeBarThreshold = 50000
	}

	// Alpha signals: ORB defaults (disabled by default)
	if cfg.ORBWindowMinutes == 0 {
		cfg.ORBWindowMinutes = 15
	}
	if cfg.ORBBufferPct == 0 {
		cfg.ORBBufferPct = 0.001
	}
	if cfg.ORBVolumeMultiplier == 0 {
		cfg.ORBVolumeMultiplier = 1.5
	}
	if cfg.ORBMaxGapPct == 0 {
		cfg.ORBMaxGapPct = 0.02
	}
	if cfg.ORBTargetMultiplier == 0 {
		cfg.ORBTargetMultiplier = 1.5
	}
	// Execution optimization: VWAP defaults (disabled by default)
	if cfg.VWAPMinOrderADVPct == 0 {
		cfg.VWAPMinOrderADVPct = 0.005
	}

	// Execution optimization: TWAP defaults (disabled by default)
	if cfg.TWAPSlices == 0 {
		cfg.TWAPSlices = 10
	}
	if cfg.TWAPWindowSeconds == 0 {
		cfg.TWAPWindowSeconds = 300
	}

	// Execution optimization: Adaptive limit defaults (disabled by default)
	if cfg.AdaptiveLimitToleranceBps == 0 {
		cfg.AdaptiveLimitToleranceBps = 5.0
	}
	if cfg.AdaptiveLimitWidenStepBps == 0 {
		cfg.AdaptiveLimitWidenStepBps = 0.5
	}
	if cfg.AdaptiveLimitWidenIntervalSec == 0 {
		cfg.AdaptiveLimitWidenIntervalSec = 5
	}
	if cfg.AdaptiveLimitMaxSlippageBps == 0 {
		cfg.AdaptiveLimitMaxSlippageBps = 20.0
	}

	// Portfolio construction: MVO defaults (disabled by default)
	if cfg.MVORiskAversion == 0 {
		cfg.MVORiskAversion = 1.0
	}
	if cfg.MVOMaxPositionPct == 0 {
		cfg.MVOMaxPositionPct = 0.05
	}
	if cfg.MVOMaxSectorPct == 0 {
		cfg.MVOMaxSectorPct = 0.25
	}
	if cfg.LedoitWolfShrinkage == 0 {
		cfg.LedoitWolfShrinkage = 0.5
	}

	// Portfolio construction: Risk parity defaults (disabled by default)
	if cfg.RiskParityEWMALambda == 0 {
		cfg.RiskParityEWMALambda = 0.94
	}
	if cfg.RiskParityRebalanceMinutes == 0 {
		cfg.RiskParityRebalanceMinutes = 30
	}
	if cfg.RiskParityDeviationThreshold == 0 {
		cfg.RiskParityDeviationThreshold = 0.20
	}

	// Portfolio construction: Factor-neutral defaults (disabled by default)
	if cfg.FactorBetaWindow == 0 {
		cfg.FactorBetaWindow = 60
	}
	if cfg.MaxNetBeta == 0 {
		cfg.MaxNetBeta = 0.30
	}

	// Portfolio construction: HHI defaults (disabled by default)
	if cfg.HHIMaxTarget == 0 {
		cfg.HHIMaxTarget = 0.10
	}
	if cfg.HHIAlertThreshold == 0 {
		cfg.HHIAlertThreshold = 0.15
	}

	// Portfolio construction: Long-short balancing defaults (disabled by default)
	if cfg.DollarNeutralTolerance == 0 {
		cfg.DollarNeutralTolerance = 0.05
	}
	if cfg.BetaNeutralThreshold == 0 {
		cfg.BetaNeutralThreshold = 0.30
	}
	if cfg.MaxGrossLeverage == 0 {
		cfg.MaxGrossLeverage = 2.0
	}
	if cfg.SectorNeutralTolerance == 0 {
		cfg.SectorNeutralTolerance = 0.10
	}

	// ML Pipeline: Fractional differentiation defaults (disabled by default)
	if cfg.FracDiffMinD == 0 {
		cfg.FracDiffMinD = 0.3
	}
	if cfg.FracDiffMaxD == 0 {
		cfg.FracDiffMaxD = 0.5
	}

	// ML Pipeline: Training defaults (disabled by default)
	if cfg.MLRetrainIntervalDays == 0 {
		cfg.MLRetrainIntervalDays = 7
	}
	if cfg.MLFeatureHorizonBars == 0 {
		cfg.MLFeatureHorizonBars = 15
	}

	// ML Pipeline: Concept drift defaults (disabled by default)
	if cfg.PSIThreshold == 0 {
		cfg.PSIThreshold = 0.2
	}
	if cfg.SharpeDecayThreshold == 0 {
		cfg.SharpeDecayThreshold = 0.5
	}

	// ML Pipeline: Meta-label confidence default
	if cfg.MetaLabelConfidenceThreshold == 0 {
		cfg.MetaLabelConfidenceThreshold = 0.5
	}

	// ML Pipeline: Ensemble defaults (disabled by default)
	if cfg.EnsembleMethod == "" {
		cfg.EnsembleMethod = "equal"
	}
	if cfg.EnsembleDiversityThreshold == 0 {
		cfg.EnsembleDiversityThreshold = 0.6
	}

	// ML Pipeline: Scoring integration defaults
	if cfg.MLScoringThreshold == 0 {
		cfg.MLScoringThreshold = 0.5
	}
	if cfg.MLScoringWeightInEnsemble == 0 {
		cfg.MLScoringWeightInEnsemble = 1.0
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
