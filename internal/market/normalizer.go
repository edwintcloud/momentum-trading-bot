package market

import (
	"math"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/volumeprofile"
)

// SeedState holds historical context for seeding the normalizer on startup.
type SeedState struct {
	PreviousClose float64
	PrevDayVolume uint64
	TodayOpen     float64
	TodayHigh     float64
	TodayVolume   uint64
	PreMarketVol  uint64
}

type minuteBar struct {
	timestamp time.Time
	high      float64
	volume    uint64
}

// symbolState tracks per-symbol daily state for normalization.
type symbolState struct {
	day              string
	previousClose    float64
	open             float64
	highOfDay        float64
	totalVolume      uint64
	preMarketVol     uint64
	prevDayVolume    uint64
	lastClose        float64
	seedDay          string
	seedHighOfDay    float64
	seedTotalVolume  uint64
	seedPreMarketVol uint64
	dailyHigh        float64
	dailyVolume      uint64
	currentFiveMin   time.Time
	currentFiveVol   uint64
	minuteBars       []minuteBar
}

// Normalizer converts raw Alpaca StreamBars into enriched domain.Ticks.
type Normalizer struct {
	states map[string]*symbolState
}

// NewNormalizer creates a new tick normalizer.
func NewNormalizer() *Normalizer {
	return &Normalizer{
		states: make(map[string]*symbolState),
	}
}

// Seed initializes the normalizer with historical context for a symbol.
// This must be called before the first bar arrives for accurate gap% and relative volume.
// Without seeding, previousClose=0 causes GapPercent=0 and prevDayVolume=0 causes
// RelativeVolume=1.0, which means all scanner filters fail on a fresh start.
// The now parameter should be the current time; it determines which trading day the
// seed state belongs to.
func (n *Normalizer) Seed(symbol string, seed SeedState, now time.Time) {
	day := now.In(markethours.Location()).Format("2006-01-02")
	state := &symbolState{
		day:              day,
		previousClose:    seed.PreviousClose,
		lastClose:        seed.PreviousClose,
		open:             seed.TodayOpen,
		highOfDay:        seed.TodayHigh,
		totalVolume:      seed.TodayVolume,
		preMarketVol:     seed.PreMarketVol,
		prevDayVolume:    seed.PrevDayVolume,
		seedDay:          day,
		seedHighOfDay:    seed.TodayHigh,
		seedTotalVolume:  seed.TodayVolume,
		seedPreMarketVol: seed.PreMarketVol,
	}
	n.states[symbol] = state
}

// Normalize converts a domain.Bar into an enriched domain.Tick with
// gap%, relative volume, pre-market volume, volume spikes, and high-of-day.
// If bar.PrevClose > 0, it overrides the tracked previousClose on day boundaries
// so gap% is accurate even when the prior day's data is absent.
func (n *Normalizer) Normalize(bar domain.Bar) domain.Tick {
	state := n.states[bar.Symbol]
	if state == nil {
		state = &symbolState{}
		n.states[bar.Symbol] = state
	}

	day := bar.Timestamp.In(markethours.Location()).Format("2006-01-02")
	if state.day != day {
		if state.day != "" && state.totalVolume > 0 {
			state.prevDayVolume = state.totalVolume
		}
		prevClose := state.lastClose
		if bar.PrevClose > 0 {
			prevClose = bar.PrevClose
		}
		state.previousClose = prevClose
		state.day = day
		state.open = bar.Open
		state.highOfDay = 0
		state.totalVolume = 0
		state.preMarketVol = 0
		state.seedDay = ""
		state.seedHighOfDay = 0
		state.seedTotalVolume = 0
		state.seedPreMarketVol = 0
		state.dailyHigh = 0
		state.dailyVolume = 0
		state.currentFiveMin = time.Time{}
		state.currentFiveVol = 0
		state.minuteBars = nil
	}

	state.lastClose = bar.Close
	state.open = firstPositive(state.open, bar.Open)
	state.applyMinuteBar(minuteBar{
		timestamp: bar.Timestamp,
		high:      bar.High,
		volume:    bar.Volume,
	})

	gapPercent := 0.0
	if state.previousClose > 0 && state.open > 0 {
		gapPercent = ((state.open - state.previousClose) / state.previousClose) * 100
	}

	relativeVolume := calculateRelativeVolume(state, bar.Timestamp)
	recentVolumes := lastNMinuteVolumes(state.minuteBars, 5)
	volumeSpike := isVolumeSpike(recentVolumes, bar.Volume, relativeVolume)
	fiveMinuteVolume := state.fiveMinuteVolumeAt(bar.Timestamp)

	return domain.Tick{
		Symbol:           bar.Symbol,
		Price:            round2(bar.Close),
		BarOpen:          round2(bar.Open),
		BarHigh:          round2(bar.High),
		BarLow:           round2(bar.Low),
		BarVolume:        bar.Volume,
		Open:             round2(state.open),
		HighOfDay:        round2(state.highOfDay),
		Volume:           state.totalVolume,
		RelativeVolume:   round2(relativeVolume),
		GapPercent:       round2(gapPercent),
		PreMarketVolume:  state.preMarketVol,
		VolumeSpike:      volumeSpike,
		PrevDayVolume:    state.prevDayVolume,
		Catalyst:         bar.Catalyst,
		CatalystURL:      bar.CatalystURL,
		Timestamp:        bar.Timestamp,
		FiveMinuteVolume: fiveMinuteVolume,
	}
}

