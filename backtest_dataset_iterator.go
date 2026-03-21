package main

import (
	"container/heap"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
)

type historicalDataset struct {
	feed string
	jobs []historicalFetchJob
}

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

func prepareHistoricalDataset(ctx context.Context, client *alpaca.Client, symbols []string, start, end time.Time, historicalRateLimit int) (historicalDataset, error) {
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

func ensureHistoricalJobCache(ctx context.Context, client *alpaca.Client, limiter *requestLimiter, job historicalFetchJob, feed string) (historicalFetchResult, error) {
	if historicalJobCacheExists(job, feed) {
		return historicalFetchResult{cacheHit: true}, nil
	}
	return fetchHistoricalJobFromAPI(ctx, client, limiter, job, feed)
}

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
		dayKey := job.start.In(markethours.Location()).Format("2006-01-02")
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
