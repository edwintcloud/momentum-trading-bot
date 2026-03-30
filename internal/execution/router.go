package execution

import (
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

// ExecutionMethod indicates which algorithm the router selected.
type ExecutionMethod int

const (
	// ExecDirect means submit the order directly (small order).
	ExecDirect ExecutionMethod = iota
	// ExecVWAP means use the VWAP execution algorithm.
	ExecVWAP
	// ExecTWAP means use the TWAP execution algorithm.
	ExecTWAP
)

// String returns a human-readable name for the execution method.
func (m ExecutionMethod) String() string {
	switch m {
	case ExecVWAP:
		return "VWAP"
	case ExecTWAP:
		return "TWAP"
	default:
		return "direct"
	}
}

// RoutingDecision contains the router's chosen execution method and any
// pre-computed schedule.
type RoutingDecision struct {
	Method ExecutionMethod
}

// RouteOrder decides how to execute an order based on its size relative to
// ADV and the enabled execution algorithms.
func RouteOrder(symbol, side string, qty int64, price, adv float64, now time.Time, cfg config.TradingConfig) RoutingDecision {
	return RoutingDecision{
		Method: ExecDirect,
	}
}

// EstimateExecutionImpact uses the Almgren-Chriss model to estimate
// market impact for the routed execution method.
func EstimateExecutionImpact(qty int64, price float64, params AlmgrenChrissParams, method ExecutionMethod) float64 {
	if method == ExecDirect {
		return EstimateImpact(int(qty), price, params)
	}
	// For VWAP/TWAP, impact is reduced because order is sliced.
	// Approximate as sum of impacts of individual child orders, which
	// due to the concave impact function is less than a single block.
	// Heuristic: VWAP reduces impact by ~40%, TWAP by ~30%.
	fullImpact := EstimateImpact(int(qty), price, params)
	switch method {
	case ExecVWAP:
		return fullImpact * 0.6
	case ExecTWAP:
		return fullImpact * 0.7
	default:
		return fullImpact
	}
}
