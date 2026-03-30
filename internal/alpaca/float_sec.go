package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SEC EDGAR requires a descriptive User-Agent header per their fair access policy.
	secUserAgent = "momentum-trading-bot admin@localhost"

	// secTickersURL returns a JSON object mapping index → {cik_str, ticker, title}.
	secTickersURL = "https://www.sec.gov/files/company_tickers.json"

	// secFramesURLTemplate returns all filers' values for a given XBRL concept/period.
	// The concept name and period are both substituted: concept then period.
	secFramesURLTemplate = "https://data.sec.gov/api/xbrl/frames/us-gaap/%s/shares/%s.json"

	// secSharesConcepts lists XBRL concepts that report share counts.
	// Companies file under different tags, so we try all and merge results.

	floatCacheDir  = ".cache"
	floatCacheFile = "float-data.csv"

	// floatCacheMaxAge controls how old the cache can be before a refresh is attempted.
	floatCacheMaxAge = 30 * 24 * time.Hour // 30 days
)

// FloatCachePath returns the default cache file path for float data.
func FloatCachePath() string {
	return filepath.Join(floatCacheDir, floatCacheFile)
}

// LoadOrFetchFloatData tries to load float data from the local cache. If the
// cache is missing or older than floatCacheMaxAge, it fetches fresh data from
// SEC EDGAR and saves the cache. Returns the number of symbols loaded.
func (fs *FloatStore) LoadOrFetchFloatData(ctx context.Context) (int, error) {
	cachePath := FloatCachePath()

	// Check if cache exists and is fresh enough.
	if info, err := os.Stat(cachePath); err == nil {
		age := time.Since(info.ModTime())
		if age < floatCacheMaxAge {
			if err := fs.LoadFromCSV(cachePath); err == nil && fs.Len() > 0 {
				log.Printf("float-store: loaded %d symbols from cache (age %s)", fs.Len(), age.Round(time.Hour))
				return fs.Len(), nil
			}
		}
	}

	// Cache miss or stale — fetch from SEC EDGAR.
	count, err := fs.fetchFromSEC(ctx)
	if err != nil {
		// If fetch fails but we have a stale cache, use it.
		if loadErr := fs.LoadFromCSV(cachePath); loadErr == nil && fs.Len() > 0 {
			log.Printf("float-store: SEC fetch failed (%v), using stale cache (%d symbols)", err, fs.Len())
			return fs.Len(), nil
		}
		return 0, fmt.Errorf("fetch float data from SEC EDGAR: %w", err)
	}

	// Save to cache.
	if saveErr := fs.saveToCSV(cachePath); saveErr != nil {
		log.Printf("float-store: cache save warning: %v", saveErr)
	}
	log.Printf("float-store: fetched %d symbols from SEC EDGAR, cached to %s", count, cachePath)
	return count, nil
}

// fetchFromSEC fetches shares-outstanding data from SEC EDGAR's XBRL frames
// API and populates the FloatStore. This uses HTTP requests:
//  1. company_tickers.json — maps CIK to ticker symbol
//  2. XBRL frames — bulk shares outstanding across multiple concepts and quarters
func (fs *FloatStore) fetchFromSEC(ctx context.Context) (int, error) {
	client := &http.Client{Timeout: 60 * time.Second}

	// Step 1: Build CIK → ticker mapping.
	cikToTicker, err := fetchCIKTickerMap(ctx, client)
	if err != nil {
		return 0, fmt.Errorf("fetch CIK-ticker map: %w", err)
	}

	// XBRL concepts that report share counts — companies file under different tags.
	concepts := []string{
		"CommonStockSharesOutstanding",
		"EntityCommonStockSharesOutstanding",
		"SharesOutstanding",
	}

	// Step 2: Build list of recent completed quarters to try.
	// Start from the last fully completed quarter (not the current in-progress one).
	now := time.Now()
	currentQuarter := (int(now.Month())-1)/3 + 1
	// Go to the start of the current quarter, then back one day → last completed quarter.
	lastCompletedQEnd := time.Date(now.Year(), time.Month((currentQuarter-1)*3+1), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)

	periods := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		d := lastCompletedQEnd.AddDate(0, -3*i, 0)
		y := d.Year()
		q := (int(d.Month())-1)/3 + 1
		periods = append(periods, fmt.Sprintf("CY%dQ%dI", y, q))
	}

	// Step 3: For the best available quarter, fetch all concepts and merge.
	for _, period := range periods {
		total := 0
		for _, concept := range concepts {
			count, fetchErr := fs.fetchFrameData(ctx, client, concept, period, cikToTicker)
			if fetchErr != nil {
				continue
			}
			total += count
		}
		if total > 0 {
			log.Printf("float-store: SEC frames %s: %d symbols (across %d concepts)", period, fs.Len(), len(concepts))
			return fs.Len(), nil
		}
		log.Printf("float-store: SEC frames %s: no data, trying older quarter", period)
	}

	return 0, fmt.Errorf("no shares-outstanding data found in recent quarters")
}

type secTickerEntry struct {
	CIK    json.Number `json:"cik_str"`
	Ticker string      `json:"ticker"`
}

func fetchCIKTickerMap(ctx context.Context, client *http.Client) (map[int64]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, secTickersURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", secUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SEC tickers: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50 MB limit
	if err != nil {
		return nil, err
	}

	// Format: { "0": {"cik_str":320193, "ticker":"AAPL", "title":"..."}, ... }
	var raw map[string]secTickerEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse SEC tickers JSON: %w", err)
	}

	result := make(map[int64]string, len(raw))
	for _, entry := range raw {
		cik, err := entry.CIK.Int64()
		if err != nil {
			continue
		}
		result[cik] = strings.ToUpper(entry.Ticker)
	}
	return result, nil
}

type secFrameResponse struct {
	Data []secFrameDataPoint `json:"data"`
}

type secFrameDataPoint struct {
	CIK int64   `json:"cik"`
	Val float64 `json:"val"`
}

func (fs *FloatStore) fetchFrameData(ctx context.Context, client *http.Client, concept, period string, cikToTicker map[int64]string) (int, error) {
	url := fmt.Sprintf(secFramesURLTemplate, concept, period)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", secUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("period %s not available", period)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("SEC frames: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20)) // 100 MB limit
	if err != nil {
		return 0, err
	}

	var frame secFrameResponse
	if err := json.Unmarshal(body, &frame); err != nil {
		return 0, fmt.Errorf("parse SEC frames JSON: %w", err)
	}

	loaded := 0
	for _, dp := range frame.Data {
		ticker, ok := cikToTicker[dp.CIK]
		if !ok || ticker == "" {
			continue
		}
		shares := int64(dp.Val)
		if shares <= 0 {
			continue
		}
		fs.Set(ticker, shares)
		loaded++
	}
	return loaded, nil
}

// saveToCSV writes the FloatStore contents to a CSV file (SYMBOL,SHARES format).
func (fs *FloatStore) saveToCSV(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, "symbol,float"); err != nil {
		return err
	}
	fs.floats.Range(func(key, value any) bool {
		sym := key.(string)
		shares := value.(int64)
		if _, err := fmt.Fprintf(f, "%s,%d\n", sym, shares); err != nil {
			return false
		}
		return true
	})
	return nil
}
