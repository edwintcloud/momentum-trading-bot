package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/backtest"
)

func TestHistoricalJobCacheRoundTrip(t *testing.T) {
	previousRoot := historicalCacheRoot
	historicalCacheRoot = t.TempDir()
	t.Cleanup(func() {
		historicalCacheRoot = previousRoot
	})

	job := historicalFetchJob{
		index: 1,
		start: time.Date(2026, 3, 10, 13, 0, 0, 0, time.UTC),
		end:   time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC),
		symbols: []string{
			"APVO",
			"EONR",
		},
	}
	want := historicalFetchResult{
		bars: []backtest.InputBar{
			{Timestamp: time.Date(2026, 3, 10, 13, 0, 0, 0, time.UTC), Symbol: "APVO", Open: 10, High: 10.5, Low: 9.9, Close: 10.2, Volume: 1000},
			{Timestamp: time.Date(2026, 3, 10, 13, 1, 0, 0, time.UTC), Symbol: "EONR", Open: 3, High: 3.2, Low: 2.9, Close: 3.1, Volume: 2000},
		},
		pageHits: 2,
	}

	if err := saveHistoricalJobCache(job, "sip", want); err != nil {
		t.Fatalf("expected cache write to succeed, got %v", err)
	}

	got, ok, err := loadHistoricalJobCache(job, "sip")
	if err != nil {
		t.Fatalf("expected cache read to succeed, got %v", err)
	}
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !reflect.DeepEqual(got.bars, want.bars) {
		t.Fatalf("unexpected cached bars: %+v", got.bars)
	}
}

func TestFetchHistoricalJobUsesCacheWhenAvailable(t *testing.T) {
	previousRoot := historicalCacheRoot
	historicalCacheRoot = t.TempDir()
	t.Cleanup(func() {
		historicalCacheRoot = previousRoot
	})

	job := historicalFetchJob{
		index:   7,
		start:   time.Date(2026, 3, 11, 13, 0, 0, 0, time.UTC),
		end:     time.Date(2026, 3, 11, 20, 0, 0, 0, time.UTC),
		symbols: []string{"APVO"},
	}
	cached := historicalFetchResult{
		bars: []backtest.InputBar{
			{Timestamp: time.Date(2026, 3, 11, 13, 0, 0, 0, time.UTC), Symbol: "APVO", Open: 11, High: 11.3, Low: 10.9, Close: 11.1, Volume: 1500},
		},
	}
	if err := saveHistoricalJobCache(job, "sip", cached); err != nil {
		t.Fatalf("expected cache write to succeed, got %v", err)
	}

	result, err := fetchHistoricalJob(context.Background(), nil, newRequestLimiter(200), job, "sip")
	if err != nil {
		t.Fatalf("expected cached historical job to succeed, got %v", err)
	}
	if !result.cacheHit {
		t.Fatal("expected cached result to be marked as a cache hit")
	}
	if len(result.bars) != 1 || result.bars[0].Symbol != "APVO" {
		t.Fatalf("unexpected cached result: %+v", result)
	}
}

