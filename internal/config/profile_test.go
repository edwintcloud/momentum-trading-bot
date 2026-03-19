package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAcceptsTradingProfilePath(t *testing.T) {
	t.Setenv("ALPACA_API_KEY", "key")
	t.Setenv("ALPACA_API_SECRET", "secret")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/momentum_bot?sslmode=disable")
	t.Setenv("CONTROL_PLANE_AUTH_TOKEN", "secret")

	path := writeTestTradingProfile(t, TradingProfile{
		Name:        StrategyProfileHighConviction,
		Version:     "20260320-high-conviction",
		GeneratedAt: time.Date(2026, time.March, 20, 21, 0, 0, 0, time.UTC),
		AsOf:        time.Date(2026, time.March, 20, 20, 0, 0, 0, time.UTC),
		Config: TradingConfig{
			RiskPerTradePct:         0.01,
			MaxTradesPerDay:         8,
			MaxOpenPositions:        2,
			MinEntryScore:           16,
			MinOneMinuteReturnPct:   0.55,
			MinThreeMinuteReturnPct: 1.00,
			MinVolumeRate:           1.55,
			MaxPriceVsOpenPct:       24,
			EntryCooldownSec:        60,
			BreakEvenHoldMinutes:    4,
			BreakEvenMinR:           0.45,
			TrailActivationR:        0.60,
			TrailATRMultiplier:      1.20,
			TightTrailTriggerR:      1.00,
			TightTrailATRMultiplier: 0.55,
			ProfitTargetR:           1.00,
			FailedBreakoutCutR:      0.04,
			StructureConfirmR:       0.08,
		},
	})
	t.Setenv("TRADING_PROFILE_PATH", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected config load with trading profile, got %v", err)
	}
	if cfg.TradingProfilePath != path {
		t.Fatalf("expected trading profile path %q, got %q", path, cfg.TradingProfilePath)
	}
}

func TestApplyTradingProfilePreservesBrokerTunedCapital(t *testing.T) {
	base := TuneTradingConfig(DefaultTradingConfig(), 42_500, 600)
	profile := TradingProfile{
		Name:    StrategyProfileContinuation,
		Version: "20260320-continuation",
		Config: TradingConfig{
			StartingCapital:         999,
			RiskPerTradePct:         0.01,
			MaxTradesPerDay:         10,
			MaxOpenPositions:        2,
			MinEntryScore:           15,
			MinOneMinuteReturnPct:   0.45,
			MinThreeMinuteReturnPct: 0.90,
			MinVolumeRate:           1.45,
			MaxPriceVsOpenPct:       22,
			EntryCooldownSec:        45,
			BreakEvenHoldMinutes:    3,
			BreakEvenMinR:           0.40,
			TrailActivationR:        0.55,
			TrailATRMultiplier:      1.10,
			TightTrailTriggerR:      0.95,
			TightTrailATRMultiplier: 0.50,
			ProfitTargetR:           1.00,
			FailedBreakoutCutR:      0.04,
			StructureConfirmR:       0.10,
		},
	}

	got := ApplyTradingProfile(base, profile)
	if got.StartingCapital != base.StartingCapital {
		t.Fatalf("expected broker-tuned starting capital %.2f to survive profile application, got %.2f", base.StartingCapital, got.StartingCapital)
	}
	if got.StrategyProfileName != string(StrategyProfileContinuation) {
		t.Fatalf("expected continuation profile name, got %q", got.StrategyProfileName)
	}
	if got.StrategyProfileVersion != profile.Version {
		t.Fatalf("expected profile version %q, got %q", profile.Version, got.StrategyProfileVersion)
	}
	if got.MaxTradesPerDay != 10 || got.MaxOpenPositions != 2 {
		t.Fatalf("expected whitelisted overrides to apply, got %+v", got)
	}
}

func TestDefaultTradingProfilePathFindsBundledProfile(t *testing.T) {
	path := DefaultTradingProfilePath()
	if path == "" {
		t.Fatal("expected bundled trading profile path")
	}
	profile, err := LoadTradingProfile(path)
	if err != nil {
		t.Fatalf("expected bundled trading profile to load, got %v", err)
	}
	if profile.Version != "20260122-high_conviction_breakout" {
		t.Fatalf("expected bundled profile version to remain pinned, got %q", profile.Version)
	}
}

func TestLocateBundledTradingProfilePathSearchesParentDirectories(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileDir, "20260122-high_conviction_breakout.json")
	raw, err := json.Marshal(TradingProfile{
		Name:    StrategyProfileHighConviction,
		Version: "test-bundled-profile",
		Config: TradingConfig{
			RiskPerTradePct: 0.01,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	deepChild := filepath.Join(root, "var", "run", "bot")
	if err := os.MkdirAll(deepChild, 0o755); err != nil {
		t.Fatal(err)
	}

	got := locateBundledTradingProfilePath(deepChild)
	if got != profilePath {
		t.Fatalf("expected upward profile lookup to find %q, got %q", profilePath, got)
	}
}

func writeTestTradingProfile(t *testing.T, profile TradingProfile) string {
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
