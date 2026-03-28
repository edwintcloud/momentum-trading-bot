package ml

import (
	"strings"
	"time"
)

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

// CandidateOutcomeLabel captures a fully causal forward outcome for a candidate
// at the moment it was evaluated.
type CandidateOutcomeLabel struct {
	LabelVersion               int       `json:"labelVersion"`
	Direction                  string    `json:"direction"`
	EntryTime                  time.Time `json:"entryTime"`
	ResolvedAt                 time.Time `json:"resolvedAt"`
	EntryPrice                 float64   `json:"entryPrice"`
	UpperBarrierPct            float64   `json:"upperBarrierPct"`
	LowerBarrierPct            float64   `json:"lowerBarrierPct"`
	MaxBars                    int       `json:"maxBars"`
	Barrier                    string    `json:"barrier"`
	Label                      int       `json:"label"`
	ReturnPct                  float64   `json:"returnPct"`
	MaxFavorableExcursionPct   float64   `json:"maxFavorableExcursionPct"`
	MaxAdverseExcursionPct     float64   `json:"maxAdverseExcursionPct"`
	BarsToResolution           int       `json:"barsToResolution"`
	MinutesToResolution        float64   `json:"minutesToResolution"`
	Profitable                 bool      `json:"profitable"`
	OutcomeBucket              string    `json:"outcomeBucket"`
	TradeLinked                bool      `json:"tradeLinked"`
	RiskApproved               bool      `json:"riskApproved"`
	ForwardBarsAvailable       int       `json:"forwardBarsAvailable"`
	InsufficientForwardBars    bool      `json:"insufficientForwardBars"`
	SimultaneousBarrierTouched bool      `json:"simultaneousBarrierTouched"`
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

// LabelCandidateAt computes a side-aware triple-barrier label plus excursion
// metrics for a candidate evaluated at entryTime using only future bars.
// If both target and stop are touched in the same bar, it resolves
// conservatively in favor of the adverse barrier.
func LabelCandidateAt(
	bars []Bar,
	entryTime time.Time,
	entryPrice float64,
	direction string,
	upperPct, lowerPct float64,
	maxBars int,
) CandidateOutcomeLabel {
	out := CandidateOutcomeLabel{
		LabelVersion:    1,
		Direction:       strings.ToLower(strings.TrimSpace(direction)),
		EntryTime:       entryTime,
		EntryPrice:      entryPrice,
		UpperBarrierPct: upperPct,
		LowerBarrierPct: lowerPct,
		MaxBars:         maxBars,
		Barrier:         "unresolved",
		OutcomeBucket:   "unresolved",
	}
	if entryPrice <= 0 || len(bars) == 0 || maxBars <= 0 {
		return out
	}

	entryIdx := -1
	for i, bar := range bars {
		if !bar.Timestamp.Before(entryTime) {
			entryIdx = i
			break
		}
	}
	if entryIdx == -1 {
		return out
	}

	lastIdx := entryIdx + maxBars
	if lastIdx >= len(bars) {
		lastIdx = len(bars) - 1
		out.InsufficientForwardBars = true
	}
	if lastIdx <= entryIdx {
		return out
	}
	out.ForwardBarsAvailable = lastIdx - entryIdx

	isShort := out.Direction == "short"
	profitLevel := entryPrice * (1 + upperPct)
	stopLevel := entryPrice * (1 - lowerPct)
	if isShort {
		profitLevel = entryPrice * (1 - upperPct)
		stopLevel = entryPrice * (1 + lowerPct)
	}

	mfe := 0.0
	mae := 0.0
	for i := entryIdx + 1; i <= lastIdx; i++ {
		bar := bars[i]

		favorable := (bar.High - entryPrice) / entryPrice
		adverse := (bar.Low - entryPrice) / entryPrice
		if isShort {
			favorable = (entryPrice - bar.Low) / entryPrice
			adverse = (entryPrice - bar.High) / entryPrice
		}
		if favorable > mfe {
			mfe = favorable
		}
		if adverse < mae {
			mae = adverse
		}

		hitProfit := false
		hitStop := false
		if isShort {
			hitProfit = bar.Low <= profitLevel
			hitStop = bar.High >= stopLevel
		} else {
			hitProfit = bar.High >= profitLevel
			hitStop = bar.Low <= stopLevel
		}

		if hitProfit && hitStop {
			out.SimultaneousBarrierTouched = true
			out.Barrier = "stop_loss"
			out.Label = -1
			out.ReturnPct = -lowerPct
			out.ResolvedAt = bar.Timestamp
			out.BarsToResolution = i - entryIdx
			out.MinutesToResolution = bar.Timestamp.Sub(entryTime).Minutes()
			out.MaxFavorableExcursionPct = mfe
			out.MaxAdverseExcursionPct = mae
			out.OutcomeBucket = "stop_loss"
			return finalizeCandidateOutcomeLabel(out)
		}
		if hitStop {
			out.Barrier = "stop_loss"
			out.Label = -1
			out.ReturnPct = -lowerPct
			out.ResolvedAt = bar.Timestamp
			out.BarsToResolution = i - entryIdx
			out.MinutesToResolution = bar.Timestamp.Sub(entryTime).Minutes()
			out.MaxFavorableExcursionPct = mfe
			out.MaxAdverseExcursionPct = mae
			out.OutcomeBucket = "stop_loss"
			return finalizeCandidateOutcomeLabel(out)
		}
		if hitProfit {
			out.Barrier = "take_profit"
			out.Label = 1
			out.ReturnPct = upperPct
			out.ResolvedAt = bar.Timestamp
			out.BarsToResolution = i - entryIdx
			out.MinutesToResolution = bar.Timestamp.Sub(entryTime).Minutes()
			out.MaxFavorableExcursionPct = mfe
			out.MaxAdverseExcursionPct = mae
			out.OutcomeBucket = "take_profit"
			return finalizeCandidateOutcomeLabel(out)
		}
	}

	finalBar := bars[lastIdx]
	ret := (finalBar.Close - entryPrice) / entryPrice
	if isShort {
		ret = (entryPrice - finalBar.Close) / entryPrice
	}
	out.Barrier = "time_barrier"
	out.ReturnPct = ret
	out.ResolvedAt = finalBar.Timestamp
	out.BarsToResolution = lastIdx - entryIdx
	out.MinutesToResolution = finalBar.Timestamp.Sub(entryTime).Minutes()
	out.MaxFavorableExcursionPct = mfe
	out.MaxAdverseExcursionPct = mae
	switch {
	case ret > 0:
		out.Label = 1
		out.OutcomeBucket = "positive_timeout"
	case ret < 0:
		out.Label = -1
		out.OutcomeBucket = "negative_timeout"
	default:
		out.Label = 0
		out.OutcomeBucket = "flat_timeout"
	}
	return finalizeCandidateOutcomeLabel(out)
}

func finalizeCandidateOutcomeLabel(out CandidateOutcomeLabel) CandidateOutcomeLabel {
	out.Profitable = out.ReturnPct > 0
	return out
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
