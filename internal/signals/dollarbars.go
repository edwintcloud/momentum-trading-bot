package signals

import (
	"sync"
)

// DollarBarConfig holds configuration for dollar bar construction.
type DollarBarConfig struct {
	Enabled   bool
	Threshold float64 // dollar volume threshold per bar (default $500k)
}

// DefaultDollarBarConfig returns sensible defaults.
func DefaultDollarBarConfig() DollarBarConfig {
	return DollarBarConfig{
		Enabled:   false,
		Threshold: 500_000,
	}
}

// VolumeBarConfig holds configuration for volume bar construction.
type VolumeBarConfig struct {
	Enabled   bool
	Threshold int64 // volume threshold per bar
}

// DefaultVolumeBarConfig returns sensible defaults.
func DefaultVolumeBarConfig() VolumeBarConfig {
	return VolumeBarConfig{
		Enabled:   false,
		Threshold: 50_000,
	}
}

// barAccumulator accumulates trades into a bar.
type barAccumulator struct {
	open   float64
	high   float64
	low    float64
	close  float64
	volume int64
	dollar float64
	hasBar bool
}

func (a *barAccumulator) reset() {
	*a = barAccumulator{}
}

func (a *barAccumulator) addBar(bar Bar) {
	dollarVol := bar.Close * float64(bar.Volume)
	if !a.hasBar {
		a.open = bar.Open
		a.high = bar.High
		a.low = bar.Low
		a.hasBar = true
	} else {
		if bar.High > a.high {
			a.high = bar.High
		}
		if bar.Low < a.low {
			a.low = bar.Low
		}
	}
	a.close = bar.Close
	a.volume += bar.Volume
	a.dollar += dollarVol
}

func (a *barAccumulator) toBar(ts Bar) Bar {
	return Bar{
		Open:      a.open,
		High:      a.high,
		Low:       a.low,
		Close:     a.close,
		Volume:    a.volume,
		Timestamp: ts.Timestamp,
	}
}

// dollarBarState tracks per-symbol dollar bar accumulation.
type dollarBarState struct {
	acc barAccumulator
}

// DollarBarBuilder constructs dollar bars from time-based bars.
// A dollar bar closes when accumulated price × volume exceeds the threshold.
type DollarBarBuilder struct {
	cfg DollarBarConfig
	mu  sync.Mutex
	sym map[string]*dollarBarState
}

// NewDollarBarBuilder creates a DollarBarBuilder.
func NewDollarBarBuilder(cfg DollarBarConfig) *DollarBarBuilder {
	return &DollarBarBuilder{
		cfg: cfg,
		sym: make(map[string]*dollarBarState),
	}
}

func (d *DollarBarBuilder) Name() SignalType { return SignalTypeDollarBar }
func (d *DollarBarBuilder) Enabled() bool    { return d.cfg.Enabled }

func (d *DollarBarBuilder) getState(symbol string) *dollarBarState {
	st, ok := d.sym[symbol]
	if !ok {
		st = &dollarBarState{}
		d.sym[symbol] = st
	}
	return st
}

// OnBar accumulates a time-based bar. Returns a Signal when a dollar bar completes.
// The signal strength encodes the normalized dollar volume.
func (d *DollarBarBuilder) OnBar(symbol string, bar Bar) *Signal {
	d.mu.Lock()
	defer d.mu.Unlock()

	st := d.getState(symbol)
	st.acc.addBar(bar)

	if st.acc.dollar < d.cfg.Threshold {
		return nil
	}

	// Dollar bar complete
	completedBar := st.acc.toBar(bar)
	st.acc.reset()

	// Direction from the completed bar
	var dir Direction
	if completedBar.Close > completedBar.Open {
		dir = DirectionLong
	} else if completedBar.Close < completedBar.Open {
		dir = DirectionShort
	}

	return &Signal{
		Type:      SignalTypeDollarBar,
		Symbol:    symbol,
		Direction: dir,
		Strength:  0.5, // neutral strength; dollar bars are primarily for downstream features
		Timestamp: Now(),
	}
}

// AddBar is a lower-level interface that returns the completed Bar (if any)
// for downstream feature computation. Returns nil if no bar completed.
func (d *DollarBarBuilder) AddBar(symbol string, bar Bar) *Bar {
	d.mu.Lock()
	defer d.mu.Unlock()

	st := d.getState(symbol)
	st.acc.addBar(bar)

	if st.acc.dollar < d.cfg.Threshold {
		return nil
	}

	completedBar := st.acc.toBar(bar)
	st.acc.reset()
	return &completedBar
}

func (d *DollarBarBuilder) Reset(symbol string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sym, symbol)
}

// volumeBarState tracks per-symbol volume bar accumulation.
type volumeBarState struct {
	acc barAccumulator
}

// VolumeBarBuilder constructs volume bars from time-based bars.
// A volume bar closes when accumulated volume exceeds the threshold.
type VolumeBarBuilder struct {
	cfg VolumeBarConfig
	mu  sync.Mutex
	sym map[string]*volumeBarState
}

// NewVolumeBarBuilder creates a VolumeBarBuilder.
func NewVolumeBarBuilder(cfg VolumeBarConfig) *VolumeBarBuilder {
	return &VolumeBarBuilder{
		cfg: cfg,
		sym: make(map[string]*volumeBarState),
	}
}

func (v *VolumeBarBuilder) Name() SignalType { return SignalTypeVolumeBar }
func (v *VolumeBarBuilder) Enabled() bool    { return v.cfg.Enabled }

func (v *VolumeBarBuilder) getState(symbol string) *volumeBarState {
	st, ok := v.sym[symbol]
	if !ok {
		st = &volumeBarState{}
		v.sym[symbol] = st
	}
	return st
}

// OnBar accumulates a time-based bar. Returns a Signal when a volume bar completes.
func (v *VolumeBarBuilder) OnBar(symbol string, bar Bar) *Signal {
	v.mu.Lock()
	defer v.mu.Unlock()

	st := v.getState(symbol)
	st.acc.addBar(bar)

	if st.acc.volume < v.cfg.Threshold {
		return nil
	}

	completedBar := st.acc.toBar(bar)
	st.acc.reset()

	var dir Direction
	if completedBar.Close > completedBar.Open {
		dir = DirectionLong
	} else if completedBar.Close < completedBar.Open {
		dir = DirectionShort
	}

	return &Signal{
		Type:      SignalTypeVolumeBar,
		Symbol:    symbol,
		Direction: dir,
		Strength:  0.5,
		Timestamp: Now(),
	}
}

// AddBar returns the completed Bar (if any) for downstream feature computation.
func (v *VolumeBarBuilder) AddBar(symbol string, bar Bar) *Bar {
	v.mu.Lock()
	defer v.mu.Unlock()

	st := v.getState(symbol)
	st.acc.addBar(bar)

	if st.acc.volume < v.cfg.Threshold {
		return nil
	}

	completedBar := st.acc.toBar(bar)
	st.acc.reset()
	return &completedBar
}

func (v *VolumeBarBuilder) Reset(symbol string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.sym, symbol)
}
