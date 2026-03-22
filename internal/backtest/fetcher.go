package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
)

const (
	defaultCacheDir = ".cache/bars"
	defaultTimeframe = "1Day"
)

// FetchRequest describes what historical data to fetch.
type FetchRequest struct {
	Symbols   []string
	Start     time.Time
	End       time.Time
	Timeframe string   // e.g. "1Min", "5Min", "1Hour", "1Day"
	CacheDir  string   // defaults to .cache/bars
}

// Fetcher retrieves historical bars from Alpaca with disk caching.
// Each (symbol, timeframe, start, end) combination is cached as a CSV file.
// Subsequent calls with the same parameters return cached data instantly.
type Fetcher struct {
	client *alpaca.Client
}

// NewFetcher creates a Fetcher backed by the given Alpaca client.
func NewFetcher(client *alpaca.Client) *Fetcher {
	return &Fetcher{client: client}
}

// Fetch retrieves bars for all requested symbols, using cached data when available.
// Returns a merged, time-sorted slice of Ticks suitable for backtesting.
func (f *Fetcher) Fetch(ctx context.Context, req FetchRequest) ([]domain.Tick, error) {
	if len(req.Symbols) == 0 {
		return nil, fmt.Errorf("fetcher: at least one symbol is required")
	}
	if req.Timeframe == "" {
		req.Timeframe = defaultTimeframe
	}
	cacheDir := req.CacheDir
	if cacheDir == "" {
		cacheDir = defaultCacheDir
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("fetcher: create cache dir: %w", err)
	}

	var allTicks []domain.Tick

	for i, symbol := range req.Symbols {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol == "" {
			continue
		}

		cachePath := f.cachePath(cacheDir, symbol, req.Timeframe, req.Start, req.End)

		// Try loading from cache
		ticks, err := f.loadCache(cachePath, symbol)
		if err == nil && len(ticks) > 0 {
			log.Printf("fetcher: [%d/%d] %s — %d bars from cache", i+1, len(req.Symbols), symbol, len(ticks))
			allTicks = append(allTicks, ticks...)
			continue
		}

		// Fetch from Alpaca
		log.Printf("fetcher: [%d/%d] %s — fetching from Alpaca (%s to %s, %s)",
			i+1, len(req.Symbols), symbol,
			req.Start.Format("2006-01-02"), req.End.Format("2006-01-02"), req.Timeframe)

		bars, err := f.client.GetHistoricalBars(ctx, symbol, req.Start, req.End, req.Timeframe)
		if err != nil {
			log.Printf("fetcher: warning: %s fetch failed: %v (skipping)", symbol, err)
			continue
		}
		if len(bars) == 0 {
			log.Printf("fetcher: warning: %s returned 0 bars (skipping)", symbol)
			continue
		}

		ticks = alpacaBarsToTicks(symbol, bars)
		log.Printf("fetcher: [%d/%d] %s — fetched %d bars from Alpaca", i+1, len(req.Symbols), symbol, len(ticks))

		// Cache to disk
		if err := f.writeCache(cachePath, symbol, bars); err != nil {
			log.Printf("fetcher: warning: cache write failed for %s: %v", symbol, err)
		}

		allTicks = append(allTicks, ticks...)
	}

	// Sort all ticks by timestamp for replay
	sort.Slice(allTicks, func(i, j int) bool {
		return allTicks[i].Timestamp.Before(allTicks[j].Timestamp)
	})

	log.Printf("fetcher: total %d bars across %d symbols", len(allTicks), len(req.Symbols))
	return allTicks, nil
}

// cachePath generates a deterministic file path for a given request.
func (f *Fetcher) cachePath(cacheDir, symbol, timeframe string, start, end time.Time) string {
	key := fmt.Sprintf("%s_%s_%s_%s",
		symbol, timeframe,
		start.Format("20060102"), end.Format("20060102"))
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:12]
	filename := fmt.Sprintf("%s_%s_%s.csv", symbol, timeframe, hash)
	return filepath.Join(cacheDir, filename)
}

