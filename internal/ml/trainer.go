package ml

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

type LabeledCandidateRow struct {
	CandidateEvaluation domain.CandidateEvaluation `json:"candidateEvaluation"`
	Label               CandidateOutcomeLabel      `json:"label"`
}

type TrainingSample struct {
	Timestamp    time.Time      `json:"timestamp"`
	TradingDay   string         `json:"tradingDay"`
	Direction    string         `json:"direction"`
	Features     ScorerFeatures `json:"features"`
	Target       float64        `json:"target"`
	ReturnPct    float64        `json:"returnPct"`
	TradeLinked  bool           `json:"tradeLinked"`
	RiskApproved bool           `json:"riskApproved"`
}

type RollingWindowSpec struct {
	TrainDays int `json:"trainDays"`
	ValidDays int `json:"validDays"`
	PurgeDays int `json:"purgeDays"`
	StepDays  int `json:"stepDays"`
}

type TrainingConfig struct {
	WindowSpec      RollingWindowSpec `json:"windowSpec"`
	SampleScope     string            `json:"sampleScope"`
	MinTrainSamples int               `json:"minTrainSamples"`
	MinValidSamples int               `json:"minValidSamples"`
	Epochs          int               `json:"epochs"`
	LearningRate    float64           `json:"learningRate"`
	L2              float64           `json:"l2"`
	CalibrationBins int               `json:"calibrationBins"`
}

type LogisticCalibrationBin struct {
	MinProb      float64 `json:"minProb"`
	MaxProb      float64 `json:"maxProb"`
	EmpiricalWin float64 `json:"empiricalWin"`
	Count        int     `json:"count"`
}

type LogisticModelArtifact struct {
	Version         int                      `json:"version"`
	ModelType       string                   `json:"modelType"`
	GeneratedAt     time.Time                `json:"generatedAt"`
	Side            string                   `json:"side"`
	FeatureNames    []string                 `json:"featureNames"`
	FeatureMeans    []float64                `json:"featureMeans"`
	FeatureStds     []float64                `json:"featureStds"`
	Weights         []float64                `json:"weights"`
	Bias            float64                  `json:"bias"`
	CalibrationBins []LogisticCalibrationBin `json:"calibrationBins"`
	TrainingSamples int                      `json:"trainingSamples"`
	PositiveRate    float64                  `json:"positiveRate"`
	TrainStart      time.Time                `json:"trainStart"`
	TrainEnd        time.Time                `json:"trainEnd"`
}

type WindowMetrics struct {
	Count      int     `json:"count"`
	WinRate    float64 `json:"winRate"`
	BrierScore float64 `json:"brierScore"`
	LogLoss    float64 `json:"logLoss"`
	AvgReturn  float64 `json:"avgReturn"`
}

type RollingWindowResult struct {
	Side       string        `json:"side"`
	TrainStart time.Time     `json:"trainStart"`
	TrainEnd   time.Time     `json:"trainEnd"`
	ValidStart time.Time     `json:"validStart"`
	ValidEnd   time.Time     `json:"validEnd"`
	TrainCount int           `json:"trainCount"`
	ValidCount int           `json:"validCount"`
	Metrics    WindowMetrics `json:"metrics"`
}

type SideTrainingReport struct {
	Side           string                `json:"side"`
	Windows        []RollingWindowResult `json:"windows"`
	Aggregate      WindowMetrics         `json:"aggregate"`
	FinalModelPath string                `json:"finalModelPath,omitempty"`
}

type TrainingReport struct {
	Version         int                           `json:"version"`
	GeneratedAt     time.Time                     `json:"generatedAt"`
	InputPaths      []string                      `json:"inputPaths"`
	Config          TrainingConfig                `json:"config"`
	TradingDays     []string                      `json:"tradingDays"`
	SideReports     map[string]SideTrainingReport `json:"sideReports"`
	AggregateMetric map[string]WindowMetrics      `json:"aggregateMetrics"`
}

func LoadLabeledCandidateRows(paths []string) ([]LabeledCandidateRow, error) {
	rows := make([]LabeledCandidateRow, 0, 4096)
	for _, path := range paths {
		fileRows, err := loadLabeledCandidateFile(path)
		if err != nil {
			return nil, err
		}
		rows = append(rows, fileRows...)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CandidateEvaluation.RecordedAt.Before(rows[j].CandidateEvaluation.RecordedAt)
	})
	return rows, nil
}

