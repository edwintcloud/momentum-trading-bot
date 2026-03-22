package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

// Optimizer runs walk-forward parameter optimization.
type Optimizer struct {
	bars   []backtest.InputBar
	asOf   time.Time
	outDir string
}

// Report records the optimizer's recommendations.
type Report struct {
	AsOf            time.Time         `json:"asOf"`
	GeneratedAt     time.Time         `json:"generatedAt"`
	SearchWeeks     int               `json:"searchWeeks"`
	ValidationWeeks int               `json:"validationWeeks"`
	HoldoutWeeks    int               `json:"holdoutWeeks"`
	Candidates      []CandidateResult `json:"candidates"`
	Recommendation  *CandidateResult  `json:"recommendation"`
}

// CandidateResult is one optimizer trial.
type CandidateResult struct {
	ProfileName      string               `json:"profileName"`
	SearchResult     backtest.Result      `json:"searchResult"`
	ValidationResult backtest.Result      `json:"validationResult"`
	HoldoutResult    *backtest.Result     `json:"holdoutResult,omitempty"`
	Score            float64              `json:"score"`
	Config           config.TradingConfig `json:"config"`
}

// NewOptimizer creates an optimizer from bars.
func NewOptimizer(bars []backtest.InputBar, asOf time.Time, outDir string) *Optimizer {
	return &Optimizer{bars: bars, asOf: asOf, outDir: outDir}
}

// Run executes the optimization.
func (o *Optimizer) Run() (Report, error) {
	if len(o.bars) == 0 {
		return Report{}, fmt.Errorf("no bars provided")
	}

	log.Printf("optimizer: starting with %d bars, as-of %s", len(o.bars), o.asOf.Format("2006-01-02"))

	// Define search space
	profiles := []config.StrategyProfile{
		config.StrategyProfileBaseline,
		config.StrategyProfileHighConviction,
		config.StrategyProfileContinuation,
	}

	var candidates []CandidateResult

	for _, profile := range profiles {
		// Generate parameter variations
		variations := o.generateVariations(profile, 10)
		for _, cfg := range variations {
			// Split data: 60% search, 20% validation, 20% holdout
			searchBars, validBars, holdoutBars := o.splitBars(0.6, 0.2)

			searchResult, err := backtest.Run(context.Background(), cfg, backtest.RunConfig{
				Bars: searchBars,
			})
			if err != nil {
				continue
			}
			validResult, err := backtest.Run(context.Background(), cfg, backtest.RunConfig{
				Bars: validBars,
			})
			if err != nil {
				continue
			}

			// Score combines search and validation
			score := o.scoreResult(searchResult, validResult)

			candidate := CandidateResult{
				ProfileName:      string(profile),
				SearchResult:     searchResult,
				ValidationResult: validResult,
				Score:            score,
				Config:           cfg,
			}

			// Run holdout for top candidates
			if score > 0 && len(holdoutBars) > 0 {
				holdoutResult, err := backtest.Run(context.Background(), cfg, backtest.RunConfig{
					Bars: holdoutBars,
				})
				if err == nil {
					candidate.HoldoutResult = &holdoutResult
				}
			}

			candidates = append(candidates, candidate)
		}
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	report := Report{
		AsOf:            o.asOf,
		GeneratedAt:     time.Now(),
		SearchWeeks:     12,
		ValidationWeeks: 4,
		HoldoutWeeks:    4,
		Candidates:      candidates,
	}

	if len(candidates) > 0 {
		report.Recommendation = &candidates[0]
	}

	// Write artifacts
	if err := o.writeArtifacts(report); err != nil {
		log.Printf("optimizer: write artifacts warning: %v", err)
	}

	return report, nil
}

func (o *Optimizer) generateVariations(profile config.StrategyProfile, count int) []config.TradingConfig {
	base := config.DefaultTradingConfig()
	base.StrategyProfileName = string(profile)

	variations := make([]config.TradingConfig, count)
	for i := 0; i < count; i++ {
		cfg := base
		r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(i)))

		// Randomize within bounds
		cfg.MinEntryScore = 1.5 + r.Float64()*2.0
		cfg.ShortMinEntryScore = 2.0 + r.Float64()*2.0
		cfg.RiskPerTradePct = 0.003 + r.Float64()*0.007
		cfg.StopLossPct = 0.01 + r.Float64()*0.03
		cfg.TrailActivationR = 0.5 + r.Float64()*1.5
		cfg.TrailATRMultiplier = 1.0 + r.Float64()*2.0
		cfg.ProfitTargetR = 2.0 + r.Float64()*4.0
		cfg.MinGapPercent = 2.0 + r.Float64()*5.0
		cfg.MinRelativeVolume = 1.5 + r.Float64()*3.0

		switch profile {
		case config.StrategyProfileHighConviction:
			cfg.MinEntryScore = 3.0 + r.Float64()*2.0
			cfg.MaxOpenPositions = 3
			cfg.RiskPerTradePct = 0.008 + r.Float64()*0.005
		case config.StrategyProfileContinuation:
			cfg.MinEntryScore = 1.5 + r.Float64()*1.5
			cfg.TrailActivationR = 0.3 + r.Float64()*0.7
		}

		cfg.StrategyProfileVersion = fmt.Sprintf("opt-%s-%d", o.asOf.Format("20060102"), i)
		variations[i] = cfg
	}
	return variations
}

