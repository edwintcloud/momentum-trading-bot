package markethours

import "time"

var newYorkLocation = mustLoadLocation("America/New_York")

const (
	premarketOpen   = 4  // 4 am
	postmarketClose = 20 // 8 pm
)

// IsTradableSessionAt reports whether US equities can be traded at the given
// timestamp, including premarket and postmarket extended hours.
func IsTradableSessionAt(at time.Time) bool {
	if at.IsZero() {
		at = time.Now()
	}
	local := at.In(Location())
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	return local.Hour() >= premarketOpen && local.Hour() < postmarketClose
}

// Location returns the shared America/New_York market timezone.
func Location() *time.Location {
	return newYorkLocation
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
