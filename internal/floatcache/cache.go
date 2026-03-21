package floatcache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	cacheFile        = ".cache/float/shares.json"
	staleDuration    = 24 * time.Hour
	yahooQuoteURL    = "https://query2.finance.yahoo.com/v7/finance/quote"
	yahooCrumbURL    = "https://query2.finance.yahoo.com/v1/test/getcrumb"
	yahooCookieURL   = "https://fc.yahoo.com/curveball"
	yahooBatchSize   = 50
	yahooTimeout     = 10 * time.Second
	yahooMinInterval = 500 * time.Millisecond
)

// Entry stores a cached float value for one symbol.
type Entry struct {
	FloatShares float64   `json:"floatShares"`
	FetchedAt   time.Time `json:"fetchedAt"`
}

// Cache provides a file-backed cache for stock float (shares available for public trading).
// It fetches from Yahoo Finance on cache miss or stale (>24h) and persists to disk.
type Cache struct {
	mu      sync.Mutex
	entries map[string]Entry
	path    string

	authMu sync.Mutex
	client *http.Client
	crumb  string
}

// NewCache loads the on-disk cache (or creates a new empty one).
func NewCache() *Cache {
	jar, _ := cookiejar.New(nil)
	c := &Cache{
		entries: make(map[string]Entry),
		path:    cacheFile,
		client:  &http.Client{Jar: jar},
	}
	c.load()
	return c
}

// Get returns the cached float for a symbol. Returns 0 if unknown.
func (c *Cache) Get(symbol string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[symbol]
	if !ok {
		return 0
	}
	return entry.FloatShares
}

// EnsureFresh fetches float data for any symbols that are missing or stale (>24h).
// To avoid blocking startup for large universes, at most maxFreshBatch symbols
// are fetched per call. Remaining symbols return 0 from Get until the next refresh.
const maxFreshBatch = 20000

func (c *Cache) EnsureFresh(ctx context.Context, symbols []string) {
	now := time.Now()
	var needed []string
	c.mu.Lock()
	for _, s := range symbols {
		entry, ok := c.entries[s]
		if !ok || now.Sub(entry.FetchedAt) > staleDuration {
			needed = append(needed, s)
		}
	}
	c.mu.Unlock()

	if len(needed) == 0 {
		return
	}

	if len(needed) > maxFreshBatch {
		log.Printf("Float cache: %d symbols need refresh, capping to %d (remaining served from cache or zero)", len(needed), maxFreshBatch)
		needed = needed[:maxFreshBatch]
	} else {
		log.Printf("Float cache: refreshing %d symbols", len(needed))
	}

	fetched := c.fetchBatched(ctx, needed)
	if len(fetched) == 0 {
		return
	}

	c.mu.Lock()
	for symbol, shares := range fetched {
		c.entries[symbol] = Entry{FloatShares: shares, FetchedAt: now}
	}
	c.mu.Unlock()
	c.save()
	log.Printf("Float cache refreshed %d/%d symbols", len(fetched), len(needed))
}

// Lookup returns the cached float and fetches on-demand if missing.
// Safe for concurrent use during live trading.
func (c *Cache) Lookup(ctx context.Context, symbol string) float64 {
	c.mu.Lock()
	entry, ok := c.entries[symbol]
	c.mu.Unlock()
	if ok && time.Since(entry.FetchedAt) <= staleDuration {
		return entry.FloatShares
	}

	fetched := c.fetchBatched(ctx, []string{symbol})
	if shares, ok := fetched[symbol]; ok {
		now := time.Now()
		c.mu.Lock()
		c.entries[symbol] = Entry{FloatShares: shares, FetchedAt: now}
		c.mu.Unlock()
		c.save()
		return shares
	}

	// Return stale value if fetch failed
	if ok {
		return entry.FloatShares
	}
	return 0
}

