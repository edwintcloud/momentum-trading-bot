package alpaca

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
)

func TestAvailableQuantityFromError(t *testing.T) {
	err := &APIError{
		StatusCode: 403,
		Status:     "403 Forbidden",
		Code:       40310000,
		Message:    "insufficient qty available for order (requested: 473, available: 170)",
		Available:  "170",
		Symbol:     "EONR",
	}

	available, ok := AvailableQuantityFromError(err)
	if !ok {
		t.Fatal("expected to extract available quantity from Alpaca error")
	}
	if available != 170 {
		t.Fatalf("expected available quantity 170, got %d", available)
	}
	if !IsInsufficientQuantityError(err) {
		t.Fatal("expected insufficient quantity error to be detected")
	}
}

func TestParseShareQuantity(t *testing.T) {
	quantity, err := ParseShareQuantity("170.0000")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if quantity != 170 {
		t.Fatalf("expected quantity 170, got %d", quantity)
	}
}

func TestGetHistoricalBarsFollowsPagination(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"bars":{"AAPL":[{"o":100,"h":101,"l":99,"c":100.5,"v":1000,"t":"2026-03-10T09:30:00Z"}]},"next_page_token":"page-2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"bars":{"AAPL":[{"o":100.5,"h":102,"l":100,"c":101.5,"v":1200,"t":"2026-03-10T09:31:00Z"}]},"next_page_token":""}`))
	}))
	defer server.Close()

	client := NewClient(config.AlpacaConfig{
		APIKey:            "key",
		APISecret:         "secret",
		DataFeed:          "iex",
		TradingBaseURL:    server.URL,
		MarketDataBaseURL: server.URL,
	})
	bars, err := client.GetHistoricalBars(context.Background(), []string{"AAPL"}, parseTestTime("2026-03-10T09:30:00Z"), parseTestTime("2026-03-10T09:31:00Z"), "1Min")
	if err != nil {
		t.Fatalf("expected historical bars to load, got %v", err)
	}
	if len(bars["AAPL"]) != 2 {
		t.Fatalf("expected two paginated bars, got %+v", bars)
	}
}

func TestListActiveEquitySymbolsFiltersTradableAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v2/assets") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"symbol":"AAPL","tradable":true,"class":"us_equity","status":"active"},
			{"symbol":"TEST","tradable":false,"class":"us_equity","status":"active"},
			{"symbol":"MSFT","tradable":true,"class":"us_equity","status":"active"}
		]`))
	}))
	defer server.Close()

	client := NewClient(config.AlpacaConfig{
		APIKey:         "key",
		APISecret:      "secret",
		TradingBaseURL: server.URL,
	})
	symbols, err := client.ListActiveEquitySymbols(context.Background())
	if err != nil {
		t.Fatalf("expected tradable assets to load, got %v", err)
	}
	if len(symbols) != 2 || symbols[0] != "AAPL" || symbols[1] != "MSFT" {
		t.Fatalf("unexpected tradable symbols: %+v", symbols)
	}
}

func TestSubmitOrderRejectsOutsideTradableSession(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ord-1","symbol":"AAPL","side":"buy","status":"accepted","qty":"10","filled_qty":"0","filled_avg_price":"0"}`))
	}))
	defer server.Close()

	client := NewClient(config.AlpacaConfig{
		APIKey:         "key",
		APISecret:      "secret",
		TradingBaseURL: server.URL,
	})
	_, err := client.SubmitOrder(context.Background(), domain.OrderRequest{
		Symbol:    "AAPL",
		Side:      "buy",
		Price:     100,
		Quantity:  10,
		Timestamp: time.Date(2026, 3, 13, 6, 30, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("expected outside-session submit to be rejected")
	}
	if requests != 0 {
		t.Fatalf("expected no HTTP request for outside-session submit, got %d", requests)
	}
}

func parseTestTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}
