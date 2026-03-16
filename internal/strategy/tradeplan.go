package strategy

import (
	"math"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

const (
	minATRPercentFallback = 0.012
	stopATRMultiplier     = 1.35
	stopATRBuffer         = 0.10
	maxRiskATRMultiplier  = 2.40
	breakEvenTriggerR     = 1.00
	breakEvenLockR        = 0.12
	trailActivationR      = 2.00
	trailATRMultiplier    = 2.60
	tightTrailTriggerR    = 3.50
	tightTrailATRMultiple = 1.85
	failedBreakoutCutR    = 0.60
	stagnationMinPeakR    = 0.75
)

// EntryPlan captures the volatility-aware stop and sizing basis for a setup.
type EntryPlan struct {
	StopPrice    float64
	RiskPerShare float64
	EntryATR     float64
	SetupType    string
}

// BuildEntryPlan derives the volatility-aware stop and risk basis for a candidate.
func BuildEntryPlan(candidate domain.Candidate) (EntryPlan, bool, string) {
	return buildEntryPlan(candidate)
}

func buildEntryPlan(candidate domain.Candidate) (EntryPlan, bool, string) {
	if candidate.Price <= 0 {
		return EntryPlan{}, false, "invalid-price"
	}
	if candidate.SetupType == "" {
		return EntryPlan{}, false, "no-setup"
	}
	atr := candidate.ATR
	if atr <= 0 {
		atr = candidate.Price * minATRPercentFallback
	}
	setupLow := candidate.SetupLow
	if setupLow <= 0 || setupLow >= candidate.Price {
		setupLow = candidate.Price - atr
	}
	structureStop := setupLow - (atr * stopATRBuffer)
	atrStop := candidate.Price - (atr * stopATRMultiplier)
	stopPrice := math.Max(structureStop, atrStop)
	if stopPrice >= candidate.Price {
		stopPrice = candidate.Price - atr
	}
	riskPerShare := candidate.Price - stopPrice
	if riskPerShare <= 0 {
		return EntryPlan{}, false, "invalid-risk"
	}
	if riskPerShare > atr*maxRiskATRMultiplier {
		return EntryPlan{}, false, "wide-risk"
	}
	return EntryPlan{
		StopPrice:    roundPrice(stopPrice),
		RiskPerShare: roundPrice(riskPerShare),
		EntryATR:     roundPrice(atr),
		SetupType:    candidate.SetupType,
	}, true, ""
}

func currentRMultiple(position domain.Position, price float64) float64 {
	if position.RiskPerShare <= 0 || position.AvgPrice <= 0 {
		return 0
	}
	return (price - position.AvgPrice) / position.RiskPerShare
}

// CurrentRMultiple reports the marked-to-market gain or loss in units of the
// trade's initial risk.
func CurrentRMultiple(position domain.Position, price float64) float64 {
	return currentRMultiple(position, price)
}

func peakRMultiple(position domain.Position, highWatermark float64) float64 {
	if position.RiskPerShare <= 0 || position.AvgPrice <= 0 {
		return 0
	}
	return (highWatermark - position.AvgPrice) / position.RiskPerShare
}

// PeakRMultiple reports the best achieved move in units of the trade's initial risk.
func PeakRMultiple(position domain.Position, highWatermark float64) float64 {
	return peakRMultiple(position, highWatermark)
}

func protectiveStop(position domain.Position, highWatermark float64) (float64, string) {
	stopPrice := position.InitialStopPrice
	reason := "stop-loss"
	if stopPrice <= 0 {
		stopPrice = position.StopPrice
	}
	riskPerShare := position.RiskPerShare
	if stopPrice <= 0 && position.AvgPrice > 0 {
		fallbackRisk := math.Max(position.EntryATR*stopATRMultiplier, position.AvgPrice*0.05)
		stopPrice = position.AvgPrice - fallbackRisk
		riskPerShare = fallbackRisk
	}
	if riskPerShare <= 0 {
		riskPerShare = position.RiskPerShare
	}
	if riskPerShare <= 0 {
		return stopPrice, reason
	}
	fallbackPosition := position
	fallbackPosition.RiskPerShare = riskPerShare
	peakR := peakRMultiple(fallbackPosition, highWatermark)
	if peakR >= breakEvenTriggerR {
		stopPrice = math.Max(stopPrice, position.AvgPrice+(riskPerShare*breakEvenLockR))
		reason = "break-even-stop"
	}
	if peakR >= trailActivationR {
		trailWidth := math.Max(position.EntryATR*trailATRMultiplier, riskPerShare*1.25)
		if peakR >= tightTrailTriggerR {
			trailWidth = math.Max(position.EntryATR*tightTrailATRMultiple, riskPerShare)
		}
		stopPrice = math.Max(stopPrice, highWatermark-trailWidth)
		reason = "trailing-stop"
	}
	return roundPrice(stopPrice), reason
}

// ProtectiveStop returns the active managed stop price and the exit reason it implies.
func ProtectiveStop(position domain.Position, highWatermark float64) (float64, string) {
	return protectiveStop(position, highWatermark)
}

func failedBreakoutPrice(position domain.Position) float64 {
	if position.RiskPerShare <= 0 {
		return 0
	}
	return roundPrice(position.AvgPrice - (position.RiskPerShare * failedBreakoutCutR))
}

// FailedBreakoutPrice returns the early-cut price used for non-follow-through setups.
func FailedBreakoutPrice(position domain.Position) float64 {
	return failedBreakoutPrice(position)
}

func shouldTimeStop(position domain.Position, at time.Time, cfgBreakoutFailureWindowMin, cfgStagnationWindowMin int) bool {
	holdingTime := at.Sub(position.OpenedAt)
	if holdingTime < time.Duration(cfgStagnationWindowMin)*time.Minute {
		return false
	}
	if holdingTime < time.Duration(cfgBreakoutFailureWindowMin)*time.Minute {
		return false
	}
	return true
}

func roundPrice(price float64) float64 {
	return math.Round(price*100) / 100
}
