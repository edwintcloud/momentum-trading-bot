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

// StrategyProfile names a bounded family of strategy behavior.
type StrategyProfile string

const (
	StrategyProfileBaseline        StrategyProfile = "baseline_breakout"
	StrategyProfileHighConviction  StrategyProfile = "high_conviction_breakout"
	StrategyProfileContinuation    StrategyProfile = "continuation_breakout"
	StrategyProfileMomentumCameron StrategyProfile = "momentum_cameron"
)

const bundledTradingProfileRelPath = "profiles/default.json"

var supportedStrategyProfiles = map[StrategyProfile]struct{}{
	StrategyProfileBaseline:        {},
	StrategyProfileHighConviction:  {},
	StrategyProfileContinuation:    {},
	StrategyProfileMomentumCameron: {},
}

// IsSupportedStrategyProfile checks if a profile name is valid.
func IsSupportedStrategyProfile(name StrategyProfile) bool {
	_, ok := supportedStrategyProfiles[name]
	return ok
}

// PromotionDecision records how a candidate profile should move through paper validation.
type PromotionDecision struct {
	DeploymentMode            string    `json:"deploymentMode"`
	Status                    string    `json:"status"`
	LastPaperValidationResult string    `json:"lastPaperValidationResult"`
	LastPaperValidationAt     time.Time `json:"lastPaperValidationAt,omitempty"`
	ApprovedAt                time.Time `json:"approvedAt,omitempty"`
	ApprovedBy                string    `json:"approvedBy,omitempty"`
	Notes                     string    `json:"notes,omitempty"`
}

// TradingProfile is a versioned optimizer artifact.
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
		return TradingProfile{}, fmt.Errorf("read trading profile %s: %w", path, err)
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
	return profile, nil
}

// LoadBundledTradingProfile loads the built-in default profile.
func LoadBundledTradingProfile() (TradingProfile, error) {
	_, callerFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(callerFile), "..", "..")
	return LoadTradingProfile(filepath.Join(root, bundledTradingProfileRelPath))
}

// SaveTradingProfile writes a profile artifact to disk.
func SaveTradingProfile(path string, profile TradingProfile) error {
	raw, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trading profile: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	return os.WriteFile(path, raw, 0o644)
}
