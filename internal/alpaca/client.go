package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

const (
	paperBaseURL = "https://paper-api.alpaca.markets"
	liveBaseURL  = "https://api.alpaca.markets"
	dataBaseURL  = "https://data.alpaca.markets"
)

// Client wraps Alpaca REST and WebSocket interactions.
type Client struct {
	apiKey    string
	apiSecret string
	baseURL   string
	dataURL   string
	http      *http.Client
	paper     bool
	tradingCfg config.TradingConfig
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
		paper: cfg.AlpacaPaper,
		tradingCfg: tradingCfg,
	}
}

// Account represents an Alpaca account.
type Account struct {
	Equity      float64 `json:"equity,string"`
	BuyingPower float64 `json:"buying_power,string"`
	Cash        float64 `json:"cash,string"`
	DayPnL      float64 `json:"unrealized_intraday_pl,string"`
	Status      string  `json:"status"`
}

// GetAccount fetches the trading account information.
func (c *Client) GetAccount(ctx context.Context) (Account, error) {
	var acct Account
	err := c.get(ctx, c.baseURL+"/v2/account", &acct)
	return acct, err
}

// AlpacaPosition represents a broker position.
type AlpacaPosition struct {
	Symbol        string `json:"symbol"`
	Qty           string `json:"qty"`
	Side          string `json:"side"`
	AvgEntryPrice string `json:"avg_entry_price"`
	CurrentPrice  string `json:"current_price"`
	MarketValue   string `json:"market_value"`
	UnrealizedPL  string `json:"unrealized_pl"`
}

// AlpacaOrder represents a broker order with fill metadata.
type AlpacaOrder struct {
	ID          string     `json:"id"`
	Symbol      string     `json:"symbol"`
	Side        string     `json:"side"`
	Status      string     `json:"status"`
	FilledQty   string     `json:"filled_qty"`
	CreatedAt   time.Time  `json:"created_at"`
	SubmittedAt *time.Time `json:"submitted_at"`
	FilledAt    *time.Time `json:"filled_at"`
}

type AlpacaQuote struct {
	AskPrice float64 `json:"ap"`
	BidPrice float64 `json:"bp"`
}

func (o AlpacaOrder) EventTime() time.Time {
	if o.FilledAt != nil && !o.FilledAt.IsZero() {
		return *o.FilledAt
	}
	if o.SubmittedAt != nil && !o.SubmittedAt.IsZero() {
		return *o.SubmittedAt
	}
	return o.CreatedAt
}

// GetPositions fetches current broker positions.
func (c *Client) GetPositions(ctx context.Context) ([]AlpacaPosition, error) {
	var positions []AlpacaPosition
	err := c.get(ctx, c.baseURL+"/v2/positions", &positions)
	return positions, err
}

// ListRecentOrders fetches the most recent account orders with fill metadata.
func (c *Client) ListRecentOrders(ctx context.Context, limit int) ([]AlpacaOrder, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 500 {
		limit = 500
	}
	query := url.Values{}
	query.Set("status", "all")
	query.Set("limit", strconv.Itoa(limit))
	query.Set("direction", "desc")

	var orders []AlpacaOrder
	err := c.get(ctx, c.baseURL+"/v2/orders?"+query.Encode(), &orders)
	return orders, err
}

// SubmitOrder submits an order to Alpaca. Always uses limit orders — never market.
func (c *Client) SubmitOrder(ctx context.Context, order domain.OrderRequest) (string, error) {
	body := map[string]interface{}{
		"symbol":         order.Symbol,
		"qty":            fmt.Sprintf("%d", order.Quantity),
		"side":           order.Side,
		"type":           "limit",
		"time_in_force":  "day",
		"extended_hours": true,
	}
	
	var limitPrice float64

	// set price to latest quote
	quote, err := c.LatestQuote(ctx, order.Symbol)
	if err != nil {
		return "", fmt.Errorf("fetch latest quote: %w", err)
	}
	if order.Side == domain.SideBuy {
		limitPrice = quote.AskPrice * (1 + c.tradingCfg.LimitOrderSlippageDollars)
	} else {
		limitPrice = quote.BidPrice * (1 - c.tradingCfg.LimitOrderSlippageDollars)
	}

	// Percentage-based slippage by liquidity tier
	slippage := computeOrderSlippage(limitPrice, order.AvgDailyVolume,
		5.0, 10.0, 20.0) // liquid, mid, illiquid bps
	if slippage < 0.01 {
		slippage = 0.05 // minimum slippage floor
	}

	// Apply slippage multiplier for retry attempts
	multiplier := order.SlippageMultiplier
	if multiplier <= 0 {
		multiplier = 1.0
	}
	slippage *= multiplier

	if order.Side == domain.SideBuy {
		limitPrice += slippage
	} else {
		limitPrice -= slippage
	}

	// Ensure limit price doesn't go below $0.01 for sells
	if limitPrice < 0.01 {
		limitPrice = 0.01
	}

	body["limit_price"] = fmt.Sprintf("%.2f", limitPrice)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal order: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v2/orders", strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", fmt.Errorf("create order request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("submit order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("order rejected: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode order response: %w", err)
	}

	return result.ID, nil
}

// PollOrderStatus checks the status of an order.
func (c *Client) PollOrderStatus(ctx context.Context, orderID string) (string, float64, int64, error) {
	var result struct {
		Status    string  `json:"status"`
		FilledAvg *string `json:"filled_avg_price"`
		FilledQty string  `json:"filled_qty"`
	}
	err := c.get(ctx, c.baseURL+"/v2/orders/"+orderID, &result)
	if err != nil {
		return "", 0, 0, err
	}

	fillPrice := 0.0
	if result.FilledAvg != nil {
		fmt.Sscanf(*result.FilledAvg, "%f", &fillPrice)
	}
	filledQty := parseFilledQuantity(result.FilledQty)

	return result.Status, fillPrice, filledQty, nil
}

