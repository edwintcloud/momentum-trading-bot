package strategy

import (
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

// TradePlan defines the expected exit targets for a position.
type TradePlan struct {
	StopPrice      float64 `json:"stopPrice"`
	Target1R       float64 `json:"target1R"`
	Target2R       float64 `json:"target2R"`
	BreakEvenR     float64 `json:"breakEvenR"`
	TrailActivateR float64 `json:"trailActivateR"`
	TrailATRMult   float64 `json:"trailAtrMult"`
	MaxHoldMinutes int     `json:"maxHoldMinutes"`
}

// BuildTradePlan creates a trade plan from a signal and config.
func BuildTradePlan(signal domain.TradeSignal, cfg config.TradingConfig) TradePlan {
	plan := TradePlan{
		StopPrice:      signal.StopPrice,
		BreakEvenR:     cfg.BreakEvenMinR,
		TrailActivateR: cfg.TrailActivationR,
		TrailATRMult:   cfg.TrailATRMultiplier,
		MaxHoldMinutes: 390, // full trading day
	}

	if signal.RiskPerShare > 0 {
		if domain.IsLong(signal.PositionSide) {
			plan.Target1R = signal.Price + signal.RiskPerShare*cfg.TrailActivationR
			plan.Target2R = signal.Price + signal.RiskPerShare*cfg.ProfitTargetR
		} else {
			plan.Target1R = signal.Price - signal.RiskPerShare*cfg.TrailActivationR
			plan.Target2R = signal.Price - signal.RiskPerShare*cfg.ProfitTargetR
		}
	}

	return plan
}
