package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
)

var easternLocation = mustLoadLocation("America/New_York")

// Client wraps Alpaca trading and market data APIs.
type Client struct {
	cfg        config.AlpacaConfig
	httpClient *http.Client
}

// MarketDataCapabilities captures plan-dependent market-data behavior detected at runtime.
type MarketDataCapabilities struct {
	DetectedFeed              string
	HistoricalRateLimitPerMin int
	UnlimitedWebsocketSymbols bool
	PlanName                  string
}

// Account is the subset of account fields used by the trading bot.
type Account struct {
	Equity         string `json:"equity"`
	LastEquity     string `json:"last_equity"`
	PortfolioValue string `json:"portfolio_value"`
	Cash           string `json:"cash"`
}

// BrokerPosition mirrors Alpaca's open position response fields needed locally.
type BrokerPosition struct {
	Symbol        string `json:"symbol"`
	Qty           string `json:"qty"`
	AvgEntryPrice string `json:"avg_entry_price"`
	CurrentPrice  string `json:"current_price"`
}

type asset struct {
	Symbol     string `json:"symbol"`
	Tradable   bool   `json:"tradable"`
	AssetClass string `json:"class"`
	Status     string `json:"status"`
}

// RESTBar is a REST representation of an Alpaca stock bar.
type RESTBar struct {
	Open      float64   `json:"o"`
	High      float64   `json:"h"`
	Low       float64   `json:"l"`
	Close     float64   `json:"c"`
	Volume    int64     `json:"v"`
	Timestamp time.Time `json:"t"`
}

// HistoricalBarsPage is a single paginated response from Alpaca's historical bars API.
type HistoricalBarsPage struct {
	Bars          map[string][]RESTBar
	NextPageToken string
	Headers       http.Header
}

// Snapshot contains the subset of snapshot fields required by the scanner.
type Snapshot struct {
	MinuteBar    *RESTBar `json:"minuteBar"`
	DailyBar     *RESTBar `json:"dailyBar"`
	PrevDailyBar *RESTBar `json:"prevDailyBar"`
}

// NewsArticle represents a single article from the Alpaca News API.
type NewsArticle struct {
	ID        int64    `json:"id"`
	Headline  string   `json:"headline"`
	URL       string   `json:"url"`
	Symbols   []string `json:"symbols"`
	CreatedAt string   `json:"created_at"`
}

// Order represents the subset of the Alpaca order object needed by execution polling.
type Order struct {
	ID             string `json:"id"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	Status         string `json:"status"`
	Qty            string `json:"qty"`
	FilledQty      string `json:"filled_qty"`
	FilledAvgPrice string `json:"filled_avg_price"`
}

// APIError preserves structured Alpaca HTTP error details for callers that
// need to react to specific rejection payloads.
type APIError struct {
	StatusCode    int    `json:"-"`
	Status        string `json:"-"`
	Body          string `json:"-"`
	Code          int    `json:"code"`
	Message       string `json:"message"`
	Available     string `json:"available"`
	ExistingQty   string `json:"existing_qty"`
	HeldForOrders string `json:"held_for_orders"`
	Symbol        string `json:"symbol"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		payload, err := json.Marshal(e)
		if err == nil {
			body = string(payload)
		}
	}
	if body == "" {
		return fmt.Sprintf("alpaca request failed: %s", e.Status)
	}
	return fmt.Sprintf("alpaca request failed: %s: %s", e.Status, body)
}

// AvailableQuantityFromError returns the broker-reported available share count
// when Alpaca rejects a sell because the requested quantity is too large.
func AvailableQuantityFromError(err error) (int64, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	if apiErr.Available == "" {
		return 0, false
	}
	quantity, parseErr := parseWholeShares(apiErr.Available)
	if parseErr != nil {
		return 0, false
	}
	return quantity, true
}

// ParseShareQuantity converts Alpaca quantity strings like "170" or "170.0000"
// into whole-share counts used by the bot.
func ParseShareQuantity(value string) (int64, error) {
	return parseWholeShares(value)
}

