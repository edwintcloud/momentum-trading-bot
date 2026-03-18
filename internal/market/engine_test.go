package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func BenchmarkHydrateBatch(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Simulated network latency

		if r.URL.Path == "/v2/stocks/snapshots" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"snapshots": {}}`))
			return
		}

		if r.URL.Path == "/v2/stocks/bars" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"bars": {}}`))
			return
		}

		if r.URL.Path == "/v1beta1/news" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"news": []}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := config.TradingConfig{
		HydrationRequestsPerMin: 1000000, // fast
	}
	alpacaCfg := config.AlpacaConfig{
		MarketDataBaseURL: server.URL,
		DataFeed:          "iex",
		APIKey:            "TEST",
		APISecret:         "TEST",
	}

	client := alpaca.NewClient(alpacaCfg)
	runtimeState := runtime.NewState()
	portfolioManager := portfolio.NewManager(cfg, runtimeState)

	engine := NewEngine(client, cfg, portfolioManager, runtimeState)

	ctx := context.Background()

	var batch []hydrationRequest
	// Add 10 distinct days to trigger multiple loop iterations
	baseDay := time.Now()
	batch = append(batch, hydrationRequest{symbol: "AAPL", timestamp: baseDay})
	batch = append(batch, hydrationRequest{symbol: "TSLA", timestamp: baseDay.AddDate(0, 0, 1)})
	batch = append(batch, hydrationRequest{symbol: "MSFT", timestamp: baseDay.AddDate(0, 0, 2)})
	batch = append(batch, hydrationRequest{symbol: "NVDA", timestamp: baseDay.AddDate(0, 0, 3)})
	batch = append(batch, hydrationRequest{symbol: "GOOG", timestamp: baseDay.AddDate(0, 0, 4)})
	batch = append(batch, hydrationRequest{symbol: "AMZN", timestamp: baseDay.AddDate(0, 0, 5)})
	batch = append(batch, hydrationRequest{symbol: "META", timestamp: baseDay.AddDate(0, 0, 6)})
	batch = append(batch, hydrationRequest{symbol: "NFLX", timestamp: baseDay.AddDate(0, 0, 7)})
	batch = append(batch, hydrationRequest{symbol: "AMD", timestamp: baseDay.AddDate(0, 0, 8)})
	batch = append(batch, hydrationRequest{symbol: "INTC", timestamp: baseDay.AddDate(0, 0, 9)})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.hydrateBatch(ctx, batch)
	}
}
