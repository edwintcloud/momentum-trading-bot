package portfolio

import (
	"math"
)

// MVOInput holds the inputs for mean-variance optimization.
type MVOInput struct {
	Symbols         []string             // ordered list of asset symbols
	ExpectedReturns []float64            // alpha z-scores per symbol
	CovMatrix       [][]float64          // sample covariance matrix (N×N)
	RiskAversion    float64              // λ: higher = more conservative
	MaxPositionPct  float64              // max weight per position (e.g. 0.05)
	MaxSectorPct    float64              // max weight per sector (e.g. 0.25)
	SectorMap       map[string]string    // symbol → sector
	ShrinkageDelta  float64              // Ledoit-Wolf shrinkage intensity [0,1]
}

// MVOResult holds the output of mean-variance optimization.
type MVOResult struct {
	Weights map[string]float64 // target portfolio weights
}

// LedoitWolfShrink applies Ledoit-Wolf shrinkage to a sample covariance matrix.
// Target is the diagonal matrix (each asset's variance on the diagonal, zeros off-diagonal).
// Σ_shrunk = (1-δ)×Σ_sample + δ×Σ_target
func LedoitWolfShrink(covMatrix [][]float64, delta float64) [][]float64 {
	n := len(covMatrix)
	if n == 0 {
		return covMatrix
	}
	if delta < 0 {
		delta = 0
	}
	if delta > 1 {
		delta = 1
	}

	shrunk := make([][]float64, n)
	for i := range shrunk {
		shrunk[i] = make([]float64, n)
		for j := range shrunk[i] {
			if i == j {
				// diagonal: blend sample variance with itself (target diagonal = sample diagonal)
				shrunk[i][j] = covMatrix[i][j]
			} else {
				// off-diagonal: shrink toward zero
				shrunk[i][j] = (1 - delta) * covMatrix[i][j]
			}
		}
	}
	return shrunk
}

// CovarianceMatrix computes the sample covariance matrix from a returns matrix.
// returns[i] is the return series for asset i. All series must have the same length.
func CovarianceMatrix(returns [][]float64) [][]float64 {
	n := len(returns)
	if n == 0 {
		return nil
	}
	t := len(returns[0])
	if t < 2 {
		// Not enough data; return identity-like matrix
		cov := make([][]float64, n)
		for i := range cov {
			cov[i] = make([]float64, n)
			cov[i][i] = 1.0
		}
		return cov
	}

	// Compute means
	means := make([]float64, n)
	for i := 0; i < n; i++ {
		sum := 0.0
		for _, r := range returns[i] {
			sum += r
		}
		means[i] = sum / float64(t)
	}

	// Compute covariance
	cov := make([][]float64, n)
	for i := range cov {
		cov[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := i; j < n; j++ {
			sum := 0.0
			for k := 0; k < t; k++ {
				sum += (returns[i][k] - means[i]) * (returns[j][k] - means[j])
			}
			cov[i][j] = sum / float64(t-1)
			cov[j][i] = cov[i][j]
		}
	}
	return cov
}

// invertMatrix inverts an N×N matrix using Gauss-Jordan elimination.
// Returns nil if the matrix is singular.
func invertMatrix(m [][]float64) [][]float64 {
	n := len(m)
	if n == 0 {
		return nil
	}

	// Create augmented matrix [m | I]
	aug := make([][]float64, n)
	for i := range aug {
		aug[i] = make([]float64, 2*n)
		copy(aug[i][:n], m[i])
		aug[i][n+i] = 1.0
	}

	for col := 0; col < n; col++ {
		// Partial pivoting
		maxRow := col
		maxVal := math.Abs(aug[col][col])
		for row := col + 1; row < n; row++ {
			if math.Abs(aug[row][col]) > maxVal {
				maxVal = math.Abs(aug[row][col])
				maxRow = row
			}
		}
		if maxVal < 1e-12 {
			return nil // singular
		}
		aug[col], aug[maxRow] = aug[maxRow], aug[col]

		// Scale pivot row
		pivot := aug[col][col]
		for j := range aug[col] {
			aug[col][j] /= pivot
		}

		// Eliminate column
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := aug[row][col]
			for j := range aug[row] {
				aug[row][j] -= factor * aug[col][j]
			}
		}
	}

	// Extract inverse
	inv := make([][]float64, n)
	for i := range inv {
		inv[i] = make([]float64, n)
		copy(inv[i], aug[i][n:])
	}
	return inv
}

