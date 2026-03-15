package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
)

const (
	historicalBatchSize    = 100
	historicalMaxRetries   = 5
	historicalCacheVersion = "v1"
)

var historicalCacheRoot = filepath.Join(".cache", "backtest", "historical-bars")

type historicalFetchJob struct {
	index   int
	start   time.Time
	end     time.Time
	symbols []string
}

type historicalFetchResult struct {
	bars     []backtest.InputBar
	pageHits int
	cacheHit bool
}

type historicalCacheEntry struct {
	Version string
	Feed    string
	Start   time.Time
	End     time.Time
	Symbols []string
	SavedAt time.Time
	Bars    []backtest.InputBar
}

type requestLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newRequestLimiter(requestsPerMinute int) *requestLimiter {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 200
	}
	interval := time.Minute / time.Duration(requestsPerMinute)
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	return &requestLimiter{interval: interval}
}

func (l *requestLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	runAt := now
	if l.next.After(runAt) {
		runAt = l.next
	}
	l.next = runAt.Add(l.interval)
	l.mu.Unlock()

	if !runAt.After(now) {
		return nil
	}
	timer := time.NewTimer(runAt.Sub(now))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *requestLimiter) DelayUntil(next time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if next.After(l.next) {
		l.next = next
	}
}

func fetchBarsFromAlpaca(ctx context.Context, client *alpaca.Client, symbols []string, start, end time.Time, historicalRateLimit int) ([]backtest.InputBar, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("no symbols available for historical fetch")
	}

	jobs := buildHistoricalFetchJobs(symbols, start, end)
	if len(jobs) == 0 {
		return []backtest.InputBar{}, nil
	}

	workerCount := historicalWorkerCount(historicalRateLimit)
	limiter := newRequestLimiter(historicalRateLimit)
	feed := client.DataFeed()
	if strings.TrimSpace(feed) == "" {
		feed = "iex"
	}
	log.Printf("Historical fetch starting jobs=%d symbols=%d workers=%d window=%s..%s", len(jobs), len(symbols), workerCount, start.Format(time.RFC3339), end.Format(time.RFC3339))

	jobCh := make(chan historicalFetchJob)
	resultCh := make(chan historicalFetchResult, len(jobs))
	errCh := make(chan error, 1)

	var completedJobs atomic.Int64
	var cacheHits atomic.Int64
	var cacheMisses atomic.Int64
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func(workerID int) {
			defer workers.Done()
			for job := range jobCh {
				result, err := fetchHistoricalJob(ctx, client, limiter, job, feed)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				if result.cacheHit {
					cacheHits.Add(1)
				} else {
					cacheMisses.Add(1)
				}
				done := completedJobs.Add(1)
				if done == 1 || done%25 == 0 || int(done) == len(jobs) {
					source := "api"
					if result.cacheHit {
						source = "cache"
					}
					log.Printf("Historical fetch progress jobs=%d/%d bars=%d pages=%d last_job=%d worker=%d source=%s", done, len(jobs), len(result.bars), result.pageHits, job.index, workerID, source)
				}
				select {
				case <-ctx.Done():
					return
				case resultCh <- result:
				}
			}
		}(worker + 1)
	}

	go func() {
		defer close(jobCh)
		for _, job := range jobs {
			select {
			case <-ctx.Done():
				return
			case jobCh <- job:
			}
		}
	}()

	go func() {
		workers.Wait()
		close(resultCh)
	}()

	inputBars := make([]backtest.InputBar, 0, len(jobs)*1024)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-errCh:
			if err != nil {
				return nil, err
			}
		case result, ok := <-resultCh:
			if !ok {
				sort.Slice(inputBars, func(i, j int) bool {
					if inputBars[i].Timestamp.Equal(inputBars[j].Timestamp) {
						return inputBars[i].Symbol < inputBars[j].Symbol
					}
					return inputBars[i].Timestamp.Before(inputBars[j].Timestamp)
				})
				log.Printf("Historical cache summary hits=%d misses=%d dir=%s", cacheHits.Load(), cacheMisses.Load(), historicalCacheRoot)
				return inputBars, nil
			}
			inputBars = append(inputBars, result.bars...)
		}
	}
}

