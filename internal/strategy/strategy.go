package strategy

import (
	"context"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

// Strategy implements breakout entries and managed exits.
type Strategy struct {
	config      config.TradingConfig
	portfolio   *portfolio.Manager
	runtime     *runtime.State
	lastEntryAt map[string]time.Time
	lastExitAt  map[string]time.Time
}

// NewStrategy creates a strategy instance.
func NewStrategy(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State) *Strategy {
	return &Strategy{
		config:      cfg,
		portfolio:   portfolioManager,
		runtime:     runtimeState,
		lastEntryAt: make(map[string]time.Time),
		lastExitAt:  make(map[string]time.Time),
	}
}

// Start listens for candidates and ticks, generating both entry and exit signals.
func (s *Strategy) Start(ctx context.Context, candidates <-chan domain.Candidate, ticks <-chan domain.Tick, out chan<- domain.TradeSignal) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case candidate := <-candidates:
			signal, ok := s.evaluateCandidate(candidate)
			if !ok {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- signal:
			}
		case tick := <-ticks:
			signal, ok := s.evaluateExit(tick)
			if !ok {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- signal:
			}
		}
	}
}

func (s *Strategy) evaluateCandidate(candidate domain.Candidate) (domain.TradeSignal, bool) {
	if !s.runtime.CanOpenNewPositions() {
		return domain.TradeSignal{}, false
	}
	if s.portfolio.HasPosition(candidate.Symbol) {
		return domain.TradeSignal{}, false
	}
	if lastEntry, exists := s.lastEntryAt[candidate.Symbol]; exists {
		if time.Since(lastEntry) < time.Duration(s.config.EntryCooldownSec)*time.Second {
			return domain.TradeSignal{}, false
		}
	}
	if candidate.Price < candidate.HighOfDay*0.995 {
		return domain.TradeSignal{}, false
	}

	quantity := int64(0)
	riskAmount := s.config.StartingCapital * s.config.RiskPerTradePct
	stopDistance := candidate.Price * s.config.StopLossPct
	if stopDistance > 0 {
		quantity = int64(riskAmount / stopDistance)
	}
	if quantity < 1 {
		quantity = 1
	}
	s.lastEntryAt[candidate.Symbol] = time.Now().UTC()

	return domain.TradeSignal{
		Symbol:     candidate.Symbol,
		Side:       "buy",
		Price:      candidate.Price,
		Quantity:   quantity,
		Reason:     "breakout-confirmation",
		Confidence: candidate.Score,
		Timestamp:  time.Now().UTC(),
	}, true
}

func (s *Strategy) evaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	position, exists := s.portfolio.Position(tick.Symbol)
	if !exists {
		return domain.TradeSignal{}, false
	}
	if lastExit, seen := s.lastExitAt[tick.Symbol]; seen {
		if time.Since(lastExit) < time.Duration(s.config.ExitCooldownSec)*time.Second {
			return domain.TradeSignal{}, false
		}
	}

	stopPrice := position.AvgPrice * (1 - s.config.StopLossPct)
	profitTarget := position.AvgPrice * (1 + s.config.ProfitTargetPct)
	trailingStop := position.HighestPrice * (1 - s.config.TrailingStopPct)

	reason := ""
	switch {
	case tick.Price <= stopPrice:
		reason = "stop-loss"
	case tick.Price >= profitTarget:
		reason = "profit-target"
	case position.HighestPrice > position.AvgPrice && tick.Price <= trailingStop:
		reason = "trailing-stop"
	default:
		return domain.TradeSignal{}, false
	}

	s.lastExitAt[tick.Symbol] = time.Now().UTC()
	return domain.TradeSignal{
		Symbol:     tick.Symbol,
		Side:       "sell",
		Price:      tick.Price,
		Quantity:   position.Quantity,
		Reason:     reason,
		Confidence: 1,
		Timestamp:  time.Now().UTC(),
	}, true
}
