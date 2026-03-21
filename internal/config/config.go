package config

// TradingConfig centralizes strategy and risk parameters.
type TradingConfig struct {
	StartingCapital                 float64 `json:"starting_capital"`
	RiskPerTradePct                 float64 `json:"risk_per_trade_pct"`
	DailyLossLimitPct               float64 `json:"daily_loss_limit_pct"`
	MaxTradesPerDay                 int     `json:"max_trades_per_day"`
	MaxOpenPositions                int     `json:"max_open_positions"`
	MaxExposurePct                  float64 `json:"max_exposure_pct"`
	StopLossPct                     float64 `json:"stop_loss_pct"`
	TrailingStopPct                 float64 `json:"trailing_stop_pct"`
	TrailingStopActivationPct       float64 `json:"trailing_stop_activation_pct"`
	EntryCooldownSec                int     `json:"entry_cooldown_sec"`
	ExitCooldownSec                 int     `json:"exit_cooldown_sec"`
	EntryModelEnabled               bool    `json:"entry_model_enabled"`
	EntryModelPath                  string  `json:"entry_model_path"`
	EntryModelMinPredictedReturnPct float64 `json:"entry_model_min_predicted_return_pct"`
	MinEntryScore                   float64 `json:"min_entry_score"`
	MinOneMinuteReturnPct           float64 `json:"min_one_minute_return_pct"`
	MinThreeMinuteReturnPct         float64 `json:"min_three_minute_return_pct"`
	MinVolumeRate                   float64 `json:"min_volume_rate"`
	MaxPriceVsOpenPct               float64 `json:"max_price_vs_open_pct"`
	BreakoutFailureWindowMin        int     `json:"breakout_failure_window_min"`
	BreakoutFailureLossPct          float64 `json:"breakout_failure_loss_pct"`
	BreakEvenActivationPct          float64 `json:"break_even_activation_pct"`
	BreakEvenFloorPct               float64 `json:"break_even_floor_pct"`
	StagnationWindowMin             int     `json:"stagnation_window_min"`
	StagnationMinPeakPct            float64 `json:"stagnation_min_peak_pct"`
	ScannerWorkers                  int     `json:"scanner_workers"`
	MinPrice                        float64 `json:"min_price"`
	MaxPrice                        float64 `json:"max_price"`
	MinGapPercent                   float64 `json:"min_gap_percent"`
	MinRelativeVolume               float64 `json:"min_relative_volume"`
	MinPremarketVolume              int64   `json:"min_premarket_volume"`
	HydrationRequestsPerMin         int     `json:"hydration_requests_per_min"`
	HydrationRetrySec               int     `json:"hydration_retry_sec"`
	HydrationQueueSize              int     `json:"hydration_queue_size"`
	// LimitOrderSlippageDollars caps the adaptive limit-order buffer used to
	// improve fill probability without over-penalizing low-priced names.
	LimitOrderSlippageDollars float64 `json:"limit_order_slippage_dollars"`
	EntryATRPercentFallback   float64 `json:"entry_atr_percent_fallback"`
	EntryStopATRMultiplier    float64 `json:"entry_stop_atr_multiplier"`
	MaxRiskATRMultiplier      float64 `json:"max_risk_atr_multiplier"`
	BreakEvenHoldMinutes      int     `json:"break_even_hold_minutes"`
	BreakEvenMinR             float64 `json:"break_even_min_r"`
	TrailActivationR          float64 `json:"trail_activation_r"`
	TrailATRMultiplier        float64 `json:"trail_atr_multiplier"`
	TightTrailTriggerR        float64 `json:"tight_trail_trigger_r"`
	TightTrailATRMultiplier   float64 `json:"tight_trail_atr_multiplier"`
	ProfitTargetR             float64 `json:"profit_target_r"`
	ProfitTrailActivationR    float64 `json:"profit_trail_activation_r"`
	ProfitTrailPct            float64 `json:"profit_trail_pct"`
	FailedBreakoutCutR        float64 `json:"failed_breakout_cut_r"`
	StructureConfirmR         float64 `json:"structure_confirm_r"`
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