func parseFilledQuantity(value string) int64 {
	return ParseOrderQuantity(value)
}

func ParseOrderQuantity(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if whole, err := strconv.ParseInt(value, 10, 64); err == nil {
		return whole
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return int64(math.Round(parsed))
}

// IsEasyToBorrow checks if a symbol is currently eligible for opening short positions.
func (c *Client) IsEasyToBorrow(symbol string) bool {
	var result struct {
		Shortable    bool `json:"shortable"`
		EasyToBorrow bool `json:"easy_to_borrow"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.get(ctx, c.baseURL+"/v2/assets/"+symbol, &result)
	if err != nil {
		return false
	}
	return result.Shortable && result.EasyToBorrow
}

func (c *Client) LatestQuote(ctx context.Context, symbol string) (AlpacaQuote, error) {
	var result struct {
		Quote AlpacaQuote `json:"quote"`
	}
	err := c.get(ctx, c.dataURL+"/v2/stocks/"+symbol+"/quotes/latest?feed="+c.DataFeed(), &result)
	if err != nil {
		return result.Quote, err
	}
	return result.Quote, nil
}

// CancelOrder cancels a pending order by ID.
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/v2/orders/"+orderID, nil)
	if err != nil {
		return fmt.Errorf("create cancel request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancel failed: status=%d body=%s", resp.StatusCode, string(body))
	}
	return nil
}

// ClosePosition closes a position directly via the Alpaca API.
func (c *Client) ClosePosition(ctx context.Context, symbol string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/v2/positions/"+symbol, nil)
	if err != nil {
		return fmt.Errorf("create close position request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("close position: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("close position failed: status=%d body=%s", resp.StatusCode, string(body))
	}
	return nil
}

// HistoricalBar is a single bar from Alpaca.
type HistoricalBar struct {
	Timestamp  time.Time `json:"t"`
	Open       float64   `json:"o"`
	High       float64   `json:"h"`
	Low        float64   `json:"l"`
	Close      float64   `json:"c"`
	Volume     int64     `json:"v"`
	TradeCount int64     `json:"n"`
	VWAP       float64   `json:"vw"`
}

// GetHistoricalBars fetches historical bars for a single symbol with automatic pagination.
func (c *Client) GetHistoricalBars(ctx context.Context, symbol string, start, end time.Time, timeframe string) ([]HistoricalBar, error) {
	var allBars []HistoricalBar
	pageToken := ""

	for {
		url := fmt.Sprintf("%s/v2/stocks/%s/bars?start=%s&end=%s&timeframe=%s&limit=10000&sort=asc&adjustment=split&feed=%s",
			c.dataURL, symbol,
			start.Format(time.RFC3339), end.Format(time.RFC3339),
			timeframe, c.DataFeed())
		if pageToken != "" {
			url += "&page_token=" + pageToken
		}

		var result struct {
			Bars          []HistoricalBar `json:"bars"`
			NextPageToken string          `json:"next_page_token"`
		}
		if err := c.get(ctx, url, &result); err != nil {
			return allBars, fmt.Errorf("fetch bars for %s: %w", symbol, err)
		}

		allBars = append(allBars, result.Bars...)

		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}

	return allBars, nil
}

// SnapshotBar is the bar data within a snapshot response.
type SnapshotBar struct {
	Open      float64   `json:"o"`
	High      float64   `json:"h"`
	Low       float64   `json:"l"`
	Close     float64   `json:"c"`
	Volume    int64     `json:"v"`
	Timestamp time.Time `json:"t"`
	VWAP      float64   `json:"vw"`
}

// Snapshot is the snapshot data for a single symbol.
type Snapshot struct {
	DailyBar     SnapshotBar `json:"dailyBar"`
	PrevDailyBar SnapshotBar `json:"prevDailyBar"`
	MinuteBar    SnapshotBar `json:"minuteBar"`
	LatestTrade  struct {
		Price     float64   `json:"p"`
		Timestamp time.Time `json:"t"`
	} `json:"latestTrade"`
}

// GetSnapshots fetches snapshot data for multiple symbols from the Alpaca data API.
// Returns previous close, today's open/high/volume for each symbol.
// Symbols are batched in groups of 100 (Alpaca limit).
func (c *Client) GetSnapshots(ctx context.Context, symbols []string) (map[string]Snapshot, error) {
	result := make(map[string]Snapshot, len(symbols))
	for i := 0; i < len(symbols); i += 100 {
		end := i + 100
		if end > len(symbols) {
			end = len(symbols)
		}
		batch := symbols[i:end]
		url := fmt.Sprintf("%s/v2/stocks/snapshots?symbols=%s&feed=%s",
			c.dataURL, strings.Join(batch, ","), c.DataFeed())
		var batchResult map[string]Snapshot
		if err := c.get(ctx, url, &batchResult); err != nil {
			return nil, fmt.Errorf("snapshots batch %d-%d: %w", i, end, err)
		}
		for k, v := range batchResult {
			result[k] = v
		}
	}
	return result, nil
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

// computeOrderSlippage computes percentage-based slippage by liquidity tier.
func computeOrderSlippage(price float64, avgDailyVolume float64, liquidBps, midBps, illiquidBps float64) float64 {
	var spreadPct float64
	switch {
	case avgDailyVolume > 5_000_000:
		spreadPct = liquidBps / 10000.0
	case avgDailyVolume > 500_000:
		spreadPct = midBps / 10000.0
	default:
		spreadPct = illiquidBps / 10000.0
	}
	return price * spreadPct
}
