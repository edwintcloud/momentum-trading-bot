package domain

import "time"

// EventRecorder persists operator-visible and trading events.
type EventRecorder interface {
	RecordCandidate(Candidate)
	RecordLog(LogEntry)
	RecordExecution(ExecutionReport)
	RecordClosedTrade(ClosedTrade)
	RecordDashboard(DashboardSnapshot)
	RecordIndicatorState(IndicatorSnapshot)
}

// TradeLoader can load persisted trades from storage.
type TradeLoader interface {
	LoadTodayClosedTrades() ([]ClosedTrade, error)
}

// HistoryLoader can load historical trade data.
type HistoryLoader interface {
	LoadClosedTradesByDate(date time.Time) ([]ClosedTrade, error)
	ListTradeDates() ([]string, error)
}

// IndicatorSnapshot captures the raw mathematical state of indicators at a point in time.
type IndicatorSnapshot struct {
	Symbol     string             `json:"symbol"`
	Timestamp  time.Time          `json:"timestamp"`
	SignalType string             `json:"signalType"`
	Reason     string             `json:"reason"`
	Indicators map[string]float64 `json:"indicators"`
}

// Tick is a normalized market data event shared across the trading pipeline.
type Tick struct {
	Symbol           string    `json:"symbol"`
	Price            float64   `json:"price"`
	BarOpen          float64   `json:"barOpen"`
	BarHigh          float64   `json:"barHigh"`
	BarLow           float64   `json:"barLow"`
	Open             float64   `json:"open"`
	HighOfDay        float64   `json:"highOfDay"`
	Volume           int64     `json:"volume"`
	RelativeVolume   float64   `json:"relativeVolume"`
	GapPercent       float64   `json:"gapPercent"`
	PreMarketVolume  int64     `json:"premarketVolume"`
	VolumeSpike      bool      `json:"volumeSpike"`
	Float            int64     `json:"float"`         // shares available to trade (0 = unknown)
	PrevDayVolume    int64     `json:"prevDayVolume"` // previous day's total volume (0 = unknown)
	Catalyst         string    `json:"catalyst"`
	CatalystURL      string    `json:"catalystUrl"`
	Timestamp        time.Time `json:"timestamp"`
	FiveMinuteVolume int64     `json:"fiveMinuteVolume"`
}

// Bar is a unified OHLCV bar used across live trading, backtest, and optimizer.
// All bar sources (Alpaca stream, historical CSV, etc.) convert into this type
// before feeding the normalizer.
type Bar struct {
	Symbol      string
	Timestamp   time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      int64
	PrevClose   float64 // optional; overrides tracked previousClose on day flip (0 = use tracked state)
	Catalyst    string  // optional metadata
	CatalystURL string  // optional metadata
}

// Candidate is a stock that passed the scanner filters.
type Candidate struct {
	Symbol                string    `json:"symbol"`
	Direction             string    `json:"direction"`
	Price                 float64   `json:"price"`
	Open                  float64   `json:"open"`
	GapPercent            float64   `json:"gapPercent"`
	RelativeVolume        float64   `json:"relativeVolume"`
	PreMarketVolume       int64     `json:"premarketVolume"`
	Volume                int64     `json:"volume"`
	HighOfDay             float64   `json:"highOfDay"`
	PriceVsOpenPct        float64   `json:"priceVsOpenPct"`
	DistanceFromHighPct   float64   `json:"distanceFromHighPct"`
	OneMinuteReturnPct    float64   `json:"oneMinuteReturnPct"`
	ThreeMinuteReturnPct  float64   `json:"threeMinuteReturnPct"`
	VolumeRate            float64   `json:"volumeRate"`
	VolumeLeaderPct       float64   `json:"volumeLeaderPct"`
	LeaderRank            int       `json:"leaderRank"`
	MinutesSinceOpen      float64   `json:"minutesSinceOpen"`
	ATR                   float64   `json:"atr"`
	ATRPct                float64   `json:"atrPct"`
	VWAP                  float64   `json:"vwap"`
	PriceVsVWAPPct        float64   `json:"priceVsVwapPct"`
	BreakoutPct           float64   `json:"breakoutPct"`
	ConsolidationRangePct float64   `json:"consolidationRangePct"`
	PullbackDepthPct      float64   `json:"pullbackDepthPct"`
	CloseOffHighPct       float64   `json:"closeOffHighPct"`
	SetupHigh             float64   `json:"setupHigh"`
	SetupLow              float64   `json:"setupLow"`
	RSI                   float64   `json:"rsi"`
	RSIMASlope            float64   `json:"rsiMASlope"`
	FiveMinRange          float64   `json:"fiveMinRange"`
	PriceVsEMA9Pct        float64   `json:"priceVsEma9Pct"`
	EMAFast               float64   `json:"emaFast"`
	EMASlow               float64   `json:"emaSlow"`
	MACDHistogram         float64   `json:"macdHistogram"`
	IntradayReturnPct     float64   `json:"intradayReturnPct"`
	SetupType             string    `json:"setupType"`
	Score                 float64   `json:"score"`
	MarketRegime          string    `json:"marketRegime"`
	RegimeConfidence      float64   `json:"regimeConfidence"`
	Playbook              string    `json:"playbook"`
	Float                 int64     `json:"float"`
	PrevDayVolume         int64     `json:"prevDayVolume"`
	Sector                string    `json:"sector"`
	Catalyst              string    `json:"catalyst"`
	CatalystURL           string    `json:"catalystUrl"`
	Timestamp             time.Time `json:"timestamp"`
}

