package main

import (
	"bufio"
	"compress/gzip"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/backtest"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

const (
	historicalBatchSize    = 100
	historicalMaxRetries   = 5
	historicalCacheVersion = "v3"
	historicalCacheMagic   = "MTBH2"
	historicalPriceScale   = 10_000
	historicalCacheBufSize = 1 << 20
)

var historicalCacheRoot = filepath.Join(".cache", "backtest", "historical-bars")

// ---------------------------------------------------------------------
// Fetch job types
// ---------------------------------------------------------------------

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

// ---------------------------------------------------------------------
// Goroutine-based token-bucket rate limiter
// ---------------------------------------------------------------------

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

// ---------------------------------------------------------------------
// Historical dataset and parallel fetch pipeline
// ---------------------------------------------------------------------

type historicalDataset struct {
	feed string
	jobs []historicalFetchJob
}

func prepareHistoricalDataset(ctx context.Context, client *alpaca.BacktestClient, symbols []string, start, end time.Time, historicalRateLimit int) (historicalDataset, error) {
	if len(symbols) == 0 {
		return historicalDataset{}, fmt.Errorf("no symbols available for historical fetch")
	}

	jobs := buildHistoricalFetchJobs(symbols, start, end)
	if len(jobs) == 0 {
		return historicalDataset{}, nil
	}

	workerCount := historicalWorkerCount(historicalRateLimit)
	limiter := newRequestLimiter(historicalRateLimit)
	defer limiter.Stop()
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
				result, err := ensureHistoricalJobCache(ctx, client, limiter, job, feed)
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

	for {
		select {
		case <-ctx.Done():
			return historicalDataset{}, ctx.Err()
		case err := <-errCh:
			if err != nil {
				return historicalDataset{}, err
			}
		case _, ok := <-resultCh:
			if !ok {
				log.Printf("Historical cache summary hits=%d misses=%d dir=%s", cacheHits.Load(), cacheMisses.Load(), historicalCacheRoot)
				return historicalDataset{feed: feed, jobs: jobs}, nil
			}
		}
	}
}

func ensureHistoricalJobCache(ctx context.Context, client *alpaca.BacktestClient, limiter *requestLimiter, job historicalFetchJob, feed string) (historicalFetchResult, error) {
	if historicalJobCacheExists(job, feed) {
		return historicalFetchResult{cacheHit: true}, nil
	}
	return fetchHistoricalJobFromAPI(ctx, client, limiter, job, feed)
}

func fetchHistoricalJobFromAPI(ctx context.Context, client *alpaca.BacktestClient, limiter *requestLimiter, job historicalFetchJob, feed string) (historicalFetchResult, error) {
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
				log.Printf("Historical cache write failed job=%d symbols=%d err=%v", job.index, len(job.symbols), err)
			}
			return result, nil
		}
		pageToken = page.NextPageToken
	}
}

func fetchHistoricalPageWithRetry(ctx context.Context, client *alpaca.BacktestClient, limiter *requestLimiter, job historicalFetchJob, pageToken string) (alpaca.HistoricalBarsPage, error) {
	var lastErr error
	for attempt := 1; attempt <= historicalMaxRetries; attempt++ {
		if err := limiter.Wait(ctx); err != nil {
			return alpaca.HistoricalBarsPage{}, err
		}
		page, err := client.GetHistoricalBarsPage(ctx, job.symbols, job.start, job.end, "1Min", pageToken)
		if err == nil {
			return page, nil
		}

		lastErr = err
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
	return alpaca.HistoricalBarsPage{}, lastErr
}

// ---------------------------------------------------------------------
// Job building
// ---------------------------------------------------------------------

func buildHistoricalFetchJobs(symbols []string, start, end time.Time) []historicalFetchJob {
	if len(symbols) == 0 || end.Before(start) {
		return nil
	}

	days := tradingDayWindows(start, end, markethours.NYLocation())
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
		return 10
	}
}

// ---------------------------------------------------------------------
// Retry and rate-limit helpers
// ---------------------------------------------------------------------

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

