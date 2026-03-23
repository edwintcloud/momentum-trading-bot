package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func TestFilesystemStore_LoadTodayClosedTrades(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	loc := markethours.Location()
	now := time.Now().In(loc)

	todayTrade := domain.ClosedTrade{
		Symbol:     "AAPL",
		Side:       "long",
		Quantity:   100,
		EntryPrice: 150.0,
		ExitPrice:  155.0,
		PnL:        500.0,
		RMultiple:  2.0,
		SetupType:  "breakout",
		ExitReason: "profit-target",
		OpenedAt:   now.Add(-1 * time.Hour),
		ClosedAt:   now.Add(-30 * time.Minute),
	}

	yesterdayTrade := domain.ClosedTrade{
		Symbol:     "TSLA",
		Side:       "long",
		Quantity:   50,
		EntryPrice: 200.0,
		ExitPrice:  195.0,
		PnL:        -250.0,
		RMultiple:  -1.0,
		ExitReason: "stop-loss",
		OpenedAt:   now.Add(-25 * time.Hour),
		ClosedAt:   now.Add(-24 * time.Hour),
	}

	// Write both trades to the JSONL file
	path := filepath.Join(dir, "closed_trades.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	for _, trade := range []domain.ClosedTrade{yesterdayTrade, todayTrade} {
		data, _ := json.Marshal(trade)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	f.Close()

	// Load today's trades
	trades, err := store.LoadTodayClosedTrades()
	if err != nil {
		t.Fatalf("LoadTodayClosedTrades: %v", err)
	}

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade from today, got %d", len(trades))
	}
	if trades[0].Symbol != "AAPL" {
		t.Errorf("expected symbol AAPL, got %s", trades[0].Symbol)
	}
	if trades[0].PnL != 500.0 {
		t.Errorf("expected PnL 500.0, got %.2f", trades[0].PnL)
	}
	if trades[0].SetupType != "breakout" {
		t.Errorf("expected setupType breakout, got %s", trades[0].SetupType)
	}
	if trades[0].ExitReason != "profit-target" {
		t.Errorf("expected exitReason profit-target, got %s", trades[0].ExitReason)
	}
}

func TestFilesystemStore_LoadTodayClosedTrades_NoFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	trades, err := store.LoadTodayClosedTrades()
	if err != nil {
		t.Fatalf("LoadTodayClosedTrades should not error on missing file: %v", err)
	}
	if trades != nil {
		t.Errorf("expected nil trades for missing file, got %v", trades)
	}
}

func TestFilesystemStore_LoadTodayClosedTrades_AllFieldsPreserved(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	loc := markethours.Location()
	now := time.Now().In(loc)

	trade := domain.ClosedTrade{
		Symbol:           "NVDA",
		Side:             "short",
		Quantity:         200,
		EntryPrice:       300.0,
		ExitPrice:        290.0,
		PnL:              2000.0,
		RMultiple:        3.5,
		SetupType:        "breakdown",
		ExitReason:       "trailing-stop",
		MarketRegime:     "trending",
		RegimeConfidence: 0.85,
		Playbook:         "breakout",
		Sector:           "Technology",
		OpenedAt:         now.Add(-2 * time.Hour),
		ClosedAt:         now.Add(-1 * time.Hour),
	}

	// Write via the store's RecordClosedTrade method
	store.RecordClosedTrade(trade)

	trades, err := store.LoadTodayClosedTrades()
	if err != nil {
		t.Fatalf("LoadTodayClosedTrades: %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}

	got := trades[0]
	if got.Symbol != trade.Symbol {
		t.Errorf("Symbol: got %s, want %s", got.Symbol, trade.Symbol)
	}
	if got.Side != trade.Side {
		t.Errorf("Side: got %s, want %s", got.Side, trade.Side)
	}
	if got.Quantity != trade.Quantity {
		t.Errorf("Quantity: got %d, want %d", got.Quantity, trade.Quantity)
	}
	if got.EntryPrice != trade.EntryPrice {
		t.Errorf("EntryPrice: got %.2f, want %.2f", got.EntryPrice, trade.EntryPrice)
	}
	if got.ExitPrice != trade.ExitPrice {
		t.Errorf("ExitPrice: got %.2f, want %.2f", got.ExitPrice, trade.ExitPrice)
	}
	if got.PnL != trade.PnL {
		t.Errorf("PnL: got %.2f, want %.2f", got.PnL, trade.PnL)
	}
	if got.RMultiple != trade.RMultiple {
		t.Errorf("RMultiple: got %.2f, want %.2f", got.RMultiple, trade.RMultiple)
	}
	if got.SetupType != trade.SetupType {
		t.Errorf("SetupType: got %s, want %s", got.SetupType, trade.SetupType)
	}
	if got.ExitReason != trade.ExitReason {
		t.Errorf("ExitReason: got %s, want %s", got.ExitReason, trade.ExitReason)
	}
	if got.MarketRegime != trade.MarketRegime {
		t.Errorf("MarketRegime: got %s, want %s", got.MarketRegime, trade.MarketRegime)
	}
	if got.RegimeConfidence != trade.RegimeConfidence {
		t.Errorf("RegimeConfidence: got %.2f, want %.2f", got.RegimeConfidence, trade.RegimeConfidence)
	}
	if got.Playbook != trade.Playbook {
		t.Errorf("Playbook: got %s, want %s", got.Playbook, trade.Playbook)
	}
	if got.Sector != trade.Sector {
		t.Errorf("Sector: got %s, want %s", got.Sector, trade.Sector)
	}
}

func TestFilesystemStore_LoadTodayClosedTrades_MultipleToday(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	loc := markethours.Location()
	now := time.Now().In(loc)

	// Record three trades today
	for i, sym := range []string{"AAPL", "TSLA", "NVDA"} {
		store.RecordClosedTrade(domain.ClosedTrade{
			Symbol:     sym,
			Side:       "long",
			Quantity:   int64(100 * (i + 1)),
			EntryPrice: 100.0,
			ExitPrice:  110.0,
			PnL:        float64(1000 * (i + 1)),
			ClosedAt:   now.Add(-time.Duration(3-i) * time.Hour),
		})
	}

	trades, err := store.LoadTodayClosedTrades()
	if err != nil {
		t.Fatalf("LoadTodayClosedTrades: %v", err)
	}
	if len(trades) != 3 {
		t.Fatalf("expected 3 trades, got %d", len(trades))
	}

	// Verify order is preserved (AAPL first since it was written first and has earliest ClosedAt)
	if trades[0].Symbol != "AAPL" {
		t.Errorf("expected first trade AAPL, got %s", trades[0].Symbol)
	}
	if trades[2].Symbol != "NVDA" {
		t.Errorf("expected last trade NVDA, got %s", trades[2].Symbol)
	}
}
