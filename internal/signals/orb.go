package signals

import (
	"math"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// ORBConfig holds configuration for the Opening Range Breakout signal.
type ORBConfig struct {
	Enabled          bool
	WindowMinutes    int     // first N minutes to define the range
	BufferPct        float64 // buffer above/below range for breakout confirmation
	VolumeMultiplier float64 // breakout volume must exceed this × avg volume
	MaxGapPct        float64 // skip if overnight gap exceeds this %
	TargetMultiplier float64 // target = entry ± this × range width
}

// DefaultORBConfig returns sensible defaults.
func DefaultORBConfig() ORBConfig {
	return ORBConfig{
		Enabled:          false,
		WindowMinutes:    15,
		BufferPct:        0.001, // 0.1%
		VolumeMultiplier: 1.5,
		MaxGapPct:        0.02, // 2%
		TargetMultiplier: 1.5,
	}
}

// orbState tracks per-symbol ORB state within a session.
type orbState struct {
	sessionDate   time.Time // date of current session (ET)
	rangeHigh     float64
	rangeLow      float64
	rangeSet      bool // true once opening range window has elapsed
	barsInRange   int
	totalVolume   int64
	barCount      int
	prevClose     float64 // previous session close for gap calculation
	hasPrevClose  bool
	fired         bool // only fire once per session
	marketOpenET  time.Time
}

// ORB implements Opening Range Breakout signal generation.
type ORB struct {
	cfg ORBConfig
	mu  sync.Mutex
	sym map[string]*orbState
}

// NewORB creates an ORB signal source.
func NewORB(cfg ORBConfig) *ORB {
	return &ORB{
		cfg: cfg,
		sym: make(map[string]*orbState),
	}
}

func (o *ORB) Name() SignalType { return SignalTypeORB }
func (o *ORB) Enabled() bool    { return o.cfg.Enabled }

func (o *ORB) getState(symbol string) *orbState {
	st, ok := o.sym[symbol]
	if !ok {
		st = &orbState{}
		o.sym[symbol] = st
	}
	return st
}

// SetPrevClose sets the previous session's close for gap calculation.
func (o *ORB) SetPrevClose(symbol string, prevClose float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.getState(symbol)
	st.prevClose = prevClose
	st.hasPrevClose = true
}

// OnBar processes each bar, building the opening range and detecting breakouts.
func (o *ORB) OnBar(symbol string, bar Bar) *Signal {
	o.mu.Lock()
	defer o.mu.Unlock()

	st := o.getState(symbol)
	barET := bar.Timestamp.In(markethours.Location())

	// Detect new session (date change in ET)
	sessionDate := time.Date(barET.Year(), barET.Month(), barET.Day(), 0, 0, 0, 0, markethours.Location())
	if !sessionDate.Equal(st.sessionDate) {
		// Save today's open price as prev close for next session
		prevClose := st.prevClose
		hasPrevClose := st.hasPrevClose
		if st.barCount > 0 && st.rangeHigh > 0 {
			prevClose = bar.Close // approximate
			hasPrevClose = true
		}
		*st = orbState{
			sessionDate:  sessionDate,
			prevClose:    prevClose,
			hasPrevClose: hasPrevClose,
			marketOpenET: time.Date(barET.Year(), barET.Month(), barET.Day(), 9, 30, 0, 0, markethours.Location()),
		}
	}

	if st.fired {
		return nil
	}

	st.barCount++
	st.totalVolume += bar.Volume

	// During opening range window: build the range
	minutesSinceOpen := barET.Sub(st.marketOpenET).Minutes()
	if minutesSinceOpen < 0 {
		// Pre-market bar, skip
		return nil
	}

	if minutesSinceOpen <= float64(o.cfg.WindowMinutes) {
		// Still in the opening range window
		if st.barsInRange == 0 {
			st.rangeHigh = bar.High
			st.rangeLow = bar.Low
		} else {
			if bar.High > st.rangeHigh {
				st.rangeHigh = bar.High
			}
			if bar.Low < st.rangeLow {
				st.rangeLow = bar.Low
			}
		}
		st.barsInRange++
		return nil
	}

	// Range window elapsed
	if !st.rangeSet {
		st.rangeSet = true
	}

	// Need valid range
	if st.rangeHigh <= st.rangeLow || st.barsInRange == 0 {
		return nil
	}

	// Gap filter: skip if overnight gap > threshold
	if st.hasPrevClose && st.prevClose > 0 {
		gapPct := math.Abs(bar.Open-st.prevClose) / st.prevClose
		if gapPct > o.cfg.MaxGapPct {
			st.fired = true // mark as fired to skip for rest of session
			return nil
		}
	}

	// Volume confirmation: current bar volume > multiplier × average
	avgVol := float64(st.totalVolume) / float64(st.barCount)
	if avgVol > 0 && float64(bar.Volume) < o.cfg.VolumeMultiplier*avgVol {
		return nil
	}

	rangeWidth := st.rangeHigh - st.rangeLow
	buffer := st.rangeHigh * o.cfg.BufferPct

	var dir Direction
	var strength float64

	// Long breakout: close > range_high + buffer
	if bar.Close > st.rangeHigh+buffer {
		dir = DirectionLong
		strength = math.Min((bar.Close-st.rangeHigh)/rangeWidth, 1.0)
	}

	// Short breakout: close < range_low - buffer
	if bar.Close < st.rangeLow-buffer {
		dir = DirectionShort
		strength = math.Min((st.rangeLow-bar.Close)/rangeWidth, 1.0)
	}

	if dir == DirectionNeutral {
		return nil
	}

	st.fired = true

	return &Signal{
		Type:      SignalTypeORB,
		Symbol:    symbol,
		Direction: dir,
		Strength:  strength,
		Timestamp: Now(),
	}
}

// OpeningRange returns the current opening range for a symbol.
// Returns (high, low, set).
func (o *ORB) OpeningRange(symbol string) (float64, float64, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st, ok := o.sym[symbol]
	if !ok {
		return 0, 0, false
	}
	return st.rangeHigh, st.rangeLow, st.rangeSet
}

func (o *ORB) Reset(symbol string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.sym, symbol)
}
