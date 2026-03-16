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
	MinGapPercent                   float64
	MinRelativeVolume               float64
	MinPremarketVolume              int64
	HydrationRequestsPerMin         int
	HydrationRetrySec               int
	HydrationQueueSize              int
	// LimitOrderSlippageDollars caps the adaptive limit-order buffer used to
	// improve fill probability without over-penalizing low-priced names.
	LimitOrderSlippageDollars float64
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
		StopLossPct:                     0.07,
		TrailingStopPct:                 0.08,
		TrailingStopActivationPct:       0.03,
		EntryCooldownSec:                45,
		ExitCooldownSec:                 5,
		EntryModelEnabled:               true,
		EntryModelMinPredictedReturnPct: 0.35,
		MinEntryScore:                   15.5,
		MinOneMinuteReturnPct:           0.10,
		MinThreeMinuteReturnPct:         0.40,
		MinVolumeRate:                   1.05,
		MaxPriceVsOpenPct:               28.0,
		BreakoutFailureWindowMin:        12,
		BreakoutFailureLossPct:          0.008,
		BreakEvenActivationPct:          0.018,
		BreakEvenFloorPct:               0.0015,
		StagnationWindowMin:             45,
		StagnationMinPeakPct:            0.012,
		ScannerWorkers:                  6,
		MinPrice:                        1.0,
		MinGapPercent:                   10.0,
		MinRelativeVolume:               5.0,
		MinPremarketVolume:              500_000,
		HydrationRequestsPerMin:         120,
		HydrationRetrySec:               300,
		HydrationQueueSize:              512,
		LimitOrderSlippageDollars:       0.10,
	}
}
