package config

// TradingConfig centralizes strategy and risk parameters.
type TradingConfig struct {
	StartingCapital                    float64
	StrategyProfileName                string
	StrategyProfileVersion             string
	EnableMarketRegime                 bool
	EnableShorts                       bool
	RiskPerTradePct                    float64
	DailyLossLimitPct                  float64
	MaxTradesPerDay                    int
	MaxOpenPositions                   int
	MaxExposurePct                     float64
	MaxShortOpenPositions              int
	MaxShortExposurePct                float64
	EntryCooldownSec                   int
	ExitCooldownSec                    int
	MinEntryScore                      float64
	ShortMinEntryScore                 float64
	MinOneMinuteReturnPct              float64
	MinThreeMinuteReturnPct            float64
	MinVolumeRate                      float64
	BreakoutFailureWindowMin           int
	StagnationWindowMin                int
	ScannerWorkers                     int
	MinPrice                           float64
	MaxPrice                           float64
	MinGapPercent                      float64
	MinRelativeVolume                  float64
	MinPremarketVolume                 int64
	MaxFloat                           int64  // max float for momentum filtering (0 = disabled)
	MinFloat                           int64  // min float to avoid micro-float stocks (0 = disabled)
	FloatOverrideURL                   string // URL or file path to CSV with symbol,float data
	ScannerMinSetupVolumeRateOffset    float64
	ScannerMinSetupRelativeVolumeExtra float64
	ScannerVWAPTolerancePct            float64
	MaxFloat                           int64   // 0 = disabled; max outstanding shares for momentum filtering
	MinFloat                           int64   // 0 = disabled; min outstanding shares
	MaxDistanceFromHighPct             float64 // 0 = disabled; longs must be within X% of HOD
	VolumeOnPullbackEnabled            bool    // score bonus/penalty based on pullback volume pattern
	MarketRegimeBenchmarkSymbols       []string
	MarketRegimeMinBenchmarks          int
	MarketRegimeEMAFastPeriod          int
	MarketRegimeEMASlowPeriod          int
	MarketRegimeReturnLookbackMin      int
	HydrationRequestsPerMin            int
	HydrationRetrySec                  int
	HydrationQueueSize                 int
	LimitOrderSlippageDollars          float64
	EntryATRPercentFallback            float64
	EntryStopATRMultiplier             float64
	MaxRiskATRMultiplier               float64
	BreakEvenMinR                      float64
	TrailActivationR                   float64
	TrailATRMultiplier                 float64
	TightTrailTriggerR                 float64
	TightTrailATRMultiplier            float64
	ProfitTargetR                      float64
	FailedBreakoutCutR                 float64
	ShortPeakExtensionMinPct           float64
	ShortVWAPBreakMinPct               float64
	ShortStopATRMultiplier             float64

	// Regime gating (Change 4)
	RegimeGatingEnabled     bool
	RegimeMixedScoreBoost   float64
	RegimeNeutralScoreBoost float64

	// Playbook-specific exits (Change 5)
	PlaybookExits PlaybookExitsConfig

	// Confidence-based sizing (Change 7)
	ConfidenceSizingEnabled bool
	ConfidenceSizingFloor   float64

	// Stagnation fix (Change 8) — R-multiple, not pct/100
	StagnationMinPeakR float64

	// Phase 2: Portfolio heat tracking (Change 1)
	PortfolioHeatEnabled  bool
	MaxPortfolioHeatPct   float64
	PortfolioHeatAlertPct float64

	// Phase 2: Graduated daily loss response (Change 2)
	DailyLossModeratePct float64
	DailyLossSeverePct   float64
	DailyLossHaltPct     float64

	// Phase 2: Sector concentration limits (Change 3)
	SectorConcentrationEnabled bool
	MaxPositionsPerSector      int
	MaxSectorExposurePct       float64

	// Phase 2: Correlation-aware position approval (Change 4)
	CorrelationCheckEnabled bool
	CorrelationWindowSize   int
	MaxAvgCorrelation       float64

	// Phase 2: Kelly Criterion sizing (Change 5)
	KellySizingEnabled bool
	KellyWindowSize    int
	KellyMinTrades     int
	KellyFraction      float64
	MaxKellyRiskPct    float64

	// Phase 2: Volatility-based position sizing (Change 6)
	VolTargetSizingEnabled bool
	TargetVolPerPosition   float64
	DefaultVolatility      float64

	// Phase 2: Drawdown-based risk reduction (Change 7)
	DrawdownRiskEnabled   bool
	MaxAcceptableDrawdown float64

	// Phase 3: RSI overbought/oversold filter (Change 1)
	RSIFilterEnabled       bool
	RSIOverboughtThreshold float64
	RSIOversoldThreshold   float64

	// Strategy quality filters
	MinRiskRewardRatio            float64 // 0 = disabled; min reward/risk ratio before entry (e.g. 2.0)
	EntryDeadlineMinutesAfterOpen int     // 0 = disabled; block entries after N minutes from open
	MidDayScoreMultiplier         float64 // 0 = use hardcoded default (1.15); score multiplier for midday entries

	// Phase 3: Time-of-day adaptive parameters (Change 3)
	TimeOfDayEnabled bool

	// Phase 3: Partial exit framework (Change 4)
	PartialExitsEnabled  bool
	PartialTrigger1R     float64
	PartialTrigger1Pct   float64
	PartialTrigger2R     float64
	PartialTrigger2Pct   float64
	MoveStopAfterPartial bool

	// Phase 3: Adaptive trailing stops (Change 5)
	AdaptiveTrailEnabled bool

	// Phase 3: Mean-reversion overlay (Change 6)
	MeanReversionEnabled bool
	MeanReversionMaxADX  float64
	BollingerPeriod      int
	BollingerK           float64

	// Phase 3: Percentage-based slippage (Change 7)
	SlippageLiquidBps   float64
	SlippageMidBps      float64
	SlippageIlliquidBps float64

	// Phase 4: Monte Carlo simulation (Change 1)
	MonteCarloEnabled bool
	MonteCarloSims    int

	// Phase 4: Transaction cost model (Change 2)
	TransactionCostsEnabled bool
	CommissionPerShare      float64
	DefaultSpreadBps        float64

	// Phase 4: Bootstrap significance testing (Change 3)
	BootstrapEnabled   bool
	BootstrapResamples int

	// Phase 4: Optimizer improvements (Change 4)
	OptimizerSamples   int
	OptimizerUseLHS    bool
	OptimizerTimeSplit bool

	// Phase 4: Walk-forward analysis (Change 7)
	WalkForwardEnabled bool
	WFISWindowDays     int
	WFOOSWindowDays    int
	WFPurgeGapDays     int
	WFStepDays         int

	// Phase 5: HMM regime detection (Change 1)
	HMMRegimeEnabled bool
	HMMConfidenceMin float64
	HMMParamsFile    string

	// Phase 5: Bayesian optimization (Change 2)
	BayesianOptEnabled  bool
	BayesianExploration int

	// Phase 5: Factor model decomposition (Change 3)
	FactorAnalysisEnabled bool

	// Backtest fixes: entry throttle and ATR minimum
	MaxEntriesPerMinute int `json:"max_entries_per_minute" yaml:"max_entries_per_minute"`
	MinATRBars          int `json:"min_atr_bars" yaml:"min_atr_bars"`

	// Phase 5: Almgren-Chriss impact model (Change 4)
	ImpactModelEnabled     bool
	MaxAcceptableImpactPct float64

	// Phase 5: ML scoring (Change 5)
	MLScoringEnabled bool
	MLModelPath      string
	MLScoreWeight    float64

	// Phase 5: Meta-labeling (Change 6)
	MetaLabelEnabled   bool
	MetaLabelModelPath string
	MetaLabelMinProb   float64

	// Phase 5: CPCV backtest validation (Change 7)
	CPCVEnabled  bool
	CPCVGroups   int
	CPCVPurgeGap int

	// Statistical validation: Multiple Hypothesis Testing (Section 5.3)
	MHTCorrectionMethod string  // "none", "bonferroni", or "benjamini-hochberg"
	MHTAlpha            float64 // significance level for MHT corrections (default 0.05)

	// Risk enhancements: VaR/CVaR (Section 2.2)
	VaREnabled         bool
	VaRConfidenceLevel float64 // e.g. 0.95
	VaRDailyLimitPct   float64 // max daily VaR as pct of account
	VaRMethod          string  // "parametric" or "historical"
	CVaRPositionSizing bool    // use CVaR for position sizing

	// Risk enhancements: GARCH(1,1) volatility forecasting (Section 2.5)
	GARCHEnabled    bool
	GARCHAlpha      float64 // ARCH coefficient
	GARCHBeta       float64 // GARCH coefficient
	GARCHLongRunVar float64 // long-run variance

	// Risk enhancements: Dynamic risk budgeting (Section 2.6)
	DynamicRiskBudgetEnabled bool
	TargetVolAnnualized      float64 // target portfolio vol (annualized)
	DailyRiskBudgetPct       float64 // daily risk budget as pct of account

	// Alpha signals: Order Flow Imbalance (OFI)
	OFIEnabled        bool
	OFIWindowBars     int
	OFIThresholdSigma float64
	OFIPersistenceMin int

	// Alpha signals: VPIN
	VPINEnabled         bool
	VPINBucketDivisor   int
	VPINLookbackBuckets int
	VPINHighThreshold   float64
	VPINLowThreshold    float64

	// Alpha signals: OBV Divergence
	OBVDivergenceEnabled bool
	OBVLookbackBars      int

	// Alpha signals: Dollar Bars
	DollarBarsEnabled  bool
	DollarBarThreshold float64

	// Alpha signals: Volume Bars
	VolumeBarsEnabled  bool
	VolumeBarThreshold int64

	// Alpha signals: Opening Range Breakout (ORB)
	ORBEnabled          bool
	ORBWindowMinutes    int
	ORBBufferPct        float64
	ORBVolumeMultiplier float64
	ORBMaxGapPct        float64
	ORBTargetMultiplier float64

	// Alpha signals: placeholders for future signals
	NewsSentimentEnabled bool
	UOAEnabled           bool

	// Execution optimization: VWAP (Section 4.1)
	VWAPExecutionEnabled bool
	VWAPMinOrderADVPct   float64 // min order size as fraction of ADV to trigger VWAP

	// Execution optimization: TWAP (Section 4.2)
	TWAPExecutionEnabled bool
	TWAPSlices           int // number of equal time slices
	TWAPWindowSeconds    int // total execution window in seconds

	// Execution optimization: Adaptive limit pricing (Section 4.5)
	AdaptiveLimitEnabled          bool
	AdaptiveLimitToleranceBps     float64 // initial limit offset from mid in bps
	AdaptiveLimitWidenStepBps     float64 // widening step per interval in bps
	AdaptiveLimitWidenIntervalSec int     // seconds between widening steps
	AdaptiveLimitMaxSlippageBps   float64 // max allowed slippage from arrival price in bps

	// Portfolio construction: Mean-Variance Optimization (Section 3.1)
	MVOEnabled           bool
	MVORiskAversion      float64 // lambda in w* = λ⁻¹ · Σ⁻¹ · μ
	MVOMaxPositionPct    float64 // max single position weight
	MVOMaxSectorPct      float64 // max sector weight
	LedoitWolfShrinkage  float64 // shrinkage intensity δ (0=sample, 1=target)

	// Portfolio construction: Risk Parity (Section 3.2)
	RiskParityEnabled            bool
	RiskParityEWMALambda         float64 // EWMA decay factor for vol estimation
	RiskParityRebalanceMinutes   int     // rebalance interval
	RiskParityDeviationThreshold float64 // rebalance trigger: weight deviation

	// Portfolio construction: Factor-Neutral (Section 3.3)
	FactorNeutralEnabled bool
	FactorBetaWindow     int     // rolling window for beta estimation
	MaxNetBeta           float64 // max absolute net portfolio beta

	// Portfolio construction: HHI Diversification (Section 3.4)
	HHIEnabled        bool
	HHIMaxTarget      float64 // log warning above this
	HHIAlertThreshold float64 // block new entries above this

	// Portfolio construction: Long-Short Balancing (Section 3.5)
	LongShortBalancingEnabled bool
	DollarNeutralTolerance    float64 // max imbalance ratio
	BetaNeutralThreshold      float64 // max |beta_long - beta_short|
	MaxGrossLeverage          float64 // (long + |short|) / equity cap
	SectorNeutralTolerance    float64 // per-sector net exposure tolerance

	// ML Pipeline: Fractional Differentiation (Section 6.1)
	FracDiffEnabled bool
	FracDiffMinD    float64 // minimum fractional diff order (default 0.3)
	FracDiffMaxD    float64 // maximum fractional diff order (default 0.5)

	// ML Pipeline: Training (Section 6.2)
	MLTrainingEnabled      bool
	MLRetrainIntervalDays  int     // days between retraining (default 7)
	MLFeatureHorizonBars   int     // forward return horizon for labels (default 15)

	// ML Pipeline: Concept Drift Detection (Section 6.3)
	ConceptDriftEnabled    bool
	PSIThreshold           float64 // PSI threshold to trigger retrain (default 0.2)
	SharpeDecayThreshold   float64 // live/backtest Sharpe ratio threshold (default 0.5)

	// ML Pipeline: Meta-Label Confidence (Section 6.4)
	MetaLabelConfidenceThreshold float64 // minimum meta-label probability (default 0.5)

	// ML Pipeline: Ensemble Methods (Section 6.5)
	EnsembleEnabled            bool
	EnsembleMethod             string  // "equal", "ir_weighted", or "regime_conditional"
	EnsembleDiversityThreshold float64 // max avg pairwise signal correlation (default 0.6)

	// ML Pipeline: Scoring Integration (Section 6.2)
	MLScoringThreshold         float64 // minimum ML score to allow trade (default 0.5)
	MLScoringWeightInEnsemble  float64 // ML signal weight in ensemble (default 1.0)

	// Strategy quality filters
	EntryDeadlineMinutesAfterOpen int     // 0 = disabled, 120 = block entries after 2 hours from open
	MinRiskRewardRatio            float64 // 0 = disabled, 2.0 = require 2:1 R:R minimum
	MaxDistanceFromHighPct        float64 // 0 = disabled, 5.0 = longs must be within 5% of HOD
	VolumeOnPullbackEnabled       bool    // enable volume-on-pullback scoring
	MidDayScoreMultiplier         float64 // 0 = use hardcoded 1.15, otherwise override midday threshold multiplier
}

// PlaybookExitConfig holds exit parameters for a single playbook.
type PlaybookExitConfig struct {
	ProfitTargetR            float64
	FailedBreakoutCutR       float64
	BreakoutFailureWindowMin int
	StagnationWindowMin      int
	StagnationMinPeakR       float64
	TrailActivationR         float64
	TrailATRMultiplier       float64
	TightTrailTriggerR       float64
	TightTrailATRMultiplier  float64
}

// PlaybookExitsConfig holds exit configs for all playbooks.
type PlaybookExitsConfig struct {
	Breakout     PlaybookExitConfig
	Pullback     PlaybookExitConfig
	Continuation PlaybookExitConfig
	Reversal     PlaybookExitConfig
}

// DefaultTradingConfig returns the tuned baseline.
func DefaultTradingConfig() TradingConfig {
	return TuneTradingConfig(TradingConfig{StartingCapital: defaultStartingCapital}, defaultStartingCapital, 0)
}
