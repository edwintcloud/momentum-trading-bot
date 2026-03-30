package signals

import (
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// Direction indicates signal direction.
type Direction int

const (
	DirectionNeutral Direction = 0
	DirectionLong    Direction = 1
	DirectionShort   Direction = -1
)

func (d Direction) String() string {
	switch d {
	case DirectionLong:
		return "long"
	case DirectionShort:
		return "short"
	default:
		return "neutral"
	}
}

// SignalType identifies the alpha signal source.
type SignalType string

const (
	SignalTypeOFI       SignalType = "ofi"
	SignalTypeVPIN      SignalType = "vpin"
	SignalTypeOBV       SignalType = "obv"
	SignalTypeORB       SignalType = "orb"
	SignalTypeDollarBar SignalType = "dollar_bar"
	SignalTypeVolumeBar SignalType = "volume_bar"
)

// Signal represents an alpha signal emitted by any signal source.
type Signal struct {
	Type      SignalType `json:"type"`
	Symbol    string     `json:"symbol"`
	Direction Direction  `json:"direction"`
	Strength  float64    `json:"strength"`
	Timestamp time.Time  `json:"timestamp"`
}

// Bar is a standard OHLCV bar used across signal computations.
type Bar struct {
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    uint64
	Timestamp time.Time
}

// SignalSource is implemented by each alpha signal generator.
type SignalSource interface {
	// Name returns the signal type identifier.
	Name() SignalType
	// Enabled reports whether this signal source is active.
	Enabled() bool
	// OnBar processes a new bar and returns any generated signal.
	OnBar(symbol string, bar Bar) *Signal
	// Reset clears internal state for the given symbol.
	Reset(symbol string)
}

// Aggregator combines signals from multiple sources for a symbol.
type Aggregator struct {
	mu      sync.RWMutex
	sources []SignalSource
}

// NewAggregator creates a signal aggregator with the given sources.
func NewAggregator(sources ...SignalSource) *Aggregator {
	enabled := make([]SignalSource, 0, len(sources))
	for _, s := range sources {
		if s.Enabled() {
			enabled = append(enabled, s)
		}
	}
	return &Aggregator{sources: enabled}
}

// OnBar feeds a bar to all signal sources and returns any signals produced.
func (a *Aggregator) OnBar(symbol string, bar Bar) []Signal {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var out []Signal
	for _, src := range a.sources {
		sig := src.OnBar(symbol, bar)
		if sig != nil {
			out = append(out, *sig)
		}
	}
	return out
}

// Now returns the current time in ET.
func Now() time.Time {
	return time.Now().In(markethours.Location())
}
