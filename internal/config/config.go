package config

import (
	"encoding/json"
	"log"
	"os"
)

// ScannerConfig holds stock selection criteria (Ross Cameron momentum filters).
type ScannerConfig struct {
	MinPrice                 float64
	MaxPrice                 float64
	MinGapPercent            float64
	MinRelativeVolume        float64
	MinPremarketVolume       uint64
	MaxFloat                 int64
	MinFloat                 int64
	MinPrevDayVolume         uint64
	ScannerWorkers           int
	MinOneMinuteReturnPct    float64
	MinThreeMinuteReturnPct  float64
	MinFiveMinuteVolume      uint64
	MinVolumeRate            float64
	MACDFastPeriod           int
	MACDSlowPeriod           int
	MACDSignalPeriod         int
	MeanReversionEnabled     bool
	MeanReversionMaxADX      float64
	BollingerPeriod          int
	BollingerK               float64
	GapFadeEnabled           bool
	GapFadeMinGapPct         float64
	GapFadeMaxRelVol         float64
	HODMomoEnabled           bool
	HODMomoMinIntradayPct    float64
	HODMomoMinRelativeVolume float64
	HODMomoMaxDistFromHigh   float64
	HODMomoPullbackMaxDist   float64
	MaxVolumeLeaders         int // only consider the top N symbols by dollar volume (0 = disabled)
}

// StrategyConfig holds entry/exit rules and position management parameters.
type StrategyConfig struct {
	EnableShorts                 bool
	RiskPerTradePct              float64
	MaxTradesPerDay              int
	MaxOpenPositions             int
	MaxExposurePct               float64
	MaxShortOpenPositions        int
	MaxShortExposurePct          float64
	EntryCooldownSec             int
	ExitCooldownSec              int
	MinEntryScore                float64
	ShortMinEntryScore           float64
	EntryATRPercentFallback      float64
	EntryStopATRMultiplier       float64
	MaxRiskATRMultiplier         float64
	BreakEvenMinR                float64
	TrailActivationR             float64
	TrailATRMultiplier           float64
	TightTrailTriggerR           float64
	TightTrailATRMultiplier      float64
	ProfitTargetR                float64
	FailedBreakoutCutR           float64
	ShortPeakExtensionMinPct     float64
	ShortVWAPBreakMinPct         float64
	StagnationMinPeakR           float64
	BreakoutFailureWindowMin     int
	StagnationWindowMin          int
	PartialExitsEnabled          bool
	PartialTrigger1R             float64
	PartialTrigger1Pct           float64
	PartialTrigger2R             float64
	PartialTrigger2Pct           float64
	MoveStopAfterPartial         bool
	MaxEntriesPerMinute          int
	MinATRBars                   int
	DisableBearPressureLongBlock bool
	DailyProfitLockPct           float64
}

// RiskConfig holds risk management and position sizing parameters.
type RiskConfig struct {
	DailyLossLimitPct       float64
	DailyLossModeratePct    float64
	DailyLossSeverePct      float64
	DailyLossHaltPct        float64
	CorrelationCheckEnabled bool
	CorrelationWindowSize   int
	MaxAvgCorrelation       float64
	VolTargetSizingEnabled  bool
	TargetVolPerPosition    float64
	DrawdownRiskEnabled     bool
	MaxAcceptableDrawdown   float64
	SlippageLiquidBps       float64
	SlippageMidBps          float64
	SlippageIlliquidBps     float64
	TargetVolAnnualized     float64
	DailyRiskBudgetPct      float64
}

// ExecutionConfig holds order execution parameters.
type ExecutionConfig struct {
	LimitOrderSlippageDollars float64
	MaxSpreadPct              float64
	HydrationRequestsPerMin   int
	HydrationRetrySec         int
	HydrationQueueSize        int
	TransactionCostsEnabled   bool
	CommissionPerShare        float64
	DefaultSpreadBps          float64
}

// BacktestConfig holds backtesting and optimization parameters.
type BacktestConfig struct {
	MonteCarloEnabled   bool
	MonteCarloSims      int
	OptimizerSamples    int
	OptimizerUseLHS     bool
	OptimizerTimeSplit  bool
	WalkForwardEnabled  bool
	WFISWindowDays      int
	WFOOSWindowDays     int
	WFPurgeGapDays      int
	WFStepDays          int
	CPCVEnabled         bool
	CPCVGroups          int
	CPCVPurgeGap        int
	BayesianOptEnabled  bool
	BayesianExploration int
	MHTCorrectionMethod string
	MHTAlpha            float64
}

// AlphaConfig holds alpha signal source parameters.
type AlphaConfig struct {
	OFIEnabled          bool
	OFIWindowBars       int
	OFIThresholdSigma   float64
	OFIPersistenceMin   int
	VPINEnabled         bool
	VPINBucketDivisor   int
	VPINLookbackBuckets int
	VPINHighThreshold   float64
	VPINLowThreshold    float64
	ORBEnabled          bool
	ORBWindowMinutes    int
	ORBBufferPct        float64
	ORBVolumeMultiplier float64
	ORBMaxGapPct        float64
	ORBTargetMultiplier float64
}

