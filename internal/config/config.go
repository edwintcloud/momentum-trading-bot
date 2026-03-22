package config

// TradingConfig centralizes strategy and risk parameters.
type TradingConfig struct {
	StartingCapital                      float64
	StrategyProfileName                  string
	StrategyProfileVersion               string
	EnableMarketRegime                   bool
	EnableShorts                         bool
	RiskPerTradePct                      float64
	DailyLossLimitPct                    float64
	MaxTradesPerDay                      int
	MaxOpenPositions                     int
	MaxExposurePct                       float64
	MaxShortOpenPositions                int
	MaxShortExposurePct                  float64
	EntryCooldownSec                     int
	ExitCooldownSec                      int
	MinEntryScore                        float64
	ShortMinEntryScore                   float64
	MinOneMinuteReturnPct                float64
	MinThreeMinuteReturnPct              float64
	MinVolumeRate                        float64
	BreakoutFailureWindowMin             int
	StagnationWindowMin                  int
	ScannerWorkers                       int
	MinPrice                             float64
	MaxPrice                             float64
	MinGapPercent                        float64
	MinRelativeVolume                    float64
	MinPremarketVolume                   int64
	ScannerMinSetupVolumeRateOffset      float64
	ScannerMinSetupRelativeVolumeExtra   float64
	ScannerVWAPTolerancePct              float64
	MarketRegimeBenchmarkSymbols         []string
	MarketRegimeMinBenchmarks            int
	MarketRegimeEMAFastPeriod            int
	MarketRegimeEMASlowPeriod            int
	MarketRegimeReturnLookbackMin        int
	HydrationRequestsPerMin              int
	HydrationRetrySec                    int
	HydrationQueueSize                   int
	LimitOrderSlippageDollars            float64
	EntryATRPercentFallback              float64
	EntryStopATRMultiplier               float64
	MaxRiskATRMultiplier                 float64
	BreakEvenMinR                        float64
	TrailActivationR                     float64
	TrailATRMultiplier                   float64
	TightTrailTriggerR                   float64
	TightTrailATRMultiplier              float64
	ProfitTargetR                        float64
	FailedBreakoutCutR                   float64
	ShortPeakExtensionMinPct             float64
	ShortVWAPBreakMinPct                 float64
	ShortStopATRMultiplier               float64

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
