package volumeprofile

import (
	"math"
	"sort"
	"time"
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

// cumulativeProfile maps minutes-since-4AM-ET to expected fraction of daily volume.
// Based on a typical U-shaped intraday volume curve for US equities.
var cumulativeProfile = []struct {
	minutesSince4AM float64
	fraction        float64
}{
	{0, 0.0},     // 4:00 AM
	{330, 0.02},  // 9:30 AM (premarket contributes ~2%)
	{360, 0.10},  // 10:00 AM
	{390, 0.20},  // 10:30 AM
	{420, 0.28},  // 11:00 AM
	{480, 0.40},  // 12:00 PM
	{540, 0.50},  // 1:00 PM
	{600, 0.58},  // 2:00 PM
	{660, 0.68},  // 3:00 PM
	{720, 0.85},  // 4:00 PM
	{960, 1.0},   // 8:00 PM (end of after-hours)
}

// ExpectedCumulativeShare returns the expected fraction of a symbol's prior
// regular-session volume that has traded by the provided New York timestamp.
func ExpectedCumulativeShare(at time.Time) float64 {
	hour := at.Hour()
	minute := at.Minute()
	minutesSince4AM := float64((hour-4)*60 + minute)
	if minutesSince4AM < 0 {
		return 0
	}

	for i := 1; i < len(cumulativeProfile); i++ {
		if minutesSince4AM <= cumulativeProfile[i].minutesSince4AM {
			prev := cumulativeProfile[i-1]
			curr := cumulativeProfile[i]
			t := (minutesSince4AM - prev.minutesSince4AM) / (curr.minutesSince4AM - prev.minutesSince4AM)
			return prev.fraction + t*(curr.fraction-prev.fraction)
		}
	}
	return 1.0
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
