package telemetry

import "github.com/edwintcloud/momentum-trading-bot/internal/domain"

// CompositeRecorder multiplexes trading events across multiple underlying recorders.
type CompositeRecorder struct {
	recorders []domain.EventRecorder
}

// NewCompositeRecorder returns a recorder that fans out to all provided sinks.
func NewCompositeRecorder(recorders ...domain.EventRecorder) *CompositeRecorder {
	return &CompositeRecorder{
		recorders: recorders,
	}
}

// RecordCandidate delegates to underlying recorders.
func (c *CompositeRecorder) RecordCandidate(candidate domain.Candidate) {
	for _, r := range c.recorders {
		r.RecordCandidate(candidate)
	}
}

func (c *CompositeRecorder) RecordCandidateEvaluation(candidate domain.CandidateEvaluation) {
	for _, r := range c.recorders {
		r.RecordCandidateEvaluation(candidate)
	}
}

// RecordLog delegates to underlying recorders.
func (c *CompositeRecorder) RecordLog(entry domain.LogEntry) {
	for _, r := range c.recorders {
		r.RecordLog(entry)
	}
}

// RecordExecution delegates to underlying recorders.
func (c *CompositeRecorder) RecordExecution(report domain.ExecutionReport) {
	for _, r := range c.recorders {
		r.RecordExecution(report)
	}
}

// RecordClosedTrade delegates to underlying recorders.
func (c *CompositeRecorder) RecordClosedTrade(trade domain.ClosedTrade) {
	for _, r := range c.recorders {
		r.RecordClosedTrade(trade)
	}
}

// RecordDashboard delegates to underlying recorders.
func (c *CompositeRecorder) RecordDashboard(snapshot domain.DashboardSnapshot) {
	for _, r := range c.recorders {
		r.RecordDashboard(snapshot)
	}
}

// RecordIndicatorState delegates to underlying recorders.
func (c *CompositeRecorder) RecordIndicatorState(state domain.IndicatorSnapshot) {
	for _, r := range c.recorders {
		r.RecordIndicatorState(state)
	}
}
