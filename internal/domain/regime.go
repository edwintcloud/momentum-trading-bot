package domain

// RegimeLabel classifies the overall market environment.
const (
	RegimeBullish = "bullish"
	RegimeBearish = "bearish"
	RegimeNeutral = "neutral"
	RegimeMixed   = "mixed"
)

// ClassifyRegime returns the aggregate market regime label from individual benchmark signals.
func ClassifyRegime(bullish, bearish, total int) (string, float64) {
	if total == 0 {
		return RegimeNeutral, 0
	}
	bullPct := float64(bullish) / float64(total)
	bearPct := float64(bearish) / float64(total)
	switch {
	case bullPct >= 0.6:
		return RegimeBullish, bullPct
	case bearPct >= 0.6:
		return RegimeBearish, bearPct
	case bullPct > 0 && bearPct > 0:
		return RegimeMixed, 1 - (bullPct - bearPct)
	default:
		return RegimeNeutral, 0.5
	}
}
