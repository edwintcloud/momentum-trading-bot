package backtest

import (
	"testing"
	"time"
)

func TestSplitIntoGroups(t *testing.T) {
	bars := make([]InputBar, 100)
	for i := range bars {
		bars[i] = InputBar{
			Timestamp: time.Now().Add(time.Duration(i) * time.Minute),
			Symbol:    "TEST",
			Close:     100.0,
		}
	}

	groups := splitIntoGroups(bars, 5)
	if len(groups) != 5 {
		t.Fatalf("expected 5 groups, got %d", len(groups))
	}

	// All bars should be accounted for
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	if total != 100 {
		t.Errorf("expected 100 total bars across groups, got %d", total)
	}
}

func TestSplitIntoGroups_SmallInput(t *testing.T) {
	bars := make([]InputBar, 3)
	for i := range bars {
		bars[i] = InputBar{Timestamp: time.Now().Add(time.Duration(i) * time.Minute)}
	}

	groups := splitIntoGroups(bars, 6)
	// Should handle more groups than bars gracefully
	if groups == nil {
		t.Fatal("expected non-nil groups")
	}
}

func TestSplitIntoGroups_Zero(t *testing.T) {
	groups := splitIntoGroups(nil, 5)
	if groups != nil {
		t.Error("expected nil for nil input")
	}
}

func TestPurgeBars(t *testing.T) {
	base := time.Date(2024, 1, 1, 9, 30, 0, 0, time.UTC)

	// Train group: minutes 0-59
	trainGroup := make([]InputBar, 60)
	for i := range trainGroup {
		trainGroup[i] = InputBar{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
		}
	}

	// Test group: minutes 100-119
	testGroup := make([]InputBar, 20)
	for i := range testGroup {
		testGroup[i] = InputBar{
			Timestamp: base.Add(time.Duration(100+i) * time.Minute),
		}
	}

	// Purge gap: 30 minutes
	purged := purgeBars(trainGroup, testGroup, 30)

	// Bars within 30 minutes before test start (minute 100) should be removed.
	// Minutes 70-99 would be removed (within 30 min of test start).
	// Since train only goes to minute 59, all bars should survive (59 < 100-30=70).
	if len(purged) != 60 {
		t.Errorf("expected 60 bars (all survive), got %d", len(purged))
	}

	// Now test with closer ranges
	trainClose := make([]InputBar, 100)
	for i := range trainClose {
		trainClose[i] = InputBar{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
		}
	}

	testClose := make([]InputBar, 20)
	for i := range testClose {
		testClose[i] = InputBar{
			Timestamp: base.Add(time.Duration(50+i) * time.Minute), // test starts at minute 50
		}
	}

	purgedClose := purgeBars(trainClose, testClose, 10)

	// Bars within 10 minutes of test boundaries should be purged.
	// Test: minutes 50-69
	// Purge before: < 40 OK, >= 40 removed
	// Purge after: <= 79 removed, > 79 OK
	// So minutes 40-79 are in the purge zone
	for _, b := range purgedClose {
		minute := int(b.Timestamp.Sub(base).Minutes())
		if minute >= 40 && minute <= 79 {
			t.Errorf("bar at minute %d should have been purged", minute)
		}
	}
}

func TestPurgeBars_EmptyInputs(t *testing.T) {
	bars := []InputBar{{Timestamp: time.Now()}}

	result := purgeBars(bars, nil, 10)
	if len(result) != 1 {
		t.Error("empty test group should return all train bars")
	}

	result2 := purgeBars(nil, bars, 10)
	if len(result2) != 0 {
		t.Error("empty train group should return empty")
	}
}
