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
	"minutes_since_open",
}

// LinearModel predicts short-horizon upside using candidate features.
type LinearModel struct {
	Name      string             `json:"name"`
	Intercept float64            `json:"intercept"`
	Weights   map[string]float64 `json:"weights"`
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
		Name:      "seeded-momentum-entry-v1",
		Intercept: -1.20,
		Weights: map[string]float64{
			"gap_percent":             0.05,
			"relative_volume":         0.18,
			"price_vs_open_pct":       0.22,
			"distance_from_high_pct":  -0.95,
			"one_minute_return_pct":   1.35,
			"three_minute_return_pct": 0.55,
			"volume_rate":             0.40,
			"minutes_since_open":      -0.01,
		},
	}
}

// Predict returns the expected short-horizon upside in percent.
func (m LinearModel) Predict(candidate domain.Candidate) float64 {
	prediction := m.Intercept
	values := featureValues(candidate)
	for name, value := range values {
		prediction += value * m.Weights[name]
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

	size := len(modelFeatureOrder) + 1
	xtx := make([][]float64, size)
	xty := make([]float64, size)
	for i := range xtx {
		xtx[i] = make([]float64, size)
	}

	for _, sample := range samples {
		row := []float64{1}
		for _, name := range modelFeatureOrder {
			row = append(row, featureValues(sample.Candidate)[name])
		}
		for i := range row {
			xty[i] += row[i] * sample.ForwardReturnPct
			for j := range row {
				xtx[i][j] += row[i] * row[j]
			}
		}
	}

	coefficients, err := solveLinearSystem(xtx, xty)
	if err != nil {
		return LinearModel{}, err
	}

	model := LinearModel{
		Name:      "trained-momentum-entry-v1",
		Intercept: coefficients[0],
		Weights:   make(map[string]float64, len(modelFeatureOrder)),
	}
	for index, name := range modelFeatureOrder {
		model.Weights[name] = coefficients[index+1]
	}
	return model, nil
}

func featureValues(candidate domain.Candidate) map[string]float64 {
	return map[string]float64{
		"gap_percent":             candidate.GapPercent,
		"relative_volume":         candidate.RelativeVolume,
		"price_vs_open_pct":       candidate.PriceVsOpenPct,
		"distance_from_high_pct":  candidate.DistanceFromHighPct,
		"one_minute_return_pct":   candidate.OneMinuteReturnPct,
		"three_minute_return_pct": candidate.ThreeMinuteReturnPct,
		"volume_rate":             candidate.VolumeRate,
		"minutes_since_open":      candidate.MinutesSinceOpen,
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
