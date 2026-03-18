package config

import "testing"

func TestLoadRejectsUnarmedLiveTrading(t *testing.T) {
	t.Setenv("ALPACA_API_KEY", "key")
	t.Setenv("ALPACA_API_SECRET", "secret")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/momentum_bot?sslmode=disable")
	t.Setenv("CONTROL_PLANE_AUTH_TOKEN", "secret")
	t.Setenv("ALPACA_PAPER", "false")
	t.Setenv("ALPACA_LIVE_TRADING_ENABLED", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("expected live trading to require explicit arming")
	}
}

func TestLoadUsesSimpleDefaults(t *testing.T) {
	t.Setenv("ALPACA_API_KEY", "key")
	t.Setenv("ALPACA_API_SECRET", "secret")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/momentum_bot?sslmode=disable")
	t.Setenv("CONTROL_PLANE_AUTH_TOKEN", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("expected localhost bind by default, got %q", cfg.HTTPAddr)
	}
	if !cfg.Alpaca.AutoSelectDataFeed {
		t.Fatal("expected alpaca feed auto-detection to stay enabled by default")
	}
	if !cfg.Alpaca.SubscribeAllBars {
		t.Fatal("expected wildcard subscriptions when no symbols are provided")
	}
}

func TestLoadRejectsMissingControlPlaneAuthToken(t *testing.T) {
	t.Setenv("ALPACA_API_KEY", "key")
	t.Setenv("ALPACA_API_SECRET", "secret")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/momentum_bot?sslmode=disable")

	_, err := Load()
	if err == nil {
		t.Fatal("expected control plane auth token to be required")
	}
}