func fetchHistoricalJob(ctx context.Context, client *alpaca.Client, limiter *requestLimiter, job historicalFetchJob, feed string) (historicalFetchResult, error) {
	if cached, ok, err := loadHistoricalJobCache(job, feed); err == nil && ok {
		cached.cacheHit = true
		return cached, nil
	} else if err != nil {
		log.Printf("Historical cache read failed job=%d symbols=%d err=%v", job.index, len(job.symbols), err)
	}

	pageToken := ""
	result := historicalFetchResult{
		bars: make([]backtest.InputBar, 0, 1024),
	}
	for {
		page, err := fetchHistoricalPageWithRetry(ctx, client, limiter, job, pageToken)
		if err != nil {
			return historicalFetchResult{}, err
		}
		result.pageHits++
		for symbol, bars := range page.Bars {
			for _, item := range bars {
				result.bars = append(result.bars, backtest.InputBar{
					Timestamp: item.Timestamp.UTC(),
					Symbol:    symbol,
					Open:      item.Open,
					High:      item.High,
					Low:       item.Low,
					Close:     item.Close,
					Volume:    item.Volume,
				})
			}
		}
		applyRateLimitHeaders(limiter, page.Headers)
		if page.NextPageToken == "" {
			if err := saveHistoricalJobCache(job, feed, result); err != nil {
				log.Printf("Historical cache write failed job=%d symbols=%d err=%v", job.index, len(job.symbols), err)
			}
			return result, nil
		}
		pageToken = page.NextPageToken
	}
}

func fetchHistoricalPageWithRetry(ctx context.Context, client *alpaca.Client, limiter *requestLimiter, job historicalFetchJob, pageToken string) (alpaca.HistoricalBarsPage, error) {
	var lastErr error
	var lastPage alpaca.HistoricalBarsPage
	for attempt := 1; attempt <= historicalMaxRetries; attempt++ {
		if err := limiter.Wait(ctx); err != nil {
			return alpaca.HistoricalBarsPage{}, err
		}
		page, err := client.GetHistoricalBarsPage(ctx, job.symbols, job.start, job.end, "1Min", pageToken)
		if err == nil {
			return page, nil
		}

		lastErr = err
		lastPage = page
		if !isRetryableHistoricalError(err) || attempt == historicalMaxRetries {
			log.Printf("Historical fetch failed job=%d symbols=%d page_token=%t attempt=%d err=%v", job.index, len(job.symbols), pageToken != "", attempt, err)
			return alpaca.HistoricalBarsPage{}, fmt.Errorf("historical fetch job %d failed after %d attempts: %w", job.index, attempt, err)
		}

		delay := retryDelay(page.Headers, attempt, err)
		limiter.DelayUntil(time.Now().Add(delay))
		log.Printf("Historical fetch retry job=%d symbols=%d page_token=%t attempt=%d delay=%s err=%v", job.index, len(job.symbols), pageToken != "", attempt, delay, err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return alpaca.HistoricalBarsPage{}, ctx.Err()
		case <-timer.C:
		}
	}
	return lastPage, lastErr
}

func buildHistoricalFetchJobs(symbols []string, start, end time.Time) []historicalFetchJob {
	if len(symbols) == 0 || end.Before(start) {
		return nil
	}
	location, _ := time.LoadLocation("America/New_York")
	if location == nil {
		location = time.UTC
	}

	days := tradingDayWindows(start, end, location)
	jobs := make([]historicalFetchJob, 0, len(days)*(len(symbols)/historicalBatchSize+1))
	index := 0
	for _, day := range days {
		for batchStart := 0; batchStart < len(symbols); batchStart += historicalBatchSize {
			batchEnd := batchStart + historicalBatchSize
			if batchEnd > len(symbols) {
				batchEnd = len(symbols)
			}
			index++
			jobs = append(jobs, historicalFetchJob{
				index:   index,
				start:   day.start,
				end:     day.end,
				symbols: symbols[batchStart:batchEnd],
			})
		}
	}
	return jobs
}

type tradingDayWindow struct {
	start time.Time
	end   time.Time
}

func tradingDayWindows(start, end time.Time, location *time.Location) []tradingDayWindow {
	if end.Before(start) {
		return nil
	}
	current := start.In(location)
	last := end.In(location)
	cursor := time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, location)
	finalDay := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, location)
	out := make([]tradingDayWindow, 0)
	for !cursor.After(finalDay) {
		if cursor.Weekday() != time.Saturday && cursor.Weekday() != time.Sunday {
			dayStart := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 4, 0, 0, 0, location).UTC()
			dayEnd := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 20, 0, 0, 0, location).UTC()
			if dayStart.Before(start) {
				dayStart = start
			}
			if dayEnd.After(end) {
				dayEnd = end
			}
			if dayEnd.After(dayStart) {
				out = append(out, tradingDayWindow{start: dayStart, end: dayEnd})
			}
		}
		cursor = cursor.Add(24 * time.Hour)
	}
	return out
}

func historicalWorkerCount(rateLimit int) int {
	if rateLimit <= 0 {
		rateLimit = 200
	}
	switch {
	case rateLimit < 180:
		return 2
	case rateLimit < 600:
		return 4
	default:
		return 6
	}
}

func isRetryableHistoricalError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *alpaca.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "timeout") || strings.Contains(message, "connection reset") || strings.Contains(message, "temporarily unavailable")
}

