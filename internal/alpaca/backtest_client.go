package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

// BacktestClient wraps Alpaca trading and market data APIs for backtesting.
type BacktestClient struct {
	mu         sync.RWMutex
	cfg        config.AlpacaConfig
	httpClient *http.Client
	assets     map[string]AssetMetadata
}

// MarketDataCapabilities captures plan-dependent market-data behavior detected at runtime.
type MarketDataCapabilities struct {
	DetectedFeed              string
	HistoricalRateLimitPerMin int
	UnlimitedWebsocketSymbols bool
	PlanName                  string
}

// AssetMetadata captures Alpaca asset properties relevant to trading decisions.
type AssetMetadata struct {
	Symbol       string
	Tradable     bool
	Shortable    bool
	EasyToBorrow bool
	Marginable   bool
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

// APIError preserves structured Alpaca HTTP error details.
type APIError struct {
	StatusCode int    `json:"-"`
	Status     string `json:"-"`
	Body       string `json:"-"`
	Code       int    `json:"code"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("alpaca request failed: %s", e.Status)
	}
	return fmt.Sprintf("alpaca request failed: %s: %s", e.Status, body)
}

type asset struct {
	Symbol       string `json:"symbol"`
	Exchange     string `json:"exchange"`
	Tradable     bool   `json:"tradable"`
	Shortable    bool   `json:"shortable"`
	EasyToBorrow bool   `json:"easy_to_borrow"`
	Marginable   bool   `json:"marginable"`
	AssetClass   string `json:"class"`
	Status       string `json:"status"`
}

// BacktestAccount is the subset of account fields used by backtest tuning.
type BacktestAccount struct {
	Equity     string `json:"equity"`
	LastEquity string `json:"last_equity"`
	Cash       string `json:"cash"`
}

var allowedPrimaryExchanges = map[string]struct{}{
	"NASDAQ": {},
	"NYSE":   {},
}

// NewBacktestClient creates a configured Alpaca client for backtesting.
func NewBacktestClient(cfg config.AlpacaConfig) *BacktestClient {
	return &BacktestClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		assets:     make(map[string]AssetMetadata),
	}
}

// SetDataFeed updates the feed used for market-data requests.
func (c *BacktestClient) SetDataFeed(feed string) {
	c.cfg.DataFeed = strings.ToLower(strings.TrimSpace(feed))
}

// DataFeed returns the currently configured market-data feed.
func (c *BacktestClient) DataFeed() string {
	return c.cfg.DataFeed
}

// GetAccount fetches account equity data.
func (c *BacktestClient) GetAccount(ctx context.Context) (BacktestAccount, error) {
	var account BacktestAccount
	err := c.getJSON(ctx, c.cfg.TradingBaseURL+"/v2/account", &account)
	return account, err
}

// Ping validates trading API credentials and reachability.
func (c *BacktestClient) Ping(ctx context.Context) error {
	_, err := c.GetAccount(ctx)
	return err
}

// DetectMarketDataCapabilities probes Alpaca market-data behavior to infer effective plan limits.
func (c *BacktestClient) DetectMarketDataCapabilities(ctx context.Context) (MarketDataCapabilities, error) {
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

// ListEquitySymbols returns Alpaca's tradable US equity symbol universe
// limited to NYSE and NASDAQ primary listings.
func (c *BacktestClient) ListEquitySymbols(ctx context.Context, includeInactive bool) ([]string, error) {
	status := "active"
	if includeInactive {
		status = "all"
	}
	endpoint := c.cfg.TradingBaseURL + fmt.Sprintf("/v2/assets?status=%s&asset_class=us_equity", status)
	var assets []asset
	if err := c.getJSON(ctx, endpoint, &assets); err != nil {
		return nil, err
	}

	symbols := make([]string, 0, len(assets))
	metadata := make(map[string]AssetMetadata, len(assets))
	for _, item := range assets {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		metadata[symbol] = AssetMetadata{
			Symbol:       symbol,
			Tradable:     item.Tradable,
			Shortable:    item.Shortable,
			EasyToBorrow: item.EasyToBorrow,
			Marginable:   item.Marginable,
		}
		if !item.Tradable {
			continue
		}
		if _, ok := allowedPrimaryExchanges[strings.ToUpper(strings.TrimSpace(item.Exchange))]; !ok {
			continue
		}
		symbols = append(symbols, symbol)
	}
	c.mu.Lock()
	c.assets = metadata
	c.mu.Unlock()
	sort.Strings(symbols)
	return symbols, nil
}

// IsShortable reports whether the latest known Alpaca asset metadata marks a
// symbol as both shortable and easy to borrow.
func (c *BacktestClient) IsShortable(symbol string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	metadata, ok := c.assets[strings.ToUpper(strings.TrimSpace(symbol))]
	if !ok {
		return false
	}
	return metadata.Shortable && metadata.EasyToBorrow
}

// GetHistoricalBarsPage fetches a single paginated page of historical bars.
func (c *BacktestClient) GetHistoricalBarsPage(ctx context.Context, symbols []string, start, end time.Time, timeframe, pageToken string) (HistoricalBarsPage, error) {
	normalized := normalizeBacktestSymbols(symbols)
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

func (c *BacktestClient) getJSON(ctx context.Context, endpoint string, target any) error {
	_, err := c.getJSONWithHeaders(ctx, endpoint, target)
	return err
}

func (c *BacktestClient) getJSONWithHeaders(ctx context.Context, endpoint string, target any) (http.Header, error) {
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
	return response.Header.Clone(), decodeBacktestResponse(response, target)
}

func (c *BacktestClient) addHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/json")
	request.Header.Set("APCA-API-KEY-ID", c.cfg.APIKey)
	request.Header.Set("APCA-API-SECRET-KEY", c.cfg.APISecret)
}

func (c *BacktestClient) detectRateLimit(ctx context.Context, feed string) (int, error) {
	endpoint := fmt.Sprintf("%s/v2/stocks/snapshots?symbols=%s&feed=%s", c.cfg.MarketDataBaseURL, url.QueryEscape("AAPL"), url.QueryEscape(feed))
	var payload json.RawMessage
	headers, err := c.getJSONWithHeaders(ctx, endpoint, &payload)
	if err != nil {
		return 0, err
	}
	return parseBacktestRateLimit(headers.Get("X-RateLimit-Limit")), nil
}

func (c *BacktestClient) detectSIPAccess(ctx context.Context) (bool, int) {
	endpoint := fmt.Sprintf("%s/v2/stocks/snapshots?symbols=%s&feed=sip", c.cfg.MarketDataBaseURL, url.QueryEscape("AAPL"))
	var payload json.RawMessage
	headers, err := c.getJSONWithHeaders(ctx, endpoint, &payload)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "subscription does not permit") || strings.Contains(message, "forbidden") || strings.Contains(message, "422") || strings.Contains(message, "403") {
			return false, 0
		}
		return false, 0
	}
	return true, parseBacktestRateLimit(headers.Get("X-RateLimit-Limit"))
}

func decodeBacktestResponse(response *http.Response, target any) error {
	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		trimmed := strings.TrimSpace(string(body))
		apiErr := &APIError{StatusCode: response.StatusCode, Status: response.Status, Body: trimmed}
		_ = json.Unmarshal(body, apiErr)
		return apiErr
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func normalizeBacktestSymbols(symbols []string) []string {
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

func parseBacktestRateLimit(value string) int {
	if value == "" {
		return 0
	}
	var result int
	fmt.Sscanf(strings.TrimSpace(value), "%d", &result)
	return result
}

// IsAPIError checks if the error is an APIError and returns it.
func IsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}