// IsInsufficientQuantityError reports whether Alpaca rejected an order because
// the requested sell size exceeded the currently available broker quantity.
func IsInsufficientQuantityError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code == 40310000 {
			return true
		}
		message := strings.ToLower(apiErr.Message)
		return strings.Contains(message, "insufficient qty available for order")
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "insufficient qty available for order") || strings.Contains(message, "40310000")
}

// StreamMessage is a normalized market-data stream event.
type StreamMessage struct {
	Type          string
	Message       string
	RawPayload    string
	Symbol        string
	Open          float64
	High          float64
	Low           float64
	Close         float64
	Volume        int64
	Timestamp     time.Time
	StatusCode    string
	StatusMessage string
	ReasonCode    string
	ReasonMessage string
}

// NewClient creates a configured Alpaca client.
func NewClient(cfg config.AlpacaConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

// Ping validates trading API credentials and reachability.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetAccount(ctx)
	return err
}

// SetDataFeed updates the feed used for market-data REST and websocket requests.
func (c *Client) SetDataFeed(feed string) {
	c.cfg.DataFeed = strings.ToLower(strings.TrimSpace(feed))
}

// DataFeed returns the currently configured market-data feed.
func (c *Client) DataFeed() string {
	return c.cfg.DataFeed
}

// DetectMarketDataCapabilities probes Alpaca market-data behavior to infer effective plan limits.
func (c *Client) DetectMarketDataCapabilities(ctx context.Context) (MarketDataCapabilities, error) {
	capabilities := MarketDataCapabilities{
		DetectedFeed:              c.cfg.DataFeed,
		HistoricalRateLimitPerMin: 200,
		UnlimitedWebsocketSymbols: false,
		PlanName:                  "basic",
	}

	rateLimit, err := c.detectRateLimit(ctx, "iex")
	if err == nil && rateLimit > 0 {
		capabilities.HistoricalRateLimitPerMin = rateLimit
	}

	sipOK, sipRateLimit := c.detectSIPAccess(ctx)
	if sipRateLimit > capabilities.HistoricalRateLimitPerMin {
		capabilities.HistoricalRateLimitPerMin = sipRateLimit
	}
	if sipOK {
		capabilities.DetectedFeed = "sip"
		capabilities.UnlimitedWebsocketSymbols = true
		capabilities.PlanName = "algo_trader_plus"
	}

	if capabilities.HistoricalRateLimitPerMin >= 10000 {
		capabilities.PlanName = "algo_trader_plus"
		capabilities.UnlimitedWebsocketSymbols = true
		if capabilities.DetectedFeed == "iex" {
			capabilities.DetectedFeed = "sip"
		}
	}

	return capabilities, nil
}

// GetAccount fetches account equity data.
func (c *Client) GetAccount(ctx context.Context) (Account, error) {
	var account Account
	err := c.getJSON(ctx, c.cfg.TradingBaseURL+"/v2/account", &account)
	return account, err
}

// ListOpenPositions fetches current broker positions.
func (c *Client) ListOpenPositions(ctx context.Context) ([]BrokerPosition, error) {
	var positions []BrokerPosition
	err := c.getJSON(ctx, c.cfg.TradingBaseURL+"/v2/positions", &positions)
	return positions, err
}

// GetPosition fetches a single open position by symbol. Returns (position, true, nil)
// when found, (BrokerPosition{}, false, nil) when no position is held, or an error
// for unexpected failures.
func (c *Client) GetPosition(ctx context.Context, symbol string) (BrokerPosition, bool, error) {
	var pos BrokerPosition
	err := c.getJSON(ctx, c.cfg.TradingBaseURL+"/v2/positions/"+url.PathEscape(strings.ToUpper(symbol)), &pos)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return BrokerPosition{}, false, nil
		}
		return BrokerPosition{}, false, err
	}
	return pos, true, nil
}

