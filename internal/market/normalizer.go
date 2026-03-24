package market

import (
	"math"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/volumeprofile"
)

// SeedState holds historical context for seeding the normalizer on startup.
type SeedState struct {
	PreviousClose float64
	PrevDayVolume int64
	TodayOpen     float64
	TodayHigh     float64
	TodayVolume   int64
	PreMarketVol  int64
}

// symbolState tracks per-symbol daily state for normalization.
type symbolState struct {
	day           string
	previousClose float64
	open          float64
	highOfDay     float64
	totalVolume   int64
	preMarketVol  int64
	prevDayVolume int64
	recentVolumes []int64
	lastClose     float64
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
		day:           day,
		previousClose: seed.PreviousClose,
		lastClose:     seed.PreviousClose,
		open:          seed.TodayOpen,
		highOfDay:     seed.TodayHigh,
		totalVolume:   seed.TodayVolume,
		preMarketVol:  seed.PreMarketVol,
		prevDayVolume: seed.PrevDayVolume,
	}
	n.states[symbol] = state
}

// Normalize converts a raw StreamBar into an enriched domain.Tick with
// gap%, relative volume, pre-market volume, volume spikes, and high-of-day.
func (n *Normalizer) Normalize(bar alpaca.StreamBar) domain.Tick {
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
		state.previousClose = state.lastClose
		state.day = day
		state.open = bar.Open
		state.highOfDay = 0
		state.totalVolume = 0
		state.preMarketVol = 0
		state.recentVolumes = nil
	}

	state.totalVolume += bar.Volume
	state.lastClose = bar.Close
	if bar.High > state.highOfDay {
		state.highOfDay = bar.High
	}
	if state.highOfDay == 0 {
		state.highOfDay = bar.High
	}

	if isPremarket(bar.Timestamp) {
		state.preMarketVol += bar.Volume
	}

	state.recentVolumes = append(state.recentVolumes, bar.Volume)
	if len(state.recentVolumes) > 5 {
		state.recentVolumes = state.recentVolumes[len(state.recentVolumes)-5:]
	}

	gapPercent := 0.0
	if state.previousClose > 0 && state.open > 0 {
		gapPercent = ((state.open - state.previousClose) / state.previousClose) * 100
	}

	relativeVolume := calculateRelativeVolume(state, bar.Timestamp)
	volumeSpike := isVolumeSpike(state.recentVolumes, bar.Volume, relativeVolume)

	return domain.Tick{
		Symbol:          bar.Symbol,
		Price:           round2(bar.Close),
		BarOpen:         round2(bar.Open),
		BarHigh:         round2(bar.High),
		BarLow:          round2(bar.Low),
		Open:            round2(state.open),
		HighOfDay:       round2(state.highOfDay),
		Volume:          state.totalVolume,
		RelativeVolume:  round2(relativeVolume),
		GapPercent:      round2(gapPercent),
		PreMarketVolume: state.preMarketVol,
		VolumeSpike:     volumeSpike,
		PrevDayVolume:   state.prevDayVolume,
		Timestamp:       bar.Timestamp,
	}
}

// UpdateDailyBar updates the session high-of-day and volume from a daily bar message.
// Daily bars are emitted every minute after market open and contain cumulative session data.
func (n *Normalizer) UpdateDailyBar(symbol string, high float64, volume int64, open float64) {
	state := n.states[symbol]
	if state == nil {
		return
	}
	if high > state.highOfDay {
		state.highOfDay = high
	}
	if open > 0 && state.open == 0 {
		state.open = open
	}
	// Daily bar volume is cumulative for the session
	if volume > state.totalVolume {
		state.totalVolume = volume
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

func isVolumeSpike(recent []int64, latest int64, relativeVolume float64) bool {
	if relativeVolume >= 5 {
		return true
	}
	if len(recent) < 3 {
		return false
	}
	var total int64
	for _, volume := range recent[:len(recent)-1] {
		total += volume
	}
	average := float64(total) / float64(len(recent)-1)
	return average > 0 && float64(latest) >= average*1.8
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
