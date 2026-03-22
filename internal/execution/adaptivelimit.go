package execution

import (
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

// AdaptiveLimitAction is the result of an adaptive limit price check.
type AdaptiveLimitAction int

const (
	// AdaptiveLimitHold means keep the current limit price.
	AdaptiveLimitHold AdaptiveLimitAction = iota
	// AdaptiveLimitWiden means update the limit price to the new wider value.
	AdaptiveLimitWiden
	// AdaptiveLimitCancel means cancel the order (max slippage exceeded).
	AdaptiveLimitCancel
)

// AdaptiveLimitState tracks the state of an adaptive limit order.
type AdaptiveLimitState struct {
	Symbol        string
	Side          string  // "buy" or "sell"
	ArrivalPrice  float64 // mid-price at order creation
	InitialLimit  float64 // first limit price set
	CurrentLimit  float64 // current limit price after widening
	ToleranceBps  float64 // initial tolerance from mid in bps
	WidenStepBps  float64 // widening step per interval in bps
	WidenInterval time.Duration
	MaxSlippageBps float64
	CreatedAt     time.Time
	LastWidenAt   time.Time
	WideningSteps int
}

// NewAdaptiveLimitState creates a new adaptive limit order state.
// For buy orders, the limit is set above mid; for sell orders, below mid.
func NewAdaptiveLimitState(symbol, side string, midPrice float64, now time.Time, cfg config.TradingConfig) *AdaptiveLimitState {
	toleranceBps := cfg.AdaptiveLimitToleranceBps
	if toleranceBps <= 0 {
		toleranceBps = 5.0
	}
	widenStepBps := cfg.AdaptiveLimitWidenStepBps
	if widenStepBps <= 0 {
		widenStepBps = 0.5
	}
	widenIntervalSec := cfg.AdaptiveLimitWidenIntervalSec
	if widenIntervalSec <= 0 {
		widenIntervalSec = 5
	}
	maxSlippageBps := cfg.AdaptiveLimitMaxSlippageBps
	if maxSlippageBps <= 0 {
		maxSlippageBps = 20.0
	}

	var initialLimit float64
	if side == "buy" {
		initialLimit = midPrice * (1.0 + toleranceBps/10000.0)
	} else {
		initialLimit = midPrice * (1.0 - toleranceBps/10000.0)
	}

	return &AdaptiveLimitState{
		Symbol:        symbol,
		Side:          side,
		ArrivalPrice:  midPrice,
		InitialLimit:  initialLimit,
		CurrentLimit:  initialLimit,
		ToleranceBps:  toleranceBps,
		WidenStepBps:  widenStepBps,
		WidenInterval: time.Duration(widenIntervalSec) * time.Second,
		MaxSlippageBps: maxSlippageBps,
		CreatedAt:     now,
		LastWidenAt:   now,
	}
}

// Check evaluates whether the limit price should be widened or the order cancelled.
// Returns the action to take and the updated limit price.
func (s *AdaptiveLimitState) Check(now time.Time) (AdaptiveLimitAction, float64) {
	// Check if enough time has passed for a widening step.
	if now.Sub(s.LastWidenAt) < s.WidenInterval {
		return AdaptiveLimitHold, s.CurrentLimit
	}

	// Compute new widened limit.
	var newLimit float64
	if s.Side == "buy" {
		newLimit = s.CurrentLimit * (1.0 + s.WidenStepBps/10000.0)
	} else {
		newLimit = s.CurrentLimit * (1.0 - s.WidenStepBps/10000.0)
	}

	// Check if the new limit exceeds max slippage from arrival price.
	if s.exceedsMaxSlippage(newLimit) {
		return AdaptiveLimitCancel, s.CurrentLimit
	}

	s.CurrentLimit = newLimit
	s.LastWidenAt = now
	s.WideningSteps++
	return AdaptiveLimitWiden, s.CurrentLimit
}

// exceedsMaxSlippage returns true if the given limit price would exceed
// the maximum allowed slippage from the arrival price.
func (s *AdaptiveLimitState) exceedsMaxSlippage(limitPrice float64) bool {
	if s.ArrivalPrice <= 0 {
		return false
	}
	maxSlippageFrac := s.MaxSlippageBps / 10000.0
	if s.Side == "buy" {
		return limitPrice > s.ArrivalPrice*(1.0+maxSlippageFrac)
	}
	return limitPrice < s.ArrivalPrice*(1.0-maxSlippageFrac)
}

// SlippageBps returns the current slippage from arrival price in basis points.
func (s *AdaptiveLimitState) SlippageBps() float64 {
	if s.ArrivalPrice <= 0 {
		return 0
	}
	if s.Side == "buy" {
		return (s.CurrentLimit/s.ArrivalPrice - 1.0) * 10000.0
	}
	return (1.0 - s.CurrentLimit/s.ArrivalPrice) * 10000.0
}
