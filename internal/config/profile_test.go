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
	profileConfig := base
	profileConfig.StartingCapital = 999
	profileConfig.EnableShorts = false
	profileConfig.RiskPerTradePct = 0.01
	profileConfig.DailyLossLimitPct = 0.11
	profileConfig.MaxTradesPerDay = 10
	profileConfig.MaxOpenPositions = 2
	profileConfig.StopLossPct = 0.03
	profileConfig.EntryCooldownSec = 45
	profileConfig.ExitCooldownSec = 9
	profileConfig.MinEntryScore = 15
	profileConfig.ShortMinEntryScore = 0
	profileConfig.MinOneMinuteReturnPct = 0.45
	profileConfig.MinThreeMinuteReturnPct = 0.90
	profileConfig.MinVolumeRate = 1.45
	profileConfig.MaxPriceVsOpenPct = 22
	profileConfig.BreakoutFailureWindowMin = 6
	profileConfig.StagnationWindowMin = 4
	profileConfig.StagnationMinPeakPct = 0.011
	profileConfig.ScannerWorkers = 7
	profileConfig.MinPrice = 5.25
	profileConfig.MaxPrice = 12.50
	profileConfig.MinGapPercent = 2.25
	profileConfig.MinRelativeVolume = 4.75
	profileConfig.MinPremarketVolume = 125_000
	profileConfig.ScannerMinPriceVsOpenPctFloor = 1.75
	profileConfig.ScannerMinPriceVsOpenGapMultiplier = 0.30
	profileConfig.ScannerMinSetupVolumeRateOffset = 0
	profileConfig.ScannerMinSetupRelativeVolumeExtra = 0
	profileConfig.ScannerVWAPTolerancePct = 0
	profileConfig.ScannerConsolidationATRMultiplier = 1.50
	profileConfig.ScannerConsolidationMaxPct = 3.25
	profileConfig.ScannerPullbackDepthMinATRMultiplier = 0.20
	profileConfig.ScannerPullbackDepthMinPct = 0.25
	profileConfig.ScannerPullbackDepthMaxATRMultiplier = 1.90
	profileConfig.ScannerPullbackDepthMaxPct = 5.50
	profileConfig.ScannerRenewedVolumeRateMin = 0.95
	profileConfig.HydrationRetrySec = 123
	profileConfig.HydrationQueueSize = 321
	profileConfig.LimitOrderSlippageDollars = 0.05
	profileConfig.EntryATRPercentFallback = 0.03
	profileConfig.EntryStopATRMultiplier = 1.50
	profileConfig.MaxRiskATRMultiplier = 3.00
	profileConfig.BreakEvenHoldMinutes = 3
	profileConfig.BreakEvenMinR = 0.40
	profileConfig.TrailActivationR = 0.55
	profileConfig.TrailATRMultiplier = 1.10
	profileConfig.TightTrailTriggerR = 0.95
	profileConfig.TightTrailATRMultiplier = 0.50
	profileConfig.ProfitTargetR = 1.00
	profileConfig.FailedBreakoutCutR = 0.04
	profileConfig.StructureConfirmR = 0
	profileConfig.ShortPeakExtensionMinPct = 10
	profileConfig.ShortVWAPBreakMinPct = 0
	profileConfig.ShortStopATRMultiplier = 1.10
	profile := TradingProfile{
		Name:    StrategyProfileContinuation,
		Version: "20260320-continuation",
		Config:  profileConfig,
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
	if got.EnableShorts {
		t.Fatal("expected profile to override enable_shorts")
	}
	if got.MinPrice != 5.25 || got.MaxPrice != 12.50 {
		t.Fatalf("expected profile price bounds to apply, got min=%.2f max=%.2f", got.MinPrice, got.MaxPrice)
	}
	if got.MinRelativeVolume != 4.75 || got.MinPremarketVolume != 125_000 {
		t.Fatalf("expected scanner liquidity thresholds to apply, got relvol=%.2f premarket=%d", got.MinRelativeVolume, got.MinPremarketVolume)
	}
	if got.ExitCooldownSec != 9 || got.ScannerWorkers != 7 {
		t.Fatalf("expected static timing/scanner overrides to apply, got exit_cooldown=%d scanner_workers=%d", got.ExitCooldownSec, got.ScannerWorkers)
	}
	if got.ScannerMinSetupVolumeRateOffset != 0 || got.ScannerVWAPTolerancePct != 0 || got.ShortVWAPBreakMinPct != 0 {
		t.Fatalf("expected explicit zero-valued overrides to apply, got volume_offset=%.2f vwap_tol=%.2f short_vwap=%.2f", got.ScannerMinSetupVolumeRateOffset, got.ScannerVWAPTolerancePct, got.ShortVWAPBreakMinPct)
	}
	if got.HydrationRetrySec != 123 || got.HydrationQueueSize != 321 {
		t.Fatalf("expected hydration tuning overrides to apply, got retry=%d queue=%d", got.HydrationRetrySec, got.HydrationQueueSize)
	}
	if got.LimitOrderSlippageDollars != 0.05 || got.EntryATRPercentFallback != 0.03 || got.EntryStopATRMultiplier != 1.50 || got.MaxRiskATRMultiplier != 3.00 {
		t.Fatalf("expected trade-plan overrides to apply, got slippage=%.2f atr_fallback=%.2f stop_atr=%.2f max_risk_atr=%.2f", got.LimitOrderSlippageDollars, got.EntryATRPercentFallback, got.EntryStopATRMultiplier, got.MaxRiskATRMultiplier)
	}
	if got.HydrationRequestsPerMin != base.HydrationRequestsPerMin {
		t.Fatalf("expected broker/capability-derived hydration budget %d to survive profile application, got %d", base.HydrationRequestsPerMin, got.HydrationRequestsPerMin)
	}
}

func TestDefaultTradingProfilePathFindsBundledProfile(t *testing.T) {
	path := DefaultTradingProfilePath()
	if path == "" {
		t.Fatal("expected bundled trading profile path")
	}
	_, err := LoadTradingProfile(path)
	if err != nil {
		t.Fatalf("expected bundled trading profile to load, got %v", err)
	}
}

func TestLocateBundledTradingProfilePathSearchesParentDirectories(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileDir, "default.json")
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