// GetSnapshot fetches the latest snapshot for a symbol.
func (c *Client) GetSnapshot(ctx context.Context, symbol string) (Snapshot, error) {
	snapshots, err := c.GetSnapshots(ctx, []string{symbol})
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, ok := snapshots[strings.ToUpper(symbol)]
	if !ok {
		return Snapshot{}, fmt.Errorf("snapshot missing for %s", symbol)
	}
	return snapshot, nil
}

// GetSnapshots fetches the latest snapshots for multiple symbols in one request.
func (c *Client) GetSnapshots(ctx context.Context, symbols []string) (map[string]Snapshot, error) {
	normalized := normalizeSymbols(symbols)
	if len(normalized) == 0 {
		return map[string]Snapshot{}, nil
	}
	endpoint := fmt.Sprintf("%s/v2/stocks/snapshots?symbols=%s&feed=%s", c.cfg.MarketDataBaseURL, url.QueryEscape(strings.Join(normalized, ",")), url.QueryEscape(c.cfg.DataFeed))
	// Alpaca returns snapshots as a direct symbol-keyed map
	var snapshots map[string]Snapshot
	if err := c.getJSON(ctx, endpoint, &snapshots); err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return map[string]Snapshot{}, nil
	}
	return snapshots, nil
}

// GetPremarketVolume sums minute bars for the current premarket session.
func (c *Client) GetPremarketVolume(ctx context.Context, symbol string, day time.Time) (int64, error) {
	volumes, err := c.GetPremarketVolumes(ctx, []string{symbol}, day)
	if err != nil {
		return 0, err
	}
	return volumes[strings.ToUpper(symbol)], nil
}

// GetPremarketVolumes sums minute bars for the current premarket session for multiple symbols in one request.
func (c *Client) GetPremarketVolumes(ctx context.Context, symbols []string, day time.Time) (map[string]int64, error) {
	normalized := normalizeSymbols(symbols)
	if len(normalized) == 0 {
		return map[string]int64{}, nil
	}
	marketDay := day.In(easternLocation)
	start := time.Date(marketDay.Year(), marketDay.Month(), marketDay.Day(), 4, 0, 0, 0, easternLocation).UTC()
	end := time.Date(marketDay.Year(), marketDay.Month(), marketDay.Day(), 9, 29, 59, 0, easternLocation).UTC()
	endpoint := fmt.Sprintf(
		"%s/v2/stocks/bars?symbols=%s&timeframe=1Min&start=%s&end=%s&adjustment=raw&feed=%s&limit=10000",
		c.cfg.MarketDataBaseURL,
		url.QueryEscape(strings.Join(normalized, ",")),
		url.QueryEscape(start.Format(time.RFC3339)),
		url.QueryEscape(end.Format(time.RFC3339)),
		url.QueryEscape(c.cfg.DataFeed),
	)
	var payload struct {
		Bars map[string][]RESTBar `json:"bars"`
	}
	if err := c.getJSON(ctx, endpoint, &payload); err != nil {
		return nil, err
	}
	volumes := make(map[string]int64, len(normalized))
	for symbol, bars := range payload.Bars {
		var total int64
		for _, bar := range bars {
			total += bar.Volume
		}
		volumes[strings.ToUpper(symbol)] = total
	}
	return volumes, nil
}

