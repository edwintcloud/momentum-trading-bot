package telemetry

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

func TestLoggerRecordsEventsToFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "telemetry-test")
	if err != nil {
		t.Fatalf("could not create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logger, err := NewLogger(tempDir)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	report := domain.ExecutionReport{
		Symbol:       "AAPL",
		Side:         "buy",
		Price:        150.50,
		Quantity:     100,
		StopPrice:    148.00,
		RiskPerShare: 2.50,
		EntryATR:     1.50,
		SetupType:    "breakout",
		Reason:       "ml-entry",
		FilledAt:     time.Now().UTC(),
	}

	trade := domain.ClosedTrade{
		Symbol:     "AAPL",
		Quantity:   100,
		EntryPrice: 150.50,
		ExitPrice:  155.50,
		PnL:        500.00,
		OpenedAt:   time.Now().UTC().Add(-10 * time.Minute),
		ClosedAt:   time.Now().UTC(),
		ExitReason: "target",
	}

	snapshot := domain.IndicatorSnapshot{
		Symbol:     "AAPL",
		Timestamp:  time.Now().UTC(),
		SignalType: "entry",
		Reason:     "ml-entry",
		Indicators: map[string]float64{
			"price": 150.50,
			"vwap":  149.00,
		},
	}

	logger.RecordExecution(report)
	logger.RecordClosedTrade(trade)
	logger.RecordIndicatorState(snapshot)
	logger.Close()

	// Verify executions.jsonl
	executionsPath := filepath.Join(tempDir, "executions.jsonl")
	verifyJSONL(t, executionsPath, report.Symbol, "executions")

	// Verify closed_trades.jsonl
	tradesPath := filepath.Join(tempDir, "closed_trades.jsonl")
	verifyJSONL(t, tradesPath, trade.Symbol, "closed_trades")

	// Verify indicators.jsonl
	indicatorsPath := filepath.Join(tempDir, "indicators.jsonl")
	verifyJSONL(t, indicatorsPath, snapshot.Symbol, "indicators")
}

func verifyJSONL(t *testing.T, path string, expectedSymbol string, fileType string) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("could not open %s log: %v", fileType, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("expected at least one line in %s log", fileType)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(scanner.Bytes(), &data); err != nil {
		t.Fatalf("failed to parse JSON from %s log: %v", fileType, err)
	}

	symbol, ok := data["symbol"].(string)
	if !ok || symbol != expectedSymbol {
		t.Errorf("expected symbol %s in %s log, got %v", expectedSymbol, fileType, data["symbol"])
	}
}
