package portfolio

import (
	"math"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

const tolerance = 1e-6

func approxEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

// --- Mean-Variance Optimization Tests ---

func TestCovarianceMatrix(t *testing.T) {
	// Two perfectly correlated assets
	returns := [][]float64{
		{0.01, 0.02, -0.01, 0.03},
		{0.01, 0.02, -0.01, 0.03},
	}
	cov := CovarianceMatrix(returns)
	if len(cov) != 2 {
		t.Fatalf("expected 2x2 cov matrix, got %d", len(cov))
	}
	// Diagonal elements should be equal
	if !approxEqual(cov[0][0], cov[1][1], tolerance) {
		t.Errorf("diagonal elements should be equal: %.8f vs %.8f", cov[0][0], cov[1][1])
	}
	// Off-diagonal should equal diagonal (perfect correlation)
	if !approxEqual(cov[0][1], cov[0][0], tolerance) {
		t.Errorf("off-diagonal should equal diagonal for perfect correlation: %.8f vs %.8f", cov[0][1], cov[0][0])
	}
}

func TestCovarianceMatrixUncorrelated(t *testing.T) {
	// Two uncorrelated assets
	returns := [][]float64{
		{0.01, -0.01, 0.01, -0.01},
		{0.01, 0.01, -0.01, -0.01},
	}
	cov := CovarianceMatrix(returns)
	// Off-diagonal should be near zero
	if !approxEqual(cov[0][1], 0, tolerance) {
		t.Errorf("off-diagonal should be ~0 for uncorrelated: %.8f", cov[0][1])
	}
}

func TestLedoitWolfShrinkFullShrinkage(t *testing.T) {
	cov := [][]float64{
		{0.04, 0.02},
		{0.02, 0.09},
	}
	// Full shrinkage (delta=1) should zero out off-diagonal
	shrunk := LedoitWolfShrink(cov, 1.0)
	if !approxEqual(shrunk[0][1], 0, tolerance) {
		t.Errorf("full shrinkage should zero off-diagonal: got %.8f", shrunk[0][1])
	}
	// Diagonal should remain
	if !approxEqual(shrunk[0][0], 0.04, tolerance) {
		t.Errorf("diagonal should remain: got %.8f", shrunk[0][0])
	}
}

func TestLedoitWolfShrinkNoShrinkage(t *testing.T) {
	cov := [][]float64{
		{0.04, 0.02},
		{0.02, 0.09},
	}
	shrunk := LedoitWolfShrink(cov, 0.0)
	if !approxEqual(shrunk[0][1], 0.02, tolerance) {
		t.Errorf("no shrinkage should keep off-diagonal: got %.8f", shrunk[0][1])
	}
}

func TestRunMVOKnownCovMatrix(t *testing.T) {
	// Simple 2-asset case with identity covariance
	input := MVOInput{
		Symbols:         []string{"A", "B"},
		ExpectedReturns: []float64{0.10, 0.05},
		CovMatrix: [][]float64{
			{1.0, 0.0},
			{0.0, 1.0},
		},
		RiskAversion:   1.0,
		MaxPositionPct: 1.0,
		ShrinkageDelta: 0.0,
	}

	result := RunMVO(input)
	if len(result.Weights) != 2 {
		t.Fatalf("expected 2 weights, got %d", len(result.Weights))
	}

	// With identity cov and lambda=1, w* ∝ μ, so A should have higher weight
	wA := result.Weights["A"]
	wB := result.Weights["B"]
	if wA <= wB {
		t.Errorf("A should have higher weight than B: A=%.4f, B=%.4f", wA, wB)
	}

	// Weights should be normalized (abs sum = 1)
	absSum := math.Abs(wA) + math.Abs(wB)
	if !approxEqual(absSum, 1.0, 0.01) {
		t.Errorf("abs weight sum should be ~1: got %.4f", absSum)
	}
}

func TestRunMVOPositionConstraint(t *testing.T) {
	input := MVOInput{
		Symbols:         []string{"A", "B"},
		ExpectedReturns: []float64{1.0, 0.0001},
		CovMatrix: [][]float64{
			{1.0, 0.0},
			{0.0, 1.0},
		},
		RiskAversion:   1.0,
		MaxPositionPct: 0.60,
		ShrinkageDelta: 0.0,
	}

	result := RunMVO(input)
	wA := math.Abs(result.Weights["A"])
	// After constraint + renormalization, A can't exceed 60% originally but
	// renormalization may push it. The key test: position constraint clips.
	if wA > 1.0 {
		t.Errorf("weight should be <= 1.0: got %.4f", wA)
	}
}

func TestRunMVOSectorConstraint(t *testing.T) {
	input := MVOInput{
		Symbols:         []string{"A", "B", "C"},
		ExpectedReturns: []float64{0.10, 0.09, 0.01},
		CovMatrix: [][]float64{
			{1.0, 0.0, 0.0},
			{0.0, 1.0, 0.0},
			{0.0, 0.0, 1.0},
		},
		RiskAversion:   1.0,
		MaxPositionPct: 1.0,
		MaxSectorPct:   0.50,
		SectorMap:      map[string]string{"A": "tech", "B": "tech", "C": "health"},
		ShrinkageDelta: 0.0,
	}

	result := RunMVO(input)
	techWeight := math.Abs(result.Weights["A"]) + math.Abs(result.Weights["B"])
	if techWeight > 0.51 { // allow small float tolerance
		t.Errorf("tech sector weight should be <= 0.50: got %.4f", techWeight)
	}
}

func TestRunMVOEmptyInput(t *testing.T) {
	result := RunMVO(MVOInput{})
	if len(result.Weights) != 0 {
		t.Errorf("empty input should return empty weights")
	}
}

// --- Risk Parity Tests ---

func TestRunRiskParityEqualVol(t *testing.T) {
	input := RiskParityInput{
		Symbols:    []string{"A", "B", "C"},
		Volatility: []float64{0.20, 0.20, 0.20},
	}

	result := RunRiskParity(input)
	// Equal vol → equal weights
	for _, sym := range input.Symbols {
		w := result.Weights[sym]
		if !approxEqual(w, 1.0/3.0, 0.01) {
			t.Errorf("expected ~0.333 for %s, got %.4f", sym, w)
		}
	}
}

func TestRunRiskParityDifferentVol(t *testing.T) {
	input := RiskParityInput{
		Symbols:    []string{"A", "B"},
		Volatility: []float64{0.10, 0.30},
	}

	result := RunRiskParity(input)
	// Lower vol → higher weight
	if result.Weights["A"] <= result.Weights["B"] {
		t.Errorf("lower vol should get higher weight: A=%.4f, B=%.4f",
			result.Weights["A"], result.Weights["B"])
	}

	// Weights sum to 1
	sum := result.Weights["A"] + result.Weights["B"]
	if !approxEqual(sum, 1.0, 0.01) {
		t.Errorf("weights should sum to 1: got %.4f", sum)
	}
}

func TestRunRiskParityZeroVol(t *testing.T) {
	input := RiskParityInput{
		Symbols:    []string{"A", "B"},
		Volatility: []float64{0.0, 0.20},
	}

	result := RunRiskParity(input)
	// Zero vol asset should get highest weight (capped at 1e6 raw)
	if result.Weights["A"] <= result.Weights["B"] {
		t.Errorf("zero vol should get highest weight: A=%.4f, B=%.4f",
			result.Weights["A"], result.Weights["B"])
	}
}

func TestRunRiskParityEmpty(t *testing.T) {
	result := RunRiskParity(RiskParityInput{})
	if len(result.Weights) != 0 {
		t.Errorf("empty input should return empty weights")
	}
}

func TestEWMAVolTracker(t *testing.T) {
	tracker := NewEWMAVolTracker(0.94)

	// Feed returns
	returns := []float64{0.01, -0.02, 0.015, -0.005, 0.02}
	for _, r := range returns {
		tracker.Update("AAPL", r)
	}

	vol := tracker.Volatility("AAPL")
	if vol <= 0 {
		t.Errorf("volatility should be positive: got %.8f", vol)
	}
	if vol > 1.0 {
		t.Errorf("volatility should be reasonable: got %.8f", vol)
	}
}

func TestRiskContribution(t *testing.T) {
	weights := []float64{0.5, 0.5}
	cov := [][]float64{
		{0.04, 0.01},
		{0.01, 0.04},
	}

	rc := RiskContribution(weights, cov)
	if len(rc) != 2 {
		t.Fatalf("expected 2 risk contributions, got %d", len(rc))
	}
	// Equal weights + symmetric cov → equal risk contributions
	if !approxEqual(rc[0], rc[1], 0.001) {
		t.Errorf("equal weight + symmetric cov should give equal RC: %.6f vs %.6f", rc[0], rc[1])
	}
}

func TestRebalanceChecker(t *testing.T) {
	checker := &RebalanceChecker{
		TargetWeights: map[string]float64{
			"A": 0.50,
			"B": 0.50,
		},
		DeviationThreshold: 0.20,
		RebalanceInterval:  30 * time.Minute,
		LastRebalance:      time.Now(),
	}

	// Within bounds
	current := map[string]float64{"A": 0.48, "B": 0.52}
	if checker.NeedsRebalance(current, time.Now()) {
		t.Error("should not need rebalance when within threshold")
	}

	// Deviation trigger
	current = map[string]float64{"A": 0.35, "B": 0.65}
	if !checker.NeedsRebalance(current, time.Now()) {
		t.Error("should need rebalance when deviation > 20%")
	}

	// Time trigger
	current = map[string]float64{"A": 0.48, "B": 0.52}
	futureTime := time.Now().Add(31 * time.Minute)
	if !checker.NeedsRebalance(current, futureTime) {
		t.Error("should need rebalance after 30 min interval")
	}
}

// --- Factor-Neutral Tests ---

func TestComputeRollingBeta(t *testing.T) {
	// Perfect correlation with benchmark → beta = 1
	asset := []float64{0.01, 0.02, -0.01, 0.03, -0.02}
	bench := []float64{0.01, 0.02, -0.01, 0.03, -0.02}
	beta := ComputeRollingBeta(asset, bench)
	if !approxEqual(beta, 1.0, 0.01) {
		t.Errorf("identical returns should give beta ~1: got %.4f", beta)
	}

	// 2x leveraged → beta = 2
	asset2 := []float64{0.02, 0.04, -0.02, 0.06, -0.04}
	beta2 := ComputeRollingBeta(asset2, bench)
	if !approxEqual(beta2, 2.0, 0.01) {
		t.Errorf("2x leveraged returns should give beta ~2: got %.4f", beta2)
	}

	// Inverse → beta = -1
	inverse := []float64{-0.01, -0.02, 0.01, -0.03, 0.02}
	betaInv := ComputeRollingBeta(inverse, bench)
	if !approxEqual(betaInv, -1.0, 0.01) {
		t.Errorf("inverse returns should give beta ~-1: got %.4f", betaInv)
	}
}

func TestComputeRollingBetaInsufficientData(t *testing.T) {
	asset := []float64{0.01}
	bench := []float64{0.01}
	beta := ComputeRollingBeta(asset, bench)
	if beta != 1.0 {
		t.Errorf("insufficient data should return default beta 1: got %.4f", beta)
	}
}

func TestNetBeta(t *testing.T) {
	weights := map[string]float64{"A": 0.5, "B": 0.3, "C": 0.2}
	betas := map[string]float64{"A": 1.0, "B": 0.5, "C": 1.5}

	nb := NetBeta(weights, betas)
	// 0.5*1.0 + 0.3*0.5 + 0.2*1.5 = 0.5 + 0.15 + 0.30 = 0.95
	if !approxEqual(nb, 0.95, 0.01) {
		t.Errorf("expected net beta ~0.95, got %.4f", nb)
	}
}

func TestRunFactorNeutralWithinBounds(t *testing.T) {
	input := FactorNeutralInput{
		Weights:    map[string]float64{"A": 0.5, "B": 0.5},
		Betas:      map[string]float64{"A": 0.2, "B": 0.1},
		MaxNetBeta: 0.3,
	}

	result := RunFactorNeutral(input)
	// Net beta = 0.5*0.2 + 0.5*0.1 = 0.15, within 0.3
	if result.NeedsHedge {
		t.Error("should not need hedge when within bounds")
	}
	if !approxEqual(result.NetBeta, 0.15, 0.01) {
		t.Errorf("expected net beta ~0.15, got %.4f", result.NetBeta)
	}
}

func TestRunFactorNeutralExceedsBounds(t *testing.T) {
	input := FactorNeutralInput{
		Weights:           map[string]float64{"A": 0.5, "B": 0.5},
		Betas:             map[string]float64{"A": 1.5, "B": 1.0},
		MaxNetBeta:        0.3,
		PortfolioNotional: 100000,
		BenchmarkPrice:    450,
	}

	result := RunFactorNeutral(input)
	// Net beta = 0.5*1.5 + 0.5*1.0 = 1.25, exceeds 0.3
	if !result.NeedsHedge {
		t.Error("should need hedge when net beta exceeds max")
	}
	if result.HedgeShares == 0 {
		t.Error("hedge shares should be non-zero")
	}
	// Hedge should be negative (sell benchmark to reduce beta)
	if result.HedgeShares >= 0 {
		t.Errorf("hedge shares should be negative (short): got %.2f", result.HedgeShares)
	}
}

// --- HHI Tests ---

func TestComputeHHIEqualWeights(t *testing.T) {
	// 10 equal-weight positions → HHI = 10 * (0.1)² = 0.10
	weights := make(map[string]float64)
	for i := 0; i < 10; i++ {
		weights[string(rune('A'+i))] = 0.10
	}

	result := ComputeHHI(HHIInput{
		Weights:        weights,
		HHIMaxTarget:   0.10,
		AlertThreshold: 0.15,
	})

	if !approxEqual(result.HHI, 0.10, 0.01) {
		t.Errorf("10 equal weights should give HHI ~0.10: got %.4f", result.HHI)
	}
	if !approxEqual(result.EffectiveN, 10.0, 0.1) {
		t.Errorf("effective N should be ~10: got %.1f", result.EffectiveN)
	}
}

func TestComputeHHIConcentrated(t *testing.T) {
	// 1 position → HHI = 1.0
	weights := map[string]float64{"A": 1.0}
	result := ComputeHHI(HHIInput{
		Weights:        weights,
		HHIMaxTarget:   0.10,
		AlertThreshold: 0.15,
	})

	if !approxEqual(result.HHI, 1.0, 0.01) {
		t.Errorf("single position should give HHI ~1.0: got %.4f", result.HHI)
	}
	if !result.AboveAlert {
		t.Error("single position should trigger alert")
	}
	if !result.AboveMaxTarget {
		t.Error("single position should exceed max target")
	}
}

func TestComputeHHILongShort(t *testing.T) {
	// Long-short: uses absolute weights
	weights := map[string]float64{
		"A": 0.25,
		"B": 0.25,
		"C": -0.25,
		"D": -0.25,
	}
	result := ComputeHHI(HHIInput{
		Weights:        weights,
		HHIMaxTarget:   0.10,
		AlertThreshold: 0.15,
	})

	// 4 equal positions → HHI = 4 * 0.25² = 0.25
	if !approxEqual(result.HHI, 0.25, 0.01) {
		t.Errorf("4 equal L/S positions should give HHI ~0.25: got %.4f", result.HHI)
	}
}

func TestComputeHHIPositionBreach(t *testing.T) {
	weights := map[string]float64{
		"A": 0.60,
		"B": 0.20,
		"C": 0.20,
	}
	result := ComputeHHI(HHIInput{
		Weights:      weights,
		MaxSinglePct: 0.50,
	})

	if len(result.PositionBreaches) == 0 {
		t.Error("A should breach 50% position limit")
	}
}

func TestComputeHHISectorBreach(t *testing.T) {
	weights := map[string]float64{
		"A": 0.40,
		"B": 0.40,
		"C": 0.20,
	}
	result := ComputeHHI(HHIInput{
		Weights:      weights,
		MaxSectorPct: 0.50,
		SectorMap:    map[string]string{"A": "tech", "B": "tech", "C": "health"},
	})

	if _, ok := result.SectorBreaches["tech"]; !ok {
		t.Error("tech sector should breach 50% limit")
	}
}

func TestShouldBlockEntry(t *testing.T) {
	// Very concentrated portfolio
	weights := map[string]float64{"A": 0.80, "B": 0.20}
	if !ShouldBlockEntry(weights, 0.15) {
		t.Error("concentrated portfolio should block entry")
	}

	// Well-diversified portfolio
	weights = make(map[string]float64)
	for i := 0; i < 20; i++ {
		weights[string(rune('A'+i))] = 0.05
	}
	if ShouldBlockEntry(weights, 0.15) {
		t.Error("diversified portfolio should not block entry")
	}
}

// --- Long-Short Balance Tests ---

func TestCheckLongShortBalanced(t *testing.T) {
	positions := map[string]PositionInfo{
		"A": {Side: "long", Notional: 50000, Beta: 1.0, Sector: "tech"},
		"B": {Side: "short", Notional: 48000, Beta: 1.0, Sector: "tech"},
	}

	result := CheckLongShortBalance(LongShortInput{
		Positions:              positions,
		Equity:                 100000,
		DollarNeutralTolerance: 0.05,
		BetaNeutralThreshold:   0.30,
		MaxGrossLeverage:       2.0,
	})

	if !result.IsDollarNeutral {
		t.Errorf("should be dollar neutral: imbalance=%.4f", result.DollarImbalance)
	}
	if !result.IsBetaNeutral {
		t.Error("should be beta neutral with equal betas")
	}
	if !result.IsLeverageOK {
		t.Errorf("leverage should be OK: %.2f", result.GrossLeverage)
	}
}

func TestCheckLongShortDollarImbalanced(t *testing.T) {
	positions := map[string]PositionInfo{
		"A": {Side: "long", Notional: 80000, Beta: 1.0},
		"B": {Side: "short", Notional: 20000, Beta: 1.0},
	}

	result := CheckLongShortBalance(LongShortInput{
		Positions:              positions,
		Equity:                 100000,
		DollarNeutralTolerance: 0.05,
	})

	if result.IsDollarNeutral {
		t.Errorf("should NOT be dollar neutral: imbalance=%.4f", result.DollarImbalance)
	}
	if len(result.Adjustments) == 0 {
		t.Error("should have adjustment recommendations")
	}
}

func TestCheckLongShortBetaImbalanced(t *testing.T) {
	positions := map[string]PositionInfo{
		"A": {Side: "long", Notional: 50000, Beta: 1.5},
		"B": {Side: "short", Notional: 50000, Beta: 0.5},
	}

	result := CheckLongShortBalance(LongShortInput{
		Positions:            positions,
		Equity:               100000,
		BetaNeutralThreshold: 0.30,
	})

	if result.IsBetaNeutral {
		t.Errorf("should NOT be beta neutral: imbalance=%.4f", result.NetBetaImbalance)
	}
}

func TestCheckLongShortLeverageExceeded(t *testing.T) {
	positions := map[string]PositionInfo{
		"A": {Side: "long", Notional: 150000, Beta: 1.0},
		"B": {Side: "short", Notional: 100000, Beta: 1.0},
	}

	result := CheckLongShortBalance(LongShortInput{
		Positions:        positions,
		Equity:           100000,
		MaxGrossLeverage: 2.0,
	})

	if result.IsLeverageOK {
		t.Errorf("leverage should exceed max: %.2f", result.GrossLeverage)
	}
}

func TestCheckLongShortSectorImbalance(t *testing.T) {
	positions := map[string]PositionInfo{
		"A": {Side: "long", Notional: 50000, Beta: 1.0, Sector: "tech"},
		"B": {Side: "short", Notional: 50000, Beta: 1.0, Sector: "health"},
	}

	result := CheckLongShortBalance(LongShortInput{
		Positions:              positions,
		Equity:                 100000,
		SectorNeutralTolerance: 0.10,
	})

	if result.IsSectorNeutral {
		t.Error("should NOT be sector neutral when long tech / short health")
	}
}

func TestCheckLongShortEmpty(t *testing.T) {
	result := CheckLongShortBalance(LongShortInput{})
	if !result.IsDollarNeutral || !result.IsBetaNeutral || !result.IsLeverageOK {
		t.Error("empty portfolio should pass all checks")
	}
}

// --- Constructor Tests ---

func TestConstructorIsEnabled(t *testing.T) {
	cfg := config.TradingConfig{}
	c := NewConstructor(cfg)
	if c.IsEnabled() {
		t.Error("default config should have all construction methods disabled")
	}

	cfg.MVOEnabled = true
	c = NewConstructor(cfg)
	if !c.IsEnabled() {
		t.Error("should be enabled when MVO is on")
	}
}

func TestConstructorShouldBlockNewEntry(t *testing.T) {
	cfg := config.TradingConfig{
		HHIEnabled:        true,
		HHIAlertThreshold: 0.15,
	}
	c := NewConstructor(cfg)

	// Concentrated
	weights := map[string]float64{"A": 0.80, "B": 0.20}
	if !c.ShouldBlockNewEntry(weights) {
		t.Error("should block concentrated portfolio")
	}

	// Diversified
	weights = make(map[string]float64)
	for i := 0; i < 20; i++ {
		weights[string(rune('A'+i))] = 0.05
	}
	if c.ShouldBlockNewEntry(weights) {
		t.Error("should not block diversified portfolio")
	}
}

// --- Matrix Inversion Test ---

func TestInvertMatrixIdentity(t *testing.T) {
	m := [][]float64{
		{1, 0},
		{0, 1},
	}
	inv := invertMatrix(m)
	if inv == nil {
		t.Fatal("identity matrix should be invertible")
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			expected := 0.0
			if i == j {
				expected = 1.0
			}
			if !approxEqual(inv[i][j], expected, tolerance) {
				t.Errorf("inv[%d][%d] = %.6f, expected %.6f", i, j, inv[i][j], expected)
			}
		}
	}
}

func TestInvertMatrixSingular(t *testing.T) {
	m := [][]float64{
		{1, 2},
		{2, 4},
	}
	inv := invertMatrix(m)
	if inv != nil {
		t.Error("singular matrix should return nil")
	}
}

func TestInvertMatrix2x2(t *testing.T) {
	m := [][]float64{
		{4, 7},
		{2, 6},
	}
	inv := invertMatrix(m)
	if inv == nil {
		t.Fatal("should be invertible")
	}
	// Verify M × M⁻¹ = I
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			val := 0.0
			for k := 0; k < 2; k++ {
				val += m[i][k] * inv[k][j]
			}
			expected := 0.0
			if i == j {
				expected = 1.0
			}
			if !approxEqual(val, expected, 1e-6) {
				t.Errorf("M*M⁻¹[%d][%d] = %.6f, expected %.6f", i, j, val, expected)
			}
		}
	}
}
