package telemetry

import (
	"log"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

// Logger is a composite event recorder that logs and optionally forwards to storage.
type Logger struct {
	storage domain.EventRecorder
}

// NewLogger creates a telemetry logger. If storage is nil, events are only logged to stdout.
func NewLogger(storage domain.EventRecorder) *Logger {
	return &Logger{
		storage: storage,
	}
}

func (l *Logger) RecordCandidate(c domain.Candidate) {
	log.Printf("[scanner] candidate %s dir=%s price=%.2f gap=%.2f%% score=%.2f",
		c.Symbol, c.Direction, c.Price, c.GapPercent, c.Score)
	if l.storage != nil {
		l.storage.RecordCandidate(c)
	}
}

func (l *Logger) RecordCandidateEvaluation(c domain.CandidateEvaluation) {
	if l.storage != nil {
		l.storage.RecordCandidateEvaluation(c)
	}
}

func (l *Logger) RecordLog(entry domain.LogEntry) {
	log.Printf("[%s] %s: %s", entry.Level, entry.Component, entry.Message)
	if l.storage != nil {
		l.storage.RecordLog(entry)
	}
}

func (l *Logger) RecordExecution(report domain.ExecutionReport) {
	log.Printf("[execution] %s %s %s qty=%d price=%.2f broker=%s status=%s",
		report.Intent, report.PositionSide, report.Symbol, report.Quantity,
		report.Price, report.BrokerOrderID, report.BrokerStatus)
	if l.storage != nil {
		l.storage.RecordExecution(report)
	}
}

func (l *Logger) RecordClosedTrade(trade domain.ClosedTrade) {
	log.Printf("[portfolio] closed %s %s pnl=%.2f R=%.2f reason=%s",
		trade.Side, trade.Symbol, trade.PnL, trade.RMultiple, trade.ExitReason)
	if l.storage != nil {
		l.storage.RecordClosedTrade(trade)
	}
}

func (l *Logger) RecordDashboard(snapshot domain.DashboardSnapshot) {
	if l.storage != nil {
		l.storage.RecordDashboard(snapshot)
	}
}

func (l *Logger) RecordIndicatorState(snapshot domain.IndicatorSnapshot) {
	if l.storage != nil {
		l.storage.RecordIndicatorState(snapshot)
	}
}
