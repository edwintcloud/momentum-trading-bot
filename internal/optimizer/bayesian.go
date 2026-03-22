package optimizer

import (
	"math"
	"math/rand"
	"sort"
)

// BayesianOptimizer uses a simplified GP surrogate with Expected Improvement.
type BayesianOptimizer struct {
	evaluated   []evaluationPoint
	paramRanges []ParameterRange
	rng         *rand.Rand
	exploration int // number of initial random samples before exploitation
}

type evaluationPoint struct {
	params []float64
	score  float64
}

// NewBayesianOptimizer creates a new optimizer with the given parameter ranges.
func NewBayesianOptimizer(paramRanges []ParameterRange, exploration int, seed int64) *BayesianOptimizer {
	if exploration <= 0 {
		exploration = 20
	}
	return &BayesianOptimizer{
		paramRanges: paramRanges,
		rng:         rand.New(rand.NewSource(seed)),
		exploration: exploration,
	}
}

// AddEvaluation records a parameter evaluation result.
func (bo *BayesianOptimizer) AddEvaluation(params []float64, score float64) {
	bo.evaluated = append(bo.evaluated, evaluationPoint{params: params, score: score})
}

// SuggestNext returns the next parameter set to evaluate using Expected Improvement.
func (bo *BayesianOptimizer) SuggestNext() []float64 {
	if len(bo.evaluated) < bo.exploration {
		return bo.randomSample()
	}

	bestScore := bo.BestObserved()
	bestEI := math.Inf(-1)
	var bestCandidate []float64

	// Evaluate EI at 1000 random candidate points
	for c := 0; c < 1000; c++ {
		candidate := bo.randomSample()

		predMean, predVar := bo.predict(candidate)

		// Expected Improvement: EI = (mu - f_best) * Phi(z) + sigma * phi(z)
		sigma := math.Sqrt(math.Max(predVar, 1e-10))
		z := (predMean - bestScore) / sigma
		ei := ExpectedImprovement(predMean, sigma, bestScore, z)

		if ei > bestEI {
			bestEI = ei
			bestCandidate = candidate
		}
	}

	return bestCandidate
}

// ExpectedImprovement computes the EI acquisition function value.
func ExpectedImprovement(predMean, sigma, bestScore, z float64) float64 {
	return (predMean-bestScore)*NormalCDF(z) + sigma*NormalPDF(z)
}

// BestObserved returns the best score seen so far.
func (bo *BayesianOptimizer) BestObserved() float64 {
	if len(bo.evaluated) == 0 {
		return math.Inf(-1)
	}
	best := bo.evaluated[0].score
	for _, ep := range bo.evaluated[1:] {
		if ep.score > best {
			best = ep.score
		}
	}
	return best
}

// predict uses k-NN weighted average as a lightweight GP approximation.
func (bo *BayesianOptimizer) predict(point []float64) (mean, variance float64) {
	k := 10
	if len(bo.evaluated) < k {
		k = len(bo.evaluated)
	}
	if k == 0 {
		return 0, 1.0
	}

	type neighbor struct {
		dist  float64
		score float64
	}
	neighbors := make([]neighbor, len(bo.evaluated))
	for i, ep := range bo.evaluated {
		neighbors[i] = neighbor{
			dist:  normalizedDistance(point, ep.params, bo.paramRanges),
			score: ep.score,
		}
	}
	sort.Slice(neighbors, func(i, j int) bool { return neighbors[i].dist < neighbors[j].dist })

	var weightSum, weightedSum, weightedSumSq float64
	for i := 0; i < k; i++ {
		w := 1.0 / (neighbors[i].dist + 0.001)
		weightSum += w
		weightedSum += w * neighbors[i].score
		weightedSumSq += w * neighbors[i].score * neighbors[i].score
	}

	mean = weightedSum / weightSum
	meanSq := weightedSumSq / weightSum
	variance = meanSq - mean*mean
	if variance < 0 {
		variance = 0
	}

	// Add exploration bonus for points far from observed data
	if len(neighbors) > 0 {
		variance += neighbors[0].dist * 0.1
	}

	return
}

func (bo *BayesianOptimizer) randomSample() []float64 {
	sample := make([]float64, len(bo.paramRanges))
	for i, pr := range bo.paramRanges {
		sample[i] = pr.Min + bo.rng.Float64()*(pr.Max-pr.Min)
	}
	return sample
}

// normalizedDistance computes the Euclidean distance in [0,1]-normalized parameter space.
func normalizedDistance(a, b []float64, ranges []ParameterRange) float64 {
	var sumSq float64
	for i := range a {
		if i >= len(b) || i >= len(ranges) {
			break
		}
		r := ranges[i].Max - ranges[i].Min
		if r <= 0 {
			continue
		}
		diff := (a[i] - b[i]) / r
		sumSq += diff * diff
	}
	return math.Sqrt(sumSq)
}

// NormalCDF computes the standard normal cumulative distribution function.
func NormalCDF(z float64) float64 {
	return 0.5 * (1 + math.Erf(z/math.Sqrt(2)))
}

// NormalPDF computes the standard normal probability density function.
func NormalPDF(z float64) float64 {
	return math.Exp(-0.5*z*z) / math.Sqrt(2*math.Pi)
}
