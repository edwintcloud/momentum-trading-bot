package markethours

import (
	"time"
)

var nyLoc *time.Location

func init() {
	var err error
	nyLoc, err = time.LoadLocation("America/New_York")
	if err != nil {
		panic("failed to load America/New_York timezone: " + err.Error())
	}
}

// NYLocation returns the Eastern Time location.
func NYLocation() *time.Location {
	return nyLoc
}

// Location is an alias for NYLocation.
func Location() *time.Location {
	return nyLoc
}

// IsTradableSessionAt reports whether US equities can be traded at the given time
// (includes premarket, postmarket and regular hours).
func IsTradableSessionAt(at time.Time) bool {
	return IsMarketOpen(at) || IsPreMarket(at) || IsPostMarket(at)
}

// MarketOpen returns 9:30 AM ET for the given date.
func MarketOpen(t time.Time) time.Time {
	ny := t.In(nyLoc)
	return time.Date(ny.Year(), ny.Month(), ny.Day(), 9, 30, 0, 0, nyLoc)
}

// MarketClose returns 4:00 PM ET for the given date.
func MarketClose(t time.Time) time.Time {
	ny := t.In(nyLoc)
	return time.Date(ny.Year(), ny.Month(), ny.Day(), 16, 0, 0, 0, nyLoc)
}

// IsMarketOpen returns true if the given time is within regular trading hours on a market day.
func IsMarketOpen(t time.Time) bool {
	ny := t.In(nyLoc)
	weekday := ny.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	if IsMarketHoliday(ny) {
		return false
	}
	open := MarketOpen(t)
	close := MarketClose(t)
	return !ny.Before(open) && ny.Before(close)
}

// IsPreMarket returns true if the time is between 4:00 AM and 9:30 AM ET on a market day.
func IsPreMarket(t time.Time) bool {
	ny := t.In(nyLoc)
	weekday := ny.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	if IsMarketHoliday(ny) {
		return false
	}
	preOpen := time.Date(ny.Year(), ny.Month(), ny.Day(), 4, 0, 0, 0, nyLoc)
	open := MarketOpen(t)
	return !ny.Before(preOpen) && ny.Before(open)
}

// IsPostMarket returns true if the time is between 4:00 PM and 8:00 PM ET on a market day.
func IsPostMarket(t time.Time) bool {
	ny := t.In(nyLoc)
	weekday := ny.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	if IsMarketHoliday(ny) {
		return false
	}
	postClose := time.Date(ny.Year(), ny.Month(), ny.Day(), 20, 0, 0, 0, nyLoc)
	close := MarketClose(t)
	return !ny.Before(close) && ny.Before(postClose)
}

// IsMarketHoliday returns true if the given date is a US stock market holiday.
func IsMarketHoliday(t time.Time) bool {
	ny := t.In(nyLoc)
	year := ny.Year()
	month := ny.Month()
	day := ny.Day()

	// New Year's Day (Jan 1, observed)
	if nyd := observedHoliday(year, time.January, 1); month == nyd.Month() && day == nyd.Day() {
		return true
	}

	// MLK Day (3rd Monday in January)
	if month == time.January && ny.Weekday() == time.Monday && nthWeekday(ny) == 3 {
		return true
	}

	// Presidents Day (3rd Monday in February)
	if month == time.February && ny.Weekday() == time.Monday && nthWeekday(ny) == 3 {
		return true
	}

	// Good Friday (2 days before Easter)
	gf := goodFriday(year)
	if month == gf.Month() && day == gf.Day() {
		return true
	}

	// Memorial Day (last Monday in May)
	if month == time.May && ny.Weekday() == time.Monday && isLastWeekdayOfMonth(ny, time.Monday) {
		return true
	}

	// Juneteenth (June 19, observed)
	if jt := observedHoliday(year, time.June, 19); month == jt.Month() && day == jt.Day() {
		return true
	}

	// Independence Day (July 4, observed)
	if id := observedHoliday(year, time.July, 4); month == id.Month() && day == id.Day() {
		return true
	}

	// Labor Day (1st Monday in September)
	if month == time.September && ny.Weekday() == time.Monday && nthWeekday(ny) == 1 {
		return true
	}

	// Thanksgiving (4th Thursday in November)
	if month == time.November && ny.Weekday() == time.Thursday && nthWeekday(ny) == 4 {
		return true
	}

	// Christmas Day (Dec 25, observed)
	if xm := observedHoliday(year, time.December, 25); month == xm.Month() && day == xm.Day() {
		return true
	}

	return false
}

// IsMarketDay returns true if the market is open on the given day (not weekend, not holiday).
func IsMarketDay(t time.Time) bool {
	ny := t.In(nyLoc)
	weekday := ny.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	return !IsMarketHoliday(ny)
}

// observedHoliday returns the observed date for a fixed holiday.
// Saturday holidays are observed on Friday; Sunday holidays on Monday.
func observedHoliday(year int, month time.Month, day int) time.Time {
	t := time.Date(year, month, day, 0, 0, 0, 0, nyLoc)
	switch t.Weekday() {
	case time.Saturday:
		return t.AddDate(0, 0, -1)
	case time.Sunday:
		return t.AddDate(0, 0, 1)
	default:
		return t
	}
}

// nthWeekday returns which occurrence of this weekday within the month (1-indexed).
func nthWeekday(t time.Time) int {
	return (t.Day()-1)/7 + 1
}

// isLastWeekdayOfMonth returns true if t is the last occurrence of the given weekday in its month.
func isLastWeekdayOfMonth(t time.Time, wd time.Weekday) bool {
	if t.Weekday() != wd {
		return false
	}
	nextWeek := t.AddDate(0, 0, 7)
	return nextWeek.Month() != t.Month()
}

// goodFriday computes Good Friday for a given year using the Anonymous Gregorian Easter algorithm.
func goodFriday(year int) time.Time {
	easter := computeEaster(year)
	return easter.AddDate(0, 0, -2)
}

// computeEaster returns Easter Sunday for the given year using the Anonymous Gregorian algorithm.
func computeEaster(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := ((h + l - 7*m + 114) % 31) + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, nyLoc)
}

// MinutesSinceOpen returns the number of minutes elapsed since market open.
func MinutesSinceOpen(t time.Time) float64 {
	open := MarketOpen(t)
	if t.Before(open) {
		return 0
	}
	return t.Sub(open).Minutes()
}

// RemainingMinutes returns the number of regular-session minutes remaining
// from time t until market close (4:00 PM ET). Returns 0 if market is closed.
func RemainingMinutes(t time.Time) int {
	close := MarketClose(t)
	if !t.Before(close) {
		return 0
	}
	open := MarketOpen(t)
	if t.Before(open) {
		return 390 // full session
	}
	remaining := int(close.Sub(t).Minutes())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// TradingDay returns the date string (YYYY-MM-DD) for the trading day.
func TradingDay(t time.Time) string {
	return t.In(nyLoc).Format("2006-01-02")
}

// PreviousTradingDay returns the prior trading day (skipping weekends and holidays).
func PreviousTradingDay(t time.Time) time.Time {
	ny := t.In(nyLoc)
	for {
		ny = ny.AddDate(0, 0, -1)
		if IsMarketDay(ny) {
			return ny
		}
	}
}
