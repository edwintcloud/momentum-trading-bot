package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LoadBacktestAlpacaConfig returns a lightweight AppConfig for backtest use
// that only requires ALPACA_API_KEY and ALPACA_API_SECRET.
func LoadBacktestAlpacaConfig(overrides map[string]string) (AppConfig, error) {
	get := func(key string) string {
		if overrides != nil {
			if v, ok := overrides[key]; ok {
				return v
			}
		}
		return strings.TrimSpace(os.Getenv(key))
	}

	cfg := AppConfig{
		AlpacaAPIKey:    get("ALPACA_API_KEY"),
		AlpacaAPISecret: get("ALPACA_API_SECRET"),
		AlpacaPaper:     true,
	}
	if v := get("ALPACA_PAPER"); v != "" {
		cfg.AlpacaPaper = v != "false" && v != "0"
	}

	if cfg.AlpacaAPIKey == "" {
		return cfg, fmt.Errorf("ALPACA_API_KEY is required")
	}
	if cfg.AlpacaAPISecret == "" {
		return cfg, fmt.Errorf("ALPACA_API_SECRET is required")
	}
	return cfg, nil
}

// ResolveTradingProfilePath returns the profile path, checking the env value
// first, then falling back to the bundled profile path.
func ResolveTradingProfilePath(envValue string) string {
	envValue = strings.TrimSpace(envValue)
	if envValue != "" {
		return envValue
	}
	// Try bundled path relative to binary
	_, callerFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Join(filepath.Dir(callerFile), "..", "..")
	bundled := filepath.Join(root, bundledTradingProfileRelPath)
	if _, err := os.Stat(bundled); err == nil {
		return bundled
	}
	// Try relative to cwd
	if _, err := os.Stat(bundledTradingProfileRelPath); err == nil {
		return bundledTradingProfileRelPath
	}
	return ""
}

// ApplyTradingProfile applies a TradingProfile's config onto a base TradingConfig.
func ApplyTradingProfile(base TradingConfig, profile TradingProfile) TradingConfig {
	cfg := profile.Config
	cfg.StartingCapital = base.StartingCapital
	cfg.StrategyProfileName = string(profile.Name)
	cfg.StrategyProfileVersion = profile.Version
	return cfg
}
