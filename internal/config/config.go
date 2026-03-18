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
	EntryModelEnabled               bool
	EntryModelPath                  string
	EntryModelMinPredictedReturnPct float64
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
		StartingCapital:                 100_000,
		RiskPerTradePct:                 0.01,
		DailyLossLimitPct:               0.03,
		MaxTradesPerDay:                 8,
		MaxOpenPositions:                4,
		MaxExposurePct:                  0.30,
		StopLossPct:                     0.07, // not currently used
		TrailingStopPct:                 0.08, // not currently used
		TrailingStopActivationPct:       0.03, // not currently used
		EntryCooldownSec:                45,
		ExitCooldownSec:                 5,
		EntryModelEnabled:               true,
		EntryModelMinPredictedReturnPct: 0.15,
		MinEntryScore:                   10.0,
		MinOneMinuteReturnPct:           0.05,
		MinThreeMinuteReturnPct:         0.15,
		MinVolumeRate:                   1.05,
		MaxPriceVsOpenPct:               50.0,
		BreakoutFailureWindowMin:        15,
		BreakoutFailureLossPct:          0.008,
		BreakEvenActivationPct:          0.025,
		BreakEvenFloorPct:               0.0015,
		StagnationWindowMin:             30,
		StagnationMinPeakPct:            0.012,
		ScannerWorkers:                  6,
		MinPrice:                        2.0,
		MaxPrice:                        40.0,
		MinGapPercent:                   10.0,
		MinRelativeVolume:               6.0,
		MinPremarketVolume:              500_000,
		HydrationRequestsPerMin:         120,
		HydrationRetrySec:               300,
		HydrationQueueSize:              512,
		LimitOrderSlippageDollars:       0.10,
		EntryATRPercentFallback:         0.02,
		EntryStopATRMultiplier:          1.00, // used
		MaxRiskATRMultiplier:            4.00,
		BreakEvenHoldMinutes:            5,
		BreakEvenMinR:                   0.50,
		TrailActivationR:                0.70,
		TrailATRMultiplier:              1.50,
		TightTrailTriggerR:              1.20,
		TightTrailATRMultiplier:         0.60,
		ProfitTrailActivationR:          1.50,
		ProfitTrailPct:                  0.03,
		FailedBreakoutCutR:              0.05,
		StructureConfirmR:               0.00,
	}
}
