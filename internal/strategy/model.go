package strategy

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

var modelFeatureOrder = []string{
	"gap_percent",
	"relative_volume",
	"price_vs_open_pct",
	"distance_from_high_pct",
	"one_minute_return_pct",
	"three_minute_return_pct",
	"volume_rate",
	"volume_leader_pct",
	"minutes_since_open",
}

// LinearModel predicts short-horizon upside using candidate features.
type LinearModel struct {
	Name          string             `json:"name"`
	Intercept     float64            `json:"intercept"`
	Weights       map[string]float64 `json:"weights"`
	FeatureMeans  map[string]float64 `json:"featureMeans,omitempty"`
	FeatureScales map[string]float64 `json:"featureScales,omitempty"`
}

// TrainingSample binds candidate features to a forward return target.
type TrainingSample struct {
	Candidate        domain.Candidate
	ForwardReturnPct float64
}

// DefaultEntryModel returns a seeded regression that favors continuation entries
// near the high of day with improving short-term momentum and accelerating volume.
func DefaultEntryModel() LinearModel {
	return LinearModel{
		Name:      "seeded-momentum-entry-v5",
		Intercept: -2.60,
		Weights: map[string]float64{
			"gap_percent":             0.04,
			"relative_volume":         0.14,
			"price_vs_open_pct":       0.12,
			"distance_from_high_pct":  -1.10,
			"one_minute_return_pct":   1.05,
			"three_minute_return_pct": 0.65,
			"volume_rate":             0.32,
			"volume_leader_pct":       1.25,
			"minutes_since_open":      -0.01,
		},
	}
}

// Predict returns the expected short-horizon upside in percent.
func (m LinearModel) Predict(candidate domain.Candidate) float64 {
	prediction := m.Intercept
	values := featureValues(candidate)
	for name, value := range values {
		prediction += normalizeFeatureValue(name, value, m.FeatureMeans, m.FeatureScales) * m.Weights[name]
	}
	return prediction
}

// LoadLinearModel reads a JSON-encoded linear model from disk.
func LoadLinearModel(path string) (LinearModel, error) {
	file, err := os.Open(path)
	if err != nil {
		return LinearModel{}, err
	}
	defer file.Close()

	var model LinearModel
	if err := json.NewDecoder(file).Decode(&model); err != nil {
		return LinearModel{}, err
	}
	if model.Weights == nil {
		model.Weights = map[string]float64{}
	}
	return model, nil
}

// SaveLinearModel writes a JSON-encoded linear model to disk.
func SaveLinearModel(path string, model LinearModel) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(model)
}

// TrainLinearModel fits an ordinary least-squares model against the supplied samples.
func TrainLinearModel(samples []TrainingSample) (LinearModel, error) {
	if len(samples) < len(modelFeatureOrder)+1 {
		return LinearModel{}, fmt.Errorf("need at least %d samples to train entry model", len(modelFeatureOrder)+1)
	}

	means, scales := featureStats(samples)
	size := len(modelFeatureOrder) + 1
	xtx := make([][]float64, size)
	xty := make([]float64, size)
	for i := range xtx {
		xtx[i] = make([]float64, size)
	}

	for _, sample := range samples {
		row := []float64{1}
		values := featureValues(sample.Candidate)
		for _, name := range modelFeatureOrder {
			row = append(row, normalizeFeatureValue(name, values[name], means, scales))
		}
		target := clamp(sample.ForwardReturnPct, -15, 20)
		for i := range row {
			xty[i] += row[i] * target
			for j := range row {
				xtx[i][j] += row[i] * row[j]
			}
		}
	}
	addRidgePenalty(xtx, 0.75)

	coefficients, err := solveLinearSystem(xtx, xty)
	if err != nil {
		return LinearModel{}, err
	}

	model := LinearModel{
		Name:          "trained-momentum-entry-v5",
		Intercept:     coefficients[0],
		Weights:       make(map[string]float64, len(modelFeatureOrder)),
		FeatureMeans:  means,
		FeatureScales: scales,
	}
	for index, name := range modelFeatureOrder {
		model.Weights[name] = coefficients[index+1]
	}
	return model, nil
}

