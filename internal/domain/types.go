package domain

import "time"

// EventRecorder persists operator-visible and trading events.
type EventRecorder interface {
	RecordCandidate(Candidate)
	RecordLog(LogEntry)
	RecordExecution(ExecutionReport)
	RecordClosedTrade(ClosedTrade)
	RecordDashboard(DashboardSnapshot)
}

// Tick is a normalized market data event shared across the trading pipeline.
type Tick struct {
	Symbol          string    `json:"symbol"`
	Price           float64   `json:"price"`
	Open            float64   `json:"open"`
	HighOfDay       float64   `json:"highOfDay"`
	Volume          int64     `json:"volume"`
	RelativeVolume  float64   `json:"relativeVolume"`
	GapPercent      float64   `json:"gapPercent"`
	PreMarketVolume int64     `json:"premarketVolume"`
	VolumeSpike     bool      `json:"volumeSpike"`
	Catalyst        string    `json:"catalyst"`
	CatalystURL     string    `json:"catalystUrl"`
	Timestamp       time.Time `json:"timestamp"`
}

// Candidate is a stock that passed the scanner filters.
type Candidate struct {
	Symbol               string    `json:"symbol"`
	Price                float64   `json:"price"`
	Open                 float64   `json:"open"`
	GapPercent           float64   `json:"gapPercent"`
	RelativeVolume       float64   `json:"relativeVolume"`
	PreMarketVolume      int64     `json:"premarketVolume"`
	Volume               int64     `json:"volume"`
	HighOfDay            float64   `json:"highOfDay"`
	PriceVsOpenPct       float64   `json:"priceVsOpenPct"`
	DistanceFromHighPct  float64   `json:"distanceFromHighPct"`
	OneMinuteReturnPct   float64   `json:"oneMinuteReturnPct"`
	ThreeMinuteReturnPct float64   `json:"threeMinuteReturnPct"`
	VolumeRate           float64   `json:"volumeRate"`
	VolumeLeaderPct      float64   `json:"volumeLeaderPct"`
	MinutesSinceOpen     float64   `json:"minutesSinceOpen"`
	Score                float64   `json:"score"`
	Catalyst             string    `json:"catalyst"`
	CatalystURL          string    `json:"catalystUrl"`
	Timestamp            time.Time `json:"timestamp"`
}

// TradeSignal is emitted by the strategy for both entries and exits.
type TradeSignal struct {
	Symbol     string    `json:"symbol"`
	Side       string    `json:"side"`
	Price      float64   `json:"price"`
	Quantity   int64     `json:"quantity"`
	Reason     string    `json:"reason"`
	Confidence float64   `json:"confidence"`
	Timestamp  time.Time `json:"timestamp"`
}

// OrderRequest is an execution-ready order approved by risk checks.
type OrderRequest struct {
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"`
	Price     float64   `json:"price"`
	Quantity  int64     `json:"quantity"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// ExecutionReport represents a broker-confirmed fill.
type ExecutionReport struct {
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"`
	Price         float64   `json:"price"`
	Quantity      int64     `json:"quantity"`
	Reason        string    `json:"reason"`
	BrokerOrderID string    `json:"brokerOrderId"`
	BrokerStatus  string    `json:"brokerStatus"`
	FilledAt      time.Time `json:"filledAt"`
}

// Position is an open portfolio holding.
type Position struct {
	Symbol        string    `json:"symbol"`
	Quantity      int64     `json:"quantity"`
	AvgPrice      float64   `json:"avgPrice"`
	LastPrice     float64   `json:"lastPrice"`
	HighestPrice  float64   `json:"highestPrice"`
	MarketValue   float64   `json:"marketValue"`
	UnrealizedPnL float64   `json:"unrealizedPnL"`
	OpenedAt      time.Time `json:"openedAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ClosedTrade records a completed round-trip trade.
type ClosedTrade struct {
	Symbol     string    `json:"symbol"`
	Quantity   int64     `json:"quantity"`
	EntryPrice float64   `json:"entryPrice"`
	ExitPrice  float64   `json:"exitPrice"`
	PnL        float64   `json:"pnl"`
	OpenedAt   time.Time `json:"openedAt"`
	ClosedAt   time.Time `json:"closedAt"`
	ExitReason string    `json:"exitReason"`
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
	OpenPositions    int       `json:"openPositions"`
	TradesToday      int       `json:"tradesToday"`
	DailyLossLimit   float64   `json:"dailyLossLimit"`
	MaxOpenPositions int       `json:"maxOpenPositions"`
	MaxTradesPerDay  int       `json:"maxTradesPerDay"`
}

// DashboardSnapshot is streamed to the frontend.
type DashboardSnapshot struct {
	Status       StatusSnapshot `json:"status"`
	Candidates   []Candidate    `json:"candidates"`
	Positions    []Position     `json:"positions"`
	ClosedTrades []ClosedTrade  `json:"closedTrades"`
	Logs         []LogEntry     `json:"logs"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}