// matVecMul multiplies an N×N matrix by an N-vector.
func matVecMul(m [][]float64, v []float64) []float64 {
	n := len(m)
	result := make([]float64, n)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			result[i] += m[i][j] * v[j]
		}
	}
	return result
}

// RunMVO performs mean-variance optimization.
// w* = λ⁻¹ · Σ⁻¹ · μ  (unconstrained), then applies position and sector constraints.
func RunMVO(input MVOInput) MVOResult {
	n := len(input.Symbols)
	result := MVOResult{Weights: make(map[string]float64, n)}

	if n == 0 || len(input.ExpectedReturns) != n || len(input.CovMatrix) != n {
		return result
	}

	lambda := input.RiskAversion
	if lambda <= 0 {
		lambda = 1.0
	}

	// Apply Ledoit-Wolf shrinkage
	cov := LedoitWolfShrink(input.CovMatrix, input.ShrinkageDelta)

	// Invert covariance matrix
	covInv := invertMatrix(cov)
	if covInv == nil {
		// Singular matrix — fall back to equal weights
		w := 1.0 / float64(n)
		for _, sym := range input.Symbols {
			result.Weights[sym] = w
		}
		return result
	}

	// Unconstrained optimal: w* = (1/λ) · Σ⁻¹ · μ
	rawWeights := matVecMul(covInv, input.ExpectedReturns)
	for i := range rawWeights {
		rawWeights[i] /= lambda
	}

	// Normalize to sum of absolute values = 1 (for long-short compatibility)
	absSum := 0.0
	for _, w := range rawWeights {
		absSum += math.Abs(w)
	}
	if absSum < 1e-12 {
		// All zero returns — equal weight
		w := 1.0 / float64(n)
		for _, sym := range input.Symbols {
			result.Weights[sym] = w
		}
		return result
	}
	for i := range rawWeights {
		rawWeights[i] /= absSum
	}

	// Apply position and sector constraints via iterative clamping.
	// Clamped weights are frozen, remaining budget is redistributed.
	maxPos := input.MaxPositionPct
	if maxPos <= 0 {
		maxPos = 1.0
	}
	maxSector := input.MaxSectorPct

	for iter := 0; iter < 10; iter++ {
		changed := false

		// Position-level clamp
		for i := range rawWeights {
			if rawWeights[i] > maxPos {
				rawWeights[i] = maxPos
				changed = true
			} else if rawWeights[i] < -maxPos {
				rawWeights[i] = -maxPos
				changed = true
			}
		}

		// Sector-level clamp
		if maxSector > 0 && input.SectorMap != nil {
			sectorWeights := make(map[string]float64)
			for i, sym := range input.Symbols {
				sector := input.SectorMap[sym]
				if sector == "" {
					sector = "unknown"
				}
				sectorWeights[sector] += math.Abs(rawWeights[i])
			}
			for sector, sw := range sectorWeights {
				if sw > maxSector {
					scale := maxSector / sw
					for i, sym := range input.Symbols {
						s := input.SectorMap[sym]
						if s == "" {
							s = "unknown"
						}
						if s == sector {
							rawWeights[i] *= scale
						}
					}
					changed = true
				}
			}
		}

		// Renormalize
		absSum = 0.0
		for _, w := range rawWeights {
			absSum += math.Abs(w)
		}
		if absSum > 1e-12 {
			for i := range rawWeights {
				rawWeights[i] /= absSum
			}
		}

		if !changed {
			break
		}
	}

	for i, sym := range input.Symbols {
		result.Weights[sym] = rawWeights[i]
	}
	return result
}
