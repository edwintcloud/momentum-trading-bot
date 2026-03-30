package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/shopspring/decimal"
)

const (
	paperBaseURL = "https://paper-api.alpaca.markets"
	liveBaseURL  = "https://api.alpaca.markets"
	dataBaseURL  = "https://data.alpaca.markets"
)

type (
	AlpacaPosition = alpaca.Position
	AlpacaOrder    = alpaca.Order
	Snapshot       = marketdata.Snapshot
)

// Client wraps Alpaca REST and WebSocket interactions.
type Client struct {
	apiKey      string
	apiSecret   string
	baseURL     string
	dataURL     string
	http        *http.Client
	paper       bool
	tradingCfg  config.TradingConfig
	tradeClient *alpaca.Client
	dataClient  *marketdata.Client
}

// NewClient creates an Alpaca API client.
func NewClient(cfg config.AppConfig, tradingCfg config.TradingConfig) *Client {
	base := paperBaseURL
	if !cfg.AlpacaPaper {
		base = liveBaseURL
	}
	return &Client{
		apiKey:    cfg.AlpacaAPIKey,
		apiSecret: cfg.AlpacaAPISecret,
		baseURL:   base,
		dataURL:   dataBaseURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		paper:      cfg.AlpacaPaper,
		tradingCfg: tradingCfg,
		tradeClient: alpaca.NewClient(alpaca.ClientOpts{
			APIKey:    cfg.AlpacaAPIKey,
			APISecret: cfg.AlpacaAPISecret,
			BaseURL:   base,
		}),
		dataClient: marketdata.NewClient(marketdata.ClientOpts{
			APIKey:    cfg.AlpacaAPIKey,
			APISecret: cfg.AlpacaAPISecret,
		}),
	}
}

// GetAccount fetches the trading account information.
func (c *Client) GetAccount(ctx context.Context) (*alpaca.Account, error) {
	return c.tradeClient.GetAccount()
}

func OrderEventTime(o alpaca.Order) time.Time {
	if o.FilledAt != nil && !o.FilledAt.IsZero() {
		return *o.FilledAt
	}
	if !o.SubmittedAt.IsZero() {
		return o.SubmittedAt
	}
	return o.CreatedAt
}

// GetPositions fetches current broker positions.
func (c *Client) GetPositions(ctx context.Context) ([]alpaca.Position, error) {
	return c.tradeClient.GetPositions()
}

// ListRecentOrders fetches the most recent account orders with fill metadata.
func (c *Client) ListRecentOrders(ctx context.Context, limit int) ([]alpaca.Order, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 500 {
		limit = 500
	}
	return c.tradeClient.GetOrders(alpaca.GetOrdersRequest{
		Status:    "all",
		Limit:     limit,
		Direction: "desc",
	})
}

// SubmitOrder submits an order to Alpaca. Always uses limit orders — never market.
func (c *Client) SubmitOrder(ctx context.Context, order domain.OrderRequest) (string, error) {
	var limitPrice float64

	quote, err := c.dataClient.GetLatestQuote(order.Symbol, marketdata.GetLatestQuoteRequest{
		Feed: c.DataFeed(),
	})
	if err != nil {
		return "", fmt.Errorf("fetch latest quote: %w", err)
	}

	// reject if spread is too wide to avoid bad fills in illiquid stocks
	spread := quote.AskPrice - quote.BidPrice
	if spread/quote.AskPrice > c.tradingCfg.MaxSpreadPct {
		return "", fmt.Errorf("spread too wide: %.2f%%", spread/quote.AskPrice*100)
	}

	// base limit price on current quote
	if order.Side == domain.SideBuy {
		limitPrice = quote.AskPrice * (1 + c.tradingCfg.LimitOrderSlippageDollars)
	} else {
		limitPrice = quote.BidPrice * (1 - c.tradingCfg.LimitOrderSlippageDollars)
	}

	adjSide := alpaca.Side(order.Side)
	decimalQty := decimal.NewFromInt(order.Quantity)
	createdOrder, err := c.tradeClient.PlaceOrder(alpaca.PlaceOrderRequest{
		Symbol:        order.Symbol,
		Qty:           &decimalQty,
		Side:          alpaca.Side(order.Side),
		Type:          "limit",
		LimitPrice:    alpaca.RoundLimitPrice(decimal.NewFromFloat(limitPrice), adjSide),
		TimeInForce:   "day",
		ExtendedHours: true,
	})

	if err != nil {
		return "", fmt.Errorf("place order: %w", err)
	}

	return createdOrder.ID, nil
}

// PollOrderStatus checks the status of an order.
func (c *Client) PollOrderStatus(ctx context.Context, orderID string) (string, float64, int64, error) {
	order, err := c.tradeClient.GetOrder(orderID)
	if err != nil {
		return "", 0, 0, err
	}

	return order.Status, order.FilledAvgPrice.InexactFloat64(), order.FilledQty.IntPart(), nil
}

// IsEasyToBorrow checks if a symbol is currently eligible for opening short positions.
func (c *Client) IsEasyToBorrow(symbol string) bool {
	asset, err := c.tradeClient.GetAsset(symbol)
	if err != nil {
		return false
	}

	return asset.Shortable && asset.EasyToBorrow
}

// CancelOrder cancels a pending order by ID.
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	return c.tradeClient.CancelOrder(orderID)
}

// HistoricalBar is a single bar from Alpaca.
type HistoricalBar struct {
	Timestamp  time.Time `json:"t"`
	Open       float64   `json:"o"`
	High       float64   `json:"h"`
	Low        float64   `json:"l"`
	Close      float64   `json:"c"`
	Volume     uint64    `json:"v"`
	TradeCount int64     `json:"n"`
	VWAP       float64   `json:"vw"`
}

// GetSnapshots fetches snapshot data for multiple symbols from the Alpaca data API.
// Returns previous close, today's open/high/volume for each symbol.
// Symbols are batched in groups of 100 (Alpaca limit).
func (c *Client) GetSnapshots(ctx context.Context, symbols []string) (map[string]*marketdata.Snapshot, error) {
	return c.dataClient.GetSnapshots(symbols, marketdata.GetSnapshotRequest{
		Feed: c.DataFeed(),
	})
}

func (c *Client) get(ctx context.Context, url string, dest interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return &APIError{StatusCode: resp.StatusCode, Message: string(body)}
	}

	return json.NewDecoder(resp.Body).Decode(dest)
}

func (c *Client) getWithHeaders(ctx context.Context, url string, dest interface{}) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return resp.Header, &APIError{StatusCode: resp.StatusCode, Message: string(body)}
	}

	return resp.Header, json.NewDecoder(resp.Body).Decode(dest)
}

func (c *Client) setAuth(req *http.Request) {
	req.Header.Set("APCA-API-KEY-ID", c.apiKey)
	req.Header.Set("APCA-API-SECRET-KEY", c.apiSecret)
}
