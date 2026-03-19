package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
)

func TestApplyConfiguredTradingProfileRequiresExplicitSelection(t *testing.T) {
	base := config.TuneTradingConfig(config.DefaultTradingConfig(), 25_000, 600)
	profileConfig := base
	profileConfig.RiskPerTradePct = 0.01
	profileConfig.MaxTradesPerDay = 8
	profileConfig.MaxOpenPositions = 2
	profileConfig.MinEntryScore = 16
	profileConfig.MinOneMinuteReturnPct = 0.50
	profileConfig.MinThreeMinuteReturnPct = 0.95
	profileConfig.MinVolumeRate = 1.55
	profileConfig.MaxPriceVsOpenPct = 24
	profileConfig.EntryCooldownSec = 60
	profileConfig.BreakEvenHoldMinutes = 4
	profileConfig.BreakEvenMinR = 0.45
	profileConfig.TrailActivationR = 0.60
	profileConfig.TrailATRMultiplier = 1.25
	profileConfig.TightTrailTriggerR = 1.00
	profileConfig.TightTrailATRMultiplier = 0.55
	profileConfig.ProfitTargetR = 1.00
	profileConfig.FailedBreakoutCutR = 0.04
	profileConfig.StructureConfirmR = 0.08
	profilePath := writeRootTestTradingProfile(t, config.TradingProfile{
		Name:        config.StrategyProfileHighConviction,
		Version:     "20260320-high-conviction",
		GeneratedAt: time.Date(2026, time.March, 20, 21, 0, 0, 0, time.UTC),
		AsOf:        time.Date(2026, time.March, 20, 20, 0, 0, 0, time.UTC),
		Config:      profileConfig,
	})

	unchanged, label, err := applyConfiguredTradingProfile(base, "")
	if err != nil {
		t.Fatalf("expected empty selection to succeed, got %v", err)
	}
	if label != "" {
		t.Fatalf("expected no profile label without explicit selection, got %q", label)
	}
	if unchanged.StrategyProfileVersion != "built-in" {
		t.Fatalf("expected built-in profile version without explicit selection, got %q", unchanged.StrategyProfileVersion)
	}

	applied, label, err := applyConfiguredTradingProfile(base, profilePath)
	if err != nil {
		t.Fatalf("expected explicit profile selection to succeed, got %v", err)
	}
	if label == "" {
		t.Fatal("expected profile label after explicit selection")
	}
	if applied.StrategyProfileVersion != "20260320-high-conviction" {
		t.Fatalf("expected selected profile version, got %q", applied.StrategyProfileVersion)
	}
	if applied.RiskPerTradePct == unchanged.RiskPerTradePct {
		t.Fatal("expected explicit selection to change trading config")
	}
}

func writeRootTestTradingProfile(t *testing.T, profile config.TradingProfile) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
