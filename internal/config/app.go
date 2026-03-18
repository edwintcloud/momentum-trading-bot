package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// AppConfig holds runtime configuration loaded from environment variables.
type AppConfig struct {
	HTTPAddr                   string
	DatabaseURL                string
	ControlPlaneAuthToken      string
	SnapshotPersistIntervalSec int
	StartupTimeoutSec          int
	ShutdownTimeoutSec         int
	Trading                    TradingConfig
	Alpaca                     AlpacaConfig
}

// AlpacaConfig contains credentials and endpoint settings for Alpaca.
type AlpacaConfig struct {
	APIKey               string
	APISecret            string
	Paper                bool
	LiveTradingEnabled   bool
	DataFeed             string
	AutoSelectDataFeed   bool
	TradingBaseURL       string
	MarketDataBaseURL    string
	MarketDataStreamURL  string
	SubscribeAllBars     bool
	Symbols              []string
	OrderFillTimeoutSec  int
	OrderPollIntervalSec int
}

// LoadBacktestAlpacaConfig loads only the Alpaca settings needed for historical
// backtests, without requiring database or app runtime configuration.
func LoadBacktestAlpacaConfig(symbolOverrides []string) (AlpacaConfig, error) {
	_ = godotenv.Load()
	return loadAlpacaConfig(symbolOverrides)
}

// Load reads configuration from the process environment and .env when present.
func Load() (AppConfig, error) {
	_ = godotenv.Load()

	trading := DefaultTradingConfig()
	alpacaCfg, err := loadAlpacaConfig(nil)
	if err != nil {
		return AppConfig{}, err
	}

	cfg := AppConfig{
		HTTPAddr:                   ":8080",
		DatabaseURL:                strings.TrimSpace(os.Getenv("DATABASE_URL")),
		ControlPlaneAuthToken:      strings.TrimSpace(os.Getenv("CONTROL_PLANE_AUTH_TOKEN")),
		SnapshotPersistIntervalSec: 10,
		StartupTimeoutSec:          30,
		ShutdownTimeoutSec:         10,
		Trading:                    trading,
		Alpaca:                     alpacaCfg,
	}

	if cfg.Alpaca.APIKey == "" {
		return AppConfig{}, fmt.Errorf("ALPACA_API_KEY is required")
	}
	if cfg.Alpaca.APISecret == "" {
		return AppConfig{}, fmt.Errorf("ALPACA_API_SECRET is required")
	}
	if cfg.DatabaseURL == "" {
		return AppConfig{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.ControlPlaneAuthToken == "" {
		return AppConfig{}, fmt.Errorf("CONTROL_PLANE_AUTH_TOKEN is required")
	}
	if cfg.Alpaca.OrderFillTimeoutSec < 5 {
		return AppConfig{}, fmt.Errorf("default order fill timeout must be at least 5 seconds")
	}
	if cfg.Alpaca.OrderPollIntervalSec < 1 {
		return AppConfig{}, fmt.Errorf("default order poll interval must be at least 1 second")
	}
	if cfg.Trading.HydrationRequestsPerMin < 1 {
		return AppConfig{}, fmt.Errorf("default hydration budget must be at least 1 request per minute")
	}
	if cfg.Trading.HydrationRetrySec < 5 {
		return AppConfig{}, fmt.Errorf("default hydration retry must be at least 5 seconds")
	}
	if cfg.Trading.HydrationQueueSize < 32 {
		return AppConfig{}, fmt.Errorf("default hydration queue size must be at least 32")
	}
	if !cfg.Alpaca.Paper && !cfg.Alpaca.LiveTradingEnabled {
		return AppConfig{}, fmt.Errorf("live trading requires ALPACA_LIVE_TRADING_ENABLED=true")
	}
	return cfg, nil
}

func loadAlpacaConfig(symbolOverrides []string) (AlpacaConfig, error) {
	paper := getEnvBool("ALPACA_PAPER", true)
	tradingBaseURL := "https://api.alpaca.markets"
	if paper {
		tradingBaseURL = "https://paper-api.alpaca.markets"
	}

	symbols := parseCSVEnv("ALPACA_SYMBOLS")
	if len(symbolOverrides) > 0 {
		symbols = normalizeSymbols(symbolOverrides)
	}

	cfg := AlpacaConfig{
		APIKey:               strings.TrimSpace(os.Getenv("ALPACA_API_KEY")),
		APISecret:            strings.TrimSpace(os.Getenv("ALPACA_API_SECRET")),
		Paper:                paper,
		LiveTradingEnabled:   getEnvBool("ALPACA_LIVE_TRADING_ENABLED", false),
		DataFeed:             "iex",
		AutoSelectDataFeed:   true,
		TradingBaseURL:       strings.TrimRight(tradingBaseURL, "/"),
		MarketDataBaseURL:    "https://data.alpaca.markets",
		MarketDataStreamURL:  "wss://stream.data.alpaca.markets",
		SubscribeAllBars:     len(symbols) == 0,
		Symbols:              symbols,
		OrderFillTimeoutSec:  20,
		OrderPollIntervalSec: 1,
	}
	if cfg.APIKey == "" {
		return AlpacaConfig{}, fmt.Errorf("ALPACA_API_KEY is required")
	}
	if cfg.APISecret == "" {
		return AlpacaConfig{}, fmt.Errorf("ALPACA_API_SECRET is required")
	}
	return cfg, nil
}

func getEnvString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getEnvBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseCSVEnv(key string) []string {
	return normalizeSymbols(strings.Split(strings.TrimSpace(os.Getenv(key)), ","))
}

func normalizeSymbols(parts []string) []string {
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.ToUpper(strings.TrimSpace(part))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
