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
	AppEnv                     string
	HTTPAddr                   string
	DatabaseURL                string
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

// Load reads configuration from the process environment and .env when present.
func Load() (AppConfig, error) {
	_ = godotenv.Load()

	trading := DefaultTradingConfig()
	trading.StartingCapital = getEnvFloat("STARTING_CAPITAL", trading.StartingCapital)
	trading.RiskPerTradePct = getEnvFloat("RISK_PER_TRADE_PCT", trading.RiskPerTradePct)
	trading.DailyLossLimitPct = getEnvFloat("DAILY_LOSS_LIMIT_PCT", trading.DailyLossLimitPct)
	trading.MaxTradesPerDay = getEnvInt("MAX_TRADES_PER_DAY", trading.MaxTradesPerDay)
	trading.MaxOpenPositions = getEnvInt("MAX_OPEN_POSITIONS", trading.MaxOpenPositions)
	trading.MaxExposurePct = getEnvFloat("MAX_EXPOSURE_PCT", trading.MaxExposurePct)
	trading.StopLossPct = getEnvFloat("STOP_LOSS_PCT", trading.StopLossPct)
	trading.ProfitTargetPct = getEnvFloat("PROFIT_TARGET_PCT", trading.ProfitTargetPct)
	trading.TrailingStopPct = getEnvFloat("TRAILING_STOP_PCT", trading.TrailingStopPct)
	trading.EntryCooldownSec = getEnvInt("ENTRY_COOLDOWN_SEC", trading.EntryCooldownSec)
	trading.ExitCooldownSec = getEnvInt("EXIT_COOLDOWN_SEC", trading.ExitCooldownSec)
	trading.ScannerWorkers = getEnvInt("SCANNER_WORKERS", trading.ScannerWorkers)
	trading.MinPrice = getEnvFloat("SCANNER_MIN_PRICE", trading.MinPrice)
	trading.MinGapPercent = getEnvFloat("SCANNER_MIN_GAP_PCT", trading.MinGapPercent)
	trading.MinRelativeVolume = getEnvFloat("SCANNER_MIN_RELATIVE_VOLUME", trading.MinRelativeVolume)
	trading.MinPremarketVolume = int64(getEnvInt("SCANNER_MIN_PREMARKET_VOLUME", int(trading.MinPremarketVolume)))
	trading.HydrationRequestsPerMin = getEnvInt("MARKET_DATA_HYDRATION_REQUESTS_PER_MIN", trading.HydrationRequestsPerMin)
	trading.HydrationRetrySec = getEnvInt("MARKET_DATA_HYDRATION_RETRY_SEC", trading.HydrationRetrySec)
	trading.HydrationQueueSize = getEnvInt("MARKET_DATA_HYDRATION_QUEUE_SIZE", trading.HydrationQueueSize)
	trading.LimitOrderSlippageDollars = getEnvFloat("LIMIT_ORDER_SLIPPAGE_DOLLARS", trading.LimitOrderSlippageDollars)

	paper := getEnvBool("ALPACA_PAPER", true)
	rawDataFeed := strings.TrimSpace(os.Getenv("ALPACA_DATA_FEED"))
	tradingBaseURL := getEnvString("ALPACA_TRADING_BASE_URL", "")
	if tradingBaseURL == "" {
		if paper {
			tradingBaseURL = "https://paper-api.alpaca.markets"
		} else {
			tradingBaseURL = "https://api.alpaca.markets"
		}
	}

	cfg := AppConfig{
		AppEnv:                     getEnvString("APP_ENV", "production"),
		HTTPAddr:                   getEnvString("HTTP_ADDR", ":8080"),
		DatabaseURL:                strings.TrimSpace(os.Getenv("DATABASE_URL")),
		SnapshotPersistIntervalSec: getEnvInt("SNAPSHOT_PERSIST_INTERVAL_SEC", 10),
		StartupTimeoutSec:          getEnvInt("STARTUP_TIMEOUT_SEC", 30),
		ShutdownTimeoutSec:         getEnvInt("SHUTDOWN_TIMEOUT_SEC", 10),
		Trading:                    trading,
		Alpaca: AlpacaConfig{
			APIKey:               strings.TrimSpace(os.Getenv("ALPACA_API_KEY")),
			APISecret:            strings.TrimSpace(os.Getenv("ALPACA_API_SECRET")),
			Paper:                paper,
			LiveTradingEnabled:   getEnvBool("ALPACA_LIVE_TRADING_ENABLED", false),
			DataFeed:             defaultDataFeed(rawDataFeed),
			AutoSelectDataFeed:   rawDataFeed == "" || strings.EqualFold(rawDataFeed, "auto"),
			TradingBaseURL:       strings.TrimRight(tradingBaseURL, "/"),
			MarketDataBaseURL:    strings.TrimRight(getEnvString("ALPACA_MARKET_DATA_BASE_URL", "https://data.alpaca.markets"), "/"),
			MarketDataStreamURL:  strings.TrimRight(getEnvString("ALPACA_MARKET_DATA_STREAM_URL", "wss://stream.data.alpaca.markets"), "/"),
			SubscribeAllBars:     getEnvBool("ALPACA_STREAM_ALL", true),
			Symbols:              parseCSVEnv("ALPACA_SYMBOLS"),
			OrderFillTimeoutSec:  getEnvInt("ALPACA_ORDER_FILL_TIMEOUT_SEC", 20),
			OrderPollIntervalSec: getEnvInt("ALPACA_ORDER_POLL_INTERVAL_SEC", 1),
		},
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
	if cfg.StartupTimeoutSec < 5 {
		return AppConfig{}, fmt.Errorf("STARTUP_TIMEOUT_SEC must be at least 5")
	}
	if cfg.ShutdownTimeoutSec < 3 {
		return AppConfig{}, fmt.Errorf("SHUTDOWN_TIMEOUT_SEC must be at least 3")
	}
	if cfg.SnapshotPersistIntervalSec < 1 {
		return AppConfig{}, fmt.Errorf("SNAPSHOT_PERSIST_INTERVAL_SEC must be at least 1")
	}
	if cfg.Alpaca.OrderFillTimeoutSec < 5 {
		return AppConfig{}, fmt.Errorf("ALPACA_ORDER_FILL_TIMEOUT_SEC must be at least 5")
	}
	if cfg.Alpaca.OrderPollIntervalSec < 1 {
		return AppConfig{}, fmt.Errorf("ALPACA_ORDER_POLL_INTERVAL_SEC must be at least 1")
	}
	if cfg.Trading.HydrationRequestsPerMin < 1 {
		return AppConfig{}, fmt.Errorf("MARKET_DATA_HYDRATION_REQUESTS_PER_MIN must be at least 1")
	}
	if cfg.Trading.HydrationRetrySec < 5 {
		return AppConfig{}, fmt.Errorf("MARKET_DATA_HYDRATION_RETRY_SEC must be at least 5")
	}
	if cfg.Trading.HydrationQueueSize < 32 {
		return AppConfig{}, fmt.Errorf("MARKET_DATA_HYDRATION_QUEUE_SIZE must be at least 32")
	}
	if !cfg.Alpaca.Paper && !cfg.Alpaca.LiveTradingEnabled {
		return AppConfig{}, fmt.Errorf("live trading requires ALPACA_LIVE_TRADING_ENABLED=true")
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

func getEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvFloat(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
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
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.ToUpper(strings.TrimSpace(part))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func defaultDataFeed(value string) string {
	if strings.EqualFold(value, "") || strings.EqualFold(value, "auto") {
		return "iex"
	}
	return strings.ToLower(strings.TrimSpace(value))
}
