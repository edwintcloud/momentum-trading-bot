package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// AppConfig holds environment-driven application settings.
type AppConfig struct {
	AlpacaAPIKey             string
	AlpacaAPISecret          string
	AlpacaPaper              bool
	AlpacaLiveTradingEnabled bool
	AlpacaSymbols            []string
	DatabaseURL              string
	ControlPlaneAuthToken    string
	ListenAddr               string
	TradingProfilePath       string
}

// LoadAppConfig reads configuration from environment variables.
func LoadAppConfig() (AppConfig, error) {
	cfg := AppConfig{
		AlpacaAPIKey:             strings.TrimSpace(os.Getenv("ALPACA_API_KEY")),
		AlpacaAPISecret:          strings.TrimSpace(os.Getenv("ALPACA_API_SECRET")),
		AlpacaPaper:              envBool("ALPACA_PAPER", true),
		AlpacaLiveTradingEnabled: envBool("ALPACA_LIVE_TRADING_ENABLED", false),
		DatabaseURL:              strings.TrimSpace(os.Getenv("DATABASE_URL")),
		ControlPlaneAuthToken:    strings.TrimSpace(os.Getenv("CONTROL_PLANE_AUTH_TOKEN")),
		ListenAddr:               envString("LISTEN_ADDR", ":8080"),
		TradingProfilePath:       strings.TrimSpace(os.Getenv("TRADING_PROFILE_PATH")),
	}

	if symbols := strings.TrimSpace(os.Getenv("ALPACA_SYMBOLS")); symbols != "" {
		for _, s := range strings.Split(symbols, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				cfg.AlpacaSymbols = append(cfg.AlpacaSymbols, strings.ToUpper(s))
			}
		}
	}

	if cfg.AlpacaAPIKey == "" {
		return cfg, fmt.Errorf("ALPACA_API_KEY is required")
	}
	if cfg.AlpacaAPISecret == "" {
		return cfg, fmt.Errorf("ALPACA_API_SECRET is required")
	}
	if cfg.ControlPlaneAuthToken == "" {
		return cfg, fmt.Errorf("CONTROL_PLANE_AUTH_TOKEN is required")
	}
	if !cfg.AlpacaPaper && !cfg.AlpacaLiveTradingEnabled {
		return cfg, fmt.Errorf("live trading requires ALPACA_LIVE_TRADING_ENABLED=true")
	}

	log.Printf("config: paper=%t live_enabled=%t symbols=%d",
		cfg.AlpacaPaper, cfg.AlpacaLiveTradingEnabled, len(cfg.AlpacaSymbols))

	return cfg, nil
}

func envBool(key string, fallback bool) bool {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		return fallback
	}
	return b
}

func envString(key, fallback string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	return val
}
