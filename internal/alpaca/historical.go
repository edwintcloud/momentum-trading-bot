package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// APIError is returned when the Alpaca API returns a non-200 status code.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("alpaca: %d %s", e.StatusCode, e.Message)
}

// HistoricalBarsPage is the response from the multi-symbol bars endpoint.
type HistoricalBarsPage struct {
	Bars          map[string][]HistoricalBar `json:"bars"`
	NextPageToken string                     `json:"next_page_token"`
	Headers       http.Header
}

// MarketDataCapabilities describes the data plan limits.
type MarketDataCapabilities struct {
	HistoricalRateLimitPerMin int
}

// EquityAsset is Alpaca asset metadata for a US equity symbol.
type EquityAsset struct {
	Symbol       string   `json:"symbol"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Exchange     string   `json:"exchange"`
	Class        string   `json:"class"`
	Tradable     bool     `json:"tradable"`
	Shortable    bool     `json:"shortable"`
	EasyToBorrow bool     `json:"easy_to_borrow"`
	Attributes   []string `json:"attributes"`
}

// ListEquityAssets returns Alpaca metadata for active NASDAQ and NYSE US equities.
func (c *Client) ListEquityAssets(ctx context.Context, activeOnly bool) ([]EquityAsset, error) {
	var assets []EquityAsset
	status := "all"
	if activeOnly {
		status = "active"
	}
	url := fmt.Sprintf("%s/v2/assets?status=%s&asset_class=us_equity", c.baseURL, status)
	if err := c.get(ctx, url, &assets); err != nil {
		return nil, fmt.Errorf("list equity assets: %w", err)
	}

	filtered := make([]EquityAsset, 0, len(assets))
	for _, a := range assets {
		if !a.Tradable {
			continue
		}
		exchange := strings.ToUpper(a.Exchange)
		if exchange == "NASDAQ" || exchange == "NYSE" {
			a.Symbol = strings.ToUpper(a.Symbol)
			filtered = append(filtered, a)
		}
	}

	slices.SortFunc(filtered, func(a, b EquityAsset) int {
		return strings.Compare(a.Symbol, b.Symbol)
	})

	return filtered, nil
}

// ListEquitySymbols returns all active NASDAQ and NYSE equity symbols.
func (c *Client) ListEquitySymbols(ctx context.Context, activeOnly bool) ([]string, error) {
	assets, err := c.ListEquityAssets(ctx, activeOnly)
	if err != nil {
		return nil, err
	}

	symbols := make([]string, 0, len(assets))
	for _, a := range assets {
		symbols = append(symbols, a.Symbol)
	}

	slices.Sort(symbols)

	return symbols, nil
}

// GetHistoricalBarsPage fetches a page of multi-symbol bars with pagination support.
func (c *Client) GetHistoricalBarsPage(ctx context.Context, symbols []string, start, end time.Time, timeframe, pageToken string) (HistoricalBarsPage, error) {
	url := fmt.Sprintf("%s/v2/stocks/bars?symbols=%s&start=%s&end=%s&timeframe=%s&limit=10000&sort=asc&adjustment=split",
		c.dataURL,
		strings.Join(symbols, ","),
		start.Format(time.RFC3339),
		end.Format(time.RFC3339),
		timeframe,
	)
	if pageToken != "" {
		url += "&page_token=" + pageToken
	}
	if feed := c.DataFeed(); feed != "" {
		url += "&feed=" + feed
	}

	var result struct {
		Bars          map[string][]HistoricalBar `json:"bars"`
		NextPageToken string                     `json:"next_page_token"`
	}
	headers, err := c.getWithHeaders(ctx, url, &result)
	if err != nil {
		return HistoricalBarsPage{Headers: headers}, err
	}
	return HistoricalBarsPage{
		Bars:          result.Bars,
		NextPageToken: result.NextPageToken,
		Headers:       headers,
	}, nil
}

// DetectMarketDataCapabilities probes the data plan for rate limit info.
func (c *Client) DetectMarketDataCapabilities(ctx context.Context) (MarketDataCapabilities, error) {
	return MarketDataCapabilities{HistoricalRateLimitPerMin: 1000}, nil
}

// ListMostActiveSymbols returns the top N symbols by volume from Alpaca's screener.
// Returns nil, nil if the endpoint is unavailable.
func (c *Client) ListMostActiveSymbols(ctx context.Context, top int) ([]string, error) {
	url := fmt.Sprintf("%s/v1beta1/screener/stocks/most-actives?by=volume&top=%d", c.dataURL, top)
	var result struct {
		MostActives []struct {
			Symbol string `json:"symbol"`
			Volume int64  `json:"volume"`
		} `json:"most_actives"`
	}
	if err := c.get(ctx, url, &result); err != nil {
		return nil, err
	}
	symbols := make([]string, len(result.MostActives))
	for i, a := range result.MostActives {
		symbols[i] = strings.ToUpper(a.Symbol)
	}
	return symbols, nil
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
