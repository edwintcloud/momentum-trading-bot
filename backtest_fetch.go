package main

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

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
)

const (
	historicalBatchSize    = 100
	historicalMaxRetries   = 5
	historicalCacheVersion = "v3"

	historicalOutlierWickThresholdPct = 0.12
	historicalBodyDriftTolerancePct   = 0.05
	historicalNeighborGapMax          = 5 * time.Minute
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
	dataset, err := prepareHistoricalDataset(ctx, client, symbols, start, end, historicalRateLimit)
	if err != nil {
		return nil, err
	}
	iterator := newHistoricalDatasetIterator(dataset)
	defer iterator.Close()

	bars := make([]backtest.InputBar, 0, len(dataset.jobs)*1024)
	for {
		bar, ok, err := iterator.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		bars = append(bars, bar)
	}
	if len(bars) == 0 {
		return []backtest.InputBar{}, nil
	}
	return bars, nil
}

func fetchHistoricalJob(ctx context.Context, client *alpaca.Client, limiter *requestLimiter, job historicalFetchJob, feed string) (historicalFetchResult, error) {
	if cached, ok, err := loadHistoricalJobCache(job, feed); err == nil && ok {
		cached.cacheHit = true
		return cached, nil
	} else if err != nil {
		log.Printf("Historical cache read failed job=%d symbols=%d err=%v", job.index, len(job.symbols), err)
	}
	return fetchHistoricalJobFromAPI(ctx, client, limiter, job, feed)
}

func fetchHistoricalJobFromAPI(ctx context.Context, client *alpaca.Client, limiter *requestLimiter, job historicalFetchJob, feed string) (historicalFetchResult, error) {
	pageToken := ""
	result := historicalFetchResult{
		bars: make([]backtest.InputBar, 0, 1024),
	}
	barsBySymbol := make(map[string][]backtest.InputBar, len(job.symbols))
	for {
		page, err := fetchHistoricalPageWithRetry(ctx, client, limiter, job, pageToken)
		if err != nil {
			return historicalFetchResult{}, err
		}
		result.pageHits++
		for symbol, bars := range page.Bars {
			normalizedSymbol := strings.ToUpper(symbol)
			for _, item := range bars {
				barsBySymbol[normalizedSymbol] = append(barsBySymbol[normalizedSymbol], backtest.InputBar{
					Timestamp: item.Timestamp.UTC(),
					Symbol:    normalizedSymbol,
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
			sanitizedBars, sanitizedCount := flattenAndSanitizeHistoricalBars(barsBySymbol)
			result.bars = sanitizedBars
			sortHistoricalBars(result.bars)
			if sanitizedCount > 0 {
				log.Printf("Historical bar sanity filter adjusted bars=%d job=%d symbols=%d window=%s..%s", sanitizedCount, job.index, len(job.symbols), job.start.Format(time.RFC3339), job.end.Format(time.RFC3339))
			}
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

func sortHistoricalBars(bars []backtest.InputBar) {
	sort.Slice(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
	})
}

func flattenAndSanitizeHistoricalBars(barsBySymbol map[string][]backtest.InputBar) ([]backtest.InputBar, int) {
	if len(barsBySymbol) == 0 {
		return nil, 0
	}
	symbols := make([]string, 0, len(barsBySymbol))
	total := 0
	for symbol, bars := range barsBySymbol {
		symbols = append(symbols, symbol)
		total += len(bars)
	}
	sort.Strings(symbols)

	out := make([]backtest.InputBar, 0, total)
	sanitized := 0
	for _, symbol := range symbols {
		bars := append([]backtest.InputBar(nil), barsBySymbol[symbol]...)
		sort.Slice(bars, func(i, j int) bool {
			return bars[i].Timestamp.Before(bars[j].Timestamp)
		})
		bars, adjusted := sanitizeHistoricalBarSeries(bars)
		sanitized += adjusted
		out = append(out, bars...)
	}
	return out, sanitized
}

func sanitizeHistoricalBarSeries(bars []backtest.InputBar) ([]backtest.InputBar, int) {
	if len(bars) < 3 {
		return bars, 0
	}
	adjusted := 0
	for index := 1; index < len(bars)-1; index++ {
		prev := bars[index-1]
		current := bars[index]
		next := bars[index+1]
		if prev.Symbol != current.Symbol || next.Symbol != current.Symbol {
			continue
		}
		if prev.Timestamp.IsZero() || current.Timestamp.IsZero() || next.Timestamp.IsZero() {
			continue
		}
		if current.Timestamp.Sub(prev.Timestamp) > historicalNeighborGapMax || next.Timestamp.Sub(current.Timestamp) > historicalNeighborGapMax {
			continue
		}

		currentBodyLow := math.Min(current.Open, current.Close)
		currentBodyHigh := math.Max(current.Open, current.Close)
		prevBodyLow := math.Min(prev.Open, prev.Close)
		prevBodyHigh := math.Max(prev.Open, prev.Close)
		nextBodyLow := math.Min(next.Open, next.Close)
		nextBodyHigh := math.Max(next.Open, next.Close)
		refLow := median3(prevBodyLow, currentBodyLow, nextBodyLow)
		refHigh := median3(prevBodyHigh, currentBodyHigh, nextBodyHigh)

		if isSuspiciousLowWick(current, currentBodyLow, prevBodyLow, nextBodyLow, refLow) {
			replacement := math.Min(currentBodyLow, math.Min(prevBodyLow, nextBodyLow))
			if replacement > 0 && replacement > current.Low {
				bars[index].Low = math.Round(replacement*10_000) / 10_000
				if bars[index].High < bars[index].Low {
					bars[index].High = bars[index].Low
				}
				adjusted++
			}
		}
		if isSuspiciousHighWick(current, currentBodyHigh, prevBodyHigh, nextBodyHigh, refHigh) {
			replacement := math.Max(currentBodyHigh, math.Max(prevBodyHigh, nextBodyHigh))
			if replacement > 0 && replacement < current.High {
				bars[index].High = math.Round(replacement*10_000) / 10_000
				if bars[index].Low > bars[index].High {
					bars[index].Low = bars[index].High
				}
				adjusted++
			}
		}
	}
	return bars, adjusted
}

func isSuspiciousLowWick(current backtest.InputBar, currentBodyLow, prevBodyLow, nextBodyLow, referenceLow float64) bool {
	if current.Low <= 0 || currentBodyLow <= 0 || referenceLow <= 0 {
		return false
	}
	if currentBodyLow < referenceLow*(1-historicalBodyDriftTolerancePct) {
		return false
	}
	if prevBodyLow < referenceLow*(1-historicalBodyDriftTolerancePct) || nextBodyLow < referenceLow*(1-historicalBodyDriftTolerancePct) {
		return false
	}
	return current.Low < referenceLow*(1-historicalOutlierWickThresholdPct)
}

func isSuspiciousHighWick(current backtest.InputBar, currentBodyHigh, prevBodyHigh, nextBodyHigh, referenceHigh float64) bool {
	if current.High <= 0 || currentBodyHigh <= 0 || referenceHigh <= 0 {
		return false
	}
	if currentBodyHigh > referenceHigh*(1+historicalBodyDriftTolerancePct) {
		return false
	}
	if prevBodyHigh > referenceHigh*(1+historicalBodyDriftTolerancePct) || nextBodyHigh > referenceHigh*(1+historicalBodyDriftTolerancePct) {
		return false
	}
	return current.High > referenceHigh*(1+historicalOutlierWickThresholdPct)
}

func median3(a, b, c float64) float64 {
	values := []float64{a, b, c}
	sort.Float64s(values)
	return values[1]
}
