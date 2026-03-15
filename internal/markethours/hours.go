package markethours

import "time"

var newYorkLocation = mustLoadLocation("America/New_York")

const (
	premarketOpenMinute   = 4 * 60
	postmarketCloseMinute = 20 * 60
)

// IsTradableSessionAt reports whether US equities can be traded at the given
// timestamp, including premarket and postmarket extended hours.
func IsTradableSessionAt(at time.Time) bool {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	local := at.In(newYorkLocation)
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= premarketOpenMinute && minutes < postmarketCloseMinute
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
