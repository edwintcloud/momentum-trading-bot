package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTradingConfigFromFile_overridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trading.json")

	if err := os.WriteFile(path, []byte(`{"min_price": 5.0, "max_trades_per_day": 12}`), 0o600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	base := DefaultTradingConfig()
	got, err := LoadTradingConfigFromFile(path, base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.MinPrice != 5.0 {
		t.Errorf("expected MinPrice 5.0, got %v", got.MinPrice)
	}
	if got.MaxTradesPerDay != 12 {
		t.Errorf("expected MaxTradesPerDay 12, got %v", got.MaxTradesPerDay)
	}
	// Fields not in the JSON file should retain base values.
	if got.MaxOpenPositions != base.MaxOpenPositions {
		t.Errorf("expected MaxOpenPositions %v, got %v", base.MaxOpenPositions, got.MaxOpenPositions)
	}
}

func TestLoadTradingConfigFromFile_missingFile(t *testing.T) {
	_, err := LoadTradingConfigFromFile("/nonexistent/path/trading.json", DefaultTradingConfig())
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadTradingConfigFromFile_invalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{invalid}`), 0o600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	_, err := LoadTradingConfigFromFile(path, DefaultTradingConfig())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWriteTradingConfigToFile_roundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trading.json")

	original := DefaultTradingConfig()
	original.MinPrice = 7.5
	original.MaxTradesPerDay = 20

	if err := WriteTradingConfigToFile(path, original); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := LoadTradingConfigFromFile(path, TradingConfig{})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if loaded.MinPrice != original.MinPrice {
		t.Errorf("MinPrice: got %v, want %v", loaded.MinPrice, original.MinPrice)
	}
	if loaded.MaxTradesPerDay != original.MaxTradesPerDay {
		t.Errorf("MaxTradesPerDay: got %v, want %v", loaded.MaxTradesPerDay, original.MaxTradesPerDay)
	}
}

func TestLoad_appliesTradingConfigPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trading.json")
	if err := os.WriteFile(path, []byte(`{"min_price": 9.0}`), 0o600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	t.Setenv("ALPACA_API_KEY", "key")
	t.Setenv("ALPACA_API_SECRET", "secret")
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("TRADING_CONFIG_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Trading.MinPrice != 9.0 {
		t.Errorf("expected MinPrice 9.0 from JSON file, got %v", cfg.Trading.MinPrice)
	}
}
