package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

// PostgresStore persists events to PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore connects to PostgreSQL and ensures schema exists.
func NewPostgresStore(databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := &PostgresStore{db: db}
	if err := store.ensureSchema(); err != nil {
		return nil, fmt.Errorf("ensure schema: %w", err)
	}

	log.Println("storage: connected to PostgreSQL")
	return store, nil
}

// Close closes the database connection.
func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) ensureSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS candidates (
		id SERIAL PRIMARY KEY,
		symbol TEXT NOT NULL,
		direction TEXT NOT NULL,
		price DOUBLE PRECISION NOT NULL,
		gap_percent DOUBLE PRECISION,
		relative_volume DOUBLE PRECISION,
		score DOUBLE PRECISION,
		data JSONB,
		created_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS logs (
		id SERIAL PRIMARY KEY,
		level TEXT NOT NULL,
		component TEXT NOT NULL,
		message TEXT NOT NULL,
		created_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS executions (
		id SERIAL PRIMARY KEY,
		symbol TEXT NOT NULL,
		side TEXT NOT NULL,
		intent TEXT NOT NULL,
		price DOUBLE PRECISION NOT NULL,
		quantity BIGINT NOT NULL,
		broker_order_id TEXT,
		broker_status TEXT,
		data JSONB,
		created_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS closed_trades (
		id SERIAL PRIMARY KEY,
		symbol TEXT NOT NULL,
		side TEXT NOT NULL,
		quantity BIGINT NOT NULL,
		entry_price DOUBLE PRECISION NOT NULL,
		exit_price DOUBLE PRECISION NOT NULL,
		pnl DOUBLE PRECISION NOT NULL,
		r_multiple DOUBLE PRECISION,
		exit_reason TEXT,
		data JSONB,
		opened_at TIMESTAMPTZ,
		closed_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS dashboard_snapshots (
		id SERIAL PRIMARY KEY,
		data JSONB NOT NULL,
		created_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS indicator_states (
		id SERIAL PRIMARY KEY,
		symbol TEXT NOT NULL,
		signal_type TEXT NOT NULL,
		reason TEXT,
		data JSONB,
		created_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_candidates_created_at ON candidates(created_at);
	CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
	CREATE INDEX IF NOT EXISTS idx_closed_trades_closed_at ON closed_trades(closed_at);
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *PostgresStore) RecordCandidate(c domain.Candidate) {
	data, _ := json.Marshal(c)
	_, err := s.db.Exec(
		`INSERT INTO candidates (symbol, direction, price, gap_percent, relative_volume, score, data) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.Symbol, c.Direction, c.Price, c.GapPercent, c.RelativeVolume, c.Score, data,
	)
	if err != nil {
		log.Printf("storage: record candidate error: %v", err)
	}
}

func (s *PostgresStore) RecordLog(entry domain.LogEntry) {
	_, err := s.db.Exec(
		`INSERT INTO logs (level, component, message, created_at) VALUES ($1, $2, $3, $4)`,
		entry.Level, entry.Component, entry.Message, entry.Timestamp,
	)
	if err != nil {
		log.Printf("storage: record log error: %v", err)
	}
}

func (s *PostgresStore) RecordExecution(report domain.ExecutionReport) {
	data, _ := json.Marshal(report)
	_, err := s.db.Exec(
		`INSERT INTO executions (symbol, side, intent, price, quantity, broker_order_id, broker_status, data) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		report.Symbol, report.Side, report.Intent, report.Price, report.Quantity, report.BrokerOrderID, report.BrokerStatus, data,
	)
	if err != nil {
		log.Printf("storage: record execution error: %v", err)
	}
}

func (s *PostgresStore) RecordClosedTrade(trade domain.ClosedTrade) {
	data, _ := json.Marshal(trade)
	_, err := s.db.Exec(
		`INSERT INTO closed_trades (symbol, side, quantity, entry_price, exit_price, pnl, r_multiple, exit_reason, data, opened_at, closed_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		trade.Symbol, trade.Side, trade.Quantity, trade.EntryPrice, trade.ExitPrice, trade.PnL, trade.RMultiple, trade.ExitReason, data, trade.OpenedAt, trade.ClosedAt,
	)
	if err != nil {
		log.Printf("storage: record closed trade error: %v", err)
	}
}

func (s *PostgresStore) RecordDashboard(snapshot domain.DashboardSnapshot) {
	data, _ := json.Marshal(snapshot)
	_, err := s.db.Exec(
		`INSERT INTO dashboard_snapshots (data) VALUES ($1)`,
		data,
	)
	if err != nil {
		log.Printf("storage: record dashboard error: %v", err)
	}
}

func (s *PostgresStore) RecordIndicatorState(snapshot domain.IndicatorSnapshot) {
	data, _ := json.Marshal(snapshot)
	_, err := s.db.Exec(
		`INSERT INTO indicator_states (symbol, signal_type, reason, data) VALUES ($1, $2, $3, $4)`,
		snapshot.Symbol, snapshot.SignalType, snapshot.Reason, data,
	)
	if err != nil {
		log.Printf("storage: record indicator state error: %v", err)
	}
}
