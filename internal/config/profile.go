package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

const bundledTradingProfileRelPath = "profiles/default.json"

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
	if _, sourceFile, _, ok := runtime.Caller(0); ok && strings.TrimSpace(sourceFile) != "" {
		searchRoots = append(searchRoots, filepath.Dir(sourceFile))
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

// ApplyTradingProfile applies profile-controlled strategy and risk overrides
// after broker-aware tuning while preserving startup-derived account and
// capability values such as capital and hydration budget.
func ApplyTradingProfile(base TradingConfig, profile TradingProfile) TradingConfig {
	cfg := base
	overrides := profile.Config

	cfg.StrategyProfileName = string(profile.Name)
	cfg.StrategyProfileVersion = profile.Version
	cfg.EnableShorts = overrides.EnableShorts
	cfg.RiskPerTradePct = overrides.RiskPerTradePct
	cfg.DailyLossLimitPct = overrides.DailyLossLimitPct
	cfg.MaxTradesPerDay = overrides.MaxTradesPerDay
	cfg.MaxOpenPositions = overrides.MaxOpenPositions
	cfg.StopLossPct = overrides.StopLossPct
	cfg.ExitCooldownSec = overrides.ExitCooldownSec
	cfg.MinEntryScore = overrides.MinEntryScore
	cfg.MinOneMinuteReturnPct = overrides.MinOneMinuteReturnPct
	cfg.MinThreeMinuteReturnPct = overrides.MinThreeMinuteReturnPct
	cfg.MinVolumeRate = overrides.MinVolumeRate
	cfg.MaxPriceVsOpenPct = overrides.MaxPriceVsOpenPct
	cfg.BreakoutFailureWindowMin = overrides.BreakoutFailureWindowMin
	cfg.StagnationWindowMin = overrides.StagnationWindowMin
	cfg.StagnationMinPeakPct = overrides.StagnationMinPeakPct
	cfg.ScannerWorkers = overrides.ScannerWorkers
	cfg.MinPrice = overrides.MinPrice
	cfg.MaxPrice = overrides.MaxPrice
	cfg.MinGapPercent = overrides.MinGapPercent
	cfg.MinRelativeVolume = overrides.MinRelativeVolume
	cfg.MinPremarketVolume = overrides.MinPremarketVolume
	cfg.EntryCooldownSec = overrides.EntryCooldownSec
	cfg.HydrationRetrySec = overrides.HydrationRetrySec
	cfg.HydrationQueueSize = overrides.HydrationQueueSize
	cfg.LimitOrderSlippageDollars = overrides.LimitOrderSlippageDollars
	cfg.EntryATRPercentFallback = overrides.EntryATRPercentFallback
	cfg.EntryStopATRMultiplier = overrides.EntryStopATRMultiplier
	cfg.MaxRiskATRMultiplier = overrides.MaxRiskATRMultiplier
	cfg.BreakEvenHoldMinutes = overrides.BreakEvenHoldMinutes
	cfg.BreakEvenMinR = overrides.BreakEvenMinR
	cfg.TrailActivationR = overrides.TrailActivationR
	cfg.TrailATRMultiplier = overrides.TrailATRMultiplier
	cfg.TightTrailTriggerR = overrides.TightTrailTriggerR
	cfg.TightTrailATRMultiplier = overrides.TightTrailATRMultiplier
	cfg.ProfitTargetR = overrides.ProfitTargetR
	cfg.FailedBreakoutCutR = overrides.FailedBreakoutCutR
	cfg.StructureConfirmR = overrides.StructureConfirmR
	if overrides.MaxShortOpenPositions > 0 {
		cfg.MaxShortOpenPositions = overrides.MaxShortOpenPositions
	}
	if overrides.MaxShortExposurePct > 0 {
		cfg.MaxShortExposurePct = overrides.MaxShortExposurePct
	}
	cfg.ShortMinEntryScore = overrides.ShortMinEntryScore
	cfg.ShortPeakExtensionMinPct = overrides.ShortPeakExtensionMinPct
	cfg.ShortVWAPBreakMinPct = overrides.ShortVWAPBreakMinPct
	cfg.ShortStopATRMultiplier = overrides.ShortStopATRMultiplier
	cfg.ScannerMinPriceVsOpenPctFloor = overrides.ScannerMinPriceVsOpenPctFloor
	cfg.ScannerMinPriceVsOpenGapMultiplier = overrides.ScannerMinPriceVsOpenGapMultiplier
	cfg.ScannerMinSetupVolumeRateOffset = overrides.ScannerMinSetupVolumeRateOffset
	cfg.ScannerMinSetupRelativeVolumeExtra = overrides.ScannerMinSetupRelativeVolumeExtra
	cfg.ScannerVWAPTolerancePct = overrides.ScannerVWAPTolerancePct
	cfg.ScannerConsolidationATRMultiplier = overrides.ScannerConsolidationATRMultiplier
	cfg.ScannerConsolidationMaxPct = overrides.ScannerConsolidationMaxPct
	cfg.ScannerPullbackDepthMinATRMultiplier = overrides.ScannerPullbackDepthMinATRMultiplier
	cfg.ScannerPullbackDepthMinPct = overrides.ScannerPullbackDepthMinPct
	cfg.ScannerPullbackDepthMaxATRMultiplier = overrides.ScannerPullbackDepthMaxATRMultiplier
	cfg.ScannerPullbackDepthMaxPct = overrides.ScannerPullbackDepthMaxPct
	cfg.ScannerRenewedVolumeRateMin = overrides.ScannerRenewedVolumeRateMin
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