func estimateHistoricalFetchTimeout(symbolCount int, start, end time.Time, requestsPerMinute int) time.Duration {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 200
	}
	if symbolCount <= 0 {
		return 20 * time.Minute
	}

	dayCount := len(tradingDayWindows(start, end, markethours.NYLocation()))
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

// ---------------------------------------------------------------------
// Streaming merge-sort iterator over cached day-jobs
// ---------------------------------------------------------------------

type historicalDatasetIterator struct {
	feed     string
	dayJobs  [][]historicalFetchJob
	dayIndex int
	current  *historicalDayIterator
}

type historicalDayIterator struct {
	streams []*historicalJobCacheReader
	heap    historicalBarHeap
}

type historicalBarItem struct {
	bar       backtest.InputBar
	streamIdx int
}

type historicalBarHeap []historicalBarItem

func newHistoricalDatasetIterator(dataset historicalDataset) *historicalDatasetIterator {
	return &historicalDatasetIterator{
		feed:    dataset.feed,
		dayJobs: groupHistoricalJobsByDay(dataset.jobs),
	}
}

func (it *historicalDatasetIterator) Next() (backtest.InputBar, bool, error) {
	for {
		if it.current == nil {
			if it.dayIndex >= len(it.dayJobs) {
				return backtest.InputBar{}, false, nil
			}
			dayIter, err := openHistoricalDayIterator(it.dayJobs[it.dayIndex], it.feed)
			if err != nil {
				return backtest.InputBar{}, false, err
			}
			it.current = dayIter
		}
		bar, ok, err := it.current.Next()
		if err != nil {
			return backtest.InputBar{}, false, err
		}
		if ok {
			return bar, true, nil
		}
		if err := it.current.Close(); err != nil {
			return backtest.InputBar{}, false, err
		}
		it.current = nil
		it.dayIndex++
	}
}

func (it *historicalDatasetIterator) Close() error {
	if it.current != nil {
		return it.current.Close()
	}
	return nil
}

func openHistoricalDayIterator(jobs []historicalFetchJob, feed string) (*historicalDayIterator, error) {
	iterator := &historicalDayIterator{
		streams: make([]*historicalJobCacheReader, 0, len(jobs)),
		heap:    make(historicalBarHeap, 0, len(jobs)),
	}
	for _, job := range jobs {
		stream, err := openHistoricalJobCacheReader(job, feed)
		if err != nil {
			_ = iterator.Close()
			return nil, err
		}
		iterator.streams = append(iterator.streams, stream)
		bar, ok, err := stream.Next()
		if err != nil {
			_ = iterator.Close()
			return nil, err
		}
		if ok {
			heap.Push(&iterator.heap, historicalBarItem{
				bar:       bar,
				streamIdx: len(iterator.streams) - 1,
			})
		}
	}
	return iterator, nil
}

func (it *historicalDayIterator) Next() (backtest.InputBar, bool, error) {
	if len(it.heap) == 0 {
		return backtest.InputBar{}, false, nil
	}
	item := heap.Pop(&it.heap).(historicalBarItem)
	stream := it.streams[item.streamIdx]
	nextBar, ok, err := stream.Next()
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	if ok {
		heap.Push(&it.heap, historicalBarItem{
			bar:       nextBar,
			streamIdx: item.streamIdx,
		})
	}
	return item.bar, true, nil
}

