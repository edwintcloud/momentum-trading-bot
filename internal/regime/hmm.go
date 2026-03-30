package regime

import (
	"math"
	"sync"
)

// HMMRegimeDetector implements a 2-state (bull/bear) Hidden Markov Model.
// Uses pre-trained transition and emission parameters, applies the forward algorithm in real-time.
type HMMRegimeDetector struct {
	mu               sync.RWMutex
	numStates        int
	transitionMatrix [][]float64 // A[i][j] = P(state_j | state_i)
	emissionMeans    []float64   // mean return per state
	emissionStddevs  []float64   // stddev return per state
	statePrior       []float64   // initial state probabilities
	forwardProbs     []float64   // current forward probabilities (real-time)
	returnHistory    []float64   // recent returns for inference
}

// NewHMMRegimeDetector creates a 2-state HMM with default parameters.
func NewHMMRegimeDetector() *HMMRegimeDetector {
	return &HMMRegimeDetector{
		numStates: 2,
		transitionMatrix: [][]float64{
			{0.98, 0.02}, // bull → bull 98%, bull → bear 2%
			{0.03, 0.97}, // bear → bull 3%, bear → bear 97%
		},
		emissionMeans:   []float64{0.0005, -0.0003}, // bull: +5bps/bar, bear: -3bps/bar
		emissionStddevs: []float64{0.008, 0.015},    // bear has higher vol
		statePrior:      []float64{0.6, 0.4},
		forwardProbs:    []float64{0.6, 0.4},
	}
}

// Update processes a new return observation and updates state probabilities
// via a single forward algorithm step.
func (h *HMMRegimeDetector) Update(ret float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.returnHistory = append(h.returnHistory, ret)
	if len(h.returnHistory) > 1000 {
		h.returnHistory = h.returnHistory[1:]
	}

	// Forward algorithm step:
	// alpha_t(j) = [sum_i alpha_{t-1}(i) * A(i,j)] * B(j, obs_t)
	newProbs := make([]float64, h.numStates)
	var sum float64

	for j := 0; j < h.numStates; j++ {
		var transSum float64
		for i := 0; i < h.numStates; i++ {
			transSum += h.forwardProbs[i] * h.transitionMatrix[i][j]
		}
		emission := gaussianPDF(ret, h.emissionMeans[j], h.emissionStddevs[j])
		newProbs[j] = transSum * emission
		sum += newProbs[j]
	}

	// Normalize
	if sum > 0 {
		for j := range newProbs {
			newProbs[j] /= sum
		}
	} else {
		// All emissions were zero (extreme outlier) — reset to prior to avoid permanent degeneration
		copy(newProbs, h.statePrior)
	}
	h.forwardProbs = newProbs
}

// CurrentRegime returns the most likely current regime and its probability.
func (h *HMMRegimeDetector) CurrentRegime() (string, float64) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	bullProb := h.forwardProbs[0]
	bearProb := h.forwardProbs[1]

	if bullProb > bearProb {
		return "bullish", bullProb
	}
	return "bearish", bearProb
}

// ForwardProbs returns a copy of the current forward probability vector.
func (h *HMMRegimeDetector) ForwardProbs() []float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]float64, len(h.forwardProbs))
	copy(out, h.forwardProbs)
	return out
}

// Reset restores the HMM to its initial prior state.
func (h *HMMRegimeDetector) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	copy(h.forwardProbs, h.statePrior)
	h.returnHistory = h.returnHistory[:0]
}

// gaussianPDF evaluates the Gaussian probability density function.
func gaussianPDF(x, mean, stddev float64) float64 {
	if stddev <= 0 {
		return 0
	}
	z := (x - mean) / stddev
	return math.Exp(-0.5*z*z) / (stddev * math.Sqrt(2*math.Pi))
}