func ExtractTrainingSamples(rows []LabeledCandidateRow, sampleScope string) []TrainingSample {
	scope := normalizeSampleScope(sampleScope)
	samples := make([]TrainingSample, 0, len(rows))
	for _, row := range rows {
		if !includeRowForSampleScope(row, scope) {
			continue
		}
		candidate := row.CandidateEvaluation.Candidate
		features := FeaturesFromCandidate(candidate)
		target := 0.0
		if row.Label.Profitable {
			target = 1.0
		}
		samples = append(samples, TrainingSample{
			Timestamp:    row.CandidateEvaluation.RecordedAt,
			TradingDay:   markethours.TradingDay(row.CandidateEvaluation.RecordedAt),
			Direction:    strings.ToLower(strings.TrimSpace(candidate.Direction)),
			Features:     features,
			Target:       target,
			ReturnPct:    row.Label.ReturnPct,
			TradeLinked:  row.Label.TradeLinked,
			RiskApproved: row.Label.RiskApproved,
		})
	}
	return samples
}

func normalizeSampleScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "trade-linked", "trade_linked", "emitted", "strategy-emitted", "strategy_emitted":
		return "trade_linked"
	case "risk-approved", "risk_approved", "approved":
		return "risk_approved"
	default:
		return "all"
	}
}

func includeRowForSampleScope(row LabeledCandidateRow, scope string) bool {
	switch scope {
	case "trade_linked":
		return row.Label.TradeLinked
	case "risk_approved":
		return row.Label.RiskApproved
	default:
		return true
	}
}

func RunRollingWindowTraining(samples []TrainingSample, cfg TrainingConfig) (TrainingReport, map[string]LogisticModelArtifact, error) {
	if cfg.WindowSpec.TrainDays <= 0 || cfg.WindowSpec.ValidDays <= 0 || cfg.WindowSpec.StepDays <= 0 {
		return TrainingReport{}, nil, fmt.Errorf("invalid rolling window configuration")
	}
	cfg.SampleScope = normalizeSampleScope(cfg.SampleScope)
	if cfg.MinTrainSamples <= 0 {
		cfg.MinTrainSamples = 50
	}
	if cfg.MinValidSamples <= 0 {
		cfg.MinValidSamples = 20
	}
	if cfg.Epochs <= 0 {
		cfg.Epochs = 50
	}
	if cfg.LearningRate <= 0 {
		cfg.LearningRate = 0.05
	}
	if cfg.L2 < 0 {
		cfg.L2 = 0
	}
	if cfg.CalibrationBins <= 0 {
		cfg.CalibrationBins = 10
	}

	uniqueDays := uniqueTradingDays(samples)
	report := TrainingReport{
		Version:         1,
		GeneratedAt:     time.Now(),
		Config:          cfg,
		TradingDays:     uniqueDays,
		SideReports:     make(map[string]SideTrainingReport),
		AggregateMetric: make(map[string]WindowMetrics),
	}
	finalModels := make(map[string]LogisticModelArtifact)

	for _, side := range []string{"long", "short"} {
		sideSamples := filterSamplesBySide(samples, side)
		windows := buildRollingWindows(uniqueDays, cfg.WindowSpec)
		sideReport := SideTrainingReport{Side: side}
		var allPreds []predictionSample

		for _, window := range windows {
			trainSamples := filterSamplesByDays(sideSamples, window.TrainDays)
			validSamples := filterSamplesByDays(sideSamples, window.ValidDays)
			if len(trainSamples) < cfg.MinTrainSamples || len(validSamples) < cfg.MinValidSamples {
				continue
			}

			model := trainLogisticModel(side, trainSamples, cfg)
			preds, metrics := evaluateLogisticModel(model, validSamples)
			allPreds = append(allPreds, preds...)

			sideReport.Windows = append(sideReport.Windows, RollingWindowResult{
				Side:       side,
				TrainStart: window.TrainDays[0],
				TrainEnd:   window.TrainDays[len(window.TrainDays)-1],
				ValidStart: window.ValidDays[0],
				ValidEnd:   window.ValidDays[len(window.ValidDays)-1],
				TrainCount: len(trainSamples),
				ValidCount: len(validSamples),
				Metrics:    metrics,
			})
		}

		if len(sideSamples) >= cfg.MinTrainSamples {
			finalModels[side] = trainLogisticModel(side, sideSamples, cfg)
		}
		sideReport.Aggregate = aggregatePredictionMetrics(allPreds)
		report.SideReports[side] = sideReport
		report.AggregateMetric[side] = sideReport.Aggregate
	}

	return report, finalModels, nil
}

