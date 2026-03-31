package backtest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

const (
	historicalBatchSize    = 100
	historicalMaxRetries   = 5
	historicalCacheVersion = "v3"
)

var historicalCacheRoot = filepath.Join(".cache", "backtest", "historical-bars")

type HistoricalFetchJob struct {
	index   int
	start   time.Time
	end     time.Time
	Symbols []string
}

type historicalFetchResult struct {
	bars     []InputBar
	pageHits int
	cacheHit bool
}

type requestLimiter struct {
	tokenCh  chan struct{}
	delayCh  chan time.Time
	stopOnce sync.Once
	stopCh   chan struct{}
}

func newRequestLimiter(requestsPerMinute int) *requestLimiter {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 200
	}
	interval := time.Minute / time.Duration(requestsPerMinute)
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	l := &requestLimiter{
		tokenCh: make(chan struct{}),
		delayCh: make(chan time.Time, 1),
		stopCh:  make(chan struct{}),
	}
	go l.run(interval)
	return l
}

func (l *requestLimiter) run(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case delay := <-l.delayCh:
			if wait := time.Until(delay); wait > 0 {
				time.Sleep(wait)
			}
		case <-ticker.C:
			select {
			case <-l.stopCh:
				return
			case l.tokenCh <- struct{}{}:
			}
		}
	}
}

func (l *requestLimiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.tokenCh:
		return nil
	}
}

func (l *requestLimiter) DelayUntil(next time.Time) {
	select {
	case l.delayCh <- next:
	default:
	}
}

func (l *requestLimiter) Stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
}

func fetchHistoricalJobFromAPI(ctx context.Context, client *alpaca.Client, limiter *requestLimiter, job HistoricalFetchJob, feed string) (historicalFetchResult, error) {
	pageToken := ""
	result := historicalFetchResult{
		bars: make([]InputBar, 0, 1024),
	}
	for {
		page, err := fetchHistoricalPageWithRetry(ctx, client, limiter, job, pageToken)
		if err != nil {
			return historicalFetchResult{}, err
		}
		result.pageHits++
		for symbol, bars := range page.Bars {
			for _, item := range bars {
				result.bars = append(result.bars, InputBar{
					Timestamp: item.Timestamp,
					Symbol:    strings.ToUpper(symbol),
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
			sortHistoricalBars(result.bars)
			if err := saveHistoricalJobCache(job, feed, result); err != nil {
				log.Printf("Historical cache write failed job=%d symbols=%d err=%v", job.index, len(job.Symbols), err)
			}
			return result, nil
		}
		pageToken = page.NextPageToken
	}
}

func fetchHistoricalPageWithRetry(ctx context.Context, client *alpaca.Client, limiter *requestLimiter, job HistoricalFetchJob, pageToken string) (alpaca.HistoricalBarsPage, error) {
	var lastErr error
	var lastPage alpaca.HistoricalBarsPage
	for attempt := 1; attempt <= historicalMaxRetries; attempt++ {
		if err := limiter.Wait(ctx); err != nil {
			return alpaca.HistoricalBarsPage{}, err
		}
		page, err := client.GetHistoricalBarsPage(ctx, job.Symbols, job.start, job.end, "1Min", pageToken)
		if err == nil {
			return page, nil
		}

		lastErr = err
		lastPage = page
		if !isRetryableHistoricalError(err) || attempt == historicalMaxRetries {
			log.Printf("Historical fetch failed job=%d symbols=%d page_token=%t attempt=%d err=%v", job.index, len(job.Symbols), pageToken != "", attempt, err)
			return alpaca.HistoricalBarsPage{}, fmt.Errorf("historical fetch job %d failed after %d attempts: %w", job.index, attempt, err)
		}

		delay := retryDelay(page.Headers, attempt, err)
		limiter.DelayUntil(time.Now().Add(delay))
		log.Printf("Historical fetch retry job=%d symbols=%d page_token=%t attempt=%d delay=%s err=%v", job.index, len(job.Symbols), pageToken != "", attempt, delay, err)
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

func BuildHistoricalFetchJobs(symbols []string, start, end time.Time) []HistoricalFetchJob {
	if len(symbols) == 0 || end.Before(start) {
		return nil
	}

	days := tradingDayWindows(start, end, markethours.Location())
	jobs := make([]HistoricalFetchJob, 0, len(days)*(len(symbols)/historicalBatchSize+1))
	index := 0
	for _, day := range days {
		for batchStart := 0; batchStart < len(symbols); batchStart += historicalBatchSize {
			batchEnd := batchStart + historicalBatchSize
			if batchEnd > len(symbols) {
				batchEnd = len(symbols)
			}
			index++
			jobs = append(jobs, HistoricalFetchJob{
				index:   index,
				start:   day.start,
				end:     day.end,
				Symbols: symbols[batchStart:batchEnd],
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
			dayStart := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 4, 0, 0, 0, location)
			dayEnd := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 20, 0, 0, 0, location)
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
		cursor = cursor.AddDate(0, 0, 1)
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
		return 10
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
		epoch := time.Unix(0, 0)
		return epoch.Add(unixSeconds)
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	return time.Time{}
}

func EstimateHistoricalFetchTimeout(symbolCount int, start, end time.Time, requestsPerMinute int) time.Duration {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 200
	}
	if symbolCount <= 0 {
		return 20 * time.Minute
	}

	dayCount := len(tradingDayWindows(start, end, markethours.Location()))
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

func sortHistoricalBars(bars []InputBar) {
	sort.Slice(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
	})
}
