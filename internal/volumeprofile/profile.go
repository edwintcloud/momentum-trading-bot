package volumeprofile

import (
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/markethours"
)

type volumePoint struct {
	minuteOfDay int
	share       float64
}

var cumulativeProfile = []volumePoint{
	{minuteOfDay: 4 * 60, share: 0.001},
	{minuteOfDay: 7 * 60, share: 0.008},
	{minuteOfDay: 8 * 60, share: 0.020},
	{minuteOfDay: 9 * 60, share: 0.055},
	{minuteOfDay: 9*60 + 30, share: 0.120},
	{minuteOfDay: 10*60 + 30, share: 0.420},
	{minuteOfDay: 12 * 60, share: 0.620},
	{minuteOfDay: 14 * 60, share: 0.780},
	{minuteOfDay: 16 * 60, share: 1.000},
	{minuteOfDay: 18 * 60, share: 1.020},
	{minuteOfDay: 20 * 60, share: 1.035},
}

// ExpectedCumulativeShare returns the expected fraction of a symbol's prior
// regular-session volume that has traded by the provided New York timestamp.
func ExpectedCumulativeShare(at time.Time) float64 {
	local := at.In(markethours.Location())
	minuteOfDay := local.Hour()*60 + local.Minute()
	if minuteOfDay <= cumulativeProfile[0].minuteOfDay {
		return cumulativeProfile[0].share
	}
	if minuteOfDay >= cumulativeProfile[len(cumulativeProfile)-1].minuteOfDay {
		return cumulativeProfile[len(cumulativeProfile)-1].share
	}
	for index := 1; index < len(cumulativeProfile); index++ {
		current := cumulativeProfile[index]
		previous := cumulativeProfile[index-1]
		if minuteOfDay > current.minuteOfDay {
			continue
		}
		span := float64(current.minuteOfDay - previous.minuteOfDay)
		if span <= 0 {
			return current.share
		}
		progress := float64(minuteOfDay-previous.minuteOfDay) / span
		return previous.share + ((current.share - previous.share) * progress)
	}
	return 1.0
}
