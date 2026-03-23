package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadMomentumCameronProfile(t *testing.T) {
	_, callerFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(callerFile), "..", "..")
	profilePath := filepath.Join(root, "profiles", "momentum_cameron.json")

	profile, err := LoadTradingProfile(profilePath)
	if err != nil {
		t.Fatalf("failed to load momentum_cameron profile: %v", err)
	}

	if profile.Name != StrategyProfileMomentumCameron {
		t.Fatalf("expected profile name %q, got %q", StrategyProfileMomentumCameron, profile.Name)
	}
	if profile.Version != "1.0.0" {
		t.Fatalf("expected version 1.0.0, got %s", profile.Version)
	}
}

func TestMomentumCameronProfileMandatoryFields(t *testing.T) {
	_, callerFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(callerFile), "..", "..")
	profilePath := filepath.Join(root, "profiles", "momentum_cameron.json")

	profile, err := LoadTradingProfile(profilePath)
	if err != nil {
		t.Fatalf("failed to load profile: %v", err)
	}
	cfg := profile.Config

	// Scanner fields (Cameron's strict criteria)
	if cfg.MinPrice != 2.0 {
		t.Errorf("MinPrice: got %f, want 2.0", cfg.MinPrice)
	}
	if cfg.MaxPrice != 20.0 {
		t.Errorf("MaxPrice: got %f, want 20.0", cfg.MaxPrice)
	}
	if cfg.MinGapPercent != 5.0 {
		t.Errorf("MinGapPercent: got %f, want 5.0", cfg.MinGapPercent)
	}
	if cfg.MinRelativeVolume != 5.0 {
		t.Errorf("MinRelativeVolume: got %f, want 5.0", cfg.MinRelativeVolume)
	}
	if cfg.MinPremarketVolume != 200000 {
		t.Errorf("MinPremarketVolume: got %d, want 200000", cfg.MinPremarketVolume)
	}
	if cfg.MaxFloat != 20000000 {
		t.Errorf("MaxFloat: got %d, want 20000000", cfg.MaxFloat)
	}
	if cfg.MinFloat != 500000 {
		t.Errorf("MinFloat: got %d, want 500000", cfg.MinFloat)
	}
	if cfg.MinEntryScore != 3.5 {
		t.Errorf("MinEntryScore: got %f, want 3.5", cfg.MinEntryScore)
	}
	if cfg.MaxDistanceFromHighPct != 5.0 {
		t.Errorf("MaxDistanceFromHighPct: got %f, want 5.0", cfg.MaxDistanceFromHighPct)
	}
	if !cfg.VolumeOnPullbackEnabled {
		t.Error("VolumeOnPullbackEnabled: got false, want true")
	}

	// Risk management (conservative)
	if cfg.EnableShorts {
		t.Error("EnableShorts: got true, want false")
	}
	if cfg.RiskPerTradePct != 0.01 {
		t.Errorf("RiskPerTradePct: got %f, want 0.01", cfg.RiskPerTradePct)
	}
	if cfg.MaxTradesPerDay != 6 {
		t.Errorf("MaxTradesPerDay: got %d, want 6", cfg.MaxTradesPerDay)
	}
	if cfg.MaxOpenPositions != 3 {
		t.Errorf("MaxOpenPositions: got %d, want 3", cfg.MaxOpenPositions)
	}
	if cfg.MaxExposurePct != 0.6 {
		t.Errorf("MaxExposurePct: got %f, want 0.6", cfg.MaxExposurePct)
	}
	if cfg.MinRiskRewardRatio != 2.0 {
		t.Errorf("MinRiskRewardRatio: got %f, want 2.0", cfg.MinRiskRewardRatio)
	}
	if cfg.EntryDeadlineMinutesAfterOpen != 120 {
		t.Errorf("EntryDeadlineMinutesAfterOpen: got %d, want 120", cfg.EntryDeadlineMinutesAfterOpen)
	}

	// Strategy (tighter entries)
	if cfg.EntryStopATRMultiplier != 1.0 {
		t.Errorf("EntryStopATRMultiplier: got %f, want 1.0", cfg.EntryStopATRMultiplier)
	}
	if cfg.BreakEvenMinR != 0.75 {
		t.Errorf("BreakEvenMinR: got %f, want 0.75", cfg.BreakEvenMinR)
	}
	if cfg.MidDayScoreMultiplier != 2.0 {
		t.Errorf("MidDayScoreMultiplier: got %f, want 2.0", cfg.MidDayScoreMultiplier)
	}
	if !cfg.PartialExitsEnabled {
		t.Error("PartialExitsEnabled: got false, want true")
	}
	if cfg.PartialTrigger2Pct != 0.25 {
		t.Errorf("PartialTrigger2Pct: got %f, want 0.25", cfg.PartialTrigger2Pct)
	}
	if cfg.MeanReversionEnabled {
		t.Error("MeanReversionEnabled: got true, want false")
	}

	// Playbook exits (Cameron-style tighter targets)
	if cfg.PlaybookExits.Breakout.ProfitTargetR != 2.5 {
		t.Errorf("Breakout ProfitTargetR: got %f, want 2.5", cfg.PlaybookExits.Breakout.ProfitTargetR)
	}
	if cfg.PlaybookExits.Pullback.ProfitTargetR != 2.0 {
		t.Errorf("Pullback ProfitTargetR: got %f, want 2.0", cfg.PlaybookExits.Pullback.ProfitTargetR)
	}
	if cfg.PlaybookExits.Continuation.ProfitTargetR != 3.0 {
		t.Errorf("Continuation ProfitTargetR: got %f, want 3.0", cfg.PlaybookExits.Continuation.ProfitTargetR)
	}
	if cfg.PlaybookExits.Reversal.StagnationWindowMin != 8 {
		t.Errorf("Reversal StagnationWindowMin: got %d, want 8", cfg.PlaybookExits.Reversal.StagnationWindowMin)
	}

	// Alpha signals (momentum-aligned enabled)
	if !cfg.OFIEnabled {
		t.Error("OFIEnabled: got false, want true")
	}
	if !cfg.VPINEnabled {
		t.Error("VPINEnabled: got false, want true")
	}
	if !cfg.ORBEnabled {
		t.Error("ORBEnabled: got false, want true")
	}
	if !cfg.OBVDivergenceEnabled {
		t.Error("OBVDivergenceEnabled: got false, want true")
	}
	if cfg.DollarBarsEnabled {
		t.Error("DollarBarsEnabled: got true, want false")
	}
	if cfg.VolumeBarsEnabled {
		t.Error("VolumeBarsEnabled: got true, want false")
	}

	// Risk enhancements
	if !cfg.VaREnabled {
		t.Error("VaREnabled: got false, want true")
	}
	if !cfg.DynamicRiskBudgetEnabled {
		t.Error("DynamicRiskBudgetEnabled: got false, want true")
	}
	if cfg.GARCHEnabled {
		t.Error("GARCHEnabled: got true, want false")
	}
	if !cfg.CorrelationCheckEnabled {
		t.Error("CorrelationCheckEnabled: got false, want true")
	}
	if !cfg.ImpactModelEnabled {
		t.Error("ImpactModelEnabled: got false, want true")
	}

	// Portfolio construction (disabled for momentum)
	if cfg.MVOEnabled {
		t.Error("MVOEnabled: got true, want false")
	}
	if cfg.RiskParityEnabled {
		t.Error("RiskParityEnabled: got true, want false")
	}
	if cfg.FactorNeutralEnabled {
		t.Error("FactorNeutralEnabled: got true, want false")
	}
	if cfg.HHIEnabled {
		t.Error("HHIEnabled: got true, want false")
	}
	if cfg.LongShortBalancingEnabled {
		t.Error("LongShortBalancingEnabled: got true, want false")
	}

	// Execution (simple)
	if cfg.VWAPExecutionEnabled {
		t.Error("VWAPExecutionEnabled: got true, want false")
	}
	if cfg.TWAPExecutionEnabled {
		t.Error("TWAPExecutionEnabled: got true, want false")
	}
	if cfg.AdaptiveLimitEnabled {
		t.Error("AdaptiveLimitEnabled: got true, want false")
	}

	// ML (disabled)
	if cfg.MLScoringEnabled {
		t.Error("MLScoringEnabled: got true, want false")
	}
	if cfg.MetaLabelEnabled {
		t.Error("MetaLabelEnabled: got true, want false")
	}
}

