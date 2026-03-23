package market

import (
	"math"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/alpaca"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// testDay is a known Wednesday (not holiday, not weekend) used consistently across tests.
var testDay = time.Date(2026, 3, 25, 9, 30, 0, 0, markethours.Location())

func TestNormalize_UnseedFirstBar_ZeroGapAndDefaultRelVol(t *testing.T) {
	n := NewNormalizer()

	// Simulate a bar arriving without any prior state (the bug scenario)
	bar := alpaca.StreamBar{
		Symbol:    "TEST",
		Open:      10.0,
		High:      10.5,
		Low:       9.8,
		Close:     10.2,
		Volume:    100000,
		Timestamp: marketTime(9, 35),
	}

	tick := n.Normalize(bar)

	// Without seeding: previousClose=0 → GapPercent=0
	if tick.GapPercent != 0 {
		t.Errorf("unseeded GapPercent = %f, want 0", tick.GapPercent)
	}
	// Without seeding: prevDayVolume=0 → RelativeVolume=1.0
	if tick.RelativeVolume != 1.0 {
		t.Errorf("unseeded RelativeVolume = %f, want 1.0", tick.RelativeVolume)
	}
}

func TestSeed_ProducesCorrectGapPercent(t *testing.T) {
	n := NewNormalizer()

	n.Seed("AAPL", SeedState{
		PreviousClose: 100.0,
		PrevDayVolume: 50_000_000,
		TodayOpen:     105.0,
		TodayHigh:     106.0,
		TodayVolume:   1_000_000,
		PreMarketVol:  200_000,
	}, testDay)

	// First bar after seeding — same day as seed
	bar := alpaca.StreamBar{
		Symbol:    "AAPL",
		Open:      105.5,
		High:      106.5,
		Low:       105.0,
		Close:     106.0,
		Volume:    50_000,
		Timestamp: marketTime(9, 35),
	}

	tick := n.Normalize(bar)

	// Gap = (open - previousClose) / previousClose * 100
	// open was set to 105.0 during seed, so gap = (105 - 100) / 100 * 100 = 5%
	expectedGap := 5.0
	if math.Abs(tick.GapPercent-expectedGap) > 0.01 {
		t.Errorf("GapPercent = %f, want %f", tick.GapPercent, expectedGap)
	}
}

func TestSeed_ProducesCorrectRelativeVolume(t *testing.T) {
	n := NewNormalizer()

	n.Seed("TSLA", SeedState{
		PreviousClose: 200.0,
		PrevDayVolume: 10_000_000,
		TodayOpen:     210.0,
		TodayHigh:     215.0,
		TodayVolume:   500_000,
		PreMarketVol:  100_000,
	}, testDay)

	bar := alpaca.StreamBar{
		Symbol:    "TSLA",
		Open:      211.0,
		High:      212.0,
		Low:       210.0,
		Close:     211.5,
		Volume:    100_000,
		Timestamp: marketTime(9, 35),
	}

	tick := n.Normalize(bar)

	// RelativeVolume should be > 1.0 since we have prevDayVolume seeded
	if tick.RelativeVolume <= 0 {
		t.Errorf("RelativeVolume = %f, want > 0", tick.RelativeVolume)
	}
	// prevDayVolume=10M, totalVolume after bar = 500k (seed) + 100k (bar) = 600k
	// At 9:35 AM the expected share is small, so relative volume should be meaningful
	if tick.RelativeVolume == 1.0 {
		t.Errorf("RelativeVolume = 1.0 (default), seeding did not take effect")
	}
}

func TestSeed_PreMarketVolume(t *testing.T) {
	n := NewNormalizer()

	n.Seed("GAPPER", SeedState{
		PreviousClose: 5.0,
		PrevDayVolume: 1_000_000,
		TodayOpen:     7.0,
		TodayHigh:     7.5,
		TodayVolume:   200_000,
		PreMarketVol:  200_000,
	}, testDay)

	bar := alpaca.StreamBar{
		Symbol:    "GAPPER",
		Open:      7.2,
		High:      7.6,
		Low:       7.1,
		Close:     7.4,
		Volume:    50_000,
		Timestamp: marketTime(9, 35), // regular hours, not premarket
	}

	tick := n.Normalize(bar)

	// PreMarketVolume should be the seeded value (no premarket bars added since we're after 9:30)
	if tick.PreMarketVolume != 200_000 {
		t.Errorf("PreMarketVolume = %d, want 200000", tick.PreMarketVolume)
	}
}

func TestSeed_HighOfDay(t *testing.T) {
	n := NewNormalizer()

	n.Seed("HOD", SeedState{
		PreviousClose: 10.0,
		PrevDayVolume: 1_000_000,
		TodayOpen:     11.0,
		TodayHigh:     12.0,
		TodayVolume:   100_000,
	}, testDay)

	// Bar with high below seeded HOD
	bar := alpaca.StreamBar{
		Symbol:    "HOD",
		Open:      11.5,
		High:      11.8,
		Low:       11.3,
		Close:     11.6,
		Volume:    10_000,
		Timestamp: marketTime(9, 35),
	}

	tick := n.Normalize(bar)

	// HOD should remain at seeded value since bar.High (11.8) < seed HOD (12.0)
	if tick.HighOfDay != 12.0 {
		t.Errorf("HighOfDay = %f, want 12.0", tick.HighOfDay)
	}

	// Bar with high above seeded HOD
	bar2 := alpaca.StreamBar{
		Symbol:    "HOD",
		Open:      12.1,
		High:      12.5,
		Low:       12.0,
		Close:     12.3,
		Volume:    20_000,
		Timestamp: marketTime(9, 36),
	}

	tick2 := n.Normalize(bar2)

	if tick2.HighOfDay != 12.5 {
		t.Errorf("HighOfDay after new high = %f, want 12.5", tick2.HighOfDay)
	}
}

func TestSeed_DoesNotOverwriteOnSubsequentBars(t *testing.T) {
	n := NewNormalizer()

	n.Seed("KEEP", SeedState{
		PreviousClose: 50.0,
		PrevDayVolume: 5_000_000,
		TodayOpen:     55.0,
		TodayHigh:     56.0,
		TodayVolume:   300_000,
		PreMarketVol:  100_000,
	}, testDay)

	// Process multiple bars — previousClose and prevDayVolume should persist
	for i := 0; i < 5; i++ {
		bar := alpaca.StreamBar{
			Symbol:    "KEEP",
			Open:      55.0 + float64(i)*0.1,
			High:      55.5 + float64(i)*0.1,
			Low:       54.8 + float64(i)*0.1,
			Close:     55.2 + float64(i)*0.1,
			Volume:    10_000,
			Timestamp: marketTime(9, 35+i),
		}
		n.Normalize(bar)
	}

	// One more bar to verify gap is still computed from seed
	finalBar := alpaca.StreamBar{
		Symbol:    "KEEP",
		Open:      55.5,
		High:      56.0,
		Low:       55.0,
		Close:     55.8,
		Volume:    10_000,
		Timestamp: marketTime(9, 40),
	}
	tick := n.Normalize(finalBar)

	// Gap should still be based on previousClose=50 and open=55 (seeded)
	expectedGap := 10.0 // (55 - 50) / 50 * 100
	if math.Abs(tick.GapPercent-expectedGap) > 0.01 {
		t.Errorf("GapPercent after multiple bars = %f, want %f", tick.GapPercent, expectedGap)
	}
}

func TestSeed_NegativeGap(t *testing.T) {
	n := NewNormalizer()

	n.Seed("GAPDN", SeedState{
		PreviousClose: 100.0,
		PrevDayVolume: 5_000_000,
		TodayOpen:     90.0,
		TodayHigh:     92.0,
		TodayVolume:   1_000_000,
		PreMarketVol:  300_000,
	}, testDay)

	bar := alpaca.StreamBar{
		Symbol:    "GAPDN",
		Open:      90.5,
		High:      91.0,
		Low:       89.5,
		Close:     90.0,
		Volume:    50_000,
		Timestamp: marketTime(9, 35),
	}

	tick := n.Normalize(bar)

	// Gap = (90 - 100) / 100 * 100 = -10%
	expectedGap := -10.0
	if math.Abs(tick.GapPercent-expectedGap) > 0.01 {
		t.Errorf("GapPercent = %f, want %f", tick.GapPercent, expectedGap)
	}
}

func TestSeed_VolumeAccumulates(t *testing.T) {
	n := NewNormalizer()

	n.Seed("VOL", SeedState{
		PreviousClose: 10.0,
		PrevDayVolume: 1_000_000,
		TodayOpen:     10.5,
		TodayHigh:     11.0,
		TodayVolume:   100_000,
	}, testDay)

	bar := alpaca.StreamBar{
		Symbol:    "VOL",
		Open:      10.6,
		High:      10.8,
		Low:       10.4,
		Close:     10.7,
		Volume:    25_000,
		Timestamp: marketTime(9, 35),
	}

	tick := n.Normalize(bar)

	// Volume should be seed (100k) + bar (25k) = 125k
	if tick.Volume != 125_000 {
		t.Errorf("Volume = %d, want 125000", tick.Volume)
	}
}

func TestSeed_PreMarketBarAddsToPreMarketVol(t *testing.T) {
	n := NewNormalizer()

	n.Seed("PM", SeedState{
		PreviousClose: 10.0,
		PrevDayVolume: 1_000_000,
		TodayOpen:     11.0,
		TodayHigh:     11.5,
		TodayVolume:   50_000,
		PreMarketVol:  50_000,
	}, testDay)

	// Bar during premarket adds to premarket volume
	bar := alpaca.StreamBar{
		Symbol:    "PM",
		Open:      11.0,
		High:      11.2,
		Low:       10.9,
		Close:     11.1,
		Volume:    10_000,
		Timestamp: marketTime(8, 0), // premarket
	}

	tick := n.Normalize(bar)

	// PreMarketVol should be seed (50k) + bar (10k) = 60k
	if tick.PreMarketVolume != 60_000 {
		t.Errorf("PreMarketVolume = %d, want 60000", tick.PreMarketVolume)
	}
}

// marketTime creates a time on the test day in ET.
func marketTime(hour, minute int) time.Time {
	return time.Date(testDay.Year(), testDay.Month(), testDay.Day(), hour, minute, 0, 0, markethours.Location())
}
