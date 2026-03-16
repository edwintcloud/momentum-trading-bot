package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

// Logger writes trading and indicator events to structured log files.
type Logger struct {
	mu             sync.Mutex
	logDir         string
	executionsFile *os.File
	tradesFile     *os.File
	indicatorsFile *os.File
}

// NewLogger initializes the telemetry logger, creating the log directory if needed.
func NewLogger(logDir string) (*Logger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	executionsPath := filepath.Join(logDir, "executions.jsonl")
	executionsFile, err := os.OpenFile(executionsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open executions log: %w", err)
	}

	tradesPath := filepath.Join(logDir, "closed_trades.jsonl")
	tradesFile, err := os.OpenFile(tradesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		executionsFile.Close()
		return nil, fmt.Errorf("failed to open trades log: %w", err)
	}

	indicatorsPath := filepath.Join(logDir, "indicators.jsonl")
	indicatorsFile, err := os.OpenFile(indicatorsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		executionsFile.Close()
		tradesFile.Close()
		return nil, fmt.Errorf("failed to open indicators log: %w", err)
	}

	return &Logger{
		logDir:         logDir,
		executionsFile: executionsFile,
		tradesFile:     tradesFile,
		indicatorsFile: indicatorsFile,
	}, nil
}

// Close flushes and closes all log files.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.executionsFile != nil {
		l.executionsFile.Close()
	}
	if l.tradesFile != nil {
		l.tradesFile.Close()
	}
	if l.indicatorsFile != nil {
		l.indicatorsFile.Close()
	}
}

// RecordCandidate satisfies the EventRecorder interface (no-op for files).
func (l *Logger) RecordCandidate(c domain.Candidate) {}

// RecordLog satisfies the EventRecorder interface (no-op for files).
func (l *Logger) RecordLog(entry domain.LogEntry) {}

// RecordDashboard satisfies the EventRecorder interface (no-op for files).
func (l *Logger) RecordDashboard(snap domain.DashboardSnapshot) {}

// RecordExecution writes an execution report to the executions log.
func (l *Logger) RecordExecution(report domain.ExecutionReport) {
	l.writeJSONL(l.executionsFile, report)
}

// RecordClosedTrade writes a closed trade to the trades log.
func (l *Logger) RecordClosedTrade(trade domain.ClosedTrade) {
	l.writeJSONL(l.tradesFile, trade)
}

// RecordIndicatorState writes the raw mathematical state to the indicators log.
func (l *Logger) RecordIndicatorState(state domain.IndicatorSnapshot) {
	l.writeJSONL(l.indicatorsFile, state)
}

func (l *Logger) writeJSONL(file *os.File, data interface{}) {
	if file == nil {
		return
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return
	}
	bytes = append(bytes, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	file.Write(bytes)
}
