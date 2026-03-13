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
	ScannerWorkers                  int
	MinPrice                        float64
	MinGapPercent                   float64
	MinRelativeVolume               float64
	MinPremarketVolume              int64
	HydrationRequestsPerMin         int
	HydrationRetrySec               int
	HydrationQueueSize              int
	// LimitOrderSlippageDollars is added to buy prices (and subtracted from sell
	// prices) to give limit orders a flat dollar buffer that improves fill probability.
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
		StopLossPct:                     0.05,
		TrailingStopPct:                 0.06,
		TrailingStopActivationPct:       0.02,
		EntryCooldownSec:                45,
		ExitCooldownSec:                 5,
		EntryModelEnabled:               true,
		EntryModelMinPredictedReturnPct: 1.50,
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
