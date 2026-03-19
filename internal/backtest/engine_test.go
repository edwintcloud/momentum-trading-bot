package backtest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

func TestRunExecutesHistoricalReplay(t *testing.T) {
	data := strings.Join([]string{
		"timestamp,symbol,open,high,low,close,volume,prev_close",
		"2026-03-09T09:30:00Z,APVO,10.00,10.10,9.95,10.05,50000,9.80",
		"2026-03-09T09:31:00Z,APVO,10.05,10.10,10.00,10.02,50000,9.80",
		"2026-03-09T09:32:00Z,APVO,10.02,10.08,10.00,10.04,50000,9.80",
		"2026-03-09T09:33:00Z,APVO,10.04,10.09,10.01,10.03,50000,9.80",
		"2026-03-09T09:34:00Z,APVO,10.03,10.07,10.00,10.01,50000,9.80",
		"2026-03-10T08:00:00Z,APVO,11.10,11.30,11.05,11.24,200000,10.01",
		"2026-03-10T08:01:00Z,APVO,11.24,11.48,11.22,11.46,250000,10.01",
		"2026-03-10T08:02:00Z,APVO,11.46,11.72,11.44,11.70,400000,10.01",
		"2026-03-10T09:30:00Z,APVO,11.70,11.86,11.66,11.84,500000,10.01",
		"2026-03-10T09:31:00Z,APVO,11.84,12.12,11.80,12.10,650000,10.01",
		"2026-03-10T09:32:00Z,APVO,12.10,12.35,12.06,12.32,700000,10.01",
		"2026-03-10T09:33:00Z,APVO,12.32,12.42,12.20,12.38,180000,10.01",
		"2026-03-10T09:34:00Z,APVO,12.30,12.32,11.40,11.55,300000,10.01",
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
	if result.Diagnostics.BarsLoaded == 0 || result.Diagnostics.BarsInWindow == 0 {
		t.Fatalf("expected replay to process bars, got %+v", result.Diagnostics)
	}
	if result.Diagnostics.EntryCandidates == 0 {
		t.Fatalf("expected scanner to emit at least one candidate, got %+v", result.Diagnostics)
	}
}

func TestRunExecutesHistoricalReplayFromInputBars(t *testing.T) {
	bars := []InputBar{
		{Timestamp: time.Date(2026, 3, 9, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 10.00, High: 10.10, Low: 9.95, Close: 10.05, Volume: 50_000, PrevClose: 9.80},
		{Timestamp: time.Date(2026, 3, 9, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 10.05, High: 10.10, Low: 10.00, Close: 10.02, Volume: 50_000, PrevClose: 9.80},
		{Timestamp: time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC), Symbol: "APVO", Open: 11.10, High: 11.30, Low: 11.05, Close: 11.24, Volume: 200_000, PrevClose: 10.01},
		{Timestamp: time.Date(2026, 3, 10, 8, 1, 0, 0, time.UTC), Symbol: "APVO", Open: 11.24, High: 11.48, Low: 11.22, Close: 11.46, Volume: 250_000, PrevClose: 10.01},
		{Timestamp: time.Date(2026, 3, 10, 8, 2, 0, 0, time.UTC), Symbol: "APVO", Open: 11.46, High: 11.72, Low: 11.44, Close: 11.70, Volume: 400_000, PrevClose: 10.01},
		{Timestamp: time.Date(2026, 3, 10, 9, 30, 0, 0, time.UTC), Symbol: "APVO", Open: 11.70, High: 11.86, Low: 11.66, Close: 11.84, Volume: 500_000, PrevClose: 10.01},
		{Timestamp: time.Date(2026, 3, 10, 9, 31, 0, 0, time.UTC), Symbol: "APVO", Open: 11.84, High: 12.12, Low: 11.80, Close: 12.10, Volume: 650_000, PrevClose: 10.01},
		{Timestamp: time.Date(2026, 3, 10, 9, 32, 0, 0, time.UTC), Symbol: "APVO", Open: 12.10, High: 12.35, Low: 12.06, Close: 12.32, Volume: 700_000, PrevClose: 10.01},
		{Timestamp: time.Date(2026, 3, 10, 9, 34, 0, 0, time.UTC), Symbol: "APVO", Open: 12.30, High: 12.32, Low: 11.40, Close: 11.55, Volume: 300_000, PrevClose: 10.01},
	}

	result, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		Bars: bars,
	})
	if err != nil {
		t.Fatalf("expected in-memory bar replay to complete, got %v", err)
	}
	if result.Diagnostics.BarsLoaded == 0 || result.Diagnostics.BarsInWindow == 0 {
		t.Fatalf("expected replay to process bars, got %+v", result.Diagnostics)
	}
	if result.Diagnostics.EntryCandidates == 0 {
		t.Fatalf("expected scanner to emit at least one candidate, got %+v", result.Diagnostics)
	}

	streamed, err := Run(context.Background(), config.DefaultTradingConfig(), RunConfig{
		Iterator: &testInputBarIterator{bars: append([]InputBar(nil), bars...)},
	})
	if err != nil {
		t.Fatalf("expected iterator bar replay to complete, got %v", err)
	}
	if !reflect.DeepEqual(streamed.Diagnostics, result.Diagnostics) {
		t.Fatalf("expected iterator diagnostics to match slice replay\nstreamed=%+v\nslice=%+v", streamed.Diagnostics, result.Diagnostics)
	}
}

func TestMaybeFillPendingOrderSupportsShortEntries(t *testing.T) {
	pending := pendingEntry{
		order: domain.OrderRequest{
			Symbol:       "GOAI",
			Side:         domain.SideSell,
			Intent:       domain.IntentOpen,
			PositionSide: domain.DirectionShort,
			Price:        5.30,
			Quantity:     100,
			StopPrice:    5.62,
			RiskPerShare: 0.32,
			EntryATR:     0.22,
			SetupType:    "parabolic-failed-reclaim-short",
		},
		barsRemaining: 2,
	}

	fill, _, filled, expired := maybeFillPendingOrder(pending, InputBar{
		Timestamp: time.Date(2026, 3, 19, 14, 1, 0, 0, time.UTC),
		Symbol:    "GOAI",
		Open:      5.36,
		High:      5.40,
		Low:       5.12,
		Close:     5.18,
		Volume:    5_000,
	})
	if !filled || !expired {
		t.Fatalf("expected short pending order to fill, filled=%v expired=%v", filled, expired)
	}
	if fill.Side != domain.SideSell || fill.Intent != domain.IntentOpen || fill.PositionSide != domain.DirectionShort {
		t.Fatalf("unexpected short fill: %+v", fill)
	}
	if fill.Price < pending.order.Price {
		t.Fatalf("expected short entry fill to respect sell limit, got %+v", fill)
	}
}

type testInputBarIterator struct {
	bars []InputBar
	next int
}

func (it *testInputBarIterator) Next() (InputBar, bool, error) {
	if it.next >= len(it.bars) {
		return InputBar{}, false, nil
	}
	item := it.bars[it.next]
	it.next++
	return item, true, nil
}

func (it *testInputBarIterator) Close() error {
	return nil
}