// UpdateDailyBar updates the session high-of-day and volume from a daily bar message.
// Daily bars are emitted every minute after market open and contain cumulative session data.
func (n *Normalizer) UpdateDailyBar(symbol string, high float64, volume uint64, open float64) {
	state := n.states[symbol]
	if state == nil {
		return
	}
	if open > 0 && state.open == 0 {
		state.open = open
	}
	if high > state.dailyHigh {
		state.dailyHigh = high
		if high > state.highOfDay {
			state.highOfDay = high
		}
	}
	if volume > state.dailyVolume {
		state.dailyVolume = volume
		if volume > state.totalVolume {
			state.totalVolume = volume
		}
	}
}

func isPremarket(timestamp time.Time) bool {
	est := timestamp.In(markethours.Location())
	minutes := est.Hour()*60 + est.Minute()
	return minutes >= 4*60 && minutes < 9*60+30
}

func calculateRelativeVolume(state *symbolState, timestamp time.Time) float64 {
	if state.prevDayVolume <= 0 {
		return 1.0
	}
	expected := float64(state.prevDayVolume) * volumeprofile.ExpectedCumulativeShare(timestamp)
	if expected < 1 {
		return 1.0
	}
	return float64(state.totalVolume) / expected
}

func isVolumeSpike(recent []uint64, latest uint64, relativeVolume float64) bool {
	if relativeVolume >= 5 {
		return true
	}
	if len(recent) < 3 {
		return false
	}
	var total uint64
	for _, volume := range recent[:len(recent)-1] {
		total += volume
	}
	average := float64(total) / float64(len(recent)-1)
	return average > 0 && float64(latest) >= average*1.8
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func (s *symbolState) applyMinuteBar(next minuteBar) {
	n := len(s.minuteBars)
	bucket := fiveMinuteBucketStart(next.timestamp)
	if n > 0 && s.minuteBars[n-1].timestamp.Equal(next.timestamp) {
		prev := s.minuteBars[n-1]
		s.minuteBars[n-1] = next
		deltaVolume := next.volume - prev.volume
		s.totalVolume += deltaVolume
		if isPremarket(next.timestamp) {
			s.preMarketVol += deltaVolume
		}
		if s.currentFiveMin.Equal(bucket) {
			s.currentFiveVol += deltaVolume
		} else {
			s.currentFiveMin = bucket
			s.currentFiveVol = next.volume
		}
		if next.high >= s.highOfDay {
			s.highOfDay = next.high
		} else if prev.high == s.highOfDay && next.high < prev.high {
			s.recomputeHighOfDay()
		}
	} else {
		s.minuteBars = append(s.minuteBars, next)
		s.totalVolume += next.volume
		if isPremarket(next.timestamp) {
			s.preMarketVol += next.volume
		}
		if next.high > s.highOfDay {
			s.highOfDay = next.high
		}
		if s.currentFiveMin.Equal(bucket) {
			s.currentFiveVol += next.volume
		} else {
			s.currentFiveMin = bucket
			s.currentFiveVol = next.volume
		}
	}
	if s.dailyVolume > s.totalVolume {
		s.totalVolume = s.dailyVolume
	}
	if s.dailyHigh > s.highOfDay {
		s.highOfDay = s.dailyHigh
	}
}

func (s *symbolState) recomputeHighOfDay() {
	highOfDay := 0.0
	for _, bar := range s.minuteBars {
		if bar.high > highOfDay {
			highOfDay = bar.high
		}
	}
	if s.seedDay == s.day && s.seedHighOfDay > highOfDay {
		highOfDay = s.seedHighOfDay
	}
	if s.dailyHigh > highOfDay {
		highOfDay = s.dailyHigh
	}
	s.highOfDay = highOfDay
}

func lastNMinuteVolumes(bars []minuteBar, n int) []uint64 {
	if n <= 0 || len(bars) == 0 {
		return nil
	}
	if len(bars) > n {
		bars = bars[len(bars)-n:]
	}
	out := make([]uint64, 0, len(bars))
	for _, bar := range bars {
		out = append(out, bar.volume)
	}
	return out
}

func (s *symbolState) fiveMinuteVolumeAt(ts time.Time) uint64 {
	if s.currentFiveMin.IsZero() {
		return 0
	}
	if s.currentFiveMin.Equal(fiveMinuteBucketStart(ts)) {
		return s.currentFiveVol
	}
	return 0
}

func fiveMinuteBucketStart(ts time.Time) time.Time {
	et := ts.In(markethours.Location())
	minute := et.Minute() - (et.Minute() % 5)
	return time.Date(et.Year(), et.Month(), et.Day(), et.Hour(), minute, 0, 0, markethours.Location())
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
