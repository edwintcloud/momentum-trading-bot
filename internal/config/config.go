package config

// TradingConfig centralizes strategy and risk parameters.
type TradingConfig struct {
	StartingCapital                      float64
	StrategyProfileName                  string
	StrategyProfileVersion               string
	EnableShorts                         bool
	RiskPerTradePct                      float64
	DailyLossLimitPct                    float64
	MaxTradesPerDay                      int
	MaxOpenPositions                     int
	MaxExposurePct                       float64
	MaxShortOpenPositions                int
	MaxShortExposurePct                  float64
	StopLossPct                          float64
	EntryCooldownSec                     int
	ExitCooldownSec                      int
	MinEntryScore                        float64
	ShortMinEntryScore                   float64
	MinOneMinuteReturnPct                float64
	MinThreeMinuteReturnPct              float64
	MinVolumeRate                        float64
	MaxPriceVsOpenPct                    float64
	BreakoutFailureWindowMin             int
	StagnationWindowMin                  int
	StagnationMinPeakPct                 float64
	ScannerWorkers                       int
	MinPrice                             float64
	MaxPrice                             float64
	MinGapPercent                        float64
	MinRelativeVolume                    float64
	MinPremarketVolume                   int64
	ScannerMinPriceVsOpenPctFloor        float64
	ScannerMinPriceVsOpenGapMultiplier   float64
	ScannerMinSetupVolumeRateOffset      float64
	ScannerMinSetupRelativeVolumeExtra   float64
	ScannerVWAPTolerancePct              float64
	ScannerConsolidationATRMultiplier    float64
	ScannerConsolidationMaxPct           float64
	ScannerPullbackDepthMinATRMultiplier float64
	ScannerPullbackDepthMinPct           float64
	ScannerPullbackDepthMaxATRMultiplier float64
	ScannerPullbackDepthMaxPct           float64
	ScannerRenewedVolumeRateMin          float64
	HydrationRequestsPerMin              int
	HydrationRetrySec                    int
	HydrationQueueSize                   int
	// LimitOrderSlippageDollars caps the adaptive limit-order buffer used to
	// improve fill probability without over-penalizing low-priced names.
	LimitOrderSlippageDollars float64
	EntryATRPercentFallback   float64
	EntryStopATRMultiplier    float64
	MaxRiskATRMultiplier      float64
	BreakEvenHoldMinutes      int
	BreakEvenMinR             float64
	TrailActivationR          float64
	TrailATRMultiplier        float64
	TightTrailTriggerR        float64
	TightTrailATRMultiplier   float64
	ProfitTargetR             float64
	FailedBreakoutCutR        float64
	StructureConfirmR         float64
	ShortPeakExtensionMinPct  float64
	ShortVWAPBreakMinPct      float64
	ShortStopATRMultiplier    float64
}

// DefaultTradingConfig returns the tuned baseline used when no broker-aware
// account values have been loaded yet.
func DefaultTradingConfig() TradingConfig {
	return TuneTradingConfig(TradingConfig{StartingCapital: defaultStartingCapital}, defaultStartingCapital, 0)
}
