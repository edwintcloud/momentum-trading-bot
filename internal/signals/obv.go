package signals

import (
	"math"
	"sync"
)

// OBVConfig holds configuration for the OBV Divergence signal.
type OBVConfig struct {
	Enabled      bool
	LookbackBars int // window for divergence detection
}

// DefaultOBVConfig returns sensible defaults for OBV Divergence.
func DefaultOBVConfig() OBVConfig {
	return OBVConfig{
		Enabled:      false,
		LookbackBars: 20,
	}
}

// obvState tracks per-symbol OBV divergence state.
type obvState struct {
	obv       float64
	lastClose float64
	hasFirst  bool
	prices    []float64 // close prices in lookback window
	obvValues []float64 // OBV values in lookback window
}

// OBVDivergence implements On-Balance Volume divergence signal generation.
type OBVDivergence struct {
	cfg OBVConfig
	mu  sync.Mutex
	sym map[string]*obvState
}

// NewOBVDivergence creates an OBV Divergence signal source.
func NewOBVDivergence(cfg OBVConfig) *OBVDivergence {
	return &OBVDivergence{
		cfg: cfg,
		sym: make(map[string]*obvState),
	}
}

func (o *OBVDivergence) Name() SignalType { return SignalTypeOBV }
func (o *OBVDivergence) Enabled() bool    { return o.cfg.Enabled }

func (o *OBVDivergence) getState(symbol string) *obvState {
	st, ok := o.sym[symbol]
	if !ok {
		st = &obvState{}
		o.sym[symbol] = st
	}
	return st
}

// OnBar computes OBV and detects divergences against price.
// OBV_t = OBV_{t-1} + Volume_t × sign(Close_t - Close_{t-1})
// Anchored from session open (reset clears state daily).
func (o *OBVDivergence) OnBar(symbol string, bar Bar) *Signal {
	o.mu.Lock()
	defer o.mu.Unlock()

	st := o.getState(symbol)

	if !st.hasFirst {
		st.lastClose = bar.Close
		st.hasFirst = true
		st.prices = append(st.prices, bar.Close)
		st.obvValues = append(st.obvValues, 0)
		return nil
	}

	// Accumulate OBV
	if bar.Close > st.lastClose {
		st.obv += float64(bar.Volume)
	} else if bar.Close < st.lastClose {
		st.obv -= float64(bar.Volume)
	}
	st.lastClose = bar.Close

	st.prices = append(st.prices, bar.Close)
	st.obvValues = append(st.obvValues, st.obv)

	// Trim to lookback window
	if len(st.prices) > o.cfg.LookbackBars {
		excess := len(st.prices) - o.cfg.LookbackBars
		st.prices = st.prices[excess:]
		st.obvValues = st.obvValues[excess:]
	}

	// Need enough data for divergence detection
	if len(st.prices) < 5 {
		return nil
	}

	dir := detectDivergence(st.prices, st.obvValues)
	if dir == DirectionNeutral {
		return nil
	}

	// Strength based on the magnitude of the divergence
	strength := divergenceStrength(st.prices, st.obvValues)

	return &Signal{
		Type:      SignalTypeOBV,
		Symbol:    symbol,
		Direction: dir,
		Strength:  strength,
		Timestamp: Now(),
	}
}

// detectDivergence checks for bullish/bearish divergence between price and OBV.
// Bullish divergence: price making lower lows, OBV making higher lows.
// Bearish divergence: price making higher highs, OBV making lower highs.
func detectDivergence(prices, obvValues []float64) Direction {
	n := len(prices)
	if n < 5 {
		return DirectionNeutral
	}

	// Find local lows and highs in the lookback window
	// Split window into two halves to compare extremes
	mid := n / 2
	firstHalf := prices[:mid]
	secondHalf := prices[mid:]
	firstHalfOBV := obvValues[:mid]
	secondHalfOBV := obvValues[mid:]

	firstLow, firstLowOBV := findMin(firstHalf, firstHalfOBV)
	secondLow, secondLowOBV := findMin(secondHalf, secondHalfOBV)

	// Bullish divergence: price lower lows + OBV higher lows
	if secondLow < firstLow && secondLowOBV > firstLowOBV {
		return DirectionLong
	}

	firstHigh, firstHighOBV := findMax(firstHalf, firstHalfOBV)
	secondHigh, secondHighOBV := findMax(secondHalf, secondHalfOBV)

	// Bearish divergence: price higher highs + OBV lower highs
	if secondHigh > firstHigh && secondHighOBV < firstHighOBV {
		return DirectionShort
	}

	return DirectionNeutral
}

// divergenceStrength computes a 0-1 strength based on the magnitude of divergence
// between price trend and OBV trend.
func divergenceStrength(prices, obvValues []float64) float64 {
	n := len(prices)
	if n < 2 {
		return 0
	}

	// Compare slopes: simple linear regression slope for price vs OBV
	priceSlope := linearSlope(prices)
	obvSlope := linearSlope(obvValues)

	// Normalize slopes
	priceRange := rangeOf(prices)
	obvRange := rangeOf(obvValues)
	if priceRange == 0 || obvRange == 0 {
		return 0
	}

	normPriceSlope := priceSlope / priceRange
	normOBVSlope := obvSlope / obvRange

	// Divergence is when slopes have opposite signs
	if normPriceSlope*normOBVSlope >= 0 {
		return 0 // not diverging
	}

	strength := math.Abs(normPriceSlope - normOBVSlope)
	if strength > 1.0 {
		strength = 1.0
	}
	return strength
}

func linearSlope(values []float64) float64 {
	n := float64(len(values))
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i, v := range values {
		x := float64(i)
		sumX += x
		sumY += v
		sumXY += x * v
		sumX2 += x * x
	}
	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}

func rangeOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	minV, maxV := values[0], values[0]
	for _, v := range values[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	return maxV - minV
}

func findMin(prices, obvValues []float64) (float64, float64) {
	if len(prices) == 0 {
		return 0, 0
	}
	minIdx := 0
	for i, p := range prices {
		if p < prices[minIdx] {
			minIdx = i
		}
	}
	return prices[minIdx], obvValues[minIdx]
}

func findMax(prices, obvValues []float64) (float64, float64) {
	if len(prices) == 0 {
		return 0, 0
	}
	maxIdx := 0
	for i, p := range prices {
		if p > prices[maxIdx] {
			maxIdx = i
		}
	}
	return prices[maxIdx], obvValues[maxIdx]
}

func (o *OBVDivergence) Reset(symbol string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.sym, symbol)
}
