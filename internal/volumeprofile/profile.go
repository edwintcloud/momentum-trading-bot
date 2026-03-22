package volumeprofile

import (
	"math"
	"sort"
)

// Level represents a price level in the volume profile.
type Level struct {
	Price  float64 `json:"price"`
	Volume int64   `json:"volume"`
}

// Profile accumulates volume at price levels.
type Profile struct {
	levels   map[int64]int64
	tickSize float64
}

// NewProfile creates a volume profile with a given tick size.
func NewProfile(tickSize float64) *Profile {
	if tickSize <= 0 {
		tickSize = 0.01
	}
	return &Profile{
		levels:   make(map[int64]int64),
		tickSize: tickSize,
	}
}

// AddBar adds a bar's volume distributed across its price range.
func (p *Profile) AddBar(high, low float64, volume int64) {
	if volume <= 0 || high <= 0 || low <= 0 || high < low {
		return
	}
	steps := int(math.Ceil((high - low) / p.tickSize))
	if steps < 1 {
		steps = 1
	}
	volumePerStep := volume / int64(steps)
	if volumePerStep < 1 {
		volumePerStep = 1
	}
	for i := 0; i <= steps; i++ {
		price := low + float64(i)*p.tickSize
		key := int64(math.Round(price / p.tickSize))
		p.levels[key] += volumePerStep
	}
}

// Levels returns sorted volume profile levels.
func (p *Profile) Levels() []Level {
	out := make([]Level, 0, len(p.levels))
	for key, vol := range p.levels {
		out = append(out, Level{
			Price:  float64(key) * p.tickSize,
			Volume: vol,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Price < out[j].Price
	})
	return out
}

// POC returns the Point of Control (price level with highest volume).
func (p *Profile) POC() Level {
	var best Level
	for key, vol := range p.levels {
		if vol > best.Volume {
			best = Level{
				Price:  float64(key) * p.tickSize,
				Volume: vol,
			}
		}
	}
	return best
}

// ValueArea returns the price range containing the specified percentage of total volume.
func (p *Profile) ValueArea(pct float64) (low, high float64) {
	levels := p.Levels()
	if len(levels) == 0 {
		return 0, 0
	}

	var totalVol int64
	for _, l := range levels {
		totalVol += l.Volume
	}
	target := int64(float64(totalVol) * pct)

	// Sort by volume descending to find the densest region
	sorted := make([]Level, len(levels))
	copy(sorted, levels)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Volume > sorted[j].Volume
	})

	var accumulated int64
	low = math.MaxFloat64
	high = 0
	for _, l := range sorted {
		accumulated += l.Volume
		if l.Price < low {
			low = l.Price
		}
		if l.Price > high {
			high = l.Price
		}
		if accumulated >= target {
			break
		}
	}
	return
}
