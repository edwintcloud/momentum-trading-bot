package alpaca

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetSnapshots_ParsesResponse(t *testing.T) {
	// Mock Alpaca snapshot API response
	mockResponse := map[string]Snapshot{
		"AAPL": {
			DailyBar: SnapshotBar{
				Open: 150.0, High: 155.0, Low: 149.0, Close: 153.0,
				Volume: 1_000_000, VWAP: 152.0,
				Timestamp: time.Date(2026, 3, 23, 14, 0, 0, 0, time.UTC),
			},
			PrevDailyBar: SnapshotBar{
				Open: 148.0, High: 152.0, Low: 147.0, Close: 151.0,
				Volume: 950_000, VWAP: 149.5,
				Timestamp: time.Date(2026, 3, 22, 14, 0, 0, 0, time.UTC),
			},
			MinuteBar: SnapshotBar{
				Open: 153.0, High: 153.5, Low: 152.8, Close: 153.2,
				Volume: 5000, VWAP: 153.1,
			},
		},
		"TSLA": {
			DailyBar: SnapshotBar{
				Open: 250.0, High: 260.0, Low: 248.0, Close: 255.0,
				Volume: 2_000_000, VWAP: 254.0,
			},
			PrevDailyBar: SnapshotBar{
				Open: 245.0, High: 252.0, Low: 244.0, Close: 248.0,
				Volume: 1_800_000, VWAP: 248.0,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify endpoint and query params
		if r.URL.Path != "/v2/stocks/snapshots" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		symbols := r.URL.Query().Get("symbols")
		if symbols == "" {
			t.Error("missing symbols query param")
		}
		feed := r.URL.Query().Get("feed")
		if feed != "sip" {
			t.Errorf("feed = %s, want sip", feed)
		}
		// Verify auth headers
		if r.Header.Get("APCA-API-KEY-ID") == "" {
			t.Error("missing API key header")
		}
		if r.Header.Get("APCA-API-SECRET-KEY") == "" {
			t.Error("missing API secret header")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := &Client{
		apiKey:    "test-key",
		apiSecret: "test-secret",
		dataURL:   server.URL,
		http:      &http.Client{Timeout: 5 * time.Second},
	}

	ctx := context.Background()
	snapshots, err := client.GetSnapshots(ctx, []string{"AAPL", "TSLA"})
	if err != nil {
		t.Fatalf("GetSnapshots error: %v", err)
	}

	if len(snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snapshots))
	}

	aapl := snapshots["AAPL"]
	if aapl.PrevDailyBar.Close != 151.0 {
		t.Errorf("AAPL PrevDailyBar.Close = %f, want 151.0", aapl.PrevDailyBar.Close)
	}
	if aapl.DailyBar.Open != 150.0 {
		t.Errorf("AAPL DailyBar.Open = %f, want 150.0", aapl.DailyBar.Open)
	}
	if aapl.DailyBar.Volume != 1_000_000 {
		t.Errorf("AAPL DailyBar.Volume = %d, want 1000000", aapl.DailyBar.Volume)
	}
	if aapl.PrevDailyBar.Volume != 950_000 {
		t.Errorf("AAPL PrevDailyBar.Volume = %d, want 950000", aapl.PrevDailyBar.Volume)
	}

	tsla := snapshots["TSLA"]
	if tsla.PrevDailyBar.Close != 248.0 {
		t.Errorf("TSLA PrevDailyBar.Close = %f, want 248.0", tsla.PrevDailyBar.Close)
	}
}

func TestGetSnapshots_Batching(t *testing.T) {
	batchCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batchCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]Snapshot{})
	}))
	defer server.Close()

	client := &Client{
		apiKey:    "test-key",
		apiSecret: "test-secret",
		dataURL:   server.URL,
		http:      &http.Client{Timeout: 5 * time.Second},
	}

	// Create 250 symbols — should result in 3 batches (100 + 100 + 50)
	symbols := make([]string, 250)
	for i := range symbols {
		symbols[i] = "SYM" + string(rune('A'+i%26))
	}

	ctx := context.Background()
	_, err := client.GetSnapshots(ctx, symbols)
	if err != nil {
		t.Fatalf("GetSnapshots error: %v", err)
	}

	if batchCount != 3 {
		t.Errorf("batch count = %d, want 3", batchCount)
	}
}

func TestGetSnapshots_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message": "forbidden"}`))
	}))
	defer server.Close()

	client := &Client{
		apiKey:    "bad-key",
		apiSecret: "bad-secret",
		dataURL:   server.URL,
		http:      &http.Client{Timeout: 5 * time.Second},
	}

	ctx := context.Background()
	_, err := client.GetSnapshots(ctx, []string{"AAPL"})
	if err == nil {
		t.Fatal("expected error for forbidden response")
	}

	apiErr, ok := err.(*APIError)
	if !ok {
		// The error is wrapped, check the inner error
		t.Logf("error type: %T, message: %v", err, err)
	} else if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusForbidden)
	}
}

func TestGetSnapshots_EmptySymbols(t *testing.T) {
	client := &Client{
		apiKey:    "test-key",
		apiSecret: "test-secret",
		dataURL:   "http://unused",
		http:      &http.Client{Timeout: 5 * time.Second},
	}

	ctx := context.Background()
	snapshots, err := client.GetSnapshots(ctx, []string{})
	if err != nil {
		t.Fatalf("GetSnapshots error: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("got %d snapshots for empty input, want 0", len(snapshots))
	}
}
