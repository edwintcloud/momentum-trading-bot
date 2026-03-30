package signals

import (
	"math"
	"sync"
)

// VPINConfig holds configuration for the VPIN signal.
type VPINConfig struct {
	Enabled         bool
	BucketDivisor   int     // ADV / BucketDivisor = bucket size
	LookbackBuckets int     // number of buckets for rolling average
	HighThreshold   float64 // above this → momentum/toxic flow
	LowThreshold    float64 // below this → mean-reversion conditions
}

// DefaultVPINConfig returns sensible defaults for VPIN.
func DefaultVPINConfig() VPINConfig {
	return VPINConfig{
		Enabled:         false,
		BucketDivisor:   50,
		LookbackBuckets: 50,
		HighThreshold:   0.7,
		LowThreshold:    0.3,
	}
}

// vpinBucket tracks buy/sell classification within a volume bucket.
type vpinBucket struct {
	buyVolume   uint64
	sellVolume  uint64
	totalVolume uint64
}

// vpinState tracks per-symbol VPIN computation state.
type vpinState struct {
	adv             float64 // average daily volume (set externally or auto-calibrated)
	bucketSize      uint64
	currentBucket   vpinBucket
	completeBuckets []vpinBucket
	lastClose       float64
	hasFirst        bool
	cumulativeVol   uint64 // accumulated volume for auto-calibration
	cumulativeBars  int    // accumulated bars for auto-calibration
}

// VPIN implements Volume-Synchronized Probability of Informed Trading.
type VPIN struct {
	cfg VPINConfig
	mu  sync.Mutex
	sym map[string]*vpinState
}

// NewVPIN creates a VPIN signal source.
func NewVPIN(cfg VPINConfig) *VPIN {
	return &VPIN{
		cfg: cfg,
		sym: make(map[string]*vpinState),
	}
}

func (v *VPIN) Name() SignalType { return SignalTypeVPIN }
func (v *VPIN) Enabled() bool    { return v.cfg.Enabled }

func (v *VPIN) getState(symbol string) *vpinState {
	st, ok := v.sym[symbol]
	if !ok {
		st = &vpinState{}
		v.sym[symbol] = st
	}
	return st
}

// SetADV sets the average daily volume for a symbol, which determines bucket size.
func (v *VPIN) SetADV(symbol string, adv float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.getState(symbol)
	st.adv = adv
	if v.cfg.BucketDivisor > 0 {
		st.bucketSize = uint64(adv / float64(v.cfg.BucketDivisor))
	}
	if st.bucketSize <= 0 {
		st.bucketSize = 1
	}
}

// OnBar processes a bar, classifies volume as buy/sell using tick rule,
// fills volume buckets, and computes VPIN.
func (v *VPIN) OnBar(symbol string, bar Bar) *Signal {
	v.mu.Lock()
	defer v.mu.Unlock()

	st := v.getState(symbol)

	// Auto-calibrate bucket size from observed volume if ADV not set externally
	st.cumulativeVol += bar.Volume
	st.cumulativeBars++
	if st.bucketSize <= 0 && st.cumulativeBars >= 30 {
		// Estimate ADV from observed average bar volume × 390 minute bars per day
		avgBarVol := float64(st.cumulativeVol) / float64(st.cumulativeBars)
		estimatedADV := avgBarVol * 390
		if v.cfg.BucketDivisor > 0 {
			st.bucketSize = uint64(estimatedADV / float64(v.cfg.BucketDivisor))
		}
		if st.bucketSize <= 0 {
			st.bucketSize = 10000
		}
	}

	// Need bucket size to proceed
	if st.bucketSize <= 0 {
		st.lastClose = bar.Close
		st.hasFirst = true
		return nil
	}

	if !st.hasFirst {
		st.lastClose = bar.Close
		st.hasFirst = true
		return nil
	}

	// Bulk trade classification using tick rule
	var buyVol, sellVol uint64
	if bar.Close > st.lastClose {
		buyVol = bar.Volume
	} else if bar.Close < st.lastClose {
		sellVol = bar.Volume
	} else {
		// Unchanged: split evenly
		buyVol = bar.Volume / 2
		sellVol = bar.Volume - buyVol
	}
	st.lastClose = bar.Close

	// Fill the current bucket
	remaining := bar.Volume
	for remaining > 0 {
		spaceInBucket := st.bucketSize - st.currentBucket.totalVolume
		if spaceInBucket <= 0 {
			// Close current bucket
			st.completeBuckets = append(st.completeBuckets, st.currentBucket)
			st.currentBucket = vpinBucket{}
			spaceInBucket = st.bucketSize
		}

		fill := remaining
		if fill > spaceInBucket {
			fill = spaceInBucket
		}

		// Distribute buy/sell proportionally
		totalBarVol := buyVol + sellVol
		if totalBarVol > 0 {
			buyFraction := float64(buyVol) / float64(totalBarVol)
			buyFill := uint64(float64(fill) * buyFraction)
			sellFill := fill - buyFill
			st.currentBucket.buyVolume += buyFill
			st.currentBucket.sellVolume += sellFill
		}
		st.currentBucket.totalVolume += fill
		remaining -= fill
	}

	// Trim to lookback window
	if len(st.completeBuckets) > v.cfg.LookbackBuckets {
		excess := len(st.completeBuckets) - v.cfg.LookbackBuckets
		st.completeBuckets = st.completeBuckets[excess:]
	}

	// Need enough buckets for meaningful VPIN
	minBuckets := v.cfg.LookbackBuckets / 2
	if minBuckets < 5 {
		minBuckets = 5
	}
	if len(st.completeBuckets) < minBuckets {
		return nil
	}

	// Compute VPIN = rolling average of |V_buy - V_sell| / V_total
	var sumImbalance float64
	for _, b := range st.completeBuckets {
		imbalance := math.Abs(float64(b.buyVolume) - float64(b.sellVolume))
		total := float64(b.totalVolume)
		if total > 0 {
			sumImbalance += imbalance / total
		}
	}
	vpinValue := sumImbalance / float64(len(st.completeBuckets))

	// Generate signal based on VPIN thresholds
	var dir Direction
	var strength float64

	if vpinValue > v.cfg.HighThreshold {
		// High VPIN → toxic/informed flow → momentum continuation
		// Direction aligns with recent price movement
		if bar.Close > bar.Open {
			dir = DirectionLong
		} else {
			dir = DirectionShort
		}
		strength = math.Min((vpinValue-v.cfg.HighThreshold)/(1.0-v.cfg.HighThreshold), 1.0)
	} else if vpinValue < v.cfg.LowThreshold {
		// Low VPIN → balanced flow → mean-reversion
		// Direction opposes recent price movement
		if bar.Close > bar.Open {
			dir = DirectionShort
		} else {
			dir = DirectionLong
		}
		strength = math.Min((v.cfg.LowThreshold-vpinValue)/v.cfg.LowThreshold, 1.0)
	} else {
		return nil
	}

	return &Signal{
		Type:      SignalTypeVPIN,
		Symbol:    symbol,
		Direction: dir,
		Strength:  strength,
		Timestamp: Now(),
	}
}

func (v *VPIN) Reset(symbol string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.sym, symbol)
}
