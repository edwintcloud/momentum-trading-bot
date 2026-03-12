package config

import "testing"

func TestLoadRejectsUnarmedLiveTrading(t *testing.T) {
	t.Setenv("ALPACA_API_KEY", "key")
	t.Setenv("ALPACA_API_SECRET", "secret")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/momentum_bot?sslmode=disable")
	t.Setenv("ALPACA_PAPER", "false")
	t.Setenv("ALPACA_LIVE_TRADING_ENABLED", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("expected live trading to require explicit arming")
	}
}
