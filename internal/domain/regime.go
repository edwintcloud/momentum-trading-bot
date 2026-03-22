package domain

import (
	"strings"
	"time"
)

const (
	MarketRegimeBullish = "bullish"
	MarketRegimeBearish = "bearish"
	MarketRegimeRanging = "ranging"
)

type BenchmarkRegimeReading struct {
	Symbol            string  `json:"symbol"`
	Price             float64 `json:"price"`
	VWAP              float64 `json:"vwap"`
	PriceVsVWAPPct    float64 `json:"priceVsVwapPct"`
	EMAFast           float64 `json:"emaFast"`
	EMASlow           float64 `json:"emaSlow"`
	ReturnLookbackPct float64 `json:"returnLookbackPct"`
}

type MarketRegimeSnapshot struct {
	Regime     string                   `json:"regime"`
	Confidence float64                  `json:"confidence"`
	Timestamp  time.Time                `json:"timestamp,omitempty"`
	Benchmarks []BenchmarkRegimeReading `json:"benchmarks,omitempty"`
}

func NormalizeMarketRegime(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case MarketRegimeBullish:
		return MarketRegimeBullish
	case MarketRegimeBearish:
		return MarketRegimeBearish
	default:
		return MarketRegimeRanging
	}
}

func TrendAlignedPlaybook(positionSide, regime, setupType string) string {
	regime = NormalizeMarketRegime(regime)
	if IsLong(positionSide) {
		switch regime {
		case MarketRegimeBullish:
			return "bullish-trend-long"
		case MarketRegimeRanging:
			return "ranging-reclaim-long"
		default:
			return "blocked-long"
		}
	}
	switch regime {
	case MarketRegimeBearish:
		return "bearish-trend-short"
	case MarketRegimeBullish:
		return "bullish-countertrend-short"
	default:
		return "ranging-countertrend-short"
	}
}
