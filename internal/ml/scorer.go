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
	MACDHistogram      float64
	Direction          string  // "long" or "short"
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
		f.MACDHistogram,
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
		"macd_histogram",
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

// RuleBasedScorer computes a composite probability from candidate features
// using weighted signal factors (MACD, RSI, EMA alignment, volume, etc.).
// Used when MLScoringEnabled is true but no external model is loaded.
type RuleBasedScorer struct{}

// Score returns P(profitable) in [0.0, 1.0] based on weighted feature signals.
// For short direction, directional signals are inverted so bearish features score positively.
func (s *RuleBasedScorer) Score(f ScorerFeatures) (float64, error) {
	// Accumulate evidence for and against the trade.
	// Each component contributes to a raw score that is then squashed to [0, 1].
	raw := 0.0

	// Directional multiplier: +1 for longs, -1 for shorts
	// Flips directional signals so bearish features help short scores.
	dir := 1.0
	if f.Direction == "short" {
		dir = -1.0
	}

	// MACD histogram: directional momentum signal
	macd := f.MACDHistogram * dir
	if macd > 0 {
		raw += clamp01(macd/0.5) * 1.5
	} else {
		raw -= clamp01(-macd/0.5) * 1.0
	}

	// EMA alignment: directional trend signal
	ema := f.EMAAlignment * dir
	if ema > 0 {
		raw += clamp01(ema/0.02) * 1.0
	} else {
		raw -= clamp01(-ema/0.02) * 0.5
	}

	// RSI: for longs high RSI = momentum; for shorts low RSI = momentum
	rsiNorm := (f.RSI - 50) / 50 * dir // [-1, +1]
	raw += rsiNorm * 0.8

	// RSI MA slope: directional slope strength
	raw += clamp(f.RSIMASlope*dir/2.0, -1, 1) * 0.5

	// Relative volume: higher volume = more conviction (non-directional)
	raw += clamp01((f.RelativeVolume-1)/5) * 1.0

	// Gap percent: larger gap = stronger catalyst (non-directional)
	raw += clamp01(f.GapPercent/10) * 0.8

	// Breakout strength: directional
	raw += clamp01(f.BreakoutPct*dir/3) * 1.0

	// Price vs VWAP: directional
	raw += clamp(f.PriceVsVWAPPct*dir/3, -1, 1) * 0.5

	// Short-term returns confirm direction
	raw += clamp(f.OneMinuteReturn*dir/2, -1, 1) * 0.3
	raw += clamp(f.ThreeMinuteReturn*dir/3, -1, 1) * 0.3

	// Time of day: slight penalty for midday chop (0.3-0.7 = midday)
	if f.TimeOfDay > 0.3 && f.TimeOfDay < 0.7 {
		raw -= 0.2
	}

	// Regime alignment
	raw += clamp(f.RegimeProb-0.5, -0.5, 0.5) * 0.5

	// Convert raw score (roughly -3..+6) to probability via sigmoid
	// Shifted so raw=2 maps ~0.5, raw=4 maps ~0.73, raw=0 maps ~0.27
	prob := sigmoid(raw - 2.0)
	return prob, nil
}

// Enabled returns true — the rule-based scorer is always ready.
func (s *RuleBasedScorer) Enabled() bool {
	return true
}

// NewRuleBasedScorer creates a scorer using weighted technical indicators.
func NewRuleBasedScorer() *RuleBasedScorer {
	return &RuleBasedScorer{}
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