func featureValues(candidate domain.Candidate) map[string]float64 {
	return map[string]float64{
		"gap_percent":             clamp(candidate.GapPercent, -10, 35),
		"relative_volume":         clamp(candidate.RelativeVolume, 0, 20),
		"price_vs_open_pct":       clamp(candidate.PriceVsOpenPct, -5, 35),
		"distance_from_high_pct":  clamp(candidate.DistanceFromHighPct, 0, 6),
		"one_minute_return_pct":   clamp(candidate.OneMinuteReturnPct, -3, 6),
		"three_minute_return_pct": clamp(candidate.ThreeMinuteReturnPct, -5, 10),
		"volume_rate":             clamp(candidate.VolumeRate, 0.5, 4),
		"volume_leader_pct":       clamp(candidate.VolumeLeaderPct, 0, 1),
		"minutes_since_open":      clamp(candidate.MinutesSinceOpen, 0, 390),
	}
}

func featureStats(samples []TrainingSample) (map[string]float64, map[string]float64) {
	sums := make(map[string]float64, len(modelFeatureOrder))
	means := make(map[string]float64, len(modelFeatureOrder))
	scales := make(map[string]float64, len(modelFeatureOrder))

	for _, sample := range samples {
		values := featureValues(sample.Candidate)
		for _, name := range modelFeatureOrder {
			sums[name] += values[name]
		}
	}
	for _, name := range modelFeatureOrder {
		means[name] = sums[name] / float64(len(samples))
	}

	for _, sample := range samples {
		values := featureValues(sample.Candidate)
		for _, name := range modelFeatureOrder {
			diff := values[name] - means[name]
			scales[name] += diff * diff
		}
	}
	for _, name := range modelFeatureOrder {
		scale := 1.0
		if len(samples) > 1 {
			scale = sqrt(scales[name] / float64(len(samples)-1))
		}
		if scale < 1e-6 {
			scale = 1
		}
		scales[name] = scale
	}
	return means, scales
}

func normalizeFeatureValue(name string, value float64, means, scales map[string]float64) float64 {
	if len(means) == 0 || len(scales) == 0 {
		return value
	}
	scale := scales[name]
	if scale < 1e-6 {
		return 0
	}
	return clamp((value-means[name])/scale, -4, 4)
}

func addRidgePenalty(matrix [][]float64, alpha float64) {
	if alpha <= 0 {
		return
	}
	for i := 1; i < len(matrix); i++ {
		matrix[i][i] += alpha
	}
}

func solveLinearSystem(matrix [][]float64, vector []float64) ([]float64, error) {
	size := len(vector)
	augmented := make([][]float64, size)
	for i := 0; i < size; i++ {
		augmented[i] = make([]float64, size+1)
		copy(augmented[i], matrix[i])
		augmented[i][size] = vector[i]
	}

	for pivot := 0; pivot < size; pivot++ {
		pivotRow := pivot
		for row := pivot + 1; row < size; row++ {
			if abs(augmented[row][pivot]) > abs(augmented[pivotRow][pivot]) {
				pivotRow = row
			}
		}
		if abs(augmented[pivotRow][pivot]) < 1e-9 {
			return nil, fmt.Errorf("training matrix is singular")
		}
		augmented[pivot], augmented[pivotRow] = augmented[pivotRow], augmented[pivot]

		pivotValue := augmented[pivot][pivot]
		for col := pivot; col <= size; col++ {
			augmented[pivot][col] /= pivotValue
		}
		for row := 0; row < size; row++ {
			if row == pivot {
				continue
			}
			factor := augmented[row][pivot]
			for col := pivot; col <= size; col++ {
				augmented[row][col] -= factor * augmented[pivot][col]
			}
		}
	}

	solution := make([]float64, size)
	for i := 0; i < size; i++ {
		solution[i] = augmented[i][size]
	}
	return solution, nil
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func sqrt(value float64) float64 {
	if value <= 0 {
		return 0
	}
	guess := value
	for i := 0; i < 10; i++ {
		guess = 0.5 * (guess + value/guess)
	}
	return guess
}
