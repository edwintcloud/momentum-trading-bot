package signals

import (
	"math"
	"sync"
)

// OFIConfig holds configuration for the Order Flow Imbalance signal.
type OFIConfig struct {
	Enabled           bool
	WindowBars        int
	ThresholdSigma    float64
	PersistenceMinBar int // minimum consecutive bars of imbalance (anti-spoofing)
}

// DefaultOFIConfig returns sensible defaults for OFI.
func DefaultOFIConfig() OFIConfig {
	return OFIConfig{
		Enabled:           false,
		WindowBars:        60,
		ThresholdSigma:    3.0,
		PersistenceMinBar: 3,
	}
}

// ofiState tracks per-symbol OFI state.
type ofiState struct {
	volumes    []uint64
	ofiValues  []float64
	lastClose  float64
	hasFirst   bool
	persistDir Direction
	persistCnt int
}

// OFI implements Order Flow Imbalance signal generation.
type OFI struct {
	cfg OFIConfig
	mu  sync.Mutex
	sym map[string]*ofiState
}

// NewOFI creates an OFI signal source.
func NewOFI(cfg OFIConfig) *OFI {
	return &OFI{
		cfg: cfg,
		sym: make(map[string]*ofiState),
	}
}

func (o *OFI) Name() SignalType { return SignalTypeOFI }
func (o *OFI) Enabled() bool    { return o.cfg.Enabled }

func (o *OFI) getState(symbol string) *ofiState {
	st, ok := o.sym[symbol]
	if !ok {
		st = &ofiState{}
		o.sym[symbol] = st
	}
	return st
}

// OnBar computes OFI from bar data using tick-rule classification:
// if close > prev close => buy-initiated (+volume), else sell-initiated (-volume).
func (o *OFI) OnBar(symbol string, bar Bar) *Signal {
	o.mu.Lock()
	defer o.mu.Unlock()

	st := o.getState(symbol)

	if !st.hasFirst {
		st.lastClose = bar.Close
		st.hasFirst = true
		return nil
	}

	// Tick-rule trade classification
	var direction float64
	if bar.Close > st.lastClose {
		direction = 1.0
	} else if bar.Close < st.lastClose {
		direction = -1.0
	}
	// direction == 0 if close unchanged

	ofi := float64(bar.Volume) * direction
	st.ofiValues = append(st.ofiValues, ofi)
	st.volumes = append(st.volumes, bar.Volume)
	st.lastClose = bar.Close

	// Trim to window
	if len(st.ofiValues) > o.cfg.WindowBars {
		excess := len(st.ofiValues) - o.cfg.WindowBars
		st.ofiValues = st.ofiValues[excess:]
		st.volumes = st.volumes[excess:]
	}

	// Need at least half the window for meaningful stats
	minBars := o.cfg.WindowBars / 2
	if minBars < 5 {
		minBars = 5
	}
	if len(st.ofiValues) < minBars {
		return nil
	}

	// Compute rolling OFI and normalized OFI
	var sumOFI float64
	var totalVol uint64
	for i, v := range st.ofiValues {
		sumOFI += v
		totalVol += st.volumes[i]
	}

	if totalVol == 0 {
		return nil
	}
	nofi := sumOFI / float64(totalVol)

	// Compute rolling stddev of OFI values
	mean := sumOFI / float64(len(st.ofiValues))
	var sumSqDiff float64
	for _, v := range st.ofiValues {
		diff := v - mean
		sumSqDiff += diff * diff
	}
	stdDev := math.Sqrt(sumSqDiff / float64(len(st.ofiValues)))

	if stdDev == 0 {
		return nil
	}

	// Current bar's OFI
	currentOFI := st.ofiValues[len(st.ofiValues)-1]

	// Signal fires when current OFI exceeds threshold × stddev
	zScore := (currentOFI - mean) / stdDev

	var dir Direction
	if zScore > o.cfg.ThresholdSigma {
		dir = DirectionLong
	} else if zScore < -o.cfg.ThresholdSigma {
		dir = DirectionShort
	} else {
		// No threshold breach; reset persistence
		st.persistDir = DirectionNeutral
		st.persistCnt = 0
		return nil
	}

	// Persistence filter (anti-spoofing): require consecutive bars
	if dir == st.persistDir {
		st.persistCnt++
	} else {
		st.persistDir = dir
		st.persistCnt = 1
	}

	if st.persistCnt < o.cfg.PersistenceMinBar {
		return nil
	}

	strength := math.Abs(nofi)
	if strength > 1.0 {
		strength = 1.0
	}

	return &Signal{
		Type:      SignalTypeOFI,
		Symbol:    symbol,
		Direction: dir,
		Strength:  strength,
		Timestamp: Now(),
	}
}

func (o *OFI) Reset(symbol string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.sym, symbol)
}
