package storage

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
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

func init() {
	_ = time.Now // keep time import
}
