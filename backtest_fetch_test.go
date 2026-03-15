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
