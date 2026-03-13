package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

// Recorder persists trading events asynchronously into PostgreSQL.
type Recorder struct {
	pool   *pgxpool.Pool
	events chan any
}

// Ping verifies the database connection.
func (r *Recorder) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

// NewRecorder creates the PostgreSQL pool, initializes schema, and starts the writer loop.
func NewRecorder(ctx context.Context, databaseURL string) (*Recorder, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	recorder := &Recorder{
		pool:   pool,
		events: make(chan any, 2048),
	}
	if err := recorder.initSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	go recorder.loop(ctx)
	return recorder, nil
}

// Close releases the database pool.
func (r *Recorder) Close() {
	if r.pool != nil {
		r.pool.Close()
	}
}

// RecordCandidate queues a candidate event.
func (r *Recorder) RecordCandidate(candidate domain.Candidate) {
	r.enqueue(candidate)
}

// RecordLog queues a log event.
func (r *Recorder) RecordLog(entry domain.LogEntry) {
	r.enqueue(entry)
}

// RecordExecution queues an execution report.
func (r *Recorder) RecordExecution(report domain.ExecutionReport) {
	r.enqueue(report)
}

// RecordClosedTrade queues a closed trade event.
func (r *Recorder) RecordClosedTrade(trade domain.ClosedTrade) {
	r.enqueue(trade)
}

// RecordDashboard queues a dashboard snapshot.
func (r *Recorder) RecordDashboard(snapshot domain.DashboardSnapshot) {
	r.enqueue(snapshot)
}

func (r *Recorder) enqueue(event any) {
	select {
	case r.events <- event:
	default:
	}
}

func (r *Recorder) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.drain()
			return
		case event, ok := <-r.events:
			if !ok {
				return
			}
			_ = r.persist(context.Background(), event)
		}
	}
}

func (r *Recorder) drain() {
	for {
		select {
		case event := <-r.events:
			_ = r.persist(context.Background(), event)
		default:
			return
		}
	}
}

func (r *Recorder) persist(ctx context.Context, event any) error {
	switch value := event.(type) {
	case domain.LogEntry:
		_, err := r.pool.Exec(ctx, `
			INSERT INTO system_logs (logged_at, level, component, message)
			VALUES ($1, $2, $3, $4)
		`, value.Timestamp, value.Level, value.Component, value.Message)
		return err
	case domain.Candidate:
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = r.pool.Exec(ctx, `
			INSERT INTO scanner_candidates (symbol, candidate_at, payload)
			VALUES ($1, $2, $3::jsonb)
		`, value.Symbol, value.Timestamp, payload)
		return err
	case domain.ExecutionReport:
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = r.pool.Exec(ctx, `
			INSERT INTO execution_reports (symbol, side, filled_at, payload)
			VALUES ($1, $2, $3, $4::jsonb)
		`, value.Symbol, value.Side, value.FilledAt, payload)
		return err
	case domain.ClosedTrade:
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = r.pool.Exec(ctx, `
			INSERT INTO closed_trades (symbol, closed_at, payload)
			VALUES ($1, $2, $3::jsonb)
		`, value.Symbol, value.ClosedAt, payload)
		return err
	case domain.DashboardSnapshot:
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = r.pool.Exec(ctx, `
			INSERT INTO dashboard_snapshots (captured_at, payload)
			VALUES ($1, $2::jsonb)
		`, value.UpdatedAt, payload)
		return err
	default:
		return fmt.Errorf("unsupported event type %T", event)
	}
}

func (r *Recorder) initSchema(ctx context.Context) error {
	statements := []string{
		`
		CREATE TABLE IF NOT EXISTS system_logs (
			id BIGSERIAL PRIMARY KEY,
			logged_at TIMESTAMPTZ NOT NULL,
			level TEXT NOT NULL,
			component TEXT NOT NULL,
			message TEXT NOT NULL
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS scanner_candidates (
			id BIGSERIAL PRIMARY KEY,
			symbol TEXT NOT NULL,
			candidate_at TIMESTAMPTZ NOT NULL,
			payload JSONB NOT NULL
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS execution_reports (
			id BIGSERIAL PRIMARY KEY,
			symbol TEXT NOT NULL,
			side TEXT NOT NULL,
			filled_at TIMESTAMPTZ NOT NULL,
			payload JSONB NOT NULL
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS closed_trades (
			id BIGSERIAL PRIMARY KEY,
			symbol TEXT NOT NULL,
			closed_at TIMESTAMPTZ NOT NULL,
			payload JSONB NOT NULL
		)
		`,
		`
		CREATE TABLE IF NOT EXISTS dashboard_snapshots (
			id BIGSERIAL PRIMARY KEY,
			captured_at TIMESTAMPTZ NOT NULL,
			payload JSONB NOT NULL
		)
		`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_logged_at ON system_logs (logged_at)`,
		`CREATE INDEX IF NOT EXISTS idx_scanner_candidates_symbol_at ON scanner_candidates (symbol, candidate_at)`,
		`CREATE INDEX IF NOT EXISTS idx_execution_reports_symbol_at ON execution_reports (symbol, filled_at)`,
		`CREATE INDEX IF NOT EXISTS idx_closed_trades_symbol_at ON closed_trades (symbol, closed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_dashboard_snapshots_captured_at ON dashboard_snapshots (captured_at)`,
	}
	for _, statement := range statements {
		if _, err := r.pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
