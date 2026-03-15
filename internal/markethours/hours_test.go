package markethours

import (
	"testing"
	"time"
)

func TestIsTradableSessionAt(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "pre-market open", at: time.Date(2026, 3, 13, 8, 0, 0, 0, newYorkLocation), want: true},
		{name: "regular hours", at: time.Date(2026, 3, 13, 10, 0, 0, 0, newYorkLocation), want: true},
		{name: "post-market", at: time.Date(2026, 3, 13, 19, 59, 0, 0, newYorkLocation), want: true},
		{name: "before pre-market", at: time.Date(2026, 3, 13, 3, 59, 0, 0, newYorkLocation), want: false},
		{name: "after post-market", at: time.Date(2026, 3, 13, 20, 0, 0, 0, newYorkLocation), want: false},
		{name: "weekend", at: time.Date(2026, 3, 14, 10, 0, 0, 0, newYorkLocation), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTradableSessionAt(tc.at); got != tc.want {
				t.Fatalf("expected %v for %s, got %v", tc.want, tc.at.Format(time.RFC3339), got)
			}
		})
	}
}
