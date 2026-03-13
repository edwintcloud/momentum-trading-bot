package config

import "testing"

func TestTuneTradingConfigUsesConservativeSmallAccountProfile(t *testing.T) {
	cfg := TuneTradingConfig(DefaultTradingConfig(), 10_000, 200)

	if cfg.RiskPerTradePct != 0.005 {
		t.Fatalf("expected reduced risk per trade for small account, got %.4f", cfg.RiskPerTradePct)
	}
	if cfg.MaxOpenPositions != 2 {
		t.Fatalf("expected smaller open-position cap, got %d", cfg.MaxOpenPositions)
	}
	if cfg.MaxExposurePct != 0.30 {
		t.Fatalf("expected coherent exposure cap for two partial positions, got %.2f", cfg.MaxExposurePct)
	}
	if cfg.HydrationRequestsPerMin != 120 {
		t.Fatalf("expected bounded hydration budget, got %d", cfg.HydrationRequestsPerMin)
	}
}

func TestTuneTradingConfigUsesBrokerScaleForLargerAccount(t *testing.T) {
	cfg := TuneTradingConfig(DefaultTradingConfig(), 150_000, 10_000)

	if cfg.RiskPerTradePct != 0.01 {
		t.Fatalf("expected standard risk per trade for larger account, got %.4f", cfg.RiskPerTradePct)
	}
	if cfg.MaxExposurePct != 0.55 {
		t.Fatalf("expected exposure cap to stay aligned with risk-based sizing, got %.2f", cfg.MaxExposurePct)
	}
	if cfg.HydrationRequestsPerMin != 2400 {
		t.Fatalf("expected hydration budget cap to apply, got %d", cfg.HydrationRequestsPerMin)
	}
}
