package backtest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
)

func TestRunExecutesHistoricalReplay(t *testing.T) {
	data := strings.Join([]string{
		"timestamp,symbol,open,high,low,close,volume,prev_close",
		"2026-03-09T09:30:00Z,APVO,10.00,10.10,9.95,10.05,50000,9.80",
		"2026-03-09T09:31:00Z,APVO,10.05,10.10,10.00,10.02,50000,9.80",
		"2026-03-09T09:32:00Z,APVO,10.02,10.08,10.00,10.04,50000,9.80",
		"2026-03-09T09:33:00Z,APVO,10.04,10.09,10.01,10.03,50000,9.80",
		"2026-03-09T09:34:00Z,APVO,10.03,10.07,10.00,10.01,50000,9.80",
		"2026-03-10T08:00:00Z,APVO,11.10,11.25,11.05,11.20,200000,10.01",
		"2026-03-10T08:01:00Z,APVO,11.20,11.35,11.18,11.30,210000,10.01",
		"2026-03-10T08:02:00Z,APVO,11.30,11.45,11.28,11.40,220000,10.01",
		"2026-03-10T09:30:00Z,APVO,11.40,11.55,11.35,11.50,230000,10.01",
		"2026-03-10T09:31:00Z,APVO,11.50,11.85,11.48,11.80,240000,10.01",
		"2026-03-10T09:32:00Z,APVO,11.80,12.05,11.75,12.00,250000,10.01",
		"2026-03-10T09:33:00Z,APVO,12.00,12.10,11.90,12.05,260000,10.01",
		"2026-03-10T09:34:00Z,APVO,12.05,12.08,11.10,11.20,270000,10.01",
	}, "\n")

	path := filepath.Join(t.TempDir(), "bars.csv")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		DataPath: path,
	})
	if err != nil {
		t.Fatalf("expected backtest to complete, got %v", err)
	}
	if result.Trades == 0 {
		t.Fatalf("expected at least one replayed trade, got %+v", result)
	}
	if len(result.ClosedTrades) == 0 {
		t.Fatalf("expected closed trades in result, got %+v", result)
	}
}

func TestRunExecutesHistoricalReplayFromInputBars(t *testing.T) {
	result, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		Bars: []InputBar{
			{Timestamp: time.Date(2026, 3, 9, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 10.00, High: 10.10, Low: 9.95, Close: 10.05, Volume: 50_000, PrevClose: 9.80},
			{Timestamp: time.Date(2026, 3, 9, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 10.05, High: 10.10, Low: 10.00, Close: 10.02, Volume: 50_000, PrevClose: 9.80},
			{Timestamp: time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC), Symbol: "APVO", Open: 11.10, High: 11.25, Low: 11.05, Close: 11.20, Volume: 200_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 8, 1, 0, 0, time.UTC), Symbol: "APVO", Open: 11.20, High: 11.35, Low: 11.18, Close: 11.30, Volume: 210_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 11.40, High: 11.55, Low: 11.35, Close: 11.50, Volume: 230_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 11.50, High: 11.85, Low: 11.48, Close: 11.80, Volume: 240_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 32, 0, 0, time.UTC), Symbol: "APVO", Open: 11.80, High: 12.05, Low: 11.75, Close: 12.00, Volume: 250_000, PrevClose: 10.01},
			{Timestamp: time.Date(2026, 3, 10, 9, 34, 0, 0, time.UTC), Symbol: "APVO", Open: 12.05, High: 12.08, Low: 11.10, Close: 11.20, Volume: 270_000, PrevClose: 10.01},
		},
	})
	if err != nil {
		t.Fatalf("expected in-memory bar replay to complete, got %v", err)
	}
	if result.Trades == 0 {
		t.Fatalf("expected at least one replayed trade, got %+v", result)
	}
}
