package risk

import (
	"math"
	"sync"
)

// VolatilityEstimator estimates realized volatility using recent returns.
type VolatilityEstimator struct {
	mu               sync.RWMutex
	estimates        map[string]*volEstimate
	defaultVol       float64
	maxVol           float64
}

type volEstimate struct {
	realizedVol float64   // annualized realized volatility
	lastPrice   float64
	returns     []float64 // recent returns for computation
}

// NewVolatilityEstimator creates a new estimator with the given default volatility.
func NewVolatilityEstimator(defaultVol float64, opts ...float64) *VolatilityEstimator {
	if defaultVol <= 0 {
		defaultVol = 0.30
	}
	maxVol := 0.0 // 0 = no cap
	if len(opts) > 0 && opts[0] > 0 {
		maxVol = opts[0]
	}
	return &VolatilityEstimator{
		estimates:  make(map[string]*volEstimate),
		defaultVol: defaultVol,
		maxVol:     maxVol,
	}
}

// SetMaxVol sets the maximum annualized vol estimate (0 = no cap).
func (ve *VolatilityEstimator) SetMaxVol(maxVol float64) {
	ve.mu.Lock()
	defer ve.mu.Unlock()
	ve.maxVol = maxVol
}

// UpdatePrice updates the volatility estimate with a new price observation.
func (ve *VolatilityEstimator) UpdatePrice(symbol string, price float64) {
	ve.mu.Lock()
	defer ve.mu.Unlock()

	est, ok := ve.estimates[symbol]
	if !ok {
		est = &volEstimate{lastPrice: price}
		ve.estimates[symbol] = est
		return
	}

	if est.lastPrice > 0 {
		ret := (price - est.lastPrice) / est.lastPrice
		est.returns = append(est.returns, ret)
		// Keep last 20 observations
		if len(est.returns) > 20 {
			est.returns = est.returns[1:]
		}

		if len(est.returns) >= 5 {
			// Compute realized vol (annualized from minute bars)
			// sqrt(390 * 252) ≈ 313.5
			var sum, sumSq float64
			n := float64(len(est.returns))
			for _, r := range est.returns {
				sum += r
				sumSq += r * r
			}
			mean := sum / n
			variance := sumSq/n - mean*mean
			if variance > 0 {
				est.realizedVol = math.Sqrt(variance) * math.Sqrt(390*252)
			}
			// Clamp to max vol if configured
			if ve.maxVol > 0 && est.realizedVol > ve.maxVol {
				est.realizedVol = ve.maxVol
			}
		}
	}
	est.lastPrice = price
}

// GetVolatility returns the estimated annualized realized volatility for a symbol.
func (ve *VolatilityEstimator) GetVolatility(symbol string) float64 {
	ve.mu.RLock()
	defer ve.mu.RUnlock()

	est, ok := ve.estimates[symbol]
	if !ok || est.realizedVol == 0 {
		return ve.defaultVol
	}
	return est.realizedVol
}
