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
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// ParameterRange defines the min/max bounds for a single optimizer parameter.
type ParameterRange struct {
	Name string
	Min  float64
	Max  float64
}

// Optimizer runs walk-forward parameter optimization.
type Optimizer struct {
	bars          []backtest.InputBar
	iterFactory   func(start, end time.Time) (backtest.InputBarIterator, error) // streaming factory
	lookbackStart time.Time                                                     // used with iterFactory for time splits
	asOf          time.Time
	outDir        string
}

// Report records the optimizer's recommendations.
type Report struct {
	AsOf            time.Time           `json:"asOf"`
	GeneratedAt     time.Time           `json:"generatedAt"`
	SearchWeeks     int                 `json:"searchWeeks"`
	ValidationWeeks int                 `json:"validationWeeks"`
	HoldoutWeeks    int                 `json:"holdoutWeeks"`
	Candidates      []CandidateResult   `json:"candidates"`
	Recommendation  *CandidateResult    `json:"recommendation"`
	DSR             float64              `json:"dsr,omitempty"`
	MHT             *backtest.MHTResult  `json:"mht,omitempty"`
	Sensitivity     *SensitivityResult   `json:"sensitivity,omitempty"`
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

// NewStreamingOptimizer creates an optimizer that streams bars from disk cache
// instead of loading them all into RAM.
func NewStreamingOptimizer(iterFactory func(start, end time.Time) (backtest.InputBarIterator, error), lookbackStart, asOf time.Time, outDir string) *Optimizer {
	return &Optimizer{iterFactory: iterFactory, lookbackStart: lookbackStart, asOf: asOf, outDir: outDir}
}

// formatDuration formats a duration as a human-readable string like "2m15s" or "1h3m".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// Run executes the optimization.
func (o *Optimizer) Run() (Report, error) {
	return o.RunWithConfig(config.DefaultTradingConfig())
}

// RunWithConfig executes the optimization with a given base configuration.
func (o *Optimizer) RunWithConfig(baseCfg config.TradingConfig) (Report, error) {
	if len(o.bars) == 0 && o.iterFactory == nil {
		return Report{}, fmt.Errorf("no bars or iterator factory provided")
	}

	streaming := o.iterFactory != nil

	now := time.Now().In(markethours.Location())
	optimizerStart := now

	log.Printf("=== Optimizer Starting ===")
	log.Printf("As-of date: %s", o.asOf.Format("2006-01-02"))
	if streaming {
		log.Printf("Mode: streaming from disk cache")
	} else {
		log.Printf("Input bars: %d", len(o.bars))
	}
	log.Printf("Start time: %s ET", now.Format("2006-01-02 15:04:05"))

	// Define search space
	profiles := []config.StrategyProfile{
		config.StrategyProfileBaseline,
		config.StrategyProfileHighConviction,
		config.StrategyProfileContinuation,
	}

	// Use configured sample count (default: 500, was 30)
	numSamples := baseCfg.OptimizerSamples
	if numSamples <= 0 {
		numSamples = 500
	}
	variationsPerProfile := numSamples / len(profiles)
	if variationsPerProfile < 1 {
		variationsPerProfile = 1
	}
	totalCombinations := len(profiles) * variationsPerProfile

	// Compute time split points for both streaming and bars paths
	var searchCfg, validCfg, holdoutCfg backtest.RunConfig
	if streaming {
		totalDuration := o.asOf.Sub(o.lookbackStart)
		searchEnd := o.lookbackStart.Add(time.Duration(float64(totalDuration) * 0.6))
		validEnd := o.lookbackStart.Add(time.Duration(float64(totalDuration) * 0.8))

		iterFactory := o.iterFactory // capture for closures
		lbStart := o.lookbackStart
		asOf := o.asOf

		searchCfg = backtest.RunConfig{
			IteratorFn: func() (backtest.InputBarIterator, error) {
				return iterFactory(lbStart, searchEnd)
			},
		}
		validCfg = backtest.RunConfig{
			IteratorFn: func() (backtest.InputBarIterator, error) {
				return iterFactory(searchEnd, validEnd)
			},
		}
		holdoutCfg = backtest.RunConfig{
			IteratorFn: func() (backtest.InputBarIterator, error) {
				return iterFactory(validEnd, asOf)
			},
		}
		log.Printf("Data split (streaming): search=%s..%s validation=%s..%s holdout=%s..%s",
			lbStart.Format("2006-01-02"), searchEnd.Format("2006-01-02"),
			searchEnd.Format("2006-01-02"), validEnd.Format("2006-01-02"),
			validEnd.Format("2006-01-02"), asOf.Format("2006-01-02"))
	} else {
		// Split data: 60% search, 20% validation, 20% holdout
		var searchBars, validBars, holdoutBars []backtest.InputBar
		if baseCfg.OptimizerTimeSplit {
			searchBars, validBars, holdoutBars = o.splitBarsByTime(0.6, 0.2)
		} else {
			searchBars, validBars, holdoutBars = o.splitBars(0.6, 0.2)
		}
		log.Printf("Data split: search=%d bars, validation=%d bars, holdout=%d bars (time_split=%v)",
			len(searchBars), len(validBars), len(holdoutBars), baseCfg.OptimizerTimeSplit)
		searchCfg = backtest.RunConfig{Bars: searchBars}
		validCfg = backtest.RunConfig{Bars: validBars}
		holdoutCfg = backtest.RunConfig{Bars: holdoutBars}
	}

	// === Search Phase ===
	log.Printf("=== Optimization Search Phase ===")
	log.Printf("Profiles: %d (%d variations each, LHS=%v)", len(profiles), variationsPerProfile, baseCfg.OptimizerUseLHS)
	log.Printf("Parameter grid: %d combinations", totalCombinations)

	type searchCandidate struct {
		profile     string
		cfg         config.TradingConfig
		score       float64
		search      backtest.Result
		valid       backtest.Result
		paramValues []float64
	}

	var searchResults []searchCandidate
	bestScore := math.Inf(-1)
	searchStart := time.Now()
	combo := 0

	paramRanges := defaultParameterRanges()
	paramNames := make([]string, len(paramRanges))
	for i, pr := range paramRanges {
		paramNames[i] = pr.Name
	}

	for _, profile := range profiles {
		if baseCfg.BayesianOptEnabled {
			// Bayesian optimization: iterative suggest-evaluate loop
			bayesOpt := NewBayesianOptimizer(paramRanges, baseCfg.BayesianExploration, 42+int64(len(profile)))
			for iter := 0; iter < variationsPerProfile; iter++ {
				combo++
				paramVals := bayesOpt.SuggestNext()
				cfg := o.applyParamsToConfig(profile, paramVals, paramRanges)

				searchResult, err := backtest.Run(context.Background(), cfg, searchCfg)
				if err != nil {
					elapsed := time.Since(searchStart)
					log.Printf("[%d/%d] (%.1f%%) profile=%s ERROR (search): %v | elapsed: %s",
						combo, totalCombinations,
						float64(combo)/float64(totalCombinations)*100,
						profile, err, formatDuration(elapsed))
					bayesOpt.AddEvaluation(paramVals, -1)
					continue
				}

				validResult, err := backtest.Run(context.Background(), cfg, validCfg)
				if err != nil {
					elapsed := time.Since(searchStart)
					log.Printf("[%d/%d] (%.1f%%) profile=%s ERROR (validation): %v | elapsed: %s",
						combo, totalCombinations,
						float64(combo)/float64(totalCombinations)*100,
						profile, err, formatDuration(elapsed))
					bayesOpt.AddEvaluation(paramVals, -1)
					continue
				}

				score := o.scoreResult(searchResult, validResult)
				bayesOpt.AddEvaluation(paramVals, score)

				elapsed := time.Since(searchStart)
				avgPerCombo := elapsed / time.Duration(combo)
				remaining := time.Duration(totalCombinations-combo) * avgPerCombo

				newBest := ""
				if score > bestScore {
					bestScore = score
					newBest = " (new best)"
				}

				log.Printf("[%d/%d] (%.1f%%) profile=%-16s score=%.4f%s | elapsed: %s | eta: ~%s",
					combo, totalCombinations,
					float64(combo)/float64(totalCombinations)*100,
					profile, score, newBest,
					formatDuration(elapsed), formatDuration(remaining))

				searchResults = append(searchResults, searchCandidate{
					profile:     string(profile),
					cfg:         cfg,
					score:       score,
					search:      searchResult,
					valid:       validResult,
					paramValues: paramVals,
				})
			}
		} else {
			// LHS or random sampling
			var variations []config.TradingConfig
			var paramSets [][]float64
			if baseCfg.OptimizerUseLHS {
				variations, paramSets = o.generateLHSVariations(profile, variationsPerProfile, paramRanges)
			} else {
				variations = o.generateVariations(profile, variationsPerProfile)
				paramSets = make([][]float64, len(variations))
			}

			for vi, cfg := range variations {
				combo++

				searchResult, err := backtest.Run(context.Background(), cfg, searchCfg)
				if err != nil {
					elapsed := time.Since(searchStart)
					log.Printf("[%d/%d] (%.1f%%) profile=%s ERROR (search): %v | elapsed: %s",
						combo, totalCombinations,
						float64(combo)/float64(totalCombinations)*100,
						profile, err, formatDuration(elapsed))
					continue
				}

				validResult, err := backtest.Run(context.Background(), cfg, validCfg)
				if err != nil {
					elapsed := time.Since(searchStart)
					log.Printf("[%d/%d] (%.1f%%) profile=%s ERROR (validation): %v | elapsed: %s",
						combo, totalCombinations,
						float64(combo)/float64(totalCombinations)*100,
						profile, err, formatDuration(elapsed))
					continue
				}

				score := o.scoreResult(searchResult, validResult)

				elapsed := time.Since(searchStart)
				avgPerCombo := elapsed / time.Duration(combo)
				remaining := time.Duration(totalCombinations-combo) * avgPerCombo

				newBest := ""
				if score > bestScore {
					bestScore = score
					newBest = " (new best)"
				}

				log.Printf("[%d/%d] (%.1f%%) profile=%-16s score=%.4f%s | elapsed: %s | eta: ~%s",
					combo, totalCombinations,
					float64(combo)/float64(totalCombinations)*100,
					profile, score, newBest,
					formatDuration(elapsed), formatDuration(remaining))

				searchResults = append(searchResults, searchCandidate{
					profile:     string(profile),
					cfg:         cfg,
					score:       score,
					search:      searchResult,
					valid:       validResult,
					paramValues: paramSets[vi],
				})
			}
		}
	}

	searchElapsed := time.Since(searchStart)
	log.Printf("Search phase complete: %d candidates scored in %s (best=%.4f)",
		len(searchResults), formatDuration(searchElapsed), bestScore)

	// === Sensitivity Analysis (Change 8) ===
	var sensitivityResult *SensitivityResult
	if len(searchResults) >= 20 {
		evals := make([]Evaluation, len(searchResults))
		for i, sc := range searchResults {
			evals[i] = Evaluation{ParamValues: sc.paramValues, Score: sc.score}
		}
		sr := ComputeSensitivity(evals, paramNames)
		sensitivityResult = &sr
		for _, ps := range sr.Parameters {
			log.Printf("Sensitivity: param=%-20s first_order=%.3f total=%.3f", ps.Name, ps.FirstOrderIdx, ps.TotalIdx)
		}
	}

	// Sort search results by score descending
	sort.Slice(searchResults, func(i, j int) bool {
		return searchResults[i].score > searchResults[j].score
	})

	// === Validation Phase ===
	log.Printf("=== Validation Phase ===")
	topN := len(searchResults)
	if topN > 10 {
		topN = 10
	}
	log.Printf("Validating top %d candidates", topN)

	var candidates []CandidateResult
	for i, sc := range searchResults {
		if i >= topN {
			break
		}
		log.Printf("  Candidate %d: profile=%-16s search_score=%.4f", i+1, sc.profile, sc.score)
		candidates = append(candidates, CandidateResult{
			ProfileName:      sc.profile,
			SearchResult:     sc.search,
			ValidationResult: sc.valid,
			Score:            sc.score,
			Config:           sc.cfg,
		})
	}

	// === Holdout Phase ===
	log.Printf("=== Holdout Phase ===")
	bestHoldoutScore := math.Inf(-1)
	holdoutHasBars := streaming || len(o.bars) > 0
	if holdoutHasBars {
		for i := range candidates {
			if candidates[i].Score <= 0 {
				log.Printf("  Candidate %d: skipped (non-positive score)", i+1)
				continue
			}
			holdoutResult, err := backtest.Run(context.Background(), candidates[i].Config, holdoutCfg)
			if err != nil {
				log.Printf("  Candidate %d: holdout ERROR: %v", i+1, err)
				continue
			}
			candidates[i].HoldoutResult = &holdoutResult
			holdoutScore := holdoutResult.ProfitFactor
			if holdoutScore > bestHoldoutScore {
				bestHoldoutScore = holdoutScore
			}
			log.Printf("  Candidate %d: holdout profit_factor=%.4f net_pnl=%.2f trades=%d",
				i+1, holdoutResult.ProfitFactor, holdoutResult.NetPnL, holdoutResult.Trades)
		}
	} else {
		log.Printf("  No holdout bars available, skipping")
	}

	// Sort by score descending (already sorted, but re-sort to be safe)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	// === Walk-Forward Validation ===
	if baseCfg.WalkForwardEnabled && len(o.bars) > 0 && len(candidates) > 0 {
		log.Printf("=== Walk-Forward Validation ===")
		wfCfg := backtest.WalkForwardConfig{
			ISWindowDays:  baseCfg.WFISWindowDays,
			OOSWindowDays: baseCfg.WFOOSWindowDays,
			PurgeGapDays:  baseCfg.WFPurgeGapDays,
			StepDays:      baseCfg.WFStepDays,
		}
		wfResult := backtest.RunWalkForward(o.bars, wfCfg, candidates[0].Config)
		if len(wfResult.Windows) > 0 {
			candidates[0].SearchResult.WalkForward = &wfResult
			log.Printf("Walk-forward: windows=%d oos_sharpe=%.4f efficiency=%.4f",
				len(wfResult.Windows), wfResult.OOSSharpe, wfResult.Efficiency)
		} else {
			log.Printf("Walk-forward: insufficient data for windows")
		}
	}

	// === CPCV Validation ===
	if baseCfg.CPCVEnabled && len(o.bars) > 0 && len(candidates) > 0 {
		log.Printf("=== CPCV Validation ===")
		cpcvResult := backtest.RunCPCV(o.bars, baseCfg.CPCVGroups, baseCfg.CPCVPurgeGap, candidates[0].Config)
		if cpcvResult.NumPaths > 0 {
			candidates[0].SearchResult.CPCV = &cpcvResult
			log.Printf("CPCV: paths=%d median_sharpe=%.4f p10=%.4f",
				cpcvResult.NumPaths, cpcvResult.MedianSharpe, cpcvResult.Percentile10)
		} else {
			log.Printf("CPCV: no valid paths generated")
		}
	}

	// === Deflated Sharpe Ratio (Change 6) ===
	dsr := 0.0
	if len(candidates) > 0 && candidates[0].ValidationResult.Trades > 0 {
		valResult := candidates[0].ValidationResult
		returns := tradeReturnsFromResult(valResult)
		if len(returns) > 5 {
			mean, std := simpleMeanStd(returns)
			observedSR := 0.0
			if std > 0 {
				observedSR = (mean / std) * math.Sqrt(252)
			}
			skew, kurt := backtest.SkewnessKurtosis(returns)
			dsr = backtest.DeflatedSharpeRatio(observedSR, len(returns), skew, kurt, totalCombinations)
			if dsr < 0.95 {
				log.Printf("WARNING: strategy does not pass Deflated Sharpe Ratio test (DSR=%.4f, trials=%d)", dsr, totalCombinations)
			} else {
				log.Printf("Deflated Sharpe Ratio: %.4f (trials=%d)", dsr, totalCombinations)
			}
		}
	}

	// === Multiple Hypothesis Testing Corrections (Section 5.3) ===
	var mhtResult *backtest.MHTResult
	mhtMethod := backtest.MHTMethod(baseCfg.MHTCorrectionMethod)
	if mhtMethod != backtest.MHTNone && mhtMethod != "" && len(candidates) > 1 {
		pValues := make([]float64, len(candidates))
		for i, c := range candidates {
			returns := tradeReturnsFromResult(c.ValidationResult)
			if len(returns) > 5 {
				mean, std := simpleMeanStd(returns)
				sr := 0.0
				if std > 0 {
					sr = (mean / std) * math.Sqrt(252)
				}
				skew, kurt := backtest.SkewnessKurtosis(returns)
				pValues[i] = backtest.SharpeRatioPValue(sr, len(returns), skew, kurt)
			} else {
				pValues[i] = 1.0
			}
		}
		result := backtest.ApplyMHTCorrection(pValues, baseCfg.MHTAlpha, mhtMethod)
		mhtResult = &result
		log.Printf("MHT correction (%s): %d/%d candidates significant at α=%.3f (trials=%d)",
			mhtMethod, result.SignificantCount, len(candidates), baseCfg.MHTAlpha, totalCombinations)
	}

	report := Report{
		AsOf:            o.asOf,
		GeneratedAt:     time.Now().In(markethours.Location()),
		SearchWeeks:     12,
		ValidationWeeks: 4,
		HoldoutWeeks:    4,
		Candidates:      candidates,
		DSR:             dsr,
		MHT:             mhtResult,
		Sensitivity:     sensitivityResult,
	}

	if len(candidates) > 0 {
		report.Recommendation = &candidates[0]
	}

	// === Artifact Saving ===
	log.Printf("=== Saving Artifacts ===")
	if err := o.writeArtifacts(report); err != nil {
		log.Printf("optimizer: write artifacts warning: %v", err)
	} else {
		log.Printf("Artifacts saved to: %s", o.outDir)
	}

	// === Final Summary ===
	totalDuration := time.Since(optimizerStart)
	log.Printf("=== Optimization Complete ===")
	log.Printf("Duration: %s", formatDuration(totalDuration))
	log.Printf("Combinations tested: %d", totalCombinations)
	log.Printf("Candidates scored: %d", len(searchResults))
	log.Printf("Best search score: %.4f", bestScore)
	if len(candidates) > 0 && candidates[0].Score > 0 {
		log.Printf("Best validation score: %.4f", candidates[0].ValidationResult.ProfitFactor)
	}
	if bestHoldoutScore > math.Inf(-1) {
		log.Printf("Holdout score: %.4f", bestHoldoutScore)
	}
	log.Printf("Artifacts saved to: %s", o.outDir)

	return report, nil
}

// defaultParameterRanges defines the bounds for each tunable parameter.
func defaultParameterRanges() []ParameterRange {
	return []ParameterRange{
		{Name: "MinEntryScore", Min: 1.5, Max: 5.0},
		{Name: "ShortMinEntryScore", Min: 2.0, Max: 4.0},
		{Name: "RiskPerTradePct", Min: 0.003, Max: 0.010},
		{Name: "TrailActivationR", Min: 0.3, Max: 2.0},
		{Name: "TrailATRMultiplier", Min: 1.0, Max: 3.0},
		{Name: "ProfitTargetR", Min: 2.0, Max: 6.0},
		{Name: "MinGapPercent", Min: 2.0, Max: 7.0},
		{Name: "MinRelativeVolume", Min: 1.5, Max: 4.5},
	}
}

// LatinHypercubeSample generates LHS samples across parameter ranges.
func LatinHypercubeSample(params []ParameterRange, numSamples int, rng *rand.Rand) [][]float64 {
	nParams := len(params)
	samples := make([][]float64, numSamples)
	for i := range samples {
		samples[i] = make([]float64, nParams)
	}

	for j := 0; j < nParams; j++ {
		perm := rng.Perm(numSamples)
		for i := 0; i < numSamples; i++ {
			lower := float64(perm[i]) / float64(numSamples)
			upper := float64(perm[i]+1) / float64(numSamples)
			u := lower + rng.Float64()*(upper-lower)
			samples[i][j] = params[j].Min + u*(params[j].Max-params[j].Min)
		}
	}
	return samples
}

// applyParamsToConfig creates a TradingConfig from parameter values (used by Bayesian optimizer).
func (o *Optimizer) applyParamsToConfig(profile config.StrategyProfile, params []float64, paramRanges []ParameterRange) config.TradingConfig {
	cfg := config.DefaultTradingConfig()
	cfg.StrategyProfileName = string(profile)
	if len(params) >= 8 {
		cfg.MinEntryScore = params[0]
		cfg.ShortMinEntryScore = params[1]
		cfg.RiskPerTradePct = params[2]
		cfg.TrailActivationR = params[3]
		cfg.TrailATRMultiplier = params[4]
		cfg.ProfitTargetR = params[5]
		cfg.MinGapPercent = params[6]
		cfg.MinRelativeVolume = params[7]
	}
	switch profile {
	case config.StrategyProfileHighConviction:
		cfg.MinEntryScore = math.Max(cfg.MinEntryScore, 3.0)
		cfg.MaxOpenPositions = 3
		cfg.RiskPerTradePct = math.Max(cfg.RiskPerTradePct, 0.008)
	case config.StrategyProfileContinuation:
		cfg.MinEntryScore = math.Min(cfg.MinEntryScore, 3.0)
		cfg.TrailActivationR = math.Min(cfg.TrailActivationR, 1.0)
	}
	cfg.StrategyProfileVersion = fmt.Sprintf("opt-%s-bayes-%d", o.asOf.Format("20060102"), len(params))
	return cfg
}

func (o *Optimizer) generateLHSVariations(profile config.StrategyProfile, count int, paramRanges []ParameterRange) ([]config.TradingConfig, [][]float64) {
	base := config.DefaultTradingConfig()
	base.StrategyProfileName = string(profile)

	rng := rand.New(rand.NewSource(42 + int64(len(profile))))
	samples := LatinHypercubeSample(paramRanges, count, rng)

	variations := make([]config.TradingConfig, count)
	for i, s := range samples {
		cfg := base
		cfg.MinEntryScore = s[0]
		cfg.ShortMinEntryScore = s[1]
		cfg.RiskPerTradePct = s[2]
		cfg.TrailActivationR = s[3]
		cfg.TrailATRMultiplier = s[4]
		cfg.ProfitTargetR = s[5]
		cfg.MinGapPercent = s[6]
		cfg.MinRelativeVolume = s[7]

		switch profile {
		case config.StrategyProfileHighConviction:
			cfg.MinEntryScore = math.Max(cfg.MinEntryScore, 3.0)
			cfg.MaxOpenPositions = 3
			cfg.RiskPerTradePct = math.Max(cfg.RiskPerTradePct, 0.008)
		case config.StrategyProfileContinuation:
			cfg.MinEntryScore = math.Min(cfg.MinEntryScore, 3.0)
			cfg.TrailActivationR = math.Min(cfg.TrailActivationR, 1.0)
		}

		cfg.StrategyProfileVersion = fmt.Sprintf("opt-%s-%d", o.asOf.Format("20060102"), i)
		variations[i] = cfg
	}
	return variations, samples
}

func (o *Optimizer) generateVariations(profile config.StrategyProfile, count int) []config.TradingConfig {
	base := config.DefaultTradingConfig()
	base.StrategyProfileName = string(profile)

	variations := make([]config.TradingConfig, count)
	for i := 0; i < count; i++ {
		cfg := base
		r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(i)))

		cfg.MinEntryScore = 1.5 + r.Float64()*2.0
		cfg.ShortMinEntryScore = 2.0 + r.Float64()*2.0
		cfg.RiskPerTradePct = 0.003 + r.Float64()*0.007
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

// splitBars splits by bar index (legacy).
func (o *Optimizer) splitBars(searchPct, validPct float64) ([]backtest.InputBar, []backtest.InputBar, []backtest.InputBar) {
	n := len(o.bars)
	searchEnd := int(float64(n) * searchPct)
	validEnd := searchEnd + int(float64(n)*validPct)
	if validEnd > n {
		validEnd = n
	}
	return o.bars[:searchEnd], o.bars[searchEnd:validEnd], o.bars[validEnd:]
}

// splitBarsByTime splits by timestamp duration (Change 4).
func (o *Optimizer) splitBarsByTime(searchPct, validPct float64) ([]backtest.InputBar, []backtest.InputBar, []backtest.InputBar) {
	if len(o.bars) == 0 {
		return nil, nil, nil
	}

	minTime := o.bars[0].Timestamp
	maxTime := o.bars[len(o.bars)-1].Timestamp
	totalDuration := maxTime.Sub(minTime)

	searchEnd := minTime.Add(time.Duration(float64(totalDuration) * searchPct))
	validEnd := minTime.Add(time.Duration(float64(totalDuration) * (searchPct + validPct)))

	var search, valid, holdout []backtest.InputBar
	for _, bar := range o.bars {
		switch {
		case bar.Timestamp.Before(searchEnd):
			search = append(search, bar)
		case bar.Timestamp.Before(validEnd):
			valid = append(valid, bar)
		default:
			holdout = append(holdout, bar)
		}
	}
	return search, valid, holdout
}

// scoreResult computes a normalized [0,1] score (Change 5).
func (o *Optimizer) scoreResult(search, validation backtest.Result) float64 {
	if search.Trades < 5 || validation.Trades < 3 {
		return -1
	}

	normWinRate := func(wr float64) float64 {
		return math.Min(wr/100.0, 1.0)
	}

	normProfitFactor := func(pf float64) float64 {
		if pf <= 0 {
			return 0
		}
		return math.Min(pf/3.0, 1.0)
	}

	normDrawdownRisk := func(maxDD, netPnL float64) float64 {
		if netPnL <= 0 {
			return 0
		}
		ratio := maxDD / netPnL
		return math.Max(0, 1.0-ratio/3.0)
	}

	normNetReturn := func(netPnL, capital float64) float64 {
		if capital <= 0 {
			return 0
		}
		ret := netPnL / capital
		// Map return [-0.1, 0.5] to [0, 1]
		return math.Max(0, math.Min((ret+0.1)/0.6, 1.0))
	}

	startingCapital := search.StartingCapital
	if startingCapital <= 0 {
		startingCapital = 25000
	}

	searchScore := normProfitFactor(search.ProfitFactor)*0.30 +
		normWinRate(search.WinRate)*0.20 +
		normDrawdownRisk(search.MaxDrawdownPct, search.NetPnL)*0.20 +
		normNetReturn(search.NetPnL, startingCapital)*0.30

	validScore := normProfitFactor(validation.ProfitFactor)*0.30 +
		normWinRate(validation.WinRate)*0.20 +
		normDrawdownRisk(validation.MaxDrawdownPct, validation.NetPnL)*0.20 +
		normNetReturn(validation.NetPnL, startingCapital)*0.30

	overfitPenalty := 0.0
	if math.Abs(searchScore) > 0.01 {
		overfitPenalty = math.Abs(searchScore-validScore) / math.Max(math.Abs(searchScore), 0.01)
	}

	return (searchScore*0.4 + validScore*0.6) * (1 - overfitPenalty*0.5)
}

func tradeReturnsFromResult(result backtest.Result) []float64 {
	if result.StartingCapital <= 0 || len(result.ClosedTrades) == 0 {
		return nil
	}
	returns := make([]float64, len(result.ClosedTrades))
	for i, t := range result.ClosedTrades {
		returns[i] = t.PnL / result.StartingCapital
	}
	return returns
}

func simpleMeanStd(data []float64) (float64, float64) {
	if len(data) == 0 {
		return 0, 0
	}
	n := float64(len(data))
	var sum, sumSq float64
	for _, v := range data {
		sum += v
		sumSq += v * v
	}
	mean := sum / n
	vari := sumSq/n - mean*mean
	return mean, math.Sqrt(math.Max(0, vari))
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