func TestMomentumCameronProfileIsRegistered(t *testing.T) {
	if !IsSupportedStrategyProfile(StrategyProfileMomentumCameron) {
		t.Fatal("momentum_cameron is not in the supported profiles list")
	}
}

func TestLoadBundledDefaultProfile(t *testing.T) {
	profile, err := LoadBundledTradingProfile()
	if err != nil {
		t.Fatalf("failed to load bundled default profile: %v", err)
	}
	if profile.Name != StrategyProfileBaseline {
		t.Fatalf("expected profile name %q, got %q", StrategyProfileBaseline, profile.Name)
	}
}

func TestMomentumCameronProfileValidJSON(t *testing.T) {
	_, callerFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(callerFile), "..", "..")
	profilePath := filepath.Join(root, "profiles", "momentum_cameron.json")

	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify required top-level fields
	requiredFields := []string{"name", "version", "generatedAt", "asOf", "config", "promotion"}
	for _, field := range requiredFields {
		if _, ok := m[field]; !ok {
			t.Errorf("missing required top-level field: %s", field)
		}
	}
}

func TestLoadProfileRejectsUnsupportedName(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "bad.json")

	profile := TradingProfile{
		Name:    "nonexistent_profile",
		Version: "1.0.0",
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, data, 0644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadTradingProfile(profilePath)
	if err == nil {
		t.Fatal("expected error for unsupported profile name")
	}
}

func TestAllProfilesLoadable(t *testing.T) {
	_, callerFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(callerFile), "..", "..")

	profiles := []struct {
		path string
		name StrategyProfile
	}{
		{"profiles/default.json", StrategyProfileBaseline},
		{"profiles/momentum_cameron.json", StrategyProfileMomentumCameron},
	}

	for _, tc := range profiles {
		t.Run(string(tc.name), func(t *testing.T) {
			p, err := LoadTradingProfile(filepath.Join(root, tc.path))
			if err != nil {
				t.Fatalf("failed to load %s: %v", tc.path, err)
			}
			if p.Name != tc.name {
				t.Fatalf("expected name %q, got %q", tc.name, p.Name)
			}
		})
	}
}
