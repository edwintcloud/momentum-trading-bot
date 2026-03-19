package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StrategyProfile names a bounded family of strategy behavior that the weekly
// optimizer can search over.
type StrategyProfile string

const (
	StrategyProfileBaseline       StrategyProfile = "baseline_breakout"
	StrategyProfileHighConviction StrategyProfile = "high_conviction_breakout"
	StrategyProfileContinuation   StrategyProfile = "continuation_breakout"
)

const bundledTradingProfileRelPath = "profiles/20260122-high_conviction_breakout.json"

var supportedStrategyProfiles = map[StrategyProfile]struct{}{
	StrategyProfileBaseline:       {},
	StrategyProfileHighConviction: {},
	StrategyProfileContinuation:   {},
}

// PromotionDecision records how a candidate profile should move through paper
// validation before an operator explicitly promotes it.
type PromotionDecision struct {
	DeploymentMode            string    `json:"deploymentMode"`
	Status                    string    `json:"status"`
	LastPaperValidationResult string    `json:"lastPaperValidationResult"`
	LastPaperValidationAt     time.Time `json:"lastPaperValidationAt,omitempty"`
	ApprovedAt                time.Time `json:"approvedAt,omitempty"`
	ApprovedBy                string    `json:"approvedBy,omitempty"`
	Notes                     string    `json:"notes,omitempty"`
}

// TradingProfile is a versioned optimizer artifact that can be selected for
// paper or live startup via TRADING_PROFILE_PATH.
type TradingProfile struct {
	Name             StrategyProfile   `json:"name"`
	Version          string            `json:"version"`
	GeneratedAt      time.Time         `json:"generatedAt"`
	AsOf             time.Time         `json:"asOf"`
	SourceReportPath string            `json:"sourceReportPath,omitempty"`
	Config           TradingConfig     `json:"config"`
	Promotion        PromotionDecision `json:"promotion"`
}

// LoadTradingProfile reads a versioned profile artifact from disk.
func LoadTradingProfile(path string) (TradingProfile, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return TradingProfile{}, fmt.Errorf("trading profile path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return TradingProfile{}, err
	}
	var profile TradingProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return TradingProfile{}, fmt.Errorf("decode trading profile %s: %w", path, err)
	}
	if strings.TrimSpace(string(profile.Name)) == "" {
		return TradingProfile{}, fmt.Errorf("trading profile %s is missing name", path)
	}
	if !IsSupportedStrategyProfile(profile.Name) {
		return TradingProfile{}, fmt.Errorf("trading profile %s has unsupported name %q", path, profile.Name)
	}
	if strings.TrimSpace(profile.Version) == "" {
		return TradingProfile{}, fmt.Errorf("trading profile %s is missing version", path)
	}
	profile.Name = StrategyProfile(strings.TrimSpace(string(profile.Name)))
	profile.Version = strings.TrimSpace(profile.Version)
	profile.SourceReportPath = strings.TrimSpace(profile.SourceReportPath)
	return profile, nil
}

// ResolveTradingProfilePath returns an explicit profile path when one is
// provided, otherwise it falls back to the bundled repo profile when present.
func ResolveTradingProfilePath(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return DefaultTradingProfilePath()
}

// DefaultTradingProfilePath returns the bundled repo profile path when the
// process is running somewhere inside the repository tree.
func DefaultTradingProfilePath() string {
	searchRoots := make([]string, 0, 2)
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		searchRoots = append(searchRoots, wd)
	}
	if executablePath, err := os.Executable(); err == nil && strings.TrimSpace(executablePath) != "" {
		searchRoots = append(searchRoots, filepath.Dir(executablePath))
	}
	return locateBundledTradingProfilePath(searchRoots...)
}

func locateBundledTradingProfilePath(searchRoots ...string) string {
	seen := make(map[string]struct{}, len(searchRoots))
	for _, root := range searchRoots {
		path := searchUpwardsForFile(root, bundledTradingProfileRelPath)
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		return cleaned
	}
	return ""
}