func (it *historicalDayIterator) Close() error {
	var firstErr error
	for _, stream := range it.streams {
		if stream == nil {
			continue
		}
		if err := stream.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	it.streams = nil
	it.heap = nil
	return firstErr
}

func groupHistoricalJobsByDay(jobs []historicalFetchJob) [][]historicalFetchJob {
	if len(jobs) == 0 {
		return nil
	}
	grouped := make([][]historicalFetchJob, 0, len(jobs))
	current := make([]historicalFetchJob, 0)
	currentDay := ""
	for _, job := range jobs {
		dayKey := job.start.In(markethours.NYLocation()).Format("2006-01-02")
		if dayKey != currentDay {
			if len(current) > 0 {
				grouped = append(grouped, current)
			}
			current = make([]historicalFetchJob, 0, 8)
			currentDay = dayKey
		}
		current = append(current, job)
	}
	if len(current) > 0 {
		grouped = append(grouped, current)
	}
	return grouped
}

func (h historicalBarHeap) Len() int {
	return len(h)
}

func (h historicalBarHeap) Less(i, j int) bool {
	if h[i].bar.Timestamp.Equal(h[j].bar.Timestamp) {
		return h[i].bar.Symbol < h[j].bar.Symbol
	}
	return h[i].bar.Timestamp.Before(h[j].bar.Timestamp)
}

func (h historicalBarHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *historicalBarHeap) Push(x any) {
	*h = append(*h, x.(historicalBarItem))
}

func (h *historicalBarHeap) Pop() any {
	old := *h
	last := len(old) - 1
	item := old[last]
	*h = old[:last]
	return item
}

// ---------------------------------------------------------------------
// Gzip + varint binary cache codec
// ---------------------------------------------------------------------

type historicalJobCacheReader struct {
	file      *os.File
	gzip      *gzip.Reader
	reader    *bufio.Reader
	startUnix int64
	symbols   []string
	remaining uint64
}

func historicalCachePath(job historicalFetchJob, feed string) string {
	hasher := sha256.New()
	hasher.Write([]byte(historicalCacheVersion))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(strings.ToLower(strings.TrimSpace(feed))))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(job.start.Format(time.RFC3339Nano)))
	hasher.Write([]byte("|"))
	hasher.Write([]byte(job.end.Format(time.RFC3339Nano)))
	for _, symbol := range job.symbols {
		hasher.Write([]byte("|"))
		hasher.Write([]byte(strings.ToUpper(strings.TrimSpace(symbol))))
	}
	key := hex.EncodeToString(hasher.Sum(nil))
	return filepath.Join(historicalCacheRoot, historicalCacheVersion, key[:2], key+".bars.gz")
}

func historicalJobCacheExists(job historicalFetchJob, feed string) bool {
	_, err := os.Stat(historicalCachePath(job, feed))
	return err == nil
}

func openHistoricalJobCacheReader(job historicalFetchJob, feed string) (*historicalJobCacheReader, error) {
	path := historicalCachePath(job, feed)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	reader := &historicalJobCacheReader{
		file:   file,
		gzip:   gzipReader,
		reader: bufio.NewReaderSize(gzipReader, historicalCacheBufSize),
	}
	if err := reader.readHeader(); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

func (r *historicalJobCacheReader) Close() error {
	var closeErr error
	if r.gzip != nil {
		closeErr = r.gzip.Close()
	}
	if r.file != nil {
		if err := r.file.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (r *historicalJobCacheReader) Next() (backtest.InputBar, bool, error) {
	if r.remaining == 0 {
		return backtest.InputBar{}, false, nil
	}
	offsetSeconds, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	symbolIndex, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	if symbolIndex >= uint64(len(r.symbols)) {
		return backtest.InputBar{}, false, fmt.Errorf("historical cache symbol index %d out of range", symbolIndex)
	}

	openPrice, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	highPrice, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	lowPrice, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	closePrice, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	prevClose, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}
	volume, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return backtest.InputBar{}, false, err
	}

	r.remaining--
	return backtest.InputBar{
		Timestamp: time.Unix(r.startUnix+int64(offsetSeconds), 0),
		Symbol:    r.symbols[symbolIndex],
		Open:      decodeHistoricalPrice(openPrice),
		High:      decodeHistoricalPrice(highPrice),
		Low:       decodeHistoricalPrice(lowPrice),
		Close:     decodeHistoricalPrice(closePrice),
		Volume:    int64(volume),
		PrevClose: decodeHistoricalPrice(prevClose),
	}, true, nil
}

func (r *historicalJobCacheReader) readHeader() error {
	magic := make([]byte, len(historicalCacheMagic))
	if _, err := io.ReadFull(r.reader, magic); err != nil {
		return err
	}
	if string(magic) != historicalCacheMagic {
		return fmt.Errorf("unsupported historical cache format %q", string(magic))
	}

	startUnix, err := binary.ReadVarint(r.reader)
	if err != nil {
		return err
	}
	if _, err := binary.ReadVarint(r.reader); err != nil {
		return err
	}
	symbolCount, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return err
	}
	symbols := make([]string, 0, symbolCount)
	for index := uint64(0); index < symbolCount; index++ {
		symbol, err := readCacheString(r.reader)
		if err != nil {
			return err
		}
		symbols = append(symbols, symbol)
	}
	recordCount, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return err
	}
	r.startUnix = startUnix
	r.symbols = symbols
	r.remaining = recordCount
	return nil
}

