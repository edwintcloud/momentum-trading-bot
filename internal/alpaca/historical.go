package alpaca

import (
	"context"
	"fmt"
	"net/http"
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

// ListEquitySymbols returns all active NASDAQ and NYSE equity symbols.
func (c *Client) ListEquitySymbols(ctx context.Context, activeOnly bool) ([]string, error) {
	var assets []struct {
		Symbol   string `json:"symbol"`
		Status   string `json:"status"`
		Exchange string `json:"exchange"`
		Class    string `json:"class"`
	}
	url := c.baseURL + "/v2/assets?status=active&asset_class=us_equity"
	if err := c.get(ctx, url, &assets); err != nil {
		return nil, fmt.Errorf("list equity symbols: %w", err)
	}

	symbols := make([]string, 0, len(assets))
	for _, a := range assets {
		exchange := strings.ToUpper(a.Exchange)
		if exchange == "NASDAQ" || exchange == "NYSE" {
			symbols = append(symbols, strings.ToUpper(a.Symbol))
		}
	}
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
	if c.paper {
		return MarketDataCapabilities{HistoricalRateLimitPerMin: 200}, nil
	}
	return MarketDataCapabilities{HistoricalRateLimitPerMin: 600}, nil
}

// DataFeed returns the data feed name based on the account type.
func (c *Client) DataFeed() string {
	if c.paper {
		return "iex"
	}
	return "sip"
}