func (c *Cache) fetchBatched(ctx context.Context, symbols []string) map[string]float64 {
	if err := c.ensureAuth(ctx); err != nil {
		log.Printf("Float auth error: %v", err)
		return nil
	}
	totalBatches := (len(symbols) + yahooBatchSize - 1) / yahooBatchSize
	result := make(map[string]float64)
	for i := 0; i < len(symbols); i += yahooBatchSize {
		select {
		case <-ctx.Done():
			log.Printf("Float fetch cancelled after %d/%d symbols", len(result), len(symbols))
			return result
		default:
		}
		end := i + yahooBatchSize
		if end > len(symbols) {
			end = len(symbols)
		}
		batchNum := i/yahooBatchSize + 1
		batch := symbols[i:end]
		fetched, err := c.fetchYahooFloat(ctx, batch)
		if err != nil {
			log.Printf("Float fetch error batch=%d/%d: %v", batchNum, totalBatches, err)
			continue
		}
		for k, v := range fetched {
			result[k] = v
		}
		if end < len(symbols) {
			select {
			case <-ctx.Done():
				return result
			case <-time.After(yahooMinInterval):
			}
		}
	}
	return result
}

// ensureAuth obtains a Yahoo session cookie and crumb if not already cached.
func (c *Cache) ensureAuth(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.crumb != "" {
		return nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, yahooTimeout)
	defer cancel()

	// Step 1: hit a known URL to get session cookies set on the jar.
	cookieReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, yahooCookieURL, nil)
	if err != nil {
		return fmt.Errorf("build cookie request: %w", err)
	}
	cookieReq.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.client.Do(cookieReq)
	if err != nil {
		return fmt.Errorf("cookie request: %w", err)
	}
	resp.Body.Close()

	// Step 2: fetch the crumb using the session cookies.
	crumbReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, yahooCrumbURL, nil)
	if err != nil {
		return fmt.Errorf("build crumb request: %w", err)
	}
	crumbReq.Header.Set("User-Agent", "Mozilla/5.0")
	crumbResp, err := c.client.Do(crumbReq)
	if err != nil {
		return fmt.Errorf("crumb request: %w", err)
	}
	defer crumbResp.Body.Close()
	crumbBytes, err := io.ReadAll(io.LimitReader(crumbResp.Body, 256))
	if err != nil {
		return fmt.Errorf("read crumb: %w", err)
	}
	crumb := strings.TrimSpace(string(crumbBytes))
	if crumb == "" || crumbResp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to obtain crumb (status %d)", crumbResp.StatusCode)
	}
	c.crumb = crumb
	return nil
}

func (c *Cache) fetchYahooFloat(ctx context.Context, symbols []string) (map[string]float64, error) {
	reqCtx, cancel := context.WithTimeout(ctx, yahooTimeout)
	defer cancel()

	u, _ := url.Parse(yahooQuoteURL)
	q := u.Query()
	q.Set("symbols", strings.Join(symbols, ","))
	q.Set("fields", "floatShares,sharesOutstanding")
	q.Set("crumb", c.crumb)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Crumb expired; clear it so next batch re-authenticates.
		c.authMu.Lock()
		c.crumb = ""
		c.authMu.Unlock()
		return nil, fmt.Errorf("yahoo finance returned status 401 (crumb expired)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yahoo finance returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		QuoteResponse struct {
			Result []struct {
				Symbol            string  `json:"symbol"`
				FloatShares       float64 `json:"floatShares"`
				SharesOutstanding float64 `json:"sharesOutstanding"`
			} `json:"result"`
			Error *struct {
				Code        string `json:"code"`
				Description string `json:"description"`
			} `json:"error"`
		} `json:"quoteResponse"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse yahoo response: %w", err)
	}
	if envelope.QuoteResponse.Error != nil {
		return nil, fmt.Errorf("yahoo api error: %s", envelope.QuoteResponse.Error.Description)
	}

	result := make(map[string]float64, len(envelope.QuoteResponse.Result))
	for _, item := range envelope.QuoteResponse.Result {
		shares := item.FloatShares
		if shares <= 0 {
			shares = item.SharesOutstanding
		}
		if shares > 0 {
			result[item.Symbol] = shares
		}
	}
	return result, nil
}

func (c *Cache) load() {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var entries map[string]Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("Float cache corrupt, starting fresh: %v", err)
		return
	}
	c.entries = entries
}

func (c *Cache) save() {
	c.mu.Lock()
	data, err := json.Marshal(c.entries)
	c.mu.Unlock()
	if err != nil {
		return
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, c.path)
}