// TradeSignal is emitted by the strategy for both entries and exits.
type TradeSignal struct {
	Symbol           string    `json:"symbol"`
	Side             string    `json:"side"`
	Intent           string    `json:"intent"`
	PositionSide     string    `json:"positionSide"`
	Price            float64   `json:"price"`
	Quantity         int64     `json:"quantity"`
	StopPrice        float64   `json:"stopPrice"`
	RiskPerShare     float64   `json:"riskPerShare"`
	EntryATR         float64   `json:"entryAtr"`
	SetupType        string    `json:"setupType"`
	Reason           string    `json:"reason"`
	Confidence       float64   `json:"confidence"`
	OrderType        string    `json:"orderType"` // "market" or "" (default=limit)
	MarketRegime     string    `json:"marketRegime"`
	RegimeConfidence float64   `json:"regimeConfidence"`
	Playbook         string    `json:"playbook"`
	Sector           string    `json:"sector"`
	AvgDailyVolume   float64   `json:"avgDailyVolume"`
	Timestamp        time.Time `json:"timestamp"`
}

// OrderRequest is an execution-ready order approved by risk checks.
type OrderRequest struct {
	Symbol             string    `json:"symbol"`
	Side               string    `json:"side"`
	Intent             string    `json:"intent"`
	PositionSide       string    `json:"positionSide"`
	Price              float64   `json:"price"`
	Quantity           int64     `json:"quantity"`
	StopPrice          float64   `json:"stopPrice"`
	RiskPerShare       float64   `json:"riskPerShare"`
	EntryATR           float64   `json:"entryAtr"`
	SetupType          string    `json:"setupType"`
	Reason             string    `json:"reason"`
	OrderType          string    `json:"orderType"`          // "limit" (default) or "market"
	SlippageMultiplier float64   `json:"slippageMultiplier"` // multiplier for limit price slippage (1.0 = normal, 2.0 = 2x wider, etc.)
	MarketRegime       string    `json:"marketRegime"`
	RegimeConfidence   float64   `json:"regimeConfidence"`
	Playbook           string    `json:"playbook"`
	Sector             string    `json:"sector"`
	AvgDailyVolume     float64   `json:"avgDailyVolume"`
	Timestamp          time.Time `json:"timestamp"`
}

// ExecutionReport represents a broker-confirmed fill.
type ExecutionReport struct {
	Symbol           string    `json:"symbol"`
	Side             string    `json:"side"`
	Intent           string    `json:"intent"`
	PositionSide     string    `json:"positionSide"`
	Price            float64   `json:"price"`
	Quantity         int64     `json:"quantity"`
	StopPrice        float64   `json:"stopPrice"`
	RiskPerShare     float64   `json:"riskPerShare"`
	EntryATR         float64   `json:"entryAtr"`
	SetupType        string    `json:"setupType"`
	Reason           string    `json:"reason"`
	MarketRegime     string    `json:"marketRegime"`
	RegimeConfidence float64   `json:"regimeConfidence"`
	Playbook         string    `json:"playbook"`
	Sector           string    `json:"sector"`
	BrokerOrderID    string    `json:"brokerOrderId"`
	BrokerStatus     string    `json:"brokerStatus"`
	FilledAt         time.Time `json:"filledAt"`
}

