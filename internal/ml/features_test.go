package ml

import (
	"math"
	"testing"
	"time"
)

func TestExtractFeatures_BasicOutput(t *testing.T) {
	// Create a timestamp during market hours (10:00 AM ET)
	loc, _ := time.LoadLocation("America/New_York")
	ts := time.Date(2024, 1, 15, 10, 0, 0, 0, loc)

	input := FeatureInput{
		Symbol:           "AAPL",
		Price:            150.0,
		Open:             148.0,
		VWAP:             149.0,
		Volume:           1000000,
		ADV:              500000,
		ATR:              2.5,
		HighOfDay:        151.0,
		LowOfDay:         147.5,
		EMAFast:          150.5,
		EMASlow:          149.0,
		RSI:              65.0,
		RSIMASlope:       0.5,
		MACDHistogram:    0.3,
		OBV:              5000000,
		OBVMean:          4500000,
		OBVStdDev:        200000,
		RealizedVol:      0.25,
		MarketRegime:     "bullish",
		RegimeConfidence: 0.8,
		Timestamp:        ts,
		RecentCloses:     make([]float64, 25),
	}
	// Fill recent closes
	for i := range input.RecentCloses {
		input.RecentCloses[i] = 148.0 + float64(i)*0.1
	}

	fv := ExtractFeatures(input)

	if fv.Symbol != "AAPL" {
		t.Errorf("expected symbol AAPL, got %s", fv.Symbol)
	}
	if len(fv.Names) == 0 {
		t.Fatal("expected non-empty feature names")
	}
	if len(fv.Names) != len(fv.Values) {
		t.Errorf("names and values length mismatch: %d vs %d", len(fv.Names), len(fv.Values))
	}

	// Check specific features
	logReturn := fv.Get("log_return_from_open")
	if logReturn <= 0 {
		t.Errorf("expected positive log return, got %f", logReturn)
	}

	volRatio := fv.Get("volume_ratio_vs_adv")
	if volRatio != 2.0 {
		t.Errorf("expected volume ratio 2.0, got %f", volRatio)
	}

	rsi := fv.Get("rsi")
	if rsi < 0 || rsi > 1 {
		t.Errorf("RSI should be normalized to [0,1], got %f", rsi)
	}

	regimeProb := fv.Get("regime_bullish_prob")
	if regimeProb != 0.8 {
		t.Errorf("expected regime prob 0.8, got %f", regimeProb)
	}
}

func TestExtractFeatures_ZeroInputs(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	ts := time.Date(2024, 1, 15, 10, 0, 0, 0, loc)

	input := FeatureInput{
		Symbol:    "TEST",
		Price:     0,
		Timestamp: ts,
	}

	fv := ExtractFeatures(input)
	if len(fv.Names) == 0 {
		t.Fatal("should still produce features for zero inputs")
	}

	// All should be 0 or default
	for _, v := range fv.Values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Error("feature value should not be NaN or Inf")
		}
	}
}

func TestExtractFeatures_CyclicTimeEncoding(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")

	// Test at market open (9:30) — normalized time = 0, sin(0) = 0, cos(0) = 1
	openTS := time.Date(2024, 1, 15, 9, 30, 0, 0, loc)
	openFV := ExtractFeatures(FeatureInput{Price: 100, Timestamp: openTS})

	// Test at 11:00 — normalized time ≈ 90/390 ≈ 0.231, sin(2π*0.231) ≈ 0.99
	midTS := time.Date(2024, 1, 15, 11, 0, 0, 0, loc)
	midFV := ExtractFeatures(FeatureInput{Price: 100, Timestamp: midTS})

	openSin := openFV.Get("time_sin")
	midSin := midFV.Get("time_sin")

	// At open, sin(0) = 0; at 11:00 sin should be ~0.99
	if math.Abs(midSin) < 0.5 {
		t.Errorf("time_sin at 11:00 should be large, got %.4f", midSin)
	}
	if math.Abs(openSin) > 0.01 {
		t.Errorf("time_sin at open should be ~0, got %.4f", openSin)
	}

	// Cos should differ: cos(0) = 1, cos(~1.45) ≈ 0.13
	openCos := openFV.Get("time_cos")
	midCos := midFV.Get("time_cos")
	if math.Abs(openCos-midCos) < 0.1 {
		t.Errorf("time_cos should differ: open=%.4f, 11am=%.4f", openCos, midCos)
	}

	// dist_to_open at open should be 0
	distOpen := openFV.Get("dist_to_open")
	if distOpen != 0 {
		t.Errorf("dist_to_open at market open should be 0, got %f", distOpen)
	}
}

func TestFeatureVectorGet_Missing(t *testing.T) {
	fv := FeatureVector{
		Names:  []string{"a", "b"},
		Values: []float64{1.0, 2.0},
	}
	if fv.Get("c") != 0 {
		t.Error("missing feature should return 0")
	}
	if fv.Get("a") != 1.0 {
		t.Error("existing feature should return correct value")
	}
}

func TestRateOfChange(t *testing.T) {
	closes := []float64{100, 101, 102, 103, 104, 105}
	roc := rateOfChange(closes, 5)
	expected := (105 - 100) / 100.0
	if math.Abs(roc-expected) > 1e-10 {
		t.Errorf("expected roc %.4f, got %.4f", expected, roc)
	}
}

func TestRateOfChange_TooShort(t *testing.T) {
	closes := []float64{100, 101}
	roc := rateOfChange(closes, 5)
	if roc != 0 {
		t.Errorf("expected 0 for too-short series, got %f", roc)
	}
}

func TestExtractScorerFeatures_Basic(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	ts := time.Date(2024, 1, 15, 10, 0, 0, 0, loc)

	input := FeatureInput{
		Symbol:           "TEST",
		Price:            50.0,
		VWAP:             49.0,
		Volume:           100000,
		ADV:              50000,
		ATR:              1.5,
		EMAFast:          50.5,
		EMASlow:          49.5,
		RSI:              60.0,
		MarketRegime:     "bullish",
		RegimeConfidence: 0.75,
		Timestamp:        ts,
	}

	sf := ExtractScorerFeatures(input)
	if sf.RelativeVolume != 2.0 {
		t.Errorf("expected relative volume 2.0, got %f", sf.RelativeVolume)
	}
	if sf.ATR != 1.5 {
		t.Errorf("expected ATR 1.5, got %f", sf.ATR)
	}
	if sf.RegimeProb != 0.75 {
		t.Errorf("expected regime prob 0.75, got %f", sf.RegimeProb)
	}
}
