package ml

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLinearModel_Predict(t *testing.T) {
	m := &LinearModel{
		Weights: []float64{1.0, -1.0, 0.5},
		Bias:    0.0,
	}

	// Test with known input
	score, err := m.Predict([]float64{1.0, 0.0, 0.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// sigmoid(1.0) ≈ 0.7310585
	expected := sigmoid(1.0)
	if math.Abs(score-expected) > 1e-6 {
		t.Errorf("expected %.6f, got %.6f", expected, score)
	}
}

func TestLinearModel_FeatureMismatch(t *testing.T) {
	m := &LinearModel{
		Weights: []float64{1.0, -1.0},
		Bias:    0.0,
	}

	_, err := m.Predict([]float64{1.0})
	if err == nil {
		t.Error("expected error for feature count mismatch")
	}
}

func TestLinearModel_FeatureCount(t *testing.T) {
	m := &LinearModel{
		Weights: []float64{1.0, -1.0, 0.5},
		Bias:    0.0,
	}
	if m.FeatureCount() != 3 {
		t.Errorf("expected 3, got %d", m.FeatureCount())
	}
}

func TestGradientBoostedStumps_Predict(t *testing.T) {
	m := &GradientBoostedStumps{
		Stumps: []DecisionStump{
			{FeatureIdx: 0, Threshold: 0.5, LeftValue: -1.0, RightValue: 1.0},
			{FeatureIdx: 1, Threshold: 0.3, LeftValue: 0.5, RightValue: -0.5},
		},
		Bias:         0.0,
		LearningRate: 0.1,
	}

	// Feature[0]=1.0 > 0.5 → right (+1.0), Feature[1]=0.1 <= 0.3 → left (+0.5)
	score, err := m.Predict([]float64{1.0, 0.1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// sum = 0.1*1.0 + 0.1*0.5 = 0.15, sigmoid(0.15) ≈ 0.5374
	expected := sigmoid(0.15)
	if math.Abs(score-expected) > 1e-4 {
		t.Errorf("expected ~%.4f, got %.4f", expected, score)
	}
}

func TestGradientBoostedStumps_FeatureCount(t *testing.T) {
	m := &GradientBoostedStumps{
		Stumps: []DecisionStump{
			{FeatureIdx: 0},
			{FeatureIdx: 5},
			{FeatureIdx: 3},
		},
	}
	if m.FeatureCount() != 6 {
		t.Errorf("expected 6, got %d", m.FeatureCount())
	}
}

func TestGradientBoostedStumps_OutOfRange(t *testing.T) {
	m := &GradientBoostedStumps{
		Stumps:       []DecisionStump{{FeatureIdx: 10}},
		LearningRate: 0.1,
	}
	_, err := m.Predict([]float64{1.0})
	if err == nil {
		t.Error("expected error for out-of-range feature index")
	}
}

func TestLoadModel_Linear(t *testing.T) {
	// Create temp model file
	dir := t.TempDir()
	path := filepath.Join(dir, "model.json")

	lm := LinearModel{Weights: []float64{0.5, -0.3, 0.1}, Bias: 0.2}
	modelJSON, _ := json.Marshal(lm)
	mf := ModelFile{Type: "linear", Model: modelJSON}
	data, _ := json.Marshal(mf)
	os.WriteFile(path, data, 0644)

	model, err := LoadModel(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	score, err := model.Predict([]float64{1.0, 1.0, 1.0})
	if err != nil {
		t.Fatalf("predict error: %v", err)
	}
	// 0.5 - 0.3 + 0.1 + 0.2 = 0.5, sigmoid(0.5) ≈ 0.6225
	if score < 0.5 || score > 0.7 {
		t.Errorf("expected score ~0.62, got %f", score)
	}
}

func TestLoadModel_GBT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.json")

	gbt := GradientBoostedStumps{
		Stumps:       []DecisionStump{{FeatureIdx: 0, Threshold: 0.5, LeftValue: -1, RightValue: 1}},
		Bias:         0,
		LearningRate: 0.1,
	}
	modelJSON, _ := json.Marshal(gbt)
	mf := ModelFile{Type: "gbt", Model: modelJSON}
	data, _ := json.Marshal(mf)
	os.WriteFile(path, data, 0644)

	model, err := LoadModel(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.FeatureCount() != 1 {
		t.Errorf("expected 1 feature, got %d", model.FeatureCount())
	}
}

func TestLoadModel_InvalidPath(t *testing.T) {
	_, err := LoadModel("/nonexistent/model.json")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadModel_UnknownType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.json")
	data := []byte(`{"type":"unknown","model":{}}`)
	os.WriteFile(path, data, 0644)

	_, err := LoadModel(path)
	if err == nil {
		t.Error("expected error for unknown model type")
	}
}

func TestModelScorer_ImplementsInterface(t *testing.T) {
	lm := &LinearModel{Weights: []float64{0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1}, Bias: 0}
	ms := NewModelScorer(lm)

	if !ms.Enabled() {
		t.Error("model scorer should be enabled")
	}

	score, err := ms.Score(ScorerFeatures{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All zeros → sigmoid(0) = 0.5
	if math.Abs(score-0.5) > 1e-6 {
		t.Errorf("expected 0.5 for zero features, got %f", score)
	}
}

func TestComputeForwardReturn(t *testing.T) {
	closes := []float64{100, 101, 102, 103, 104, 105}

	ret := ComputeForwardReturn(closes, 0, 3)
	expected := (103 - 100) / 100.0
	if math.Abs(ret-expected) > 1e-10 {
		t.Errorf("expected %.4f, got %.4f", expected, ret)
	}
}

func TestComputeForwardReturn_BeyondEnd(t *testing.T) {
	closes := []float64{100, 110}
	ret := ComputeForwardReturn(closes, 0, 10)
	expected := (110 - 100) / 100.0
	if math.Abs(ret-expected) > 1e-10 {
		t.Errorf("expected %.4f, got %.4f", expected, ret)
	}
}

func TestComputeForwardReturn_Invalid(t *testing.T) {
	ret := ComputeForwardReturn([]float64{100}, -1, 5)
	if ret != 0 {
		t.Errorf("expected 0 for invalid index, got %f", ret)
	}
}

func TestSigmoid(t *testing.T) {
	if math.Abs(sigmoid(0)-0.5) > 1e-10 {
		t.Error("sigmoid(0) should be 0.5")
	}
	if sigmoid(100) > 1.0 || sigmoid(100) < 0.99 {
		t.Error("sigmoid(100) should be ~1.0")
	}
	if sigmoid(-100) < 0 || sigmoid(-100) > 0.01 {
		t.Error("sigmoid(-100) should be ~0.0")
	}
}
