package portfolio

import (
	"math"
)

// HHIInput holds the inputs for HHI computation.
type HHIInput struct {
	Weights        map[string]float64 // symbol → portfolio weight (can be negative for shorts)
	SectorMap      map[string]string  // symbol → sector
	MaxSinglePct   float64            // max single position pct (e.g. 0.05)
	MaxSectorPct   float64            // max sector pct (e.g. 0.25)
	HHIMaxTarget   float64            // warning threshold
	AlertThreshold float64            // block threshold
}

// HHIResult holds the output of HHI analysis.
type HHIResult struct {
	HHI             float64            // Herfindahl-Hirschman Index
	EffectiveN      float64            // effective number of positions (1/HHI)
	AboveMaxTarget  bool               // HHI > max target (warning)
	AboveAlert      bool               // HHI > alert threshold (block)
	PositionBreaches []string           // symbols exceeding single-position limit
	SectorBreaches  map[string]float64 // sectors exceeding sector limit
}

// ComputeHHI computes the HHI and related diversification metrics.
// HHI = Σ w_i² using absolute weights for long/short portfolios.
func ComputeHHI(input HHIInput) HHIResult {
	result := HHIResult{
		SectorBreaches: make(map[string]float64),
	}

	if len(input.Weights) == 0 {
		return result
	}

	// Compute absolute weight sum for normalization
	absSum := 0.0
	for _, w := range input.Weights {
		absSum += math.Abs(w)
	}
	if absSum < 1e-12 {
		return result
	}

	// Compute HHI using normalized absolute weights
	for _, w := range input.Weights {
		normalizedW := math.Abs(w) / absSum
		result.HHI += normalizedW * normalizedW
	}

	// Effective N
	if result.HHI > 1e-12 {
		result.EffectiveN = 1.0 / result.HHI
	}

	// Check thresholds
	maxTarget := input.HHIMaxTarget
	if maxTarget <= 0 {
		maxTarget = 0.10
	}
	alertThreshold := input.AlertThreshold
	if alertThreshold <= 0 {
		alertThreshold = 0.15
	}

	result.AboveMaxTarget = result.HHI > maxTarget
	result.AboveAlert = result.HHI > alertThreshold

	// Check position-level concentration
	maxSingle := input.MaxSinglePct
	if maxSingle > 0 {
		for sym, w := range input.Weights {
			normalizedW := math.Abs(w) / absSum
			if normalizedW > maxSingle {
				result.PositionBreaches = append(result.PositionBreaches, sym)
			}
		}
	}

	// Check sector-level concentration
	maxSector := input.MaxSectorPct
	if maxSector > 0 && input.SectorMap != nil {
		sectorWeights := make(map[string]float64)
		for sym, w := range input.Weights {
			sector := input.SectorMap[sym]
			if sector == "" {
				sector = "unknown"
			}
			sectorWeights[sector] += math.Abs(w) / absSum
		}
		for sector, sw := range sectorWeights {
			if sw > maxSector {
				result.SectorBreaches[sector] = sw
			}
		}
	}

	return result
}

// ShouldBlockEntry returns true if HHI exceeds the alert threshold,
// indicating new entries should be blocked.
func ShouldBlockEntry(weights map[string]float64, alertThreshold float64) bool {
	if len(weights) == 0 {
		return false
	}
	input := HHIInput{
		Weights:        weights,
		AlertThreshold: alertThreshold,
	}
	result := ComputeHHI(input)
	return result.AboveAlert
}
