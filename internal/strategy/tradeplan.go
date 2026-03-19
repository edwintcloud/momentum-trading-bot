package strategy

import (
	"math"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

// EntryPlan captures the volatility-aware stop and sizing basis for a setup.
type EntryPlan struct {
	StopPrice    float64
	RiskPerShare float64
	EntryATR     float64
	SetupType    string
}

func buildEntryPlan(cfg config.TradingConfig, candidate domain.Candidate) (EntryPlan, bool, string) {
	if domain.IsShort(candidate.Direction) {
		return buildShortEntryPlan(cfg, candidate)
	}
	return buildLongEntryPlan(cfg, candidate)
}

func buildLongEntryPlan(cfg config.TradingConfig, candidate domain.Candidate) (EntryPlan, bool, string) {
	if candidate.Price <= 0 {
		return EntryPlan{}, false, "invalid-price"
	}
	if candidate.SetupType == "" {
		return EntryPlan{}, false, "no-setup"
	}
	atr := candidate.ATR
	if atr <= 0 {
		atr = candidate.Price * cfg.EntryATRPercentFallback
	}
	setupLow := candidate.SetupLow
	if setupLow <= 0 || setupLow >= candidate.Price {
		setupLow = candidate.Price - (atr * cfg.EntryStopATRMultiplier)
	}
	atrStop := candidate.Price - (atr * cfg.EntryStopATRMultiplier)
	stopPrice := math.Min(setupLow, atrStop)
	if stopPrice >= candidate.Price {
		stopPrice = atrStop
	}
	riskPerShare := candidate.Price - stopPrice
	if riskPerShare <= 0 {
		return EntryPlan{}, false, "invalid-risk"
	}
	if riskPerShare > atr*cfg.MaxRiskATRMultiplier {
		return EntryPlan{}, false, "wide-risk"
	}
	return EntryPlan{
		StopPrice:    roundPrice(stopPrice),
		RiskPerShare: roundPrice(riskPerShare),
		EntryATR:     roundPrice(atr),
		SetupType:    candidate.SetupType,
	}, true, ""
}

func buildShortEntryPlan(cfg config.TradingConfig, candidate domain.Candidate) (EntryPlan, bool, string) {
	if candidate.Price <= 0 {
		return EntryPlan{}, false, "invalid-price"
	}
	if candidate.SetupType == "" {
		return EntryPlan{}, false, "no-setup"
	}
	atr := candidate.ATR
	if atr <= 0 {
		atr = candidate.Price * cfg.EntryATRPercentFallback
	}
	setupHigh := candidate.SetupHigh
	if setupHigh <= candidate.Price {
		setupHigh = candidate.Price + (atr * cfg.ShortStopATRMultiplier)
	}
	atrStop := candidate.Price + (atr * cfg.ShortStopATRMultiplier)
	stopPrice := math.Max(setupHigh, atrStop)
	riskPerShare := stopPrice - candidate.Price
	if riskPerShare <= 0 {
		return EntryPlan{}, false, "invalid-risk"
	}
	if riskPerShare > atr*cfg.MaxRiskATRMultiplier {
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
	if domain.IsShort(position.Side) {
		return (position.AvgPrice - price) / position.RiskPerShare
	}
	return (price - position.AvgPrice) / position.RiskPerShare
}

func peakRMultiple(position domain.Position, watermark float64) float64 {
	if position.RiskPerShare <= 0 || position.AvgPrice <= 0 {
		return 0
	}
	if domain.IsShort(position.Side) {
		return (position.AvgPrice - watermark) / position.RiskPerShare
	}
	return (watermark - position.AvgPrice) / position.RiskPerShare
}

// PeakRMultiple reports the best achieved move in units of the trade's initial risk.
func PeakRMultiple(position domain.Position, highWatermark float64) float64 {
	return peakRMultiple(position, highWatermark)
}

func protectiveStop(cfg config.TradingConfig, position domain.Position, watermark, currentPrice float64, at time.Time) (float64, string) {
	stopPrice := position.InitialStopPrice
	reason := "stop-loss"
	if stopPrice <= 0 {
		stopPrice = position.StopPrice
	}
	riskPerShare := position.RiskPerShare
	if stopPrice <= 0 && position.AvgPrice > 0 {
		fallbackRisk := math.Max(position.EntryATR*cfg.EntryStopATRMultiplier, position.AvgPrice*cfg.StopLossPct)
		if domain.IsShort(position.Side) {
			stopPrice = position.AvgPrice + fallbackRisk
		} else {
			stopPrice = position.AvgPrice - fallbackRisk
		}
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
	peakR := peakRMultiple(fallbackPosition, watermark)
	currentR := currentRMultiple(fallbackPosition, currentPrice)
	// Time-based break-even: if open long enough with confirmed positive
	// excursion, move stop to entry to prevent winners from becoming losses.
	holdingTime := at.Sub(position.OpenedAt)
	if !position.OpenedAt.IsZero() && !at.IsZero() && holdingTime >= time.Duration(cfg.BreakEvenHoldMinutes)*time.Minute && peakR >= cfg.BreakEvenMinR && currentR >= 0 {
		breakEvenStop := position.AvgPrice
		if (domain.IsShort(position.Side) && (stopPrice == 0 || breakEvenStop < stopPrice)) ||
			(!domain.IsShort(position.Side) && breakEvenStop > stopPrice) {
			stopPrice = breakEvenStop
			reason = "break-even-stop"
		}
	}

	// if peakR >= cfg.ProfitTargetR {
	// 	return roundPrice(currentPrice), "profit-target"
	// }

	if peakR >= cfg.TrailActivationR && currentR >= cfg.StructureConfirmR {
		trailWidth := math.Max(position.EntryATR*cfg.TrailATRMultiplier, riskPerShare*1.25)
		if peakR >= cfg.TightTrailTriggerR {
			trailWidth = math.Max(position.EntryATR*cfg.TightTrailATRMultiplier, riskPerShare*0.75)
		}
		// Graduated trail floor: don't lock in break-even until the trade
		// has moved enough in our favor.
		if domain.IsShort(position.Side) {
			trailCeiling := position.AvgPrice + (riskPerShare * 0.30)
			if peakR >= 1.00 {
				trailCeiling = position.AvgPrice
			}
			if peakR >= 1.50 {
				trailCeiling = position.AvgPrice - (riskPerShare * 0.50)
			}
			if stopPrice == 0 || trailCeiling < stopPrice {
				stopPrice = trailCeiling
			}
			if candidate := watermark + trailWidth; stopPrice == 0 || candidate < stopPrice {
				stopPrice = candidate
			}
		} else {
			trailFloor := position.AvgPrice - (riskPerShare * 0.30)
			if peakR >= 1.00 {
				trailFloor = position.AvgPrice
			}
			if peakR >= 1.50 {
				trailFloor = position.AvgPrice + (riskPerShare * 0.50)
			}
			stopPrice = math.Max(stopPrice, trailFloor)
			stopPrice = math.Max(stopPrice, watermark-trailWidth)
		}
		reason = "trailing-stop"
	}
	return roundPrice(stopPrice), reason
}

// ProtectiveStop returns the active managed stop price and the exit reason it implies.
func ProtectiveStop(cfg config.TradingConfig, position domain.Position, highWatermark, currentPrice float64, at time.Time) (float64, string) {
	return protectiveStop(cfg, position, highWatermark, currentPrice, at)
}

func failedBreakoutPrice(cfg config.TradingConfig, position domain.Position) float64 {
	if position.RiskPerShare <= 0 {
		return 0
	}
	if domain.IsShort(position.Side) {
		return roundPrice(position.AvgPrice + (position.RiskPerShare * cfg.FailedBreakoutCutR))
	}
	return roundPrice(position.AvgPrice - (position.RiskPerShare * cfg.FailedBreakoutCutR))
}

// FailedBreakoutPrice returns the early-cut price used for non-follow-through setups.
func FailedBreakoutPrice(cfg config.TradingConfig, position domain.Position) float64 {
	return failedBreakoutPrice(cfg, position)
}

func roundPrice(price float64) float64 {
	return math.Round(price*100) / 100
}
