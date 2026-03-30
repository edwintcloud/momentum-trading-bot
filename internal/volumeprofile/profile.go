package volumeprofile

import (
	"time"
)

// cumulativeProfile maps minutes-since-4AM-ET to expected fraction of daily volume.
// Based on a typical U-shaped intraday volume curve for US equities.
var cumulativeProfile = []struct {
	minutesSince4AM float64
	fraction        float64
}{
	{0, 0.0},    // 4:00 AM
	{330, 0.02}, // 9:30 AM (premarket contributes ~2%)
	{360, 0.10}, // 10:00 AM
	{390, 0.20}, // 10:30 AM
	{420, 0.28}, // 11:00 AM
	{480, 0.40}, // 12:00 PM
	{540, 0.50}, // 1:00 PM
	{600, 0.58}, // 2:00 PM
	{660, 0.68}, // 3:00 PM
	{720, 0.85}, // 4:00 PM
	{960, 1.0},  // 8:00 PM (end of after-hours)
}

// ExpectedCumulativeShare returns the expected fraction of a symbol's prior
// regular-session volume that has traded by the provided New York timestamp.
func ExpectedCumulativeShare(at time.Time) float64 {
	hour := at.Hour()
	minute := at.Minute()
	minutesSince4AM := float64((hour-4)*60 + minute)
	if minutesSince4AM < 0 {
		return 0
	}

	for i := 1; i < len(cumulativeProfile); i++ {
		if minutesSince4AM <= cumulativeProfile[i].minutesSince4AM {
			prev := cumulativeProfile[i-1]
			curr := cumulativeProfile[i]
			t := (minutesSince4AM - prev.minutesSince4AM) / (curr.minutesSince4AM - prev.minutesSince4AM)
			return prev.fraction + t*(curr.fraction-prev.fraction)
		}
	}
	return 1.0
}
