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
	profilePath := writeRootTestTradingProfile(t, config.TradingProfile{
		Name:       config.StrategyProfileHighConviction,
		Version:    "20260320-high-conviction",
		GeneratedAt: time.Date(2026, time.March, 20, 21, 0, 0, 0, time.UTC),
		AsOf:        time.Date(2026, time.March, 20, 20, 0, 0, 0, time.UTC),
		Config: config.TradingConfig{
			RiskPerTradePct:       0.01,
			MaxTradesPerDay:       8,
			MaxOpenPositions:      2,
			MinEntryScore:         16,
			MinOneMinuteReturnPct: 0.50,
			MinThreeMinuteReturnPct: 0.95,
			MinVolumeRate:           1.55,
			MaxPriceVsOpenPct:       24,
			EntryCooldownSec:        60,
			BreakEvenHoldMinutes:    4,
			BreakEvenMinR:           0.45,
			TrailActivationR:        0.60,
			TrailATRMultiplier:      1.25,
			TightTrailTriggerR:      1.00,
			TightTrailATRMultiplier: 0.55,
			ProfitTargetR:           1.00,
			ProfitTrailActivationR:  1.25,
			ProfitTrailPct:          0.02,
			FailedBreakoutCutR:      0.04,
			StructureConfirmR:       0.08,
		},
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