func SaveTrainingArtifacts(outDir string, report *TrainingReport, models map[string]LogisticModelArtifact) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for side, model := range models {
		path := filepath.Join(outDir, fmt.Sprintf("%s_model.json", side))
		data, err := json.MarshalIndent(model, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		sideReport := report.SideReports[side]
		sideReport.FinalModelPath = path
		report.SideReports[side] = sideReport
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "training_report.json"), data, 0o644)
}

type rollingWindow struct {
	TrainDays []time.Time
	ValidDays []time.Time
}

func buildRollingWindows(days []string, spec RollingWindowSpec) []rollingWindow {
	if len(days) < spec.TrainDays+spec.PurgeDays+spec.ValidDays {
		return nil
	}
	parsed := make([]time.Time, 0, len(days))
	for _, day := range days {
		if ts, err := time.ParseInLocation("2006-01-02", day, markethours.Location()); err == nil {
			parsed = append(parsed, ts)
		}
	}
	windows := make([]rollingWindow, 0)
	for start := 0; start+spec.TrainDays+spec.PurgeDays+spec.ValidDays <= len(parsed); start += spec.StepDays {
		trainEnd := start + spec.TrainDays
		validStart := trainEnd + spec.PurgeDays
		validEnd := validStart + spec.ValidDays
		windows = append(windows, rollingWindow{
			TrainDays: append([]time.Time(nil), parsed[start:trainEnd]...),
			ValidDays: append([]time.Time(nil), parsed[validStart:validEnd]...),
		})
	}
	return windows
}

