package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// FilesystemStore is a fallback event recorder that writes to local files.
type FilesystemStore struct {
	dir string
}

// NewFilesystemStore creates a filesystem-backed event store.
func NewFilesystemStore(dir string) *FilesystemStore {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("storage: filesystem mkdir warning: %v", err)
	}
	return &FilesystemStore{dir: dir}
}

func (f *FilesystemStore) RecordCandidate(c domain.Candidate) {
	f.appendJSON("candidates.jsonl", c)
}

func (f *FilesystemStore) RecordLog(entry domain.LogEntry) {
	f.appendJSON("logs.jsonl", entry)
}

func (f *FilesystemStore) RecordExecution(report domain.ExecutionReport) {
	f.appendJSON("executions.jsonl", report)
}

func (f *FilesystemStore) RecordClosedTrade(trade domain.ClosedTrade) {
	f.appendJSON("closed_trades.jsonl", trade)
}

func (f *FilesystemStore) RecordDashboard(snapshot domain.DashboardSnapshot) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	path := filepath.Join(f.dir, "dashboard_latest.json")
	_ = os.WriteFile(path, data, 0o644)
}

func (f *FilesystemStore) RecordIndicatorState(snapshot domain.IndicatorSnapshot) {
	f.appendJSON("indicators.jsonl", snapshot)
}

func (f *FilesystemStore) appendJSON(filename string, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	path := filepath.Join(f.dir, filename)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	data = append(data, '\n')
	_, _ = file.Write(data)
}

// NewFilesystemRecorder creates a FilesystemStore scoped to the given directory,
// returned as a domain.EventRecorder interface.
func NewFilesystemRecorder(ctx context.Context, dir string) (domain.EventRecorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("filesystem recorder: %w", err)
	}
	return NewFilesystemStore(dir), nil
}

// LoadTodayClosedTrades reads closed_trades.jsonl and returns trades from today (ET).
func (f *FilesystemStore) LoadTodayClosedTrades() ([]domain.ClosedTrade, error) {
	loc := markethours.Location()
	now := time.Now().In(loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	path := filepath.Join(f.dir, "closed_trades.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No trades file yet
		}
		return nil, err
	}
	defer file.Close()

	var trades []domain.ClosedTrade
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var t domain.ClosedTrade
		if err := json.Unmarshal(scanner.Bytes(), &t); err != nil {
			continue
		}
		if !t.ClosedAt.IsZero() && t.ClosedAt.In(loc).After(dayStart) {
			trades = append(trades, t)
		}
	}
	return trades, scanner.Err()
}