func (o *Optimizer) splitBars(searchPct, validPct float64) ([]backtest.InputBar, []backtest.InputBar, []backtest.InputBar) {
	n := len(o.bars)
	searchEnd := int(float64(n) * searchPct)
	validEnd := searchEnd + int(float64(n)*validPct)
	if validEnd > n {
		validEnd = n
	}
	return o.bars[:searchEnd], o.bars[searchEnd:validEnd], o.bars[validEnd:]
}

func (o *Optimizer) scoreResult(search, validation backtest.Result) float64 {
	if search.Trades < 5 || validation.Trades < 3 {
		return -1
	}

	// Weighted combination
	searchScore := search.ProfitFactor*0.3 + search.WinRate*0.2 - search.MaxDrawdownPct/math.Max(search.NetPnL, 1)*0.2
	validScore := validation.ProfitFactor*0.3 + validation.WinRate*0.2 - validation.MaxDrawdownPct/math.Max(validation.NetPnL, 1)*0.2

	// Penalize overfitting (large gap between search and validation)
	overfit := math.Abs(searchScore-validScore) / math.Max(math.Abs(searchScore), 1)

	return (searchScore*0.4 + validScore*0.6) * (1 - overfit*0.5)
}

func (o *Optimizer) writeArtifacts(report Report) error {
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		return err
	}

	// Write report
	reportData, _ := json.MarshalIndent(report, "", "  ")
	reportPath := filepath.Join(o.outDir, "latest-report.json")
	if err := os.WriteFile(reportPath, reportData, 0o644); err != nil {
		return err
	}

	// Write recommended profile
	if report.Recommendation != nil {
		profile := config.TradingProfile{
			Name:        config.StrategyProfile(report.Recommendation.ProfileName),
			Version:     report.Recommendation.Config.StrategyProfileVersion,
			GeneratedAt: report.GeneratedAt,
			AsOf:        report.AsOf,
			Config:      report.Recommendation.Config,
			Promotion: config.PromotionDecision{
				DeploymentMode: "paper",
				Status:         "pending-paper-validation",
			},
		}
		profileData, _ := json.MarshalIndent(profile, "", "  ")
		profilePath := filepath.Join(o.outDir, "latest-candidate-profile.json")
		if err := os.WriteFile(profilePath, profileData, 0o644); err != nil {
			return err
		}
	}

	return nil
}
