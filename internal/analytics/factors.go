package analytics

import "math"

// FactorDecomposition decomposes strategy returns into factor exposures.
// R_strategy = alpha + beta_mkt * R_mkt + beta_mom * R_mom + beta_size * R_size + epsilon
type FactorDecomposition struct {
	Alpha        float64 // intercept — true alpha after factor adjustment
	BetaMarket   float64 // market factor loading (SPY return)
	BetaMomentum float64 // momentum factor loading
	BetaSize     float64 // size factor loading
	RSquared     float64 // fraction of variance explained
	AlphaTStat   float64 // t-statistic on alpha
}

// DecomposeReturns runs OLS regression of strategy returns on factor returns.
// Uses the normal equations: beta = (X'X)^(-1) X'Y.
func DecomposeReturns(stratReturns, mktReturns, momReturns, sizeReturns []float64) FactorDecomposition {
	n := len(stratReturns)
	if n < 20 || n != len(mktReturns) || n != len(momReturns) || n != len(sizeReturns) {
		return FactorDecomposition{}
	}

	// Build X'X (4x4) and X'Y (4x1) where X = [1, mkt, mom, size]
	const p = 4
	var xtx [p][p]float64
	var xty [p]float64

	for i := 0; i < n; i++ {
		x := [p]float64{1.0, mktReturns[i], momReturns[i], sizeReturns[i]}
		for r := 0; r < p; r++ {
			for c := 0; c < p; c++ {
				xtx[r][c] += x[r] * x[c]
			}
			xty[r] += x[r] * stratReturns[i]
		}
	}

	// Solve via Gaussian elimination with partial pivoting
	betas := solveLinearSystem4(xtx, xty)
	if betas == nil {
		return FactorDecomposition{}
	}

	// Compute residuals and R-squared
	var ssRes, ssTot, yMean float64
	for i := 0; i < n; i++ {
		yMean += stratReturns[i]
	}
	yMean /= float64(n)

	for i := 0; i < n; i++ {
		pred := betas[0] + betas[1]*mktReturns[i] + betas[2]*momReturns[i] + betas[3]*sizeReturns[i]
		resid := stratReturns[i] - pred
		ssRes += resid * resid
		ssTot += (stratReturns[i] - yMean) * (stratReturns[i] - yMean)
	}

	rSquared := 0.0
	if ssTot > 0 {
		rSquared = 1.0 - ssRes/ssTot
	}

	// Compute standard error of alpha for t-statistic
	alphaTStat := 0.0
	if n > p {
		mse := ssRes / float64(n-p)
		// SE(alpha) = sqrt(MSE * (X'X)^{-1}[0][0])
		inv := invert4x4(xtx)
		if inv != nil {
			seAlpha := math.Sqrt(mse * inv[0][0])
			if seAlpha > 0 {
				alphaTStat = betas[0] / seAlpha
			}
		}
	}

	return FactorDecomposition{
		Alpha:        betas[0],
		BetaMarket:   betas[1],
		BetaMomentum: betas[2],
		BetaSize:     betas[3],
		RSquared:     rSquared,
		AlphaTStat:   alphaTStat,
	}
}

// solveLinearSystem4 solves a 4x4 system Ax = b via Gaussian elimination with partial pivoting.
func solveLinearSystem4(A [4][4]float64, b [4]float64) []float64 {
	const n = 4
	// Copy augmented matrix
	var aug [n][n + 1]float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			aug[i][j] = A[i][j]
		}
		aug[i][n] = b[i]
	}

	// Forward elimination with partial pivoting
	for col := 0; col < n; col++ {
		// Find pivot
		maxVal := math.Abs(aug[col][col])
		maxRow := col
		for row := col + 1; row < n; row++ {
			if math.Abs(aug[row][col]) > maxVal {
				maxVal = math.Abs(aug[row][col])
				maxRow = row
			}
		}
		if maxVal < 1e-12 {
			return nil // singular
		}

		// Swap rows
		aug[col], aug[maxRow] = aug[maxRow], aug[col]

		// Eliminate below
		for row := col + 1; row < n; row++ {
			factor := aug[row][col] / aug[col][col]
			for j := col; j <= n; j++ {
				aug[row][j] -= factor * aug[col][j]
			}
		}
	}

	// Back substitution
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		x[i] = aug[i][n]
		for j := i + 1; j < n; j++ {
			x[i] -= aug[i][j] * x[j]
		}
		if math.Abs(aug[i][i]) < 1e-12 {
			return nil
		}
		x[i] /= aug[i][i]
	}
	return x
}

// invert4x4 inverts a 4x4 matrix using Gauss-Jordan elimination.
func invert4x4(A [4][4]float64) *[4][4]float64 {
	const n = 4
	var aug [n][2 * n]float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			aug[i][j] = A[i][j]
		}
		aug[i][n+i] = 1.0
	}

	for col := 0; col < n; col++ {
		// Pivot
		maxVal := math.Abs(aug[col][col])
		maxRow := col
		for row := col + 1; row < n; row++ {
			if math.Abs(aug[row][col]) > maxVal {
				maxVal = math.Abs(aug[row][col])
				maxRow = row
			}
		}
		if maxVal < 1e-12 {
			return nil
		}
		aug[col], aug[maxRow] = aug[maxRow], aug[col]

		// Scale pivot row
		scale := aug[col][col]
		for j := 0; j < 2*n; j++ {
			aug[col][j] /= scale
		}

		// Eliminate
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := aug[row][col]
			for j := 0; j < 2*n; j++ {
				aug[row][j] -= factor * aug[col][j]
			}
		}
	}

	var inv [4][4]float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			inv[i][j] = aug[i][n+j]
		}
	}
	return &inv
}
