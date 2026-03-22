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

// AlpacaConfig contains credentials and endpoint settings for Alpaca.
type AlpacaConfig struct {
	APIKey              string
	APISecret           string
	Paper               bool
	LiveTradingEnabled  bool
	DataFeed            string
	AutoSelectDataFeed  bool
	TradingBaseURL      string
	MarketDataBaseURL   string
	MarketDataStreamURL string
	SubscribeAllBars    bool
	Symbols             []string
}

// LoadBacktestAlpacaConfig loads only the Alpaca settings needed for historical
// backtests, without requiring database or app runtime configuration.
func LoadBacktestAlpacaConfig(symbolOverrides []string) (AlpacaConfig, error) {
	paper := envBool("ALPACA_PAPER", true)
	tradingBaseURL := "https://api.alpaca.markets"
	if paper {
		tradingBaseURL = "https://paper-api.alpaca.markets"
	}

	symbols := symbolOverrides
	if len(symbols) == 0 {
		if raw := strings.TrimSpace(os.Getenv("ALPACA_SYMBOLS")); raw != "" {
			for _, s := range strings.Split(raw, ",") {
				s = strings.ToUpper(strings.TrimSpace(s))
				if s != "" {
					symbols = append(symbols, s)
				}
			}
		}
	}

	cfg := AlpacaConfig{
		APIKey:              strings.TrimSpace(os.Getenv("ALPACA_API_KEY")),
		APISecret:           strings.TrimSpace(os.Getenv("ALPACA_API_SECRET")),
		Paper:               paper,
		LiveTradingEnabled:  envBool("ALPACA_LIVE_TRADING_ENABLED", false),
		DataFeed:            "iex",
		AutoSelectDataFeed:  true,
		TradingBaseURL:      strings.TrimRight(tradingBaseURL, "/"),
		MarketDataBaseURL:   "https://data.alpaca.markets",
		MarketDataStreamURL: "wss://stream.data.alpaca.markets",
		SubscribeAllBars:    len(symbols) == 0,
		Symbols:             symbols,
	}
	if cfg.APIKey == "" {
		return AlpacaConfig{}, fmt.Errorf("ALPACA_API_KEY is required")
	}
	if cfg.APISecret == "" {
		return AlpacaConfig{}, fmt.Errorf("ALPACA_API_SECRET is required")
	}
	return cfg, nil
}
