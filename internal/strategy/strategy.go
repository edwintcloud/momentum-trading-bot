package strategy

import (
	"context"
	"fmt"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

// Strategy implements breakout entries and managed exits.
type Strategy struct {
	config      config.TradingConfig
	portfolio   *portfolio.Manager
	runtime     *runtime.State
	entryModel  LinearModel
	lastEntryAt map[string]time.Time
	lastExitAt  map[string]time.Time
}

// NewStrategy creates a strategy instance.
func NewStrategy(cfg config.TradingConfig, portfolioManager *portfolio.Manager, runtimeState *runtime.State) *Strategy {
	entryModel := DefaultEntryModel()
	if cfg.EntryModelPath != "" {
		loaded, err := LoadLinearModel(cfg.EntryModelPath)
		if err != nil {
			runtimeState.RecordLog("warn", "strategy", "could not load entry model from "+cfg.EntryModelPath+": "+err.Error())
		} else {
			entryModel = loaded
			runtimeState.RecordLog("info", "strategy", "loaded entry model "+entryModel.Name)
		}
	}
	return &Strategy{
		config:      cfg,
		portfolio:   portfolioManager,
		runtime:     runtimeState,
		entryModel:  entryModel,
		lastEntryAt: make(map[string]time.Time),
		lastExitAt:  make(map[string]time.Time),
	}
}

// SetEntryModel swaps the active entry model at runtime.
func (s *Strategy) SetEntryModel(model LinearModel) {
	s.entryModel = model
}

// Start listens for candidates and ticks, generating both entry and exit signals.
func (s *Strategy) Start(ctx context.Context, candidates <-chan domain.Candidate, ticks <-chan domain.Tick, out chan<- domain.TradeSignal) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case candidate, ok := <-candidates:
			if !ok {
				return fmt.Errorf("strategy candidates channel closed")
			}
			signal, shouldEmit := s.evaluateCandidate(candidate)
			if !shouldEmit {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case out <- signal:
			}
		case tick, ok := <-ticks:
			if !ok {
				return fmt.Errorf("strategy ticks channel closed")
			}
			signal, shouldEmit := s.evaluateExit(tick)
			if !shouldEmit {
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

// EvaluateCandidate applies the entry rules to a scanner candidate.
func (s *Strategy) EvaluateCandidate(candidate domain.Candidate) (domain.TradeSignal, bool) {
	signal, ok, _ := s.evaluateCandidateDetailed(candidate)
	return signal, ok
}

// EvaluateCandidateDetailed applies the entry rules and returns the block reason when rejected.
func (s *Strategy) EvaluateCandidateDetailed(candidate domain.Candidate) (domain.TradeSignal, bool, string) {
	return s.evaluateCandidateDetailed(candidate)
}

// EvaluateExit applies the managed exit rules to a market tick.
func (s *Strategy) EvaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	signal, ok, _ := s.evaluateExitDetailed(tick)
	return signal, ok
}

// EvaluateExitDetailed applies the exit rules and returns the decision reason.
func (s *Strategy) EvaluateExitDetailed(tick domain.Tick) (domain.TradeSignal, bool, string) {
	return s.evaluateExitDetailed(tick)
}

func (s *Strategy) evaluateCandidate(candidate domain.Candidate) (domain.TradeSignal, bool) {
	signal, ok, _ := s.evaluateCandidateDetailed(candidate)
	return signal, ok
}

func (s *Strategy) evaluateCandidateDetailed(candidate domain.Candidate) (domain.TradeSignal, bool, string) {
	decisionAt := decisionTime(candidate.Timestamp)
	if !markethours.IsTradableSessionAt(decisionAt) {
		return domain.TradeSignal{}, false, "outside-session"
	}
	if blockReason := s.runtime.EntryBlockReasonAt(decisionAt); blockReason != "" {
		return domain.TradeSignal{}, false, blockReason
	}
	if s.portfolio.HasPosition(candidate.Symbol) {
		return domain.TradeSignal{}, false, "has-position"
	}
	if lastEntry, exists := s.lastEntryAt[candidate.Symbol]; exists {
		if decisionAt.Sub(lastEntry) < time.Duration(s.config.EntryCooldownSec)*time.Second {
			return domain.TradeSignal{}, false, "entry-cooldown"
		}
	}
	if candidate.Price < candidate.HighOfDay*0.995 {
		return domain.TradeSignal{}, false, "below-breakout-zone"
	}
	predictedReturn := s.entryModel.Predict(candidate)
	if s.config.EntryModelEnabled && predictedReturn < s.config.EntryModelMinPredictedReturnPct {
		return domain.TradeSignal{}, false, "model-threshold"
	}

	quantity := int64(0)
	riskAmount := s.portfolio.EffectiveCapital() * s.config.RiskPerTradePct
	stopDistance := candidate.Price * s.config.StopLossPct
	if stopDistance > 0 {
		quantity = int64(riskAmount / stopDistance)
	}
	if quantity < 1 {
		quantity = 1
	}
	s.lastEntryAt[candidate.Symbol] = decisionAt

	return domain.TradeSignal{
		Symbol:     candidate.Symbol,
		Side:       "buy",
		Price:      candidate.Price,
		Quantity:   quantity,
		Reason:     "ml-breakout-entry",
		Confidence: predictedReturn,
		Timestamp:  decisionAt,
	}, true, "entry-signal"
}

func (s *Strategy) evaluateExit(tick domain.Tick) (domain.TradeSignal, bool) {
	signal, ok, _ := s.evaluateExitDetailed(tick)
	return signal, ok
}

func (s *Strategy) evaluateExitDetailed(tick domain.Tick) (domain.TradeSignal, bool, string) {
	decisionAt := decisionTime(tick.Timestamp)
	if !markethours.IsTradableSessionAt(decisionAt) {
		return domain.TradeSignal{}, false, "outside-session"
	}
	position, exists := s.portfolio.Position(tick.Symbol)
	if !exists {
		return domain.TradeSignal{}, false, "no-position"
	}
	if lastExit, seen := s.lastExitAt[tick.Symbol]; seen {
		if decisionAt.Sub(lastExit) < time.Duration(s.config.ExitCooldownSec)*time.Second {
			return domain.TradeSignal{}, false, "exit-cooldown"
		}
	}

	stopPrice := position.AvgPrice * (1 - s.config.StopLossPct)
	trailingStop := position.HighestPrice * (1 - s.config.TrailingStopPct)
	trailingActivationPrice := position.AvgPrice * (1 + s.config.TrailingStopActivationPct)

	reason := ""
	switch {
	case tick.Price <= stopPrice:
		reason = "stop-loss"
	case position.HighestPrice >= trailingActivationPrice && tick.Price <= trailingStop:
		reason = "trailing-stop"
	default:
		return domain.TradeSignal{}, false, "hold"
	}

	s.lastExitAt[tick.Symbol] = decisionAt
	return domain.TradeSignal{
		Symbol:     tick.Symbol,
		Side:       "sell",
		Price:      tick.Price,
		Quantity:   position.Quantity,
		Reason:     reason,
		Confidence: 1,
		Timestamp:  decisionAt,
	}, true, reason
}

func decisionTime(timestamp time.Time) time.Time {
	if timestamp.IsZero() {
		return time.Now().UTC()
	}
	return timestamp.UTC()
}
