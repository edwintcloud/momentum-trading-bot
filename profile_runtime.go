package main

import (
	"fmt"
	"strings"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

func applyConfiguredTradingProfile(base config.TradingConfig, profilePath string) (config.TradingConfig, string, error) {
	cfg := config.NormalizeStrategyProfile(base)
	if strings.TrimSpace(profilePath) == "" {
		return cfg, "", nil
	}
	profile, err := config.LoadTradingProfile(profilePath)
	if err != nil {
		return config.TradingConfig{}, "", err
	}
	cfg = config.ApplyTradingProfile(cfg, profile)
	cfg = config.NormalizeStrategyProfile(cfg)
	return cfg, fmt.Sprintf("%s@%s", profile.Name, profile.Version), nil
}
