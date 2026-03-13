package scanner

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

var marketLocation = mustLoadLocation("America/New_York")

type symbolSnapshot struct {
	timestamp time.Time
	price     float64
	volume    int64
}

type symbolState struct {
	snapshots []symbolSnapshot
	deltas    []int64
}

// Scanner scans market ticks for momentum candidates.
type Scanner struct {
	config  config.TradingConfig
	runtime *runtime.State
	mu      sync.Mutex
	state   map[string]*symbolState
}

// NewScanner creates a scanner with the configured filters.
func NewScanner(cfg config.TradingConfig, runtimeState *runtime.State) *Scanner {
	return &Scanner{config: cfg, runtime: runtimeState, state: make(map[string]*symbolState)}
}

// Start evaluates ticks concurrently and emits candidates.
func (s *Scanner) Start(ctx context.Context, in <-chan domain.Tick, out chan<- domain.Candidate) error {
	workerCount := s.config.ScannerWorkers
	if workerCount < 1 {
		workerCount = 1
	}

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case tick, ok := <-in:
					if !ok {
						s.runtime.RecordLog("warn", "scanner", "input channel closed")
						return
					}
					candidate, shouldEmit := s.evaluateTick(tick)
					if !shouldEmit {
						continue
					}
					s.runtime.RecordCandidate(candidate)
					select {
					case <-ctx.Done():
						return
					case out <- candidate:
					}
				}
			}
		}()
	}

	<-ctx.Done()
	workers.Wait()
	return ctx.Err()
}

// EvaluateTick applies the scanner filters and feature extraction to a tick.
func (s *Scanner) EvaluateTick(tick domain.Tick) (domain.Candidate, bool) {
	candidate, ok, _ := s.evaluateTickDetailed(tick)
	return candidate, ok
}

// EvaluateTickDetailed applies the scanner filters and returns the block reason when rejected.
func (s *Scanner) EvaluateTickDetailed(tick domain.Tick) (domain.Candidate, bool, string) {
	return s.evaluateTickDetailed(tick)
}

func (s *Scanner) evaluateTick(tick domain.Tick) (domain.Candidate, bool) {
	candidate, ok, _ := s.evaluateTickDetailed(tick)
	return candidate, ok
}

func (s *Scanner) evaluateTickDetailed(tick domain.Tick) (domain.Candidate, bool, string) {
	if tick.Price <= s.config.MinPrice {
		return domain.Candidate{}, false, "min-price"
	}
	if tick.GapPercent <= s.config.MinGapPercent {
		return domain.Candidate{}, false, "min-gap"
	}
	if tick.RelativeVolume <= s.config.MinRelativeVolume {
		return domain.Candidate{}, false, "min-relative-volume"
	}
	if tick.PreMarketVolume <= s.config.MinPremarketVolume {
		return domain.Candidate{}, false, "min-premarket-volume"
	}
	if !tick.VolumeSpike {
		return domain.Candidate{}, false, "volume-spike"
	}

	oneMinuteReturn, threeMinuteReturn, volumeRate := s.updateSymbolState(tick)
	priceVsOpenPct := percentChange(tick.Open, tick.Price)
	distanceFromHighPct := percentChange(tick.Price, tick.HighOfDay)
	score := (tick.GapPercent * 0.30) +
		(tick.RelativeVolume * 1.60) +
		(priceVsOpenPct * 1.10) +
		(oneMinuteReturn * 3.40) +
		(threeMinuteReturn * 1.80) +
		(volumeRate * 1.20) -
		(distanceFromHighPct * 2.50)
	return domain.Candidate{
		Symbol:               tick.Symbol,
		Price:                tick.Price,
		Open:                 tick.Open,
		GapPercent:           tick.GapPercent,
		RelativeVolume:       tick.RelativeVolume,
		PreMarketVolume:      tick.PreMarketVolume,
		Volume:               tick.Volume,
		HighOfDay:            tick.HighOfDay,
		PriceVsOpenPct:       round2(scoreOrZero(priceVsOpenPct)),
		DistanceFromHighPct:  round2(scoreOrZero(distanceFromHighPct)),
		OneMinuteReturnPct:   round2(scoreOrZero(oneMinuteReturn)),
		ThreeMinuteReturnPct: round2(scoreOrZero(threeMinuteReturn)),
		VolumeRate:           round2(scoreOrZero(volumeRate)),
		MinutesSinceOpen:     round2(minutesSinceOpen(tick.Timestamp)),
		Score:                score,
		Catalyst:             tick.Catalyst,
		CatalystURL:          tick.CatalystURL,
		Timestamp:            tick.Timestamp,
	}, true, "candidate"
}

func (s *Scanner) updateSymbolState(tick domain.Tick) (float64, float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.state[tick.Symbol]
	if state == nil {
		state = &symbolState{}
		s.state[tick.Symbol] = state
	}

	deltaVolume := tick.Volume
	if last := len(state.snapshots); last > 0 {
		deltaVolume = tick.Volume - state.snapshots[last-1].volume
		if deltaVolume < 0 {
			deltaVolume = tick.Volume
		}
	}

	state.snapshots = append(state.snapshots, symbolSnapshot{
		timestamp: tick.Timestamp.UTC(),
		price:     tick.Price,
		volume:    tick.Volume,
	})
	cutoff := tick.Timestamp.UTC().Add(-5 * time.Minute)
	trimmed := state.snapshots[:0]
	for _, snapshot := range state.snapshots {
		if snapshot.timestamp.Before(cutoff) {
			continue
		}
		trimmed = append(trimmed, snapshot)
	}
	state.snapshots = trimmed

	state.deltas = append(state.deltas, deltaVolume)
	if len(state.deltas) > 6 {
		state.deltas = state.deltas[len(state.deltas)-6:]
	}

	return sampleReturn(state.snapshots, tick.Timestamp.UTC(), 1*time.Minute),
		sampleReturn(state.snapshots, tick.Timestamp.UTC(), 3*time.Minute),
		volumeRate(state.deltas, deltaVolume)
}

func sampleReturn(samples []symbolSnapshot, current time.Time, lookback time.Duration) float64 {
	if len(samples) < 2 {
		return 0
	}
	target := current.Add(-lookback)
	baseline := samples[0].price
	for _, sample := range samples {
		if sample.timestamp.After(target) {
			break
		}
		baseline = sample.price
	}
	return percentChange(baseline, samples[len(samples)-1].price)
}

func volumeRate(deltas []int64, latest int64) float64 {
	if len(deltas) < 2 {
		return 1
	}
	var total int64
	for _, delta := range deltas[:len(deltas)-1] {
		total += delta
	}
	if total <= 0 {
		return 1
	}
	average := float64(total) / float64(len(deltas)-1)
	if average <= 0 {
		return 1
	}
	return float64(latest) / average
}

func percentChange(from, to float64) float64 {
	if from == 0 {
		return 0
	}
	return ((to - from) / from) * 100
}

func minutesSinceOpen(timestamp time.Time) float64 {
	est := timestamp.In(marketLocation)
	open := time.Date(est.Year(), est.Month(), est.Day(), 9, 30, 0, 0, est.Location())
	return maxFloat(0, est.Sub(open).Minutes())
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func scoreOrZero(value float64) float64 {
	if value != value {
		return 0
	}
	return value
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
