package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadTradingConfigFromFile reads a JSON file at path and applies any non-zero
// values it contains as overrides onto base. Fields absent from the JSON file
// (or explicitly set to zero/false) are left at their base values.
func LoadTradingConfigFromFile(path string, base TradingConfig) (TradingConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return base, fmt.Errorf("reading trading config file %q: %w", path, err)
	}
	// Unmarshal into the base so that missing keys keep their default values.
	if err := json.Unmarshal(data, &base); err != nil {
		return base, fmt.Errorf("parsing trading config file %q: %w", path, err)
	}
	return base, nil
}

// WriteTradingConfigToFile serialises cfg as indented JSON and writes it to
// path. This is useful for generating a starting configuration file.
func WriteTradingConfigToFile(path string, cfg TradingConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("serialising trading config: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing trading config file %q: %w", path, err)
	}
	return nil
}