func searchUpwardsForFile(start, relativePath string) string {
	if strings.TrimSpace(start) == "" {
		return ""
	}
	current := filepath.Clean(start)
	for {
		candidate := filepath.Join(current, relativePath)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// ApplyTradingProfile applies whitelisted strategy and risk overrides after
// broker-aware tuning so profile promotion stays reproducible without
// clobbering startup-derived account values.
func ApplyTradingProfile(base TradingConfig, profile TradingProfile) TradingConfig {
	cfg := base
	overrides := profile.Config

	cfg.StrategyProfileName = string(profile.Name)
	cfg.StrategyProfileVersion = profile.Version
	cfg.RiskPerTradePct = overrides.RiskPerTradePct
	cfg.MaxTradesPerDay = overrides.MaxTradesPerDay
	cfg.MaxOpenPositions = overrides.MaxOpenPositions
	cfg.MinEntryScore = overrides.MinEntryScore
	cfg.MinOneMinuteReturnPct = overrides.MinOneMinuteReturnPct
	cfg.MinThreeMinuteReturnPct = overrides.MinThreeMinuteReturnPct
	cfg.MinVolumeRate = overrides.MinVolumeRate
	cfg.MaxPriceVsOpenPct = overrides.MaxPriceVsOpenPct
	cfg.EntryCooldownSec = overrides.EntryCooldownSec
	cfg.BreakEvenHoldMinutes = overrides.BreakEvenHoldMinutes
	cfg.BreakEvenMinR = overrides.BreakEvenMinR
	cfg.TrailActivationR = overrides.TrailActivationR
	cfg.TrailATRMultiplier = overrides.TrailATRMultiplier
	cfg.TightTrailTriggerR = overrides.TightTrailTriggerR
	cfg.TightTrailATRMultiplier = overrides.TightTrailATRMultiplier
	cfg.ProfitTargetR = overrides.ProfitTargetR
	cfg.FailedBreakoutCutR = overrides.FailedBreakoutCutR
	cfg.StructureConfirmR = overrides.StructureConfirmR
	if overrides.ScannerMinPriceVsOpenPctFloor > 0 {
		cfg.ScannerMinPriceVsOpenPctFloor = overrides.ScannerMinPriceVsOpenPctFloor
	}
	if overrides.ScannerMinPriceVsOpenGapMultiplier > 0 {
		cfg.ScannerMinPriceVsOpenGapMultiplier = overrides.ScannerMinPriceVsOpenGapMultiplier
	}
	if overrides.ScannerMinSetupVolumeRateOffset != 0 {
		cfg.ScannerMinSetupVolumeRateOffset = overrides.ScannerMinSetupVolumeRateOffset
	}
	if overrides.ScannerMinSetupRelativeVolumeExtra != 0 {
		cfg.ScannerMinSetupRelativeVolumeExtra = overrides.ScannerMinSetupRelativeVolumeExtra
	}
	if overrides.ScannerVWAPTolerancePct != 0 {
		cfg.ScannerVWAPTolerancePct = overrides.ScannerVWAPTolerancePct
	}
	if overrides.ScannerConsolidationATRMultiplier > 0 {
		cfg.ScannerConsolidationATRMultiplier = overrides.ScannerConsolidationATRMultiplier
	}
	if overrides.ScannerConsolidationMaxPct > 0 {
		cfg.ScannerConsolidationMaxPct = overrides.ScannerConsolidationMaxPct
	}
	if overrides.ScannerPullbackDepthMinATRMultiplier > 0 {
		cfg.ScannerPullbackDepthMinATRMultiplier = overrides.ScannerPullbackDepthMinATRMultiplier
	}
	if overrides.ScannerPullbackDepthMinPct > 0 {
		cfg.ScannerPullbackDepthMinPct = overrides.ScannerPullbackDepthMinPct
	}
	if overrides.ScannerPullbackDepthMaxATRMultiplier > 0 {
		cfg.ScannerPullbackDepthMaxATRMultiplier = overrides.ScannerPullbackDepthMaxATRMultiplier
	}
	if overrides.ScannerPullbackDepthMaxPct > 0 {
		cfg.ScannerPullbackDepthMaxPct = overrides.ScannerPullbackDepthMaxPct
	}
	if overrides.ScannerRenewedVolumeRateMin > 0 {
		cfg.ScannerRenewedVolumeRateMin = overrides.ScannerRenewedVolumeRateMin
	}
	return cfg
}

// IsSupportedStrategyProfile reports whether a named strategy profile is known.
func IsSupportedStrategyProfile(name StrategyProfile) bool {
	_, ok := supportedStrategyProfiles[StrategyProfile(strings.TrimSpace(string(name)))]
	return ok
}

// NormalizeStrategyProfile keeps startup behavior on the baseline profile when
// no optimizer artifact is selected.
func NormalizeStrategyProfile(cfg TradingConfig) TradingConfig {
	if !IsSupportedStrategyProfile(StrategyProfile(cfg.StrategyProfileName)) {
		cfg.StrategyProfileName = string(StrategyProfileBaseline)
	}
	if strings.TrimSpace(cfg.StrategyProfileVersion) == "" {
		cfg.StrategyProfileVersion = "built-in"
	}
	return cfg
}

// TradingProfilePathLabel returns a short artifact label for operator-facing
// status displays.
func TradingProfilePathLabel(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Base(strings.TrimSpace(path))
}
