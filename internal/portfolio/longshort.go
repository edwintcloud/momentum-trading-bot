package portfolio

import (
	"math"
)

// LongShortInput holds the inputs for long-short balance checks.
type LongShortInput struct {
	Positions              map[string]PositionInfo // symbol → position info
	Equity                 float64                 // account equity
	DollarNeutralTolerance float64                 // max |long-short|/max(long,short)
	BetaNeutralThreshold   float64                 // max |beta_long - beta_short|
	MaxGrossLeverage       float64                 // (long + |short|) / equity cap
	SectorNeutralTolerance float64                 // per-sector net exposure tolerance
}

// PositionInfo holds per-position data for long-short analysis.
type PositionInfo struct {
	Side     string  // "long" or "short"
	Notional float64 // absolute notional value
	Beta     float64 // beta vs benchmark
	Sector   string  // sector classification
}

// LongShortResult holds the output of long-short balance analysis.
type LongShortResult struct {
	LongNotional       float64            // total long notional
	ShortNotional      float64            // total short notional
	DollarImbalance    float64            // |long - short| / max(long, short)
	IsDollarNeutral    bool               // within tolerance
	BetaLong           float64            // weighted beta of long positions
	BetaShort          float64            // weighted beta of short positions
	NetBetaImbalance   float64            // |beta_long - beta_short|
	IsBetaNeutral      bool               // within threshold
	GrossLeverage      float64            // (long + short) / equity
	IsLeverageOK       bool               // within max leverage
	SectorImbalances   map[string]float64 // sector → net exposure ratio
	IsSectorNeutral    bool               // all sectors within tolerance
	Adjustments        []Adjustment       // recommended adjustments
}

// Adjustment represents a recommended portfolio adjustment.
type Adjustment struct {
	Type    string // "dollar", "beta", "leverage", "sector"
	Message string // human-readable description
}

// CheckLongShortBalance analyzes portfolio balance across multiple dimensions.
func CheckLongShortBalance(input LongShortInput) LongShortResult {
	result := LongShortResult{
		SectorImbalances: make(map[string]float64),
		IsDollarNeutral:  true,
		IsBetaNeutral:    true,
		IsLeverageOK:     true,
		IsSectorNeutral:  true,
	}

	if len(input.Positions) == 0 {
		return result
	}

	// Aggregate by side
	var longNotional, shortNotional float64
	var longBetaWeighted, shortBetaWeighted float64
	sectorLong := make(map[string]float64)
	sectorShort := make(map[string]float64)

	for _, pos := range input.Positions {
		if pos.Side == "long" {
			longNotional += pos.Notional
			longBetaWeighted += pos.Notional * pos.Beta
			sector := pos.Sector
			if sector == "" {
				sector = "unknown"
			}
			sectorLong[sector] += pos.Notional
		} else {
			shortNotional += pos.Notional
			shortBetaWeighted += pos.Notional * pos.Beta
			sector := pos.Sector
			if sector == "" {
				sector = "unknown"
			}
			sectorShort[sector] += pos.Notional
		}
	}

	result.LongNotional = longNotional
	result.ShortNotional = shortNotional

	// Dollar-neutral check
	maxNotional := math.Max(longNotional, shortNotional)
	tolerance := input.DollarNeutralTolerance
	if tolerance <= 0 {
		tolerance = 0.05
	}
	if maxNotional > 0 {
		result.DollarImbalance = math.Abs(longNotional-shortNotional) / maxNotional
		if result.DollarImbalance > tolerance {
			result.IsDollarNeutral = false
			result.Adjustments = append(result.Adjustments, Adjustment{
				Type:    "dollar",
				Message: "dollar imbalance exceeds tolerance; consider rebalancing long/short notional",
			})
		}
	}

	// Beta-neutral check
	betaThreshold := input.BetaNeutralThreshold
	if betaThreshold <= 0 {
		betaThreshold = 0.30
	}
	if longNotional > 0 {
		result.BetaLong = longBetaWeighted / longNotional
	}
	if shortNotional > 0 {
		result.BetaShort = shortBetaWeighted / shortNotional
	}
	result.NetBetaImbalance = math.Abs(result.BetaLong - result.BetaShort)
	if result.NetBetaImbalance > betaThreshold {
		result.IsBetaNeutral = false
		result.Adjustments = append(result.Adjustments, Adjustment{
			Type:    "beta",
			Message: "beta imbalance exceeds threshold; consider adjusting position betas",
		})
	}

	// Gross leverage check
	maxLeverage := input.MaxGrossLeverage
	if maxLeverage <= 0 {
		maxLeverage = 2.0
	}
	if input.Equity > 0 {
		result.GrossLeverage = (longNotional + shortNotional) / input.Equity
		if result.GrossLeverage > maxLeverage {
			result.IsLeverageOK = false
			result.Adjustments = append(result.Adjustments, Adjustment{
				Type:    "leverage",
				Message: "gross leverage exceeds maximum; reduce positions",
			})
		}
	}

	// Sector-neutral check
	sectorTolerance := input.SectorNeutralTolerance
	if sectorTolerance <= 0 {
		sectorTolerance = 0.10
	}
	allSectors := make(map[string]bool)
	for s := range sectorLong {
		allSectors[s] = true
	}
	for s := range sectorShort {
		allSectors[s] = true
	}
	grossNotional := longNotional + shortNotional
	for sector := range allSectors {
		if grossNotional < 1e-12 {
			continue
		}
		netExposure := (sectorLong[sector] - sectorShort[sector]) / grossNotional
		result.SectorImbalances[sector] = netExposure
		if math.Abs(netExposure) > sectorTolerance {
			result.IsSectorNeutral = false
		}
	}
	if !result.IsSectorNeutral {
		result.Adjustments = append(result.Adjustments, Adjustment{
			Type:    "sector",
			Message: "sector exposure imbalance detected; rebalance sector allocations",
		})
	}

	return result
}
