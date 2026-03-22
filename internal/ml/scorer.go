package ml

// ScorerFeatures extracts the feature vector for ML scoring from a candidate.
type ScorerFeatures struct {
	RelativeVolume     float64
	GapPercent         float64
	VolumeRate         float64
	OneMinuteReturn    float64
	ThreeMinuteReturn  float64
	BreakoutPct        float64
	PriceVsVWAPPct     float64
	EMAAlignment       float64 // (emaFast - emaSlow) / emaSlow
	RSI                float64
	RSIMASlope         float64
	ATR                float64
	ConsolidationRange float64
	PullbackDepth      float64
	TimeOfDay          float64 // minutes since market open / 390
	RegimeProb         float64 // bullish probability
	VolumeLeaderPct    float64
}

// ToSlice converts features to a float64 slice for model input.
func (f ScorerFeatures) ToSlice() []float64 {
	return []float64{
		f.RelativeVolume,
		f.GapPercent,
		f.VolumeRate,
		f.OneMinuteReturn,
		f.ThreeMinuteReturn,
		f.BreakoutPct,
		f.PriceVsVWAPPct,
		f.EMAAlignment,
		f.RSI,
		f.RSIMASlope,
		f.ATR,
		f.ConsolidationRange,
		f.PullbackDepth,
		f.TimeOfDay,
		f.RegimeProb,
		f.VolumeLeaderPct,
	}
}

// FeatureNames returns the ordered names corresponding to ToSlice.
func FeatureNames() []string {
	return []string{
		"relative_volume",
		"gap_percent",
		"volume_rate",
		"one_minute_return",
		"three_minute_return",
		"breakout_pct",
		"price_vs_vwap_pct",
		"ema_alignment",
		"rsi",
		"rsi_ma_slope",
		"atr",
		"consolidation_range",
		"pullback_depth",
		"time_of_day",
		"regime_prob",
		"volume_leader_pct",
	}
}

// Scorer defines the interface for ML-based trade probability scoring.
type Scorer interface {
	// Score returns P(profitable trade) for the given features.
	Score(features ScorerFeatures) (float64, error)
	// Enabled returns whether the scorer has a loaded model.
	Enabled() bool
}

// StubScorer is a no-op scorer that returns a neutral probability.
// Used when no trained model is available.
type StubScorer struct{}

// Score always returns 0.5 (neutral) with no error.
func (s *StubScorer) Score(_ ScorerFeatures) (float64, error) {
	return 0.5, nil
}

// Enabled returns false since no model is loaded.
func (s *StubScorer) Enabled() bool {
	return false
}

// NewStubScorer creates a stub scorer that returns neutral predictions.
func NewStubScorer() *StubScorer {
	return &StubScorer{}
}
