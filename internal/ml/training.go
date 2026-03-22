package ml

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Model defines the interface for ML-based trade scoring.
// Implementations load serialized weights and run pure-Go inference.
type Model interface {
	// Predict returns a score in [0, 1] for the given feature vector.
	Predict(features []float64) (float64, error)
	// FeatureCount returns the number of features the model expects.
	FeatureCount() int
}

// LinearModel implements a simple linear model: score = sigmoid(w·x + b).
// Weights can be loaded from a JSON file for Go-side inference.
type LinearModel struct {
	Weights []float64 `json:"weights"`
	Bias    float64   `json:"bias"`
}

// Predict computes sigmoid(w·x + b).
func (m *LinearModel) Predict(features []float64) (float64, error) {
	if len(features) != len(m.Weights) {
		return 0, fmt.Errorf("feature count mismatch: got %d, want %d", len(features), len(m.Weights))
	}
	dot := m.Bias
	for i, w := range m.Weights {
		dot += w * features[i]
	}
	return sigmoid(dot), nil
}

// FeatureCount returns the number of weights.
func (m *LinearModel) FeatureCount() int {
	return len(m.Weights)
}

// DecisionStump is a single split on one feature.
type DecisionStump struct {
	FeatureIdx int     `json:"feature_idx"`
	Threshold  float64 `json:"threshold"`
	LeftValue  float64 `json:"left_value"`  // prediction if feature <= threshold
	RightValue float64 `json:"right_value"` // prediction if feature > threshold
}

// GradientBoostedStumps implements a simple gradient-boosted stumps model.
// Each stump contributes additively: score = sigmoid(Σ stump_i(x) + bias).
type GradientBoostedStumps struct {
	Stumps       []DecisionStump `json:"stumps"`
	Bias         float64         `json:"bias"`
	LearningRate float64         `json:"learning_rate"`
}

// Predict computes the ensemble prediction.
func (m *GradientBoostedStumps) Predict(features []float64) (float64, error) {
	sum := m.Bias
	for _, stump := range m.Stumps {
		if stump.FeatureIdx < 0 || stump.FeatureIdx >= len(features) {
			return 0, fmt.Errorf("stump feature index %d out of range [0, %d)", stump.FeatureIdx, len(features))
		}
		if features[stump.FeatureIdx] <= stump.Threshold {
			sum += m.LearningRate * stump.LeftValue
		} else {
			sum += m.LearningRate * stump.RightValue
		}
	}
	return sigmoid(sum), nil
}

// FeatureCount returns the maximum feature index + 1 referenced by stumps.
func (m *GradientBoostedStumps) FeatureCount() int {
	maxIdx := 0
	for _, s := range m.Stumps {
		if s.FeatureIdx > maxIdx {
			maxIdx = s.FeatureIdx
		}
	}
	return maxIdx + 1
}

// ModelFile represents a serialized model with type metadata.
type ModelFile struct {
	Type   string          `json:"type"` // "linear" or "gbt"
	Model  json.RawMessage `json:"model"`
}

// LoadModel loads a model from a JSON file.
func LoadModel(path string) (Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading model file: %w", err)
	}

	var mf ModelFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parsing model file: %w", err)
	}

	switch mf.Type {
	case "linear":
		var m LinearModel
		if err := json.Unmarshal(mf.Model, &m); err != nil {
			return nil, fmt.Errorf("parsing linear model: %w", err)
		}
		return &m, nil
	case "gbt":
		var m GradientBoostedStumps
		if err := json.Unmarshal(mf.Model, &m); err != nil {
			return nil, fmt.Errorf("parsing gbt model: %w", err)
		}
		if m.LearningRate == 0 {
			m.LearningRate = 0.1
		}
		return &m, nil
	default:
		return nil, fmt.Errorf("unknown model type: %q", mf.Type)
	}
}

// ModelScorer wraps a Model to implement the Scorer interface.
type ModelScorer struct {
	model Model
}

// NewModelScorer creates a Scorer backed by the given Model.
func NewModelScorer(m Model) *ModelScorer {
	return &ModelScorer{model: m}
}

// Score returns the model's prediction for the given features.
func (ms *ModelScorer) Score(features ScorerFeatures) (float64, error) {
	return ms.model.Predict(features.ToSlice())
}

// Enabled returns true since a model is loaded.
func (ms *ModelScorer) Enabled() bool {
	return ms.model != nil
}

// TrainingLabel defines the label for a training sample.
type TrainingLabel struct {
	ForwardReturn float64 // return over horizon h
	BinaryLabel   int     // +1 profitable, -1 loss, 0 timeout (from triple barrier)
}

// ComputeForwardReturn computes the return from entry to entry+horizon.
func ComputeForwardReturn(closes []float64, entryIdx, horizon int) float64 {
	if entryIdx < 0 || entryIdx >= len(closes) {
		return 0
	}
	exitIdx := entryIdx + horizon
	if exitIdx >= len(closes) {
		exitIdx = len(closes) - 1
	}
	if closes[entryIdx] == 0 {
		return 0
	}
	return (closes[exitIdx] - closes[entryIdx]) / closes[entryIdx]
}

// PSIRetrainNeeded checks if the PSI between training and live feature
// distributions exceeds the threshold, signaling model staleness.
func PSIRetrainNeeded(trainDist, liveDist []float64, threshold float64) bool {
	psi := ComputePSI(trainDist, liveDist)
	return psi > threshold
}

func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}
