package scanner

import (
	"context"
	"sync"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

// Scanner scans market ticks for momentum candidates.
type Scanner struct {
	config  config.TradingConfig
	runtime *runtime.State
}

// NewScanner creates a scanner with the configured filters.
func NewScanner(cfg config.TradingConfig, runtimeState *runtime.State) *Scanner {
	return &Scanner{config: cfg, runtime: runtimeState}
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

func (s *Scanner) evaluateTick(tick domain.Tick) (domain.Candidate, bool) {
	if tick.Price <= s.config.MinPrice {
		return domain.Candidate{}, false
	}
	if tick.GapPercent <= s.config.MinGapPercent {
		return domain.Candidate{}, false
	}
	if tick.RelativeVolume <= s.config.MinRelativeVolume {
		return domain.Candidate{}, false
	}
	if tick.PreMarketVolume <= s.config.MinPremarketVolume {
		return domain.Candidate{}, false
	}
	if !tick.VolumeSpike {
		return domain.Candidate{}, false
	}

	score := (tick.GapPercent * 0.40) + (tick.RelativeVolume * 2.10)
	return domain.Candidate{
		Symbol:          tick.Symbol,
		Price:           tick.Price,
		GapPercent:      tick.GapPercent,
		RelativeVolume:  tick.RelativeVolume,
		PreMarketVolume: tick.PreMarketVolume,
		HighOfDay:       tick.HighOfDay,
		Score:           score,
		Catalyst:        tick.Catalyst,
		CatalystURL:     tick.CatalystURL,
		Timestamp:       tick.Timestamp,
	}, true
}
