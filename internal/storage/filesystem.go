package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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
	return f.LoadClosedTradesByDate(time.Now())
}

// LoadClosedTradesByDate reads closed_trades.jsonl and returns trades for a specific date (ET).
func (f *FilesystemStore) LoadClosedTradesByDate(date time.Time) ([]domain.ClosedTrade, error) {
	loc := markethours.Location()
	d := date.In(loc)
	dayStart := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)

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
		closedET := t.ClosedAt.In(loc)
		if !t.ClosedAt.IsZero() && !closedET.Before(dayStart) && closedET.Before(dayEnd) {
			trades = append(trades, t)
		}
	}
	return trades, scanner.Err()
}

// ListTradeDates returns all dates (ET) that have at least one closed trade, descending.
func (f *FilesystemStore) ListTradeDates() ([]string, error) {
	loc := markethours.Location()

	path := filepath.Join(f.dir, "closed_trades.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var t domain.ClosedTrade
		if err := json.Unmarshal(scanner.Bytes(), &t); err != nil {
			continue
		}
		if !t.ClosedAt.IsZero() {
			dateStr := t.ClosedAt.In(loc).Format("2006-01-02")
			seen[dateStr] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Sort descending
	dates := make([]string, 0, len(seen))
	for d := range seen {
		dates = append(dates, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))

	// Limit to 90
	if len(dates) > 90 {
		dates = dates[:90]
	}
	return dates, nil
}