func uniqueTradingDays(samples []TrainingSample) []string {
	set := make(map[string]struct{})
	for _, sample := range samples {
		set[sample.TradingDay] = struct{}{}
	}
	days := make([]string, 0, len(set))
	for day := range set {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

func filterSamplesBySide(samples []TrainingSample, side string) []TrainingSample {
	filtered := make([]TrainingSample, 0, len(samples))
	for _, sample := range samples {
		if sample.Direction == side {
			filtered = append(filtered, sample)
		}
	}
	return filtered
}

func filterSamplesByDays(samples []TrainingSample, days []time.Time) []TrainingSample {
	daySet := make(map[string]struct{}, len(days))
	for _, day := range days {
		daySet[day.Format("2006-01-02")] = struct{}{}
	}
	filtered := make([]TrainingSample, 0, len(samples))
	for _, sample := range samples {
		if _, ok := daySet[sample.TradingDay]; ok {
			filtered = append(filtered, sample)
		}
	}
	return filtered
}

type predictionSample struct {
	target float64
	prob   float64
	ret    float64
}

func trainLogisticModel(side string, samples []TrainingSample, cfg TrainingConfig) LogisticModelArtifact {
	featureNames := FeatureNames()
	featureCount := len(featureNames)
	means := make([]float64, featureCount)
	stds := make([]float64, featureCount)
	weights := make([]float64, featureCount)

	for _, sample := range samples {
		values := sample.Features.ToSlice()
		for i, value := range values {
			means[i] += value
		}
	}
	for i := range means {
		means[i] /= float64(len(samples))
	}
	for _, sample := range samples {
		values := sample.Features.ToSlice()
		for i, value := range values {
			diff := value - means[i]
			stds[i] += diff * diff
		}
	}
	for i := range stds {
		stds[i] = math.Sqrt(stds[i] / float64(len(samples)))
		if stds[i] == 0 {
			stds[i] = 1
		}
	}

	bias := 0.0
	for epoch := 0; epoch < cfg.Epochs; epoch++ {
		eta := cfg.LearningRate / (1 + float64(epoch)*0.02)
		for _, sample := range samples {
			values := standardize(sample.Features.ToSlice(), means, stds)
			pred := sigmoid(dot(weights, values) + bias)
			err := pred - sample.Target
			for i := range weights {
				weights[i] -= eta * (err*values[i] + cfg.L2*weights[i])
			}
			bias -= eta * err
		}
	}

	calibration := buildCalibrationBins(weights, bias, means, stds, samples, cfg.CalibrationBins)
	positiveRate := 0.0
	for _, sample := range samples {
		positiveRate += sample.Target
	}
	positiveRate /= float64(len(samples))

	trainStart := samples[0].Timestamp
	trainEnd := samples[len(samples)-1].Timestamp
	return LogisticModelArtifact{
		Version:         1,
		ModelType:       "logistic_sgd_v1",
		GeneratedAt:     time.Now(),
		Side:            side,
		FeatureNames:    featureNames,
		FeatureMeans:    means,
		FeatureStds:     stds,
		Weights:         weights,
		Bias:            bias,
		CalibrationBins: calibration,
		TrainingSamples: len(samples),
		PositiveRate:    positiveRate,
		TrainStart:      trainStart,
		TrainEnd:        trainEnd,
	}
}

func evaluateLogisticModel(model LogisticModelArtifact, samples []TrainingSample) ([]predictionSample, WindowMetrics) {
	preds := make([]predictionSample, 0, len(samples))
	for _, sample := range samples {
		prob := scoreLogisticModel(model, sample.Features)
		preds = append(preds, predictionSample{
			target: sample.Target,
			prob:   prob,
			ret:    sample.ReturnPct,
		})
	}
	return preds, aggregatePredictionMetrics(preds)
}

func aggregatePredictionMetrics(preds []predictionSample) WindowMetrics {
	if len(preds) == 0 {
		return WindowMetrics{}
	}
	var wins, brier, logLoss, avgReturn float64
	for _, pred := range preds {
		if pred.target > 0.5 {
			wins++
		}
		diff := pred.prob - pred.target
		brier += diff * diff
		logLoss += binaryLogLoss(pred.target, pred.prob)
		avgReturn += pred.ret
	}
	n := float64(len(preds))
	return WindowMetrics{
		Count:      len(preds),
		WinRate:    wins / n,
		BrierScore: brier / n,
		LogLoss:    logLoss / n,
		AvgReturn:  avgReturn / n,
	}
}

func scoreLogisticModel(model LogisticModelArtifact, features ScorerFeatures) float64 {
	values := standardize(features.ToSlice(), model.FeatureMeans, model.FeatureStds)
	raw := sigmoid(dot(model.Weights, values) + model.Bias)
	return calibrateProbability(raw, model.CalibrationBins)
}

func buildCalibrationBins(weights []float64, bias float64, means, stds []float64, samples []TrainingSample, bins int) []LogisticCalibrationBin {
	if bins <= 0 {
		bins = 10
	}
	type bucket struct {
		sum   float64
		count int
	}
	acc := make([]bucket, bins)
	for _, sample := range samples {
		raw := sigmoid(dot(weights, standardize(sample.Features.ToSlice(), means, stds)) + bias)
		idx := int(raw * float64(bins))
		if idx >= bins {
			idx = bins - 1
		}
		if idx < 0 {
			idx = 0
		}
		acc[idx].sum += sample.Target
		acc[idx].count++
	}
	out := make([]LogisticCalibrationBin, 0, bins)
	for idx := 0; idx < bins; idx++ {
		empirical := 0.5
		if acc[idx].count > 0 {
			empirical = acc[idx].sum / float64(acc[idx].count)
		}
		out = append(out, LogisticCalibrationBin{
			MinProb:      float64(idx) / float64(bins),
			MaxProb:      float64(idx+1) / float64(bins),
			EmpiricalWin: empirical,
			Count:        acc[idx].count,
		})
	}
	return out
}

func calibrateProbability(prob float64, bins []LogisticCalibrationBin) float64 {
	if len(bins) == 0 {
		return prob
	}
	for _, bin := range bins {
		if prob >= bin.MinProb && prob < bin.MaxProb {
			return bin.EmpiricalWin
		}
	}
	return bins[len(bins)-1].EmpiricalWin
}

func standardize(values, means, stds []float64) []float64 {
	out := make([]float64, len(values))
	for i := range values {
		out[i] = (values[i] - means[i]) / stds[i]
	}
	return out
}

func dot(a, b []float64) float64 {
	sum := 0.0
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func binaryLogLoss(target, prob float64) float64 {
	p := math.Min(math.Max(prob, 1e-6), 1-1e-6)
	return -(target*math.Log(p) + (1-target)*math.Log(1-p))
}

func loadLabeledCandidateFile(path string) ([]LabeledCandidateRow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	rows := make([]LabeledCandidateRow, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row LabeledCandidateRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("parse labeled candidate row %s: %w", path, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
