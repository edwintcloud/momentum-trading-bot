package autooptimize

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func TestScheduler_NextSaturdayWeekly(t *testing.T) {
	loc := markethours.Location()
	s := &Scheduler{Schedule: "weekly"}

	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "monday -> saturday",
			now:  time.Date(2026, 3, 23, 10, 0, 0, 0, loc), // Monday
			want: time.Date(2026, 3, 28, 6, 0, 0, 0, loc),  // Saturday 6 AM
		},
		{
			name: "friday -> saturday",
			now:  time.Date(2026, 3, 27, 18, 0, 0, 0, loc), // Friday
			want: time.Date(2026, 3, 28, 6, 0, 0, 0, loc),  // Saturday 6 AM
		},
		{
			name: "saturday before 6am -> same saturday",
			now:  time.Date(2026, 3, 28, 4, 0, 0, 0, loc), // Saturday 4 AM
			want: time.Date(2026, 3, 28, 6, 0, 0, 0, loc),  // Saturday 6 AM (same day)
		},
		{
			name: "saturday after 6am -> next saturday",
			now:  time.Date(2026, 3, 28, 8, 0, 0, 0, loc), // Saturday 8 AM
			want: time.Date(2026, 4, 4, 6, 0, 0, 0, loc),  // Next Saturday 6 AM
		},
		{
			name: "sunday -> next saturday",
			now:  time.Date(2026, 3, 29, 12, 0, 0, 0, loc), // Sunday
			want: time.Date(2026, 4, 4, 6, 0, 0, 0, loc),   // Next Saturday 6 AM
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.nextRunTime(tt.now)
			if !got.Equal(tt.want) {
				t.Errorf("nextRunTime(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

func TestScheduler_NextDaily(t *testing.T) {
	loc := markethours.Location()
	s := &Scheduler{Schedule: "daily"}

	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before 6am -> same day",
			now:  time.Date(2026, 3, 23, 4, 0, 0, 0, loc),
			want: time.Date(2026, 3, 23, 6, 0, 0, 0, loc),
		},
		{
			name: "after 6am -> next day",
			now:  time.Date(2026, 3, 23, 8, 0, 0, 0, loc),
			want: time.Date(2026, 3, 24, 6, 0, 0, 0, loc),
		},
		{
			name: "exactly 6am -> next day",
			now:  time.Date(2026, 3, 23, 6, 0, 0, 0, loc),
			want: time.Date(2026, 3, 24, 6, 0, 0, 0, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.nextRunTime(tt.now)
			if !got.Equal(tt.want) {
				t.Errorf("nextRunTime(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

func TestScheduler_NextRunTimeIsInET(t *testing.T) {
	loc := markethours.Location()
	s := &Scheduler{Schedule: "weekly"}

	// Use a UTC time
	now := time.Date(2026, 3, 23, 15, 0, 0, 0, time.UTC) // Monday 3 PM UTC
	got := s.nextRunTime(now)

	// Result should be in ET
	if got.Location().String() != loc.String() {
		t.Errorf("expected ET timezone, got %s", got.Location())
	}
}
