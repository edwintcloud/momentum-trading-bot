package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

// FilesystemRecorder persists trading events asynchronously as JSONL files on disk.
type FilesystemRecorder struct {
	logDir    string
	timestamp string
	events    chan any
}

// NewFilesystemRecorder creates a FilesystemRecorder that writes to logDir and
// starts the background writer loop.
func NewFilesystemRecorder(ctx context.Context, logDir string) (*FilesystemRecorder, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("filesystem recorder: create log dir: %w", err)
	}
	r := &FilesystemRecorder{
		logDir:    logDir,
		timestamp: time.Now().UTC().Format("20060102_150405"),
		events:    make(chan any, 2048),
	}
	go r.loop(ctx)
	return r, nil
}

// RecordCandidate queues a candidate event.
func (r *FilesystemRecorder) RecordCandidate(candidate domain.Candidate) {
	r.enqueue(candidate)
}

// RecordLog queues a log entry.
func (r *FilesystemRecorder) RecordLog(entry domain.LogEntry) {
	r.enqueue(entry)
}

// RecordExecution queues an execution report.
func (r *FilesystemRecorder) RecordExecution(report domain.ExecutionReport) {
	r.enqueue(report)
}

// RecordClosedTrade queues a closed trade event.
func (r *FilesystemRecorder) RecordClosedTrade(trade domain.ClosedTrade) {
	r.enqueue(trade)
}

// RecordDashboard queues a dashboard snapshot.
func (r *FilesystemRecorder) RecordDashboard(snapshot domain.DashboardSnapshot) {
	r.enqueue(snapshot)
}

// RecordIndicatorState queues an indicator state snapshot.
func (r *FilesystemRecorder) RecordIndicatorState(state domain.IndicatorSnapshot) {
	r.enqueue(state)
}

func (r *FilesystemRecorder) enqueue(event any) {
	select {
	case r.events <- event:
	default:
	}
}

func (r *FilesystemRecorder) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.drain()
			return
		case event, ok := <-r.events:
			if !ok {
				return
			}
			_ = r.persist(event)
		}
	}
}

func (r *FilesystemRecorder) drain() {
	for {
		select {
		case event := <-r.events:
			_ = r.persist(event)
		default:
			return
		}
	}
}

func (r *FilesystemRecorder) persist(event any) error {
	var base string
	switch event.(type) {
	case domain.LogEntry:
		base = "system_logs"
	case domain.Candidate:
		base = "scanner_candidates"
	case domain.ExecutionReport:
		base = "execution_reports"
	case domain.ClosedTrade:
		base = "closed_trades"
	case domain.DashboardSnapshot:
		base = "dashboard_snapshots"
	case domain.IndicatorSnapshot:
		base = "indicator_snapshots"
	default:
		return fmt.Errorf("filesystem recorder: unsupported event type %T", event)
	}
	filename := base + "_" + r.timestamp + ".jsonl"

	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("filesystem recorder: marshal: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(filepath.Join(r.logDir, filename), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("filesystem recorder: open %s: %w", filename, err)
	}
	_, err = f.Write(line)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}