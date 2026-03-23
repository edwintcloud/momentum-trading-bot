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

func TestFilesystemStore_LoadClosedTradesByDate(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	loc := markethours.Location()

	// Create trades on specific dates
	march20 := time.Date(2026, 3, 20, 10, 30, 0, 0, loc)
	march21 := time.Date(2026, 3, 21, 11, 0, 0, 0, loc)
	march22 := time.Date(2026, 3, 22, 14, 0, 0, 0, loc)

	store.RecordClosedTrade(domain.ClosedTrade{
		Symbol: "AAPL", Side: "long", Quantity: 100,
		EntryPrice: 150, ExitPrice: 155, PnL: 500,
		OpenedAt: march20.Add(-time.Hour), ClosedAt: march20,
	})
	store.RecordClosedTrade(domain.ClosedTrade{
		Symbol: "TSLA", Side: "short", Quantity: 50,
		EntryPrice: 200, ExitPrice: 195, PnL: 250,
		OpenedAt: march21.Add(-2 * time.Hour), ClosedAt: march21,
	})
	store.RecordClosedTrade(domain.ClosedTrade{
		Symbol: "NVDA", Side: "long", Quantity: 200,
		EntryPrice: 300, ExitPrice: 310, PnL: 2000,
		OpenedAt: march22.Add(-30 * time.Minute), ClosedAt: march22,
	})

	// Query March 20 — should get AAPL only
	trades, err := store.LoadClosedTradesByDate(march20)
	if err != nil {
		t.Fatalf("LoadClosedTradesByDate(march20): %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade on March 20, got %d", len(trades))
	}
	if trades[0].Symbol != "AAPL" {
		t.Errorf("expected AAPL, got %s", trades[0].Symbol)
	}

	// Query March 21 — should get TSLA only
	trades, err = store.LoadClosedTradesByDate(march21)
	if err != nil {
		t.Fatalf("LoadClosedTradesByDate(march21): %v", err)
	}
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade on March 21, got %d", len(trades))
	}
	if trades[0].Symbol != "TSLA" {
		t.Errorf("expected TSLA, got %s", trades[0].Symbol)
	}

	// Query a date with no trades
	noTradesDate := time.Date(2026, 3, 19, 12, 0, 0, 0, loc)
	trades, err = store.LoadClosedTradesByDate(noTradesDate)
	if err != nil {
		t.Fatalf("LoadClosedTradesByDate(no trades): %v", err)
	}
	if len(trades) != 0 {
		t.Errorf("expected 0 trades on March 19, got %d", len(trades))
	}
}

func TestFilesystemStore_LoadClosedTradesByDate_NoFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	loc := markethours.Location()

	trades, err := store.LoadClosedTradesByDate(time.Now().In(loc))
	if err != nil {
		t.Fatalf("LoadClosedTradesByDate should not error on missing file: %v", err)
	}
	if trades != nil {
		t.Errorf("expected nil trades for missing file, got %v", trades)
	}
}

func TestFilesystemStore_ListTradeDates(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)
	loc := markethours.Location()

	// Record trades on three different dates
	store.RecordClosedTrade(domain.ClosedTrade{
		Symbol: "AAPL", PnL: 100,
		ClosedAt: time.Date(2026, 3, 20, 10, 0, 0, 0, loc),
	})
	store.RecordClosedTrade(domain.ClosedTrade{
		Symbol: "TSLA", PnL: -50,
		ClosedAt: time.Date(2026, 3, 20, 14, 0, 0, 0, loc),
	})
	store.RecordClosedTrade(domain.ClosedTrade{
		Symbol: "NVDA", PnL: 200,
		ClosedAt: time.Date(2026, 3, 18, 11, 0, 0, 0, loc),
	})
	store.RecordClosedTrade(domain.ClosedTrade{
		Symbol: "MSFT", PnL: 150,
		ClosedAt: time.Date(2026, 3, 21, 9, 30, 0, 0, loc),
	})

	dates, err := store.ListTradeDates()
	if err != nil {
		t.Fatalf("ListTradeDates: %v", err)
	}
	if len(dates) != 3 {
		t.Fatalf("expected 3 distinct dates, got %d: %v", len(dates), dates)
	}

	// Should be in descending order
	if dates[0] != "2026-03-21" {
		t.Errorf("expected first date 2026-03-21, got %s", dates[0])
	}
	if dates[1] != "2026-03-20" {
		t.Errorf("expected second date 2026-03-20, got %s", dates[1])
	}
	if dates[2] != "2026-03-18" {
		t.Errorf("expected third date 2026-03-18, got %s", dates[2])
	}
}

func TestFilesystemStore_ListTradeDates_NoFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(dir)

	dates, err := store.ListTradeDates()
	if err != nil {
		t.Fatalf("ListTradeDates should not error on missing file: %v", err)
	}
	if dates != nil {
		t.Errorf("expected nil dates for missing file, got %v", dates)
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
