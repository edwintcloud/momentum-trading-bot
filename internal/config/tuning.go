package config

const defaultStartingCapital = 25000.0

// TuneTradingConfig adjusts parameters based on capital and broker PnL.
func TuneTradingConfig(base TradingConfig, equity float64, brokerDayPnL float64) TradingConfig {
	cfg := base
	if equity <= 0 {
		equity = defaultStartingCapital
	}
	cfg.StartingCapital = equity

	return cfg
}