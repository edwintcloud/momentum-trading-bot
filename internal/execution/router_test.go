package execution

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func TestRouteOrder_LargeOrderUsesVWAP(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: true,
		VWAPMinOrderADVPct:   0.005,
		TWAPExecutionEnabled: true,
		TWAPSlices:           10,
		TWAPWindowSeconds:    300,
	}

	// 6000 shares / 1M ADV = 0.6% > 0.5% threshold
	decision := RouteOrder("AAPL", "buy", 6000, 150.0, 1000000, now, cfg)

	if decision.Method != ExecVWAP {
		t.Errorf("expected VWAP for large order, got %s", decision.Method)
	}
	if decision.VWAPSchedule == nil {
		t.Error("expected VWAPSchedule to be populated")
	}
	if decision.TWAPSchedule != nil {
		t.Error("expected TWAPSchedule to be nil for VWAP route")
	}
}

func TestRouteOrder_MediumOrderUsesTWAP(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: true,
		VWAPMinOrderADVPct:   0.005,
		TWAPExecutionEnabled: true,
		TWAPSlices:           10,
		TWAPWindowSeconds:    300,
	}

	// 1000 shares / 1M ADV = 0.1% < 0.5% — not large enough for VWAP.
	decision := RouteOrder("AAPL", "buy", 1000, 150.0, 1000000, now, cfg)

	if decision.Method != ExecTWAP {
		t.Errorf("expected TWAP for medium order, got %s", decision.Method)
	}
	if decision.TWAPSchedule == nil {
		t.Error("expected TWAPSchedule to be populated")
	}
}

func TestRouteOrder_SmallOrderDirect(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: false,
		TWAPExecutionEnabled: false,
	}

	decision := RouteOrder("AAPL", "buy", 100, 150.0, 1000000, now, cfg)

	if decision.Method != ExecDirect {
		t.Errorf("expected Direct for small order, got %s", decision.Method)
	}
}

func TestRouteOrder_DirectWithAdaptiveLimit(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled:          false,
		TWAPExecutionEnabled:          false,
		AdaptiveLimitEnabled:          true,
		AdaptiveLimitToleranceBps:     5.0,
		AdaptiveLimitWidenStepBps:     0.5,
		AdaptiveLimitWidenIntervalSec: 5,
		AdaptiveLimitMaxSlippageBps:   20.0,
	}

	decision := RouteOrder("AAPL", "buy", 100, 150.0, 1000000, now, cfg)

	if decision.Method != ExecDirect {
		t.Errorf("expected Direct, got %s", decision.Method)
	}
	if decision.AdaptiveLimit == nil {
		t.Error("expected AdaptiveLimit to be populated")
	}
}

func TestRouteOrder_VWAPWithAdaptiveLimit(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled:          true,
		VWAPMinOrderADVPct:            0.005,
		AdaptiveLimitEnabled:          true,
		AdaptiveLimitToleranceBps:     5.0,
		AdaptiveLimitWidenStepBps:     0.5,
		AdaptiveLimitWidenIntervalSec: 5,
		AdaptiveLimitMaxSlippageBps:   20.0,
	}

	// Large order for VWAP.
	decision := RouteOrder("AAPL", "buy", 10000, 150.0, 1000000, now, cfg)

	if decision.Method != ExecVWAP {
		t.Errorf("expected VWAP, got %s", decision.Method)
	}
	if decision.AdaptiveLimit == nil {
		t.Error("expected AdaptiveLimit to be populated alongside VWAP")
	}
}

func TestRouteOrder_AllDisabledGoesToDirect(t *testing.T) {
	loc := markethours.Location()
	now := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{}

	decision := RouteOrder("AAPL", "buy", 50000, 150.0, 1000000, now, cfg)

	if decision.Method != ExecDirect {
		t.Errorf("expected Direct when all algorithms disabled, got %s", decision.Method)
	}
}

func TestExecutionMethod_String(t *testing.T) {
	tests := []struct {
		method ExecutionMethod
		want   string
	}{
		{ExecDirect, "direct"},
		{ExecVWAP, "VWAP"},
		{ExecTWAP, "TWAP"},
	}
	for _, tt := range tests {
		if got := tt.method.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.method, got, tt.want)
		}
	}
}

func TestEstimateExecutionImpact_ReducedForAlgos(t *testing.T) {
	params := DefaultImpactParams(1000000, 0.02)
	directImpact := EstimateExecutionImpact(10000, 50.0, params, ExecDirect)
	vwapImpact := EstimateExecutionImpact(10000, 50.0, params, ExecVWAP)
	twapImpact := EstimateExecutionImpact(10000, 50.0, params, ExecTWAP)

	if vwapImpact >= directImpact {
		t.Errorf("VWAP impact %f should be less than direct %f", vwapImpact, directImpact)
	}
	if twapImpact >= directImpact {
		t.Errorf("TWAP impact %f should be less than direct %f", twapImpact, directImpact)
	}
	if vwapImpact >= twapImpact {
		t.Errorf("VWAP impact %f should be less than TWAP %f", vwapImpact, twapImpact)
	}
}
