package main

import (
	"fmt"
	"strings"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/optimizer"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
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

func buildRuntimeOptimizerStatus(active config.TradingConfig) runtime.OptimizerStatus {
	status := runtime.OptimizerStatus{
		ActiveProfileName:    active.StrategyProfileName,
		ActiveProfileVersion: active.StrategyProfileVersion,
	}
	artifactStatus, err := optimizer.LoadArtifactStatus(optimizer.DefaultArtifactDir)
	if err != nil {
		status.LastPaperValidationResult = "optimizer-artifacts-unavailable"
		return status
	}
	status.PendingProfileName = artifactStatus.PendingProfileName
	status.PendingProfileVersion = artifactStatus.PendingProfileVersion
	status.LastOptimizerRun = artifactStatus.LastOptimizerRun
	status.LastPaperValidationResult = artifactStatus.LastPaperValidationResult
	if status.LastPaperValidationResult == "" {
		status.LastPaperValidationResult = "no-pending-candidate"
	}
	if status.PendingProfileName == status.ActiveProfileName && status.PendingProfileVersion == status.ActiveProfileVersion {
		status.PendingProfileName = ""
		status.PendingProfileVersion = ""
	}
	return status
}