// loadCache reads cached bars from a CSV file.
func (f *Fetcher) loadCache(path, symbol string) ([]domain.Tick, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read cache csv: %w", err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("cache file empty")
	}

	var ticks []domain.Tick
	for _, record := range records[1:] { // skip header
		if len(record) < 7 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, record[0])
		if err != nil {
			continue
		}
		open, _ := strconv.ParseFloat(record[1], 64)
		high, _ := strconv.ParseFloat(record[2], 64)
		low, _ := strconv.ParseFloat(record[3], 64)
		close_, _ := strconv.ParseFloat(record[4], 64)
		volume, _ := strconv.ParseInt(record[5], 10, 64)
		tradeCount, _ := strconv.ParseInt(record[6], 10, 64)
		vwap, _ := strconv.ParseFloat(record[7], 64)

		ticks = append(ticks, domain.Tick{
			Symbol:    symbol,
			Price:     close_,
			BarOpen:   open,
			BarHigh:   high,
			BarLow:    low,
			Open:      open,
			HighOfDay: high,
			Volume:    volume + tradeCount*0, // tradeCount preserved for reference
			Timestamp: ts,
			RelativeVolume: func() float64 {
				if vwap > 0 {
					return close_ / vwap // rough proxy
				}
				return 0
			}(),
		})
	}

	return ticks, nil
}

// writeCache writes bars to a CSV cache file.
func (f *Fetcher) writeCache(path, symbol string, bars []alpaca.HistoricalBar) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create cache file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Header
	if err := writer.Write([]string{
		"timestamp", "open", "high", "low", "close", "volume", "trade_count", "vwap",
	}); err != nil {
		return err
	}

	for _, bar := range bars {
		record := []string{
			bar.Timestamp.Format(time.RFC3339),
			strconv.FormatFloat(bar.Open, 'f', 6, 64),
			strconv.FormatFloat(bar.High, 'f', 6, 64),
			strconv.FormatFloat(bar.Low, 'f', 6, 64),
			strconv.FormatFloat(bar.Close, 'f', 6, 64),
			strconv.FormatInt(bar.Volume, 10),
			strconv.FormatInt(bar.TradeCount, 10),
			strconv.FormatFloat(bar.VWAP, 'f', 6, 64),
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}

	return nil
}

// alpacaBarsToTicks converts Alpaca bars to domain Ticks.
func alpacaBarsToTicks(symbol string, bars []alpaca.HistoricalBar) []domain.Tick {
	ticks := make([]domain.Tick, 0, len(bars))

	var prevClose float64
	for _, bar := range bars {
		tick := domain.Tick{
			Symbol:    symbol,
			Price:     bar.Close,
			BarOpen:   bar.Open,
			BarHigh:   bar.High,
			BarLow:    bar.Low,
			Open:      bar.Open,
			HighOfDay: bar.High,
			Volume:    bar.Volume,
			Timestamp: bar.Timestamp,
		}

		// Compute gap percent from previous close
		if prevClose > 0 {
			tick.GapPercent = (bar.Open - prevClose) / prevClose * 100
		}
		prevClose = bar.Close

		ticks = append(ticks, tick)
	}

	return ticks
}

// ClearCache removes all cached bar files.
func ClearCache(cacheDir string) error {
	if cacheDir == "" {
		cacheDir = defaultCacheDir
	}
	return os.RemoveAll(cacheDir)
}

// CacheStats returns the number of cached files and total size.
func CacheStats(cacheDir string) (int, int64, error) {
	if cacheDir == "" {
		cacheDir = defaultCacheDir
	}
	var count int
	var totalSize int64
	err := filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, ".csv") {
			count++
			totalSize += info.Size()
		}
		return nil
	})
	return count, totalSize, err
}