// GetHistoricalBars fetches historical bars for multiple symbols over a window.
func (c *Client) GetHistoricalBars(ctx context.Context, symbols []string, start, end time.Time, timeframe string) (map[string][]RESTBar, error) {
	normalized := normalizeSymbols(symbols)
	if len(normalized) == 0 {
		return map[string][]RESTBar{}, nil
	}
	if timeframe == "" {
		timeframe = "1Min"
	}

	allBars := make(map[string][]RESTBar, len(normalized))
	pageToken := ""
	for {
		page, err := c.GetHistoricalBarsPage(ctx, normalized, start, end, timeframe, pageToken)
		if err != nil {
			return nil, err
		}
		for symbol, bars := range page.Bars {
			allBars[strings.ToUpper(symbol)] = append(allBars[strings.ToUpper(symbol)], bars...)
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}

	return allBars, nil
}

// GetHistoricalBarsPage fetches a single paginated page of historical bars.
func (c *Client) GetHistoricalBarsPage(ctx context.Context, symbols []string, start, end time.Time, timeframe, pageToken string) (HistoricalBarsPage, error) {
	normalized := normalizeSymbols(symbols)
	if len(normalized) == 0 {
		return HistoricalBarsPage{Bars: map[string][]RESTBar{}}, nil
	}
	if timeframe == "" {
		timeframe = "1Min"
	}
	endpoint := fmt.Sprintf(
		"%s/v2/stocks/bars?symbols=%s&timeframe=%s&start=%s&end=%s&adjustment=raw&feed=%s&sort=asc&limit=10000",
		c.cfg.MarketDataBaseURL,
		url.QueryEscape(strings.Join(normalized, ",")),
		url.QueryEscape(timeframe),
		url.QueryEscape(start.UTC().Format(time.RFC3339)),
		url.QueryEscape(end.UTC().Format(time.RFC3339)),
		url.QueryEscape(c.cfg.DataFeed),
	)
	if pageToken != "" {
		endpoint += "&page_token=" + url.QueryEscape(pageToken)
	}

	var payload struct {
		Bars          map[string][]RESTBar `json:"bars"`
		NextPageToken string               `json:"next_page_token"`
	}
	headers, err := c.getJSONWithHeaders(ctx, endpoint, &payload)
	page := HistoricalBarsPage{
		Bars:          payload.Bars,
		NextPageToken: payload.NextPageToken,
		Headers:       headers,
	}
	if page.Bars == nil {
		page.Bars = map[string][]RESTBar{}
	}
	if err != nil {
		return page, err
	}
	return page, nil
}

// ListActiveEquitySymbols returns Alpaca's current tradable US equity symbol universe.
func (c *Client) ListActiveEquitySymbols(ctx context.Context) ([]string, error) {
	endpoint := c.cfg.TradingBaseURL + "/v2/assets?status=active&asset_class=us_equity"
	var assets []asset
	if err := c.getJSON(ctx, endpoint, &assets); err != nil {
		return nil, err
	}

	symbols := make([]string, 0, len(assets))
	for _, item := range assets {
		if !item.Tradable {
			continue
		}
		symbols = append(symbols, strings.ToUpper(strings.TrimSpace(item.Symbol)))
	}
	sort.Strings(symbols)
	return symbols, nil
}

// NewsCatalyst holds a headline and its source URL.
type NewsCatalyst struct {
	Headline string
	URL      string
}

// GetNews fetches recent news articles for the given symbols from the Alpaca News API.
func (c *Client) GetNews(ctx context.Context, symbols []string, limit int) (map[string]NewsCatalyst, error) {
	normalized := normalizeSymbols(symbols)
	if len(normalized) == 0 {
		return map[string]NewsCatalyst{}, nil
	}
	if limit < 1 {
		limit = 3
	}
	endpoint := fmt.Sprintf("%s/v1beta1/news?symbols=%s&limit=%d&sort=desc",
		c.cfg.MarketDataBaseURL,
		url.QueryEscape(strings.Join(normalized, ",")),
		limit,
	)
	var payload struct {
		News []NewsArticle `json:"news"`
	}
	if err := c.getJSON(ctx, endpoint, &payload); err != nil {
		return nil, err
	}
	// Return the first headline found per symbol
	catalysts := make(map[string]NewsCatalyst, len(normalized))
	for _, article := range payload.News {
		for _, sym := range article.Symbols {
			upper := strings.ToUpper(sym)
			if _, exists := catalysts[upper]; !exists && article.Headline != "" {
				catalysts[upper] = NewsCatalyst{Headline: article.Headline, URL: article.URL}
			}
		}
	}
	return catalysts, nil
}

// SubmitOrder creates a limit order with extended_hours enabled so it executes
// during pre-market, regular, and post-market sessions.
func (c *Client) SubmitOrder(ctx context.Context, request domain.OrderRequest) (Order, error) {
	if !markethours.IsTradableSessionAt(request.Timestamp) {
		return Order{}, fmt.Errorf("order rejected outside tradable session for %s at %s", request.Symbol, request.Timestamp.UTC().Format(time.RFC3339))
	}
	payload := map[string]any{
		"symbol":         request.Symbol,
		"qty":            strconv.FormatInt(request.Quantity, 10),
		"side":           request.Side,
		"type":           "limit",
		"limit_price":    strconv.FormatFloat(request.Price, 'f', 2, 64),
		"time_in_force":  "day",
		"extended_hours": true,
	}
	var order Order
	err := c.postJSON(ctx, c.cfg.TradingBaseURL+"/v2/orders", payload, &order)
	return order, err
}

// GetOrder fetches an order by ID.
func (c *Client) GetOrder(ctx context.Context, orderID string) (Order, error) {
	var order Order
	err := c.getJSON(ctx, c.cfg.TradingBaseURL+"/v2/orders/"+url.PathEscape(orderID), &order)
	return order, err
}

// StreamMarketData consumes Alpaca's stock websocket and normalizes messages.
func (c *Client) StreamMarketData(ctx context.Context, handler func(StreamMessage) error) error {
	backoff := 2 * time.Second
	for {
		err := c.streamOnce(ctx, handler)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		if err := handler(StreamMessage{Type: "stream-error", Message: err.Error()}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, handler func(StreamMessage) error) error {
	streamURL := fmt.Sprintf("%s/v2/%s", c.cfg.MarketDataStreamURL, c.cfg.DataFeed)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, streamURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	authMessage := map[string]string{
		"action": "auth",
		"key":    c.cfg.APIKey,
		"secret": c.cfg.APISecret,
	}
	if err := conn.WriteJSON(authMessage); err != nil {
		return err
	}

	subscriptionSymbols := c.cfg.Symbols
	if c.cfg.SubscribeAllBars || len(subscriptionSymbols) == 0 {
		subscriptionSymbols = []string{"*"}
	}
	subscribeMessage := map[string]any{
		"action":      "subscribe",
		"bars":        subscriptionSymbols,
		"updatedBars": subscriptionSymbols,
		"statuses":    subscriptionSymbols,
	}
	if err := conn.WriteJSON(subscribeMessage); err != nil {
		return err
	}

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var messages []json.RawMessage
		if err := json.Unmarshal(payload, &messages); err != nil {
			return err
		}
		for _, raw := range messages {
			envelopeType, err := extractStreamType(raw)
			if err != nil {
				return err
			}
			switch envelopeType {
			case "success":
				var lifecycle struct {
					Message string `json:"msg"`
				}
				if err := json.Unmarshal(raw, &lifecycle); err != nil {
					return err
				}
				if err := handler(StreamMessage{
					Type:    envelopeType,
					Message: lifecycle.Message,
				}); err != nil {
					return err
				}
				continue
			case "subscription":
				var subscription struct {
					Trades      []string `json:"trades"`
					Quotes      []string `json:"quotes"`
					Bars        []string `json:"bars"`
					UpdatedBars []string `json:"updatedBars"`
					Statuses    []string `json:"statuses"`
				}
				if err := json.Unmarshal(raw, &subscription); err != nil {
					return err
				}
				message := fmt.Sprintf(
					"bars=%s updatedBars=%s statuses=%s",
					summarizeSymbols(subscription.Bars),
					summarizeSymbols(subscription.UpdatedBars),
					summarizeSymbols(subscription.Statuses),
				)
				if err := handler(StreamMessage{
					Type:    envelopeType,
					Message: message,
				}); err != nil {
					return err
				}
				continue
			case "error":
				var streamErr struct {
					Code    int    `json:"code"`
					Message string `json:"msg"`
				}
				if err := json.Unmarshal(raw, &streamErr); err != nil {
					return err
				}
				return fmt.Errorf("alpaca stream error %d: %s", streamErr.Code, streamErr.Message)
			case "b", "u", "d":
				bar, err := decodeBarMessage(raw)
				if err != nil {
					return err
				}
				if err := handler(StreamMessage{
					Type:      envelopeType,
					Symbol:    bar.Symbol,
					Open:      bar.Open,
					High:      bar.High,
					Low:       bar.Low,
					Close:     bar.Close,
					Volume:    bar.Volume,
					Timestamp: bar.Timestamp.UTC(),
				}); err != nil {
					return err
				}
			case "s":
				status, err := decodeStatusMessage(raw)
				if err != nil {
					return err
				}
				if err := handler(StreamMessage{
					Type:          envelopeType,
					Symbol:        status.Symbol,
					StatusCode:    status.StatusCode,
					StatusMessage: status.StatusMessage,
					ReasonCode:    status.ReasonCode,
					ReasonMessage: status.ReasonMessage,
					Timestamp:     status.Timestamp.UTC(),
				}); err != nil {
					return err
				}
			default:
				if err := handler(StreamMessage{
					Type:       envelopeType,
					Message:    "unhandled stream payload",
					RawPayload: summarizeRawMessage(raw),
				}); err != nil {
					return err
				}
			}
		}
	}
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	_, err := c.getJSONWithHeaders(ctx, endpoint, target)
	return err
}

func (c *Client) getJSONWithHeaders(ctx context.Context, endpoint string, target any) (http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.addHeaders(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return response.Header.Clone(), decodeResponse(response, target)
}

func summarizeSymbols(symbols []string) string {
	if len(symbols) == 0 {
		return "[]"
	}
	if len(symbols) == 1 {
		return symbols[0]
	}
	if len(symbols) <= 5 {
		return strings.Join(symbols, ",")
	}
	return fmt.Sprintf("%s,+%d more", strings.Join(symbols[:5], ","), len(symbols)-5)
}

func extractStreamType(raw json.RawMessage) (string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	typeField, ok := envelope["T"]
	if !ok {
		return "", fmt.Errorf("stream payload missing T field: %s", summarizeRawMessage(raw))
	}
	var eventType string
	if err := json.Unmarshal(typeField, &eventType); err != nil {
		return "", err
	}
	return eventType, nil
}

func decodeBarMessage(raw json.RawMessage) (struct {
	Symbol    string
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    int64
	Timestamp time.Time
}, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return struct {
			Symbol    string
			Open      float64
			High      float64
			Low       float64
			Close     float64
			Volume    int64
			Timestamp time.Time
		}{}, err
	}
	var bar struct {
		Symbol    string
		Open      float64
		High      float64
		Low       float64
		Close     float64
		Volume    int64
		Timestamp time.Time
	}
	if err := unmarshalExactField(payload, "S", &bar.Symbol); err != nil {
		return bar, err
	}
	if err := unmarshalExactField(payload, "o", &bar.Open); err != nil {
		return bar, err
	}
	if err := unmarshalExactField(payload, "h", &bar.High); err != nil {
		return bar, err
	}
	if err := unmarshalExactField(payload, "l", &bar.Low); err != nil {
		return bar, err
	}
	if err := unmarshalExactField(payload, "c", &bar.Close); err != nil {
		return bar, err
	}
	if err := unmarshalExactField(payload, "v", &bar.Volume); err != nil {
		return bar, err
	}
	if err := unmarshalExactField(payload, "t", &bar.Timestamp); err != nil {
		return bar, err
	}
	return bar, nil
}

func decodeStatusMessage(raw json.RawMessage) (struct {
	Symbol        string
	StatusCode    string
	StatusMessage string
	ReasonCode    string
	ReasonMessage string
	Timestamp     time.Time
}, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return struct {
			Symbol        string
			StatusCode    string
			StatusMessage string
			ReasonCode    string
			ReasonMessage string
			Timestamp     time.Time
		}{}, err
	}
	var status struct {
		Symbol        string
		StatusCode    string
		StatusMessage string
		ReasonCode    string
		ReasonMessage string
		Timestamp     time.Time
	}
	if err := unmarshalExactField(payload, "S", &status.Symbol); err != nil {
		return status, err
	}
	if err := unmarshalExactField(payload, "sc", &status.StatusCode); err != nil {
		return status, err
	}
	if err := unmarshalExactField(payload, "sm", &status.StatusMessage); err != nil {
		return status, err
	}
	if err := unmarshalExactField(payload, "rc", &status.ReasonCode); err != nil {
		return status, err
	}
	if err := unmarshalExactField(payload, "rm", &status.ReasonMessage); err != nil {
		return status, err
	}
	if err := unmarshalExactField(payload, "t", &status.Timestamp); err != nil {
		return status, err
	}
	return status, nil
}

func unmarshalExactField(payload map[string]json.RawMessage, key string, target any) error {
	value, ok := payload[key]
	if !ok {
		return fmt.Errorf("stream payload missing %s field", key)
	}
	return json.Unmarshal(value, target)
}

func summarizeRawMessage(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if len(text) <= 240 {
		return text
	}
	return text[:240] + "..."
}

func (c *Client) postJSON(ctx context.Context, endpoint string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.addHeaders(request)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeResponse(response, target)
}

func (c *Client) addHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("APCA-API-KEY-ID", c.cfg.APIKey)
	request.Header.Set("APCA-API-SECRET-KEY", c.cfg.APISecret)
}

