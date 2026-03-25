package ml

import "time"

// Bar is a minimal OHLCV bar for triple barrier labeling.
type Bar struct {
	Timestamp time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    int64
}

// BarrierLabel labels a trade based on which barrier is hit first.
type BarrierLabel struct {
	Label    int     // +1 (profit), -1 (loss), 0 (timeout)
	Return   float64 // actual return achieved
	Duration int     // bars to resolution
}

// TripleBarrierLabeling labels a trade entry based on which barrier is hit first:
// upper barrier (profit target), lower barrier (stop loss), or time barrier.
func TripleBarrierLabeling(bars []Bar, entryIdx int, upperPct, lowerPct float64, maxBars int) BarrierLabel {
	if entryIdx < 0 || entryIdx >= len(bars) {
		return BarrierLabel{}
	}

	entryPrice := bars[entryIdx].Close
	if entryPrice <= 0 {
		return BarrierLabel{}
	}

	upper := entryPrice * (1 + upperPct)
	lower := entryPrice * (1 - lowerPct)

	lastIdx := entryIdx + maxBars
	if lastIdx >= len(bars) {
		lastIdx = len(bars) - 1
	}

	for i := entryIdx + 1; i <= lastIdx; i++ {
		// Check upper barrier (profit target) using high
		if bars[i].High >= upper {
			return BarrierLabel{Label: 1, Return: upperPct, Duration: i - entryIdx}
		}
		// Check lower barrier (stop loss) using low
		if bars[i].Low <= lower {
			return BarrierLabel{Label: -1, Return: -lowerPct, Duration: i - entryIdx}
		}
	}

	// Time barrier: use actual return at expiration
	ret := (bars[lastIdx].Close - entryPrice) / entryPrice
	label := 0
	if ret > 0 {
		label = 1
	} else if ret < 0 {
		label = -1
	}
	return BarrierLabel{Label: label, Return: ret, Duration: lastIdx - entryIdx}
}

// MetaLabelSizing adjusts position sizing based on meta-model probability.
// The primary model (rule-based signals) decides direction.
// The meta-model predicts P(primary signal is profitable).
// Position size = direction * f(meta_probability).
func MetaLabelSizing(metaProbability float64, baseQuantity int, minProb float64) int {
	if minProb <= 0 {
		minProb = 0.40
	}

	if metaProbability < minProb {
		return 0 // skip trade — meta-model says low probability
	}

	// Scale from 0.5x (at minProb) to 1.5x (at 0.80)
	denom := 0.80 - minProb
	if denom <= 0 {
		denom = 0.01
	}
	scaleFactor := 0.5 + (metaProbability-minProb)/denom*1.0
	if scaleFactor > 1.5 {
		scaleFactor = 1.5
	}
	if scaleFactor < 0.5 {
		scaleFactor = 0.5
	}

	result := int(float64(baseQuantity) * scaleFactor)
	if result < 0 {
		result = 0
	}
	return result
}