func saveHistoricalJobCache(job historicalFetchJob, feed string, result historicalFetchResult) error {
	path := historicalCachePath(job, feed)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	bars := append([]backtest.InputBar(nil), result.bars...)
	sort.Slice(bars, func(i, j int) bool {
		if bars[i].Timestamp.Equal(bars[j].Timestamp) {
			return bars[i].Symbol < bars[j].Symbol
		}
		return bars[i].Timestamp.Before(bars[j].Timestamp)
	})

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

	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	if err != nil {
		return err
	}
	buffered := bufio.NewWriterSize(gzipWriter, historicalCacheBufSize)

	if _, err := io.WriteString(buffered, historicalCacheMagic); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := writeVarint(buffered, job.start.Unix()); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := writeVarint(buffered, job.end.Unix()); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := writeUvarint(buffered, uint64(len(job.symbols))); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	for _, symbol := range job.symbols {
		if err := writeCacheString(buffered, strings.ToUpper(strings.TrimSpace(symbol))); err != nil {
			_ = gzipWriter.Close()
			return err
		}
	}
	if err := writeUvarint(buffered, uint64(len(bars))); err != nil {
		_ = gzipWriter.Close()
		return err
	}

	symbolIndex := make(map[string]uint64, len(job.symbols))
	for index, symbol := range job.symbols {
		symbolIndex[strings.ToUpper(strings.TrimSpace(symbol))] = uint64(index)
	}
	jobStartUnix := job.start.Unix()
	for _, item := range bars {
		index, ok := symbolIndex[strings.ToUpper(strings.TrimSpace(item.Symbol))]
		if !ok {
			_ = gzipWriter.Close()
			return fmt.Errorf("historical cache symbol %q missing from job symbol table", item.Symbol)
		}
		offsetSeconds := item.Timestamp.Unix() - jobStartUnix
		if offsetSeconds < 0 {
			_ = gzipWriter.Close()
			return fmt.Errorf("historical cache bar timestamp %s is before job start %s", item.Timestamp.Format(time.RFC3339), job.start.Format(time.RFC3339))
		}
		if err := writeUvarint(buffered, uint64(offsetSeconds)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, index); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.Open)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.High)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.Low)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.Close)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, encodeHistoricalPrice(item.PrevClose)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
		if err := writeUvarint(buffered, uint64(item.Volume)); err != nil {
			_ = gzipWriter.Close()
			return err
		}
	}

	if err := buffered.Flush(); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
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

func encodeHistoricalPrice(value float64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(math.Round(value * historicalPriceScale))
}

func decodeHistoricalPrice(value uint64) float64 {
	if value == 0 {
		return 0
	}
	return float64(value) / historicalPriceScale
}

func writeVarint(writer io.Writer, value int64) error {
	var buffer [binary.MaxVarintLen64]byte
	size := binary.PutVarint(buffer[:], value)
	_, err := writer.Write(buffer[:size])
	return err
}

func writeUvarint(writer io.Writer, value uint64) error {
	var buffer [binary.MaxVarintLen64]byte
	size := binary.PutUvarint(buffer[:], value)
	_, err := writer.Write(buffer[:size])
	return err
}

func writeCacheString(writer io.Writer, value string) error {
	if err := writeUvarint(writer, uint64(len(value))); err != nil {
		return err
	}
	_, err := io.WriteString(writer, value)
	return err
}

func readCacheString(reader *bufio.Reader) (string, error) {
	size, err := binary.ReadUvarint(reader)
	if err != nil {
		return "", err
	}
	buffer := make([]byte, size)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", err
	}
	return string(buffer), nil
}
