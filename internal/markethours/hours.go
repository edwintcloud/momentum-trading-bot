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

// IsMarketOpen returns true if the given time is within regular trading hours.
func IsMarketOpen(t time.Time) bool {
	ny := t.In(nyLoc)
	weekday := ny.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	open := MarketOpen(t)
	close := MarketClose(t)
	return !ny.Before(open) && ny.Before(close)
}

// IsPreMarket returns true if the time is between 4:00 AM and 9:30 AM ET.
func IsPreMarket(t time.Time) bool {
	ny := t.In(nyLoc)
	weekday := ny.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	preOpen := time.Date(ny.Year(), ny.Month(), ny.Day(), 4, 0, 0, 0, nyLoc)
	open := MarketOpen(t)
	return !ny.Before(preOpen) && ny.Before(open)
}

// MinutesSinceOpen returns the number of minutes elapsed since market open.
func MinutesSinceOpen(t time.Time) float64 {
	open := MarketOpen(t)
	if t.Before(open) {
		return 0
	}
	return t.Sub(open).Minutes()
}

// TradingDay returns the date string (YYYY-MM-DD) for the trading day.
func TradingDay(t time.Time) string {
	return t.In(nyLoc).Format("2006-01-02")
}

// PreviousTradingDay returns the prior weekday.
func PreviousTradingDay(t time.Time) time.Time {
	ny := t.In(nyLoc)
	for {
		ny = ny.AddDate(0, 0, -1)
		if ny.Weekday() != time.Saturday && ny.Weekday() != time.Sunday {
			return ny
		}
	}
}