// MLConfig holds machine learning pipeline parameters.
type MLConfig struct {
	MLScoringEnabled                   bool
	MLModelPath                        string
	MLScoreWeight                      float64
	MLScoringThreshold                 float64
	MLAdvisoryEnabled                  bool
	MLAdvisoryVetoEnabled              bool
	MLAdvisoryUpsizeEnabled            bool
	MLAdvisoryDownsizeEnabled          bool
	MLAdvisoryMinProb                  float64
	MLAdvisoryUpsizeThreshold          float64
	MLAdvisoryDownsizeThreshold        float64
	MLAdvisoryLongDownsizeThreshold    float64
	MLAdvisoryShortDownsizeThreshold   float64
	MLAdvisoryProtectEliteShortMinProb float64
	MLAdvisoryUpsizeMultiplier         float64
	MLAdvisoryDownsizeMultiplier       float64
	MLAdvisoryMaxVetosPerDay           int
	MLAdvisoryProtectTopDayRank        int
	MLAdvisoryProtectTopBarRank        int
	MLScoringWeightInEnsemble          float64
	MetaLabelEnabled                   bool
	MetaLabelModelPath                 string
	MetaLabelMinProb                   float64
	MetaLabelConfidenceThreshold       float64
	FracDiffEnabled                    bool
	FracDiffMinD                       float64
	FracDiffMaxD                       float64
	MLTrainingEnabled                  bool
	MLRetrainIntervalDays              int
	MLFeatureHorizonBars               int
	ConceptDriftEnabled                bool
	PSIThreshold                       float64
	SharpeDecayThreshold               float64
	EnsembleEnabled                    bool
	EnsembleMethod                     string
	EnsembleDiversityThreshold         float64
}

// RegimeConfig holds market regime detection parameters.
type RegimeConfig struct {
	EnableMarketRegime            bool
	MarketRegimeBenchmarkSymbols  []string
	MarketRegimeMinBenchmarks     int
	MarketRegimeEMAFastPeriod     int
	MarketRegimeEMASlowPeriod     int
	MarketRegimeReturnLookbackMin int
	HMMRegimeEnabled              bool
	HMMConfidenceMin              float64
	HMMParamsFile                 string
}

// TradingConfig centralizes all trading parameters organized by section.
// Sub-structs are embedded so fields can be accessed directly (e.g., cfg.MinPrice).
type TradingConfig struct {
	StartingCapital        float64
	StrategyProfileName    string
	StrategyProfileVersion string

	ScannerConfig
	StrategyConfig
	RiskConfig
	ExecutionConfig
	BacktestConfig
	AlphaConfig
	MLConfig
	RegimeConfig
}

// tradingConfigJSON is the sectioned JSON representation of TradingConfig.
type tradingConfigJSON struct {
	StartingCapital        float64         `json:"StartingCapital"`
	StrategyProfileName    string          `json:"StrategyProfileName,omitempty"`
	StrategyProfileVersion string          `json:"StrategyProfileVersion,omitempty"`
	Scanner                ScannerConfig   `json:"Scanner"`
	Strategy               StrategyConfig  `json:"Strategy"`
	Risk                   RiskConfig      `json:"Risk"`
	Execution              ExecutionConfig `json:"Execution"`
	Backtest               BacktestConfig  `json:"Backtest"`
	Alpha                  AlphaConfig     `json:"Alpha"`
	ML                     MLConfig        `json:"ML"`
	Regime                 RegimeConfig    `json:"Regime"`
}

// MarshalJSON produces sectioned JSON with separate Scanner, Strategy, Risk, etc. sections.
func (tc TradingConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(tradingConfigJSON{
		StartingCapital:        tc.StartingCapital,
		StrategyProfileName:    tc.StrategyProfileName,
		StrategyProfileVersion: tc.StrategyProfileVersion,
		Scanner:                tc.ScannerConfig,
		Strategy:               tc.StrategyConfig,
		Risk:                   tc.RiskConfig,
		Execution:              tc.ExecutionConfig,
		Backtest:               tc.BacktestConfig,
		Alpha:                  tc.AlphaConfig,
		ML:                     tc.MLConfig,
		Regime:                 tc.RegimeConfig,
	})
}

// UnmarshalJSON reads sectioned JSON. Falls back to legacy flat format for compatibility.
func (tc *TradingConfig) UnmarshalJSON(data []byte) error {
	// Try sectioned format first.
	var j tradingConfigJSON
	if err := json.Unmarshal(data, &j); err == nil && j.Scanner.MaxPrice > 0 {
		tc.StartingCapital = j.StartingCapital
		tc.StrategyProfileName = j.StrategyProfileName
		tc.StrategyProfileVersion = j.StrategyProfileVersion
		tc.ScannerConfig = j.Scanner
		tc.StrategyConfig = j.Strategy
		tc.RiskConfig = j.Risk
		tc.ExecutionConfig = j.Execution
		tc.BacktestConfig = j.Backtest
		tc.AlphaConfig = j.Alpha
		tc.MLConfig = j.ML
		tc.RegimeConfig = j.Regime
		return nil
	}
	// Legacy flat format: decode all fields directly.
	type flat TradingConfig // avoid infinite recursion
	var f flat
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*tc = TradingConfig(f)
	return nil
}

// DefaultTradingConfig returns the tuned baseline.
func DefaultTradingConfig() TradingConfig {
	profilePath := ResolveTradingProfilePath(os.Getenv("TRADING_PROFILE_PATH"))
	if profilePath != "" {
		profile, err := LoadTradingProfile(profilePath)
		if err == nil {
			log.Printf("config: loaded trading profile %s version %s", profile.Name, profile.Version)
			return profile.Config
		}
	}
	return TradingConfig{}
}