func retryDelay(headers http.Header, attempt int, err error) time.Duration {
	if next := rateResetTime(headers); !next.IsZero() {
		delay := time.Until(next)
		if delay > 0 {
			if delay > 30*time.Second {
				return 30 * time.Second
			}
			return delay
		}
	}
	if retryAfter := strings.TrimSpace(headers.Get("Retry-After")); retryAfter != "" {
		if seconds, parseErr := time.ParseDuration(retryAfter + "s"); parseErr == nil && seconds > 0 {
			return seconds
		}
	}
	base := math.Pow(2, float64(attempt-1))
	delay := time.Duration(base) * time.Second
	if delay > 20*time.Second {
		delay = 20 * time.Second
	}
	var apiErr *alpaca.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
		if delay < 5*time.Second {
			delay = 5 * time.Second
		}
	}
	return delay
}

func applyRateLimitHeaders(limiter *requestLimiter, headers http.Header) {
	if headers == nil {
		return
	}
	if remaining := strings.TrimSpace(headers.Get("X-RateLimit-Remaining")); remaining == "0" {
		if resetAt := rateResetTime(headers); !resetAt.IsZero() {
			limiter.DelayUntil(resetAt)
		}
	}
}

func rateResetTime(headers http.Header) time.Time {
	if headers == nil {
		return time.Time{}
	}
	value := strings.TrimSpace(headers.Get("X-RateLimit-Reset"))
	if value == "" {
		return time.Time{}
	}
	if unixSeconds, err := time.ParseDuration(value + "s"); err == nil {
		epoch := time.Unix(0, 0).UTC()
		return epoch.Add(unixSeconds)
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

func estimateHistoricalFetchTimeout(symbolCount int, start, end time.Time, requestsPerMinute int) time.Duration {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 200
	}
	if symbolCount <= 0 {
		return 20 * time.Minute
	}
	location, _ := time.LoadLocation("America/New_York")
	if location == nil {
		location = time.UTC
	}
	dayCount := len(tradingDayWindows(start, end, location))
	if dayCount == 0 {
		dayCount = 1
	}
	jobCount := dayCount * int(math.Ceil(float64(symbolCount)/float64(historicalBatchSize)))
	if jobCount == 0 {
		return 15 * time.Minute
	}
	estimatedRequests := float64(jobCount * 5)
	minutes := estimatedRequests / float64(requestsPerMinute)
	timeout := time.Duration(minutes*float64(time.Minute)) + 10*time.Minute
	if timeout < 20*time.Minute {
		timeout = 20 * time.Minute
	}
	if timeout > 2*time.Hour {
		timeout = 2 * time.Hour
	}
	return timeout
}

func historicalCachePath(job historicalFetchJob, feed string) string {
	hasher := sha256.New()
	hasher.Write([]byte(historicalCacheVersion))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(strings.ToLower(strings.TrimSpace(feed))))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(job.start.UTC().Format(time.RFC3339Nano)))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(job.end.UTC().Format(time.RFC3339Nano)))
	for _, symbol := range job.symbols {
		hasher.Write([]byte("|"))
		hasher.Write([]byte(strings.ToUpper(strings.TrimSpace(symbol))))
	}
	key := hex.EncodeToString(hasher.Sum(nil))
	return filepath.Join(historicalCacheRoot, historicalCacheVersion, key[:2], key+".gob.gz")
}

func loadHistoricalJobCache(job historicalFetchJob, feed string) (historicalFetchResult, bool, error) {
	path := historicalCachePath(job, feed)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return historicalFetchResult{}, false, nil
		}
		return historicalFetchResult{}, false, err
	}
	defer file.Close()

	reader, err := gzip.NewReader(file)
	if err != nil {
		return historicalFetchResult{}, false, err
	}
	defer reader.Close()

	var entry historicalCacheEntry
	if err := gob.NewDecoder(reader).Decode(&entry); err != nil {
		return historicalFetchResult{}, false, err
	}
	if entry.Version != historicalCacheVersion {
		return historicalFetchResult{}, false, nil
	}
	return historicalFetchResult{bars: entry.Bars}, true, nil
}

func saveHistoricalJobCache(job historicalFetchJob, feed string, result historicalFetchResult) error {
	path := historicalCachePath(job, feed)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tempPath := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	file, err := os.Create(tempPath)
	if err != nil {
		return err
	}

	success := false
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !success {
			_ = os.Remove(tempPath)
		}
	}()

	writer := gzip.NewWriter(file)
	entry := historicalCacheEntry{
		Version: historicalCacheVersion,
		Feed:    strings.ToLower(strings.TrimSpace(feed)),
		Start:   job.start.UTC(),
		End:     job.end.UTC(),
		Symbols: append([]string(nil), job.symbols...),
		SavedAt: time.Now().UTC(),
		Bars:    append([]backtest.InputBar(nil), result.bars...),
	}
	if err := gob.NewEncoder(writer).Encode(entry); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(tempPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr == nil {
			if retryErr := os.Rename(tempPath, path); retryErr == nil {
				success = true
				return nil
			}
		}
		return err
	}
	success = true
	return nil
}