func (c *Client) detectRateLimit(ctx context.Context, feed string) (int, error) {
	endpoint := fmt.Sprintf("%s/v2/stocks/snapshots?symbols=%s&feed=%s", c.cfg.MarketDataBaseURL, url.QueryEscape("AAPL"), url.QueryEscape(feed))
	var payload struct {
		Snapshots map[string]Snapshot `json:"snapshots"`
	}
	headers, err := c.getJSONWithHeaders(ctx, endpoint, &payload)
	if err != nil {
		return 0, err
	}
	return parseRateLimit(headers.Get("X-RateLimit-Limit")), nil
}

func (c *Client) detectSIPAccess(ctx context.Context) (bool, int) {
	endpoint := fmt.Sprintf("%s/v2/stocks/snapshots?symbols=%s&feed=sip", c.cfg.MarketDataBaseURL, url.QueryEscape("AAPL"))
	var payload struct {
		Snapshots map[string]Snapshot `json:"snapshots"`
	}
	headers, err := c.getJSONWithHeaders(ctx, endpoint, &payload)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "subscription does not permit") || strings.Contains(message, "forbidden") || strings.Contains(message, "422") || strings.Contains(message, "403") {
			return false, 0
		}
		return false, 0
	}
	return true, parseRateLimit(headers.Get("X-RateLimit-Limit"))
}

func parseRateLimit(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

func normalizeSymbols(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		normalized := strings.ToUpper(strings.TrimSpace(symbol))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func parseWholeShares(value string) (int64, error) {
	whole := strings.TrimSpace(value)
	if index := strings.IndexByte(whole, '.'); index >= 0 {
		whole = whole[:index]
	}
	if whole == "" {
		return 0, nil
	}
	return strconv.ParseInt(whole, 10, 64)
}

func decodeResponse(response *http.Response, target any) error {
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		trimmed := strings.TrimSpace(string(body))
		apiErr := &APIError{StatusCode: response.StatusCode, Status: response.Status, Body: trimmed}
		_ = json.Unmarshal(body, apiErr)
		return apiErr
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
