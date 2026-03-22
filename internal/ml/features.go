package ml

import (
	"math"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// FeatureVector holds a named set of features for a single observation.
type FeatureVector struct {
	Symbol    string
	Timestamp time.Time
	Names     []string
	Values    []float64
}

// Get returns the value for a named feature, or 0 if not found.
func (fv FeatureVector) Get(name string) float64 {
	for i, n := range fv.Names {
		if n == name {
			return fv.Values[i]
		}
	}
	return 0
}

// FeatureInput holds the raw data needed to compute features for a single stock.
type FeatureInput struct {
	Symbol           string
	Price            float64
	Open             float64
	VWAP             float64
	Volume           int64
	ADV              float64 // average daily volume
	ATR              float64
	HighOfDay        float64
	LowOfDay         float64
	EMAFast          float64
	EMASlow          float64
	RSI              float64
	RSIMASlope       float64
	MACDHistogram    float64
	OBV              float64
	OBVMean          float64
	OBVStdDev        float64
	RealizedVol      float64 // annualized realized volatility
	MarketRegime     string
	RegimeConfidence float64
	Timestamp        time.Time

	// Rolling price series for return computation (most recent last).
	RecentCloses []float64
}

// ExtractFeatures computes the full feature vector from raw input.
// All features are causal (no future data) and stationary (z-scored or bounded).
func ExtractFeatures(input FeatureInput) FeatureVector {
	names := make([]string, 0, 20)
	values := make([]float64, 0, 20)

	add := func(name string, val float64) {
		names = append(names, name)
		values = append(values, val)
	}

	// Price-based features
	if input.Price > 0 && input.Open > 0 {
		logReturn := math.Log(input.Price / input.Open)
		add("log_return_from_open", logReturn)
	} else {
		add("log_return_from_open", 0)
	}

	if input.VWAP > 0 {
		add("z_score_vs_vwap", (input.Price-input.VWAP)/math.Max(input.ATR, 0.01))
	} else {
		add("z_score_vs_vwap", 0)
	}

	// Volume features
	if input.ADV > 0 {
		add("volume_ratio_vs_adv", float64(input.Volume)/input.ADV)
	} else {
		add("volume_ratio_vs_adv", 0)
	}

	if input.OBVStdDev > 0 {
		add("obv_z_score", (input.OBV-input.OBVMean)/input.OBVStdDev)
	} else {
		add("obv_z_score", 0)
	}

	// Momentum features
	add("rsi", clampFeature(input.RSI/100.0, 0, 1)) // normalize to [0,1]
	add("rsi_ma_slope", input.RSIMASlope)
	add("macd_histogram", input.MACDHistogram)

	// Multi-timescale returns
	if len(input.RecentCloses) >= 6 {
		n := len(input.RecentCloses)
		add("roc_5", rateOfChange(input.RecentCloses, 5))
		if n >= 11 {
			add("roc_10", rateOfChange(input.RecentCloses, 10))
		} else {
			add("roc_10", 0)
		}
		if n >= 21 {
			add("roc_20", rateOfChange(input.RecentCloses, 20))
		} else {
			add("roc_20", 0)
		}
	} else {
		add("roc_5", 0)
		add("roc_10", 0)
		add("roc_20", 0)
	}

	// Volatility features
	if input.RealizedVol > 0 {
		add("realized_vol", input.RealizedVol)
	} else {
		add("realized_vol", 0)
	}

	if input.Price > 0 {
		add("atr_ratio", input.ATR/input.Price)
	} else {
		add("atr_ratio", 0)
	}

	// EMA alignment
	if input.EMASlow > 0 {
		add("ema_alignment", (input.EMAFast-input.EMASlow)/input.EMASlow)
	} else {
		add("ema_alignment", 0)
	}

	// Regime features
	regimeProb := 0.5
	switch input.MarketRegime {
	case "bullish":
		regimeProb = input.RegimeConfidence
	case "bearish":
		regimeProb = 1.0 - input.RegimeConfidence
	}
	add("regime_bullish_prob", regimeProb)

	// Calendar features with cyclic encoding
	localTime := input.Timestamp.In(markethours.Location())
	minutesSinceOpen := float64(localTime.Hour()*60+localTime.Minute()-9*60-30) / 390.0
	minutesSinceOpen = clampFeature(minutesSinceOpen, 0, 1)

	// Cyclic encoding: sin/cos of normalized time
	add("time_sin", math.Sin(2*math.Pi*minutesSinceOpen))
	add("time_cos", math.Cos(2*math.Pi*minutesSinceOpen))

	// Distance to open/close (linear)
	add("dist_to_open", minutesSinceOpen)
	add("dist_to_close", 1.0-minutesSinceOpen)

	return FeatureVector{
		Symbol:    input.Symbol,
		Timestamp: input.Timestamp,
		Names:     names,
		Values:    values,
	}
}

// ExtractScorerFeatures converts a FeatureInput into the existing ScorerFeatures
// struct for compatibility with the Scorer interface.
func ExtractScorerFeatures(input FeatureInput) ScorerFeatures {
	emaAlignment := 0.0
	if input.EMASlow > 0 {
		emaAlignment = (input.EMAFast - input.EMASlow) / input.EMASlow
	}
	minutesSinceOpen := 0.0
	if !input.Timestamp.IsZero() {
		local := input.Timestamp.In(markethours.Location())
		minutesSinceOpen = float64(local.Hour()*60+local.Minute()-9*60-30) / 390.0
	}
	regimeProb := 0.5
	switch input.MarketRegime {
	case "bullish":
		regimeProb = input.RegimeConfidence
	case "bearish":
		regimeProb = 1.0 - input.RegimeConfidence
	}

	return ScorerFeatures{
		RelativeVolume: func() float64 {
			if input.ADV > 0 {
				return float64(input.Volume) / input.ADV
			}
			return 0
		}(),
		VolumeRate:     float64(input.Volume) / math.Max(input.ADV, 1),
		BreakoutPct:    0, // requires setupHigh context
		PriceVsVWAPPct: func() float64 {
			if input.VWAP > 0 {
				return (input.Price - input.VWAP) / input.VWAP * 100
			}
			return 0
		}(),
		EMAAlignment:       emaAlignment,
		RSI:                input.RSI,
		RSIMASlope:         input.RSIMASlope,
		ATR:                input.ATR,
		TimeOfDay:          minutesSinceOpen,
		RegimeProb:         regimeProb,
	}
}

// rateOfChange computes the percentage change over the last n periods.
func rateOfChange(closes []float64, n int) float64 {
	if len(closes) <= n {
		return 0
	}
	prev := closes[len(closes)-1-n]
	if prev == 0 {
		return 0
	}
	return (closes[len(closes)-1] - prev) / prev
}

// clampFeature restricts a value to [min, max].
func clampFeature(val, min, max float64) float64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}
