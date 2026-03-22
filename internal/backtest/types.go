package backtest

import "time"

// InputBar is an external bar shape accepted by the backtest engine.
type InputBar struct {
	Timestamp   time.Time
	Symbol      string
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      int64
	PrevClose   float64
	Catalyst    string
	CatalystURL string
}

// RunConfig controls a historical simulation.
type RunConfig struct {
	DataPath           string
	Bars               []InputBar
	Start              time.Time
	End                time.Time
	TrainStart         time.Time
	TrainEnd           time.Time
	LabelLookaheadBars int
	ModelOutputPath    string
}

// Diagnostics summarizes how bars moved through the backtest decision funnel.
type Diagnostics struct {
	BarsLoaded         int
	BarsInWindow       int
	EntryCandidates    int
	EntrySignals       int
	EntryRiskApproved  int
	ExitChecks         int
	ExitSignals        int
	ExitRiskApproved   int
	TrainingCandidates int
	TrainingSamples    int
	TrainingRuns       int
	ScannerRejects     map[string]int
	EntryRejects       map[string]int
	EntryRiskRejects   map[string]int
	ExitRejects        map[string]int
	ExitRiskRejects    map[string]int
	EntrySignalSamples []EntrySample
	EntryRejectSamples map[string]EntrySample
}

// EntrySample captures a representative entry decision for diagnostics.
type EntrySample struct {
	Symbol                  string
	Timestamp               time.Time
	Reason                  string
	Price                   float64
	GapPercent              float64
	RelativeVolume          float64
	PriceVsOpenPct          float64
	DistanceFromHighPct     float64
	AllowedDistanceHighPct  float64
	OneMinuteReturnPct      float64
	ThreeMinuteReturnPct    float64
	VolumeRate              float64
	VolumeLeaderPct         float64
	LeaderRank              int
	ATRPct                  float64
	PriceVsVWAPPct          float64
	BreakoutPct             float64
	SetupType               string
	Score                   float64
	PredictedReturnPct      float64
	RequiredPredictedRetPct float64
	StrongSqueeze           bool
}

// EntrySampleReject captures a rejected entry for diagnostics.
type EntrySampleReject = EntrySample

// RunResult extends Result with diagnostics from the full-market backtest engine.
type RunResult struct {
	Result
	ModelName            string
	ModelTrainingWarning string
	Diagnostics          Diagnostics
	OpenPositionsAtEnd   int
	Wins                 int
	Losses               int
	AvgWinPnL            float64
	AvgLossPnL           float64
	AvgWinR              float64
	AvgLossR             float64
	AvgMFER              float64
	AvgMAER              float64
	TrailingStopExitPct  float64
	AvgTimeToStopMin     float64
	ClosedTrades         []ClosedTradeEntry
}

// ClosedTradeEntry captures a single closed trade for reporting.
type ClosedTradeEntry struct {
	Symbol     string
	Quantity   int64
	EntryPrice float64
	ExitPrice  float64
	PnL        float64
	ExitReason string
	OpenedAt   time.Time
	ClosedAt   time.Time
}
