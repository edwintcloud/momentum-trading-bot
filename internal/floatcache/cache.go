package floatcache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	yahooQuoteURL    = "https://query1.finance.yahoo.com/v7/finance/quote"
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
}

// NewCache loads the on-disk cache (or creates a new empty one).
func NewCache() *Cache {
	c := &Cache{
		entries: make(map[string]Entry),
		path:    cacheFile,
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
// Intended to be called once at startup or beginning of each trading day.
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
	result := make(map[string]float64)
	for i := 0; i < len(symbols); i += yahooBatchSize {
		end := i + yahooBatchSize
		if end > len(symbols) {
			end = len(symbols)
		}
		batch := symbols[i:end]
		fetched, err := fetchYahooFloat(ctx, batch)
		if err != nil {
			log.Printf("Float fetch error batch=%d: %v", i/yahooBatchSize, err)
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

func fetchYahooFloat(ctx context.Context, symbols []string) (map[string]float64, error) {
	reqCtx, cancel := context.WithTimeout(ctx, yahooTimeout)
	defer cancel()

	u, _ := url.Parse(yahooQuoteURL)
	q := u.Query()
	q.Set("symbols", strings.Join(symbols, ","))
	q.Set("fields", "floatShares,sharesOutstanding")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

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