func TestHistoricalDatasetIteratorMergesCachedShardsInOrder(t *testing.T) {
	previousRoot := historicalCacheRoot
	historicalCacheRoot = t.TempDir()
	t.Cleanup(func() {
		historicalCacheRoot = previousRoot
	})

	dayStart := time.Date(2026, 3, 12, 13, 0, 0, 0, time.UTC)
	dayEnd := time.Date(2026, 3, 12, 20, 0, 0, 0, time.UTC)
	jobs := []historicalFetchJob{
		{
			index:   1,
			start:   dayStart,
			end:     dayEnd,
			symbols: []string{"APVO"},
		},
		{
			index:   2,
			start:   dayStart,
			end:     dayEnd,
			symbols: []string{"EONR"},
		},
	}
	if err := saveHistoricalJobCache(jobs[0], "sip", historicalFetchResult{
		bars: []backtest.InputBar{
			{Timestamp: dayStart.Add(2 * time.Minute), Symbol: "APVO", Open: 10.10, High: 10.40, Low: 10.05, Close: 10.30, Volume: 3000},
			{Timestamp: dayStart, Symbol: "APVO", Open: 10.00, High: 10.20, Low: 9.95, Close: 10.15, Volume: 1000},
		},
	}); err != nil {
		t.Fatalf("expected APVO shard to save, got %v", err)
	}
	if err := saveHistoricalJobCache(jobs[1], "sip", historicalFetchResult{
		bars: []backtest.InputBar{
			{Timestamp: dayStart.Add(time.Minute), Symbol: "EONR", Open: 3.00, High: 3.10, Low: 2.95, Close: 3.05, Volume: 2000},
		},
	}); err != nil {
		t.Fatalf("expected EONR shard to save, got %v", err)
	}

	iterator := newHistoricalDatasetIterator(historicalDataset{
		feed: "sip",
		jobs: jobs,
	})
	defer iterator.Close()

	var got []backtest.InputBar
	for {
		bar, ok, err := iterator.Next()
		if err != nil {
			t.Fatalf("expected merged iterator to succeed, got %v", err)
		}
		if !ok {
			break
		}
		got = append(got, bar)
	}

	want := []backtest.InputBar{
		{Timestamp: dayStart, Symbol: "APVO", Open: 10.00, High: 10.20, Low: 9.95, Close: 10.15, Volume: 1000},
		{Timestamp: dayStart.Add(time.Minute), Symbol: "EONR", Open: 3.00, High: 3.10, Low: 2.95, Close: 3.05, Volume: 2000},
		{Timestamp: dayStart.Add(2 * time.Minute), Symbol: "APVO", Open: 10.10, High: 10.40, Low: 10.05, Close: 10.30, Volume: 3000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected merged bars:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestSanitizeHistoricalBarSeriesClampsSuspiciousLowWick(t *testing.T) {
	loc := time.UTC
	bars := []backtest.InputBar{
		{Timestamp: time.Date(2026, 1, 20, 13, 1, 0, 0, loc), Symbol: "CRVS", Open: 11.66, High: 11.73, Low: 11.65, Close: 11.65},
		{Timestamp: time.Date(2026, 1, 20, 13, 2, 0, 0, loc), Symbol: "CRVS", Open: 11.66, High: 12.50, Low: 9.26, Close: 11.67},
		{Timestamp: time.Date(2026, 1, 20, 13, 3, 0, 0, loc), Symbol: "CRVS", Open: 11.65, High: 11.77, Low: 11.65, Close: 11.76},
	}

	got, adjusted := sanitizeHistoricalBarSeries(bars)
	if adjusted == 0 {
		t.Fatal("expected suspicious wick to be adjusted")
	}
	if got[1].Low != 11.65 {
		t.Fatalf("expected low wick to be clamped to 11.65, got %.2f", got[1].Low)
	}
	if got[1].High != 12.50 {
		t.Fatalf("expected high to remain unchanged, got %.2f", got[1].High)
	}
}

func TestSanitizeHistoricalBarSeriesKeepsLegitimateWideBar(t *testing.T) {
	loc := time.UTC
	bars := []backtest.InputBar{
		{Timestamp: time.Date(2026, 1, 20, 13, 1, 0, 0, loc), Symbol: "FAST", Open: 10.00, High: 10.20, Low: 9.98, Close: 10.15},
		{Timestamp: time.Date(2026, 1, 20, 13, 2, 0, 0, loc), Symbol: "FAST", Open: 10.12, High: 11.45, Low: 9.35, Close: 11.10},
		{Timestamp: time.Date(2026, 1, 20, 13, 3, 0, 0, loc), Symbol: "FAST", Open: 11.05, High: 11.30, Low: 10.90, Close: 11.20},
	}

	got, adjusted := sanitizeHistoricalBarSeries(bars)
	if adjusted != 0 {
		t.Fatalf("expected legitimate wide bar to remain unchanged, adjusted=%d got=%+v", adjusted, got[1])
	}
	if !reflect.DeepEqual(got, bars) {
		t.Fatalf("expected bars to remain unchanged:\n got=%+v\nwant=%+v", got, bars)
	}
}
