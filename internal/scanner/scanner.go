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
	config         config.TradingConfig
	runtime        *runtime.State
	mu             sync.Mutex
	state          map[string]*symbolState
	leaderDay      string
	leaderMetric   float64
	leaderSymbol   string
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
	oneMinuteReturn, threeMinuteReturn, volumeRate := s.updateSymbolState(tick)
	priceVsOpenPct := percentChange(tick.Open, tick.Price)

	if tick.Price <= s.config.MinPrice {
		return domain.Candidate{}, false, "min-price"
	}
	if tick.RelativeVolume <= s.config.MinRelativeVolume {
		return domain.Candidate{}, false, "min-relative-volume"
	}
	if !tick.VolumeSpike {
		return domain.Candidate{}, false, "volume-spike"
	}
	if !s.qualifiesMomentumProfile(tick, priceVsOpenPct, oneMinuteReturn, threeMinuteReturn, volumeRate) {
		return domain.Candidate{}, false, "not-gap-or-squeeze"
	}
	volumeLeaderPct := s.updateVolumeLeadership(tick)
	distanceFromHighPct := percentChange(tick.Price, tick.HighOfDay)
	score := s.momentumScore(tick, priceVsOpenPct, distanceFromHighPct, oneMinuteReturn, threeMinuteReturn, volumeRate, volumeLeaderPct)
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
		VolumeLeaderPct:      clampFloat(scoreOrZero(volumeLeaderPct), 0, 1),
		MinutesSinceOpen:     round2(minutesSinceOpen(tick.Timestamp)),
		Score:                round2(scoreOrZero(score)),
		Catalyst:             tick.Catalyst,
		CatalystURL:          tick.CatalystURL,
		Timestamp:            tick.Timestamp,
	}, true, "candidate"
}

func (s *Scanner) momentumScore(tick domain.Tick, priceVsOpenPct, distanceFromHighPct, oneMinuteReturn, threeMinuteReturn, volumeRate, volumeLeaderPct float64) float64 {
	maxGap := s.config.MinGapPercent + 25
	if maxGap < 25 {
		maxGap = 25
	}
	maxRelativeVolume := s.config.MinRelativeVolume + 15
	if maxRelativeVolume < 12 {
		maxRelativeVolume = 12
	}
	maxPriceVsOpen := s.config.MaxPriceVsOpenPct + 5
	if maxPriceVsOpen < 20 {
		maxPriceVsOpen = 20
	}

	return (clampFloat(tick.GapPercent, -10, maxGap) * 0.30) +
		(clampFloat(tick.RelativeVolume, 0, maxRelativeVolume) * 1.60) +
		(clampFloat(priceVsOpenPct, -5, maxPriceVsOpen) * 1.10) +
		(clampFloat(oneMinuteReturn, -3, 6) * 3.40) +
		(clampFloat(threeMinuteReturn, -5, 10) * 1.80) +
		(clampFloat(volumeRate, 0.5, 4) * 1.20) -
		(clampFloat(distanceFromHighPct, 0, 6) * 2.50) +
		(clampFloat(volumeLeaderPct, 0, 1) * 6.00)
}

func (s *Scanner) qualifiesMomentumProfile(tick domain.Tick, priceVsOpenPct, oneMinuteReturn, threeMinuteReturn, volumeRate float64) bool {
	if tick.GapPercent >= s.config.MinGapPercent && tick.PreMarketVolume >= s.config.MinPremarketVolume {
		return true
	}

	intradayMoveThreshold := maxFloat(3.5, s.config.MinGapPercent*0.35)
	if priceVsOpenPct < intradayMoveThreshold {
		return false
	}
	if threeMinuteReturn < s.config.MinThreeMinuteReturnPct && oneMinuteReturn < s.config.MinOneMinuteReturnPct {
		return false
	}
	if volumeRate < maxFloat(1.0, s.config.MinVolumeRate-0.05) {
		return false
	}
	return tick.RelativeVolume >= s.config.MinRelativeVolume+0.25
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

func (s *Scanner) updateVolumeLeadership(tick domain.Tick) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	dayKey := tick.Timestamp.In(marketLocation).Format("2006-01-02")
	if s.leaderDay != dayKey {
		s.leaderDay = dayKey
		s.leaderMetric = 0
		s.leaderSymbol = ""
	}
	leaderMetric := momentumLeaderMetric(tick)
	if leaderMetric > s.leaderMetric {
		s.leaderMetric = leaderMetric
		s.leaderSymbol = tick.Symbol
	}
	if s.leaderMetric <= 0 {
		return 1
	}
	return leaderMetric / s.leaderMetric
}

func momentumLeaderMetric(tick domain.Tick) float64 {
	relativeVolume := clampFloat(tick.RelativeVolume, 1, 25)
	return tick.Price * float64(tick.Volume) * relativeVolume
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

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
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