// Position is an open portfolio holding.
type Position struct {
	Symbol           string    `json:"symbol"`
	Side             string    `json:"side"`
	Quantity         int64     `json:"quantity"`
	OriginalQuantity int64     `json:"originalQuantity"`
	PartialsExecuted int       `json:"partialsExecuted"`
	AvgPrice         float64   `json:"avgPrice"`
	StopPrice        float64   `json:"stopPrice"`
	InitialStopPrice float64   `json:"initialStopPrice"`
	RiskPerShare     float64   `json:"riskPerShare"`
	EntryATR         float64   `json:"entryAtr"`
	SetupType        string    `json:"setupType"`
	MarketRegime     string    `json:"marketRegime"`
	RegimeConfidence float64   `json:"regimeConfidence"`
	Playbook         string    `json:"playbook"`
	Sector           string    `json:"sector"`
	LastPrice        float64   `json:"lastPrice"`
	HighestPrice     float64   `json:"highestPrice"`
	LowestPrice      float64   `json:"lowestPrice"`
	MarketValue      float64   `json:"marketValue"`
	UnrealizedPnL    float64   `json:"unrealizedPnL"`
	BrokerSeeded     bool      `json:"brokerSeeded"`
	OpenedAt         time.Time `json:"openedAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ClosedTrade records a completed round-trip trade.
type ClosedTrade struct {
	Symbol           string    `json:"symbol"`
	Side             string    `json:"side"`
	Quantity         int64     `json:"quantity"`
	EntryPrice       float64   `json:"entryPrice"`
	ExitPrice        float64   `json:"exitPrice"`
	PnL              float64   `json:"pnl"`
	RMultiple        float64   `json:"rMultiple"`
	MFER             float64   `json:"mfeR"`  // max favorable excursion in R-multiples
	MAER             float64   `json:"maeR"`  // max adverse excursion in R-multiples
	SetupType        string    `json:"setupType"`
	OpenedAt         time.Time `json:"openedAt"`
	ClosedAt         time.Time `json:"closedAt"`
	ExitReason       string    `json:"exitReason"`
	MarketRegime     string    `json:"marketRegime"`
	RegimeConfidence float64   `json:"regimeConfidence"`
	Playbook         string    `json:"playbook"`
	Sector           string    `json:"sector"`
}

// LogEntry is a structured operational event.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Message   string    `json:"message"`
}

// StatusSnapshot captures current system health and trading metrics.
type StatusSnapshot struct {
	Running          bool      `json:"running"`
	Paused           bool      `json:"paused"`
	EmergencyStop    bool      `json:"emergencyStop"`
	LastUpdate       time.Time `json:"lastUpdate"`
	StartingCapital  float64   `json:"startingCapital"`
	BrokerEquity     float64   `json:"brokerEquity"`
	DayPnL           float64   `json:"dayPnL"`
	RealizedPnL      float64   `json:"realizedPnL"`
	UnrealizedPnL    float64   `json:"unrealizedPnL"`
	NetPnL           float64   `json:"netPnL"`
	Exposure         float64   `json:"exposure"`
	LongExposure     float64   `json:"longExposure"`
	ShortExposure    float64   `json:"shortExposure"`
	OpenPositions    int       `json:"openPositions"`
	TradesToday      int       `json:"tradesToday"`
	EntriesToday     int       `json:"entriesToday"`
	DailyLossLimit   float64   `json:"dailyLossLimit"`
	MaxOpenPositions int       `json:"maxOpenPositions"`
	MaxTradesPerDay  int       `json:"maxTradesPerDay"`
	ActiveProfile    string    `json:"activeProfile"`
	ActiveVersion    string    `json:"activeVersion"`
	PendingProfile   string    `json:"pendingProfile"`
	PendingVersion   string    `json:"pendingVersion"`
	LastOptimizerRun time.Time `json:"lastOptimizerRun,omitempty"`
	PaperValidation  string    `json:"paperValidation"`
	CurrentRegime    string    `json:"currentRegime"`
	RegimeConfidence float64   `json:"regimeConfidence"`
}

// MarketRegimeBenchmark holds a single benchmark's regime metrics.
type MarketRegimeBenchmark struct {
	Symbol            string  `json:"symbol"`
	PriceVsVwapPct    float64 `json:"priceVsVwapPct"`
	EMAFast           float64 `json:"emaFast"`
	EMASlow           float64 `json:"emaSlow"`
	ReturnLookbackPct float64 `json:"returnLookbackPct"`
}

// MarketRegimeSnapshot captures the regime state for the dashboard.
type MarketRegimeSnapshot struct {
	Regime     string                  `json:"regime"`
	Confidence float64                 `json:"confidence"`
	Benchmarks []MarketRegimeBenchmark `json:"benchmarks"`
	Timestamp  time.Time               `json:"timestamp"`
}

// DashboardSnapshot is streamed to the frontend.
type DashboardSnapshot struct {
	Status       StatusSnapshot       `json:"status"`
	MarketRegime MarketRegimeSnapshot `json:"marketRegime"`
	Candidates   []Candidate          `json:"candidates"`
	Positions    []Position           `json:"positions"`
	ClosedTrades []ClosedTrade        `json:"closedTrades"`
	Logs         []LogEntry           `json:"logs"`
	UpdatedAt    time.Time            `json:"updatedAt"`
}

// PerformanceMetrics provides trading performance statistics.
type PerformanceMetrics struct {
	TotalTrades   int     `json:"totalTrades"`
	WinRate       float64 `json:"winRate"`
	AvgWin        float64 `json:"avgWin"`
	AvgLoss       float64 `json:"avgLoss"`
	ProfitFactor  float64 `json:"profitFactor"`
	SharpeRatio   float64 `json:"sharpeRatio"`
	SortinoRatio  float64 `json:"sortinoRatio"`
	MaxDrawdown   float64 `json:"maxDrawdown"`
	AvgRMultiple  float64 `json:"avgRMultiple"`
	ExpectancyR   float64 `json:"expectancyR"`
	LargestWin    float64 `json:"largestWin"`
	LargestLoss   float64 `json:"largestLoss"`
	AvgHoldTimeMs int64   `json:"avgHoldTimeMs"`
}
