package portfolio

import (
	"log"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

// Constructor orchestrates portfolio construction methods (MVO, risk parity,
// factor-neutral, HHI, long-short balancing). It is disabled by default and
// only activates when the corresponding config flags are set.
type Constructor struct {
	mu     sync.RWMutex
	config config.TradingConfig

	// EWMA volatility tracker for risk parity
	ewmaTracker *EWMAVolTracker

	// Rebalance state
	rebalanceChecker *RebalanceChecker

	// Cached target weights from the last portfolio construction run
	targetWeights map[string]float64

	// Beta cache for factor-neutral
	betas map[string]float64
}

// NewConstructor creates a portfolio constructor with the given config.
func NewConstructor(cfg config.TradingConfig) *Constructor {
	return &Constructor{
		config:        cfg,
		ewmaTracker:   NewEWMAVolTracker(cfg.RiskParityEWMALambda),
		targetWeights: make(map[string]float64),
		betas:         make(map[string]float64),
		rebalanceChecker: &RebalanceChecker{
			TargetWeights:      make(map[string]float64),
			DeviationThreshold: cfg.RiskParityDeviationThreshold,
			RebalanceInterval:  time.Duration(cfg.RiskParityRebalanceMinutes) * time.Minute,
		},
	}
}

// UpdateVolatility feeds a new return observation to the EWMA tracker.
func (c *Constructor) UpdateVolatility(symbol string, ret float64) {
	if !c.config.RiskParityEnabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ewmaTracker.Update(symbol, ret)
}

// UpdateBeta updates the cached beta for a symbol.
func (c *Constructor) UpdateBeta(symbol string, beta float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.betas[symbol] = beta
}

// ComputeMVOWeights runs mean-variance optimization if enabled.
func (c *Constructor) ComputeMVOWeights(symbols []string, expectedReturns []float64, covMatrix [][]float64, sectorMap map[string]string) map[string]float64 {
	if !c.config.MVOEnabled {
		return nil
	}

	input := MVOInput{
		Symbols:         symbols,
		ExpectedReturns: expectedReturns,
		CovMatrix:       covMatrix,
		RiskAversion:    c.config.MVORiskAversion,
		MaxPositionPct:  c.config.MVOMaxPositionPct,
		MaxSectorPct:    c.config.MVOMaxSectorPct,
		SectorMap:       sectorMap,
		ShrinkageDelta:  c.config.LedoitWolfShrinkage,
	}

	result := RunMVO(input)

	c.mu.Lock()
	c.targetWeights = result.Weights
	c.mu.Unlock()

	return result.Weights
}

// ComputeRiskParityWeights runs risk parity if enabled.
func (c *Constructor) ComputeRiskParityWeights(symbols []string) map[string]float64 {
	if !c.config.RiskParityEnabled {
		return nil
	}

	c.mu.RLock()
	vols := make([]float64, len(symbols))
	for i, sym := range symbols {
		vols[i] = c.ewmaTracker.Volatility(sym)
		if vols[i] <= 0 {
			vols[i] = 0.02 // default 2% vol if unknown
		}
	}
	c.mu.RUnlock()

	input := RiskParityInput{
		Symbols:    symbols,
		Volatility: vols,
	}

	result := RunRiskParity(input)

	c.mu.Lock()
	c.targetWeights = result.Weights
	c.rebalanceChecker.TargetWeights = result.Weights
	c.rebalanceChecker.LastRebalance = time.Now()
	c.mu.Unlock()

	return result.Weights
}

// NeedsRebalance checks if risk parity rebalance is needed.
func (c *Constructor) NeedsRebalance(currentWeights map[string]float64) bool {
	if !c.config.RiskParityEnabled {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rebalanceChecker.NeedsRebalance(currentWeights, time.Now())
}

// CheckFactorNeutral analyzes factor neutrality and returns hedge info.
func (c *Constructor) CheckFactorNeutral(weights map[string]float64, portfolioNotional, benchmarkPrice float64) FactorNeutralResult {
	if !c.config.FactorNeutralEnabled {
		return FactorNeutralResult{AdjustedWeights: weights}
	}

	c.mu.RLock()
	betas := make(map[string]float64, len(c.betas))
	for k, v := range c.betas {
		betas[k] = v
	}
	c.mu.RUnlock()

	input := FactorNeutralInput{
		Weights:           weights,
		Betas:             betas,
		MaxNetBeta:        c.config.MaxNetBeta,
		PortfolioNotional: portfolioNotional,
		BenchmarkPrice:    benchmarkPrice,
	}

	return RunFactorNeutral(input)
}

// CheckHHI computes portfolio concentration metrics.
func (c *Constructor) CheckHHI(weights map[string]float64, sectorMap map[string]string) HHIResult {
	if !c.config.HHIEnabled {
		return HHIResult{}
	}

	input := HHIInput{
		Weights:        weights,
		SectorMap:      sectorMap,
		MaxSinglePct:   c.config.MVOMaxPositionPct,
		MaxSectorPct:   c.config.MVOMaxSectorPct,
		HHIMaxTarget:   c.config.HHIMaxTarget,
		AlertThreshold: c.config.HHIAlertThreshold,
	}

	result := ComputeHHI(input)
	if result.AboveMaxTarget {
		log.Printf("[portfolio] HHI warning: %.4f > %.4f (effective N=%.1f)", result.HHI, c.config.HHIMaxTarget, result.EffectiveN)
	}
	return result
}

// ShouldBlockNewEntry returns true if HHI indicates too much concentration.
func (c *Constructor) ShouldBlockNewEntry(weights map[string]float64) bool {
	if !c.config.HHIEnabled {
		return false
	}
	return ShouldBlockEntry(weights, c.config.HHIAlertThreshold)
}

// CheckLongShort analyzes long-short portfolio balance.
func (c *Constructor) CheckLongShort(positions map[string]PositionInfo, equity float64) LongShortResult {
	if !c.config.LongShortBalancingEnabled {
		return LongShortResult{
			IsDollarNeutral: true,
			IsBetaNeutral:   true,
			IsLeverageOK:    true,
			IsSectorNeutral: true,
		}
	}

	input := LongShortInput{
		Positions:              positions,
		Equity:                 equity,
		DollarNeutralTolerance: c.config.DollarNeutralTolerance,
		BetaNeutralThreshold:   c.config.BetaNeutralThreshold,
		MaxGrossLeverage:       c.config.MaxGrossLeverage,
		SectorNeutralTolerance: c.config.SectorNeutralTolerance,
	}

	return CheckLongShortBalance(input)
}

// TargetWeights returns the cached target weights from the last construction run.
func (c *Constructor) TargetWeights() map[string]float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]float64, len(c.targetWeights))
	for k, v := range c.targetWeights {
		out[k] = v
	}
	return out
}

// IsEnabled returns true if any portfolio construction method is enabled.
func (c *Constructor) IsEnabled() bool {
	return c.config.MVOEnabled ||
		c.config.RiskParityEnabled ||
		c.config.FactorNeutralEnabled ||
		c.config.HHIEnabled ||
		c.config.LongShortBalancingEnabled
}
