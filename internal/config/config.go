package config

// TradingConfig centralizes strategy and risk parameters.
type TradingConfig struct {
	StartingCapital                 float64
	RiskPerTradePct                 float64
	DailyLossLimitPct               float64
	MaxTradesPerDay                 int
	MaxOpenPositions                int
	MaxExposurePct                  float64
	StopLossPct                     float64
	TrailingStopPct                 float64
	TrailingStopActivationPct       float64
	EntryCooldownSec                int
	ExitCooldownSec                 int
	MinEntryScore                   float64
	MinOneMinuteReturnPct           float64
	MinThreeMinuteReturnPct         float64
	MinVolumeRate                   float64
	MaxPriceVsOpenPct               float64
	BreakoutFailureWindowMin        int
	BreakoutFailureLossPct          float64
	BreakEvenActivationPct          float64
	BreakEvenFloorPct               float64
	StagnationWindowMin             int
	StagnationMinPeakPct            float64
	ScannerWorkers                  int
	MinPrice                        float64
	MaxPrice                        float64
	MinGapPercent                   float64
	MinRelativeVolume               float64
	MinPremarketVolume              int64
	HydrationRequestsPerMin         int
	HydrationRetrySec               int
	HydrationQueueSize              int
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
	ProfitTrailActivationR    float64
	ProfitTrailPct            float64
	FailedBreakoutCutR        float64
	StructureConfirmR         float64
}

// DefaultTradingConfig returns a simulation-safe baseline.
func DefaultTradingConfig() TradingConfig {
	return TradingConfig{
		StartingCapital:         100_000,
		HydrationRequestsPerMin: 120,
		HydrationRetrySec:       300,
		HydrationQueueSize:      512,
	}
}
