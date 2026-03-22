package signals

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func TestORB_Name(t *testing.T) {
	orb := NewORB(DefaultORBConfig())
	if orb.Name() != SignalTypeORB {
		t.Errorf("expected %s, got %s", SignalTypeORB, orb.Name())
	}
}

func TestORB_Disabled(t *testing.T) {
	cfg := DefaultORBConfig()
	cfg.Enabled = false
	orb := NewORB(cfg)
	if orb.Enabled() {
		t.Error("expected disabled")
	}
}

func TestORB_EmptyData(t *testing.T) {
	cfg := DefaultORBConfig()
	cfg.Enabled = true
	orb := NewORB(cfg)

	// Pre-market bar should not produce signal
	loc := markethours.Location()
	ts := time.Date(2026, 3, 20, 8, 30, 0, 0, loc) // 8:30 AM ET, before market open
	sig := orb.OnBar("AAPL", Bar{
		Open: 100, High: 101, Low: 99, Close: 100,
		Volume: 1000, Timestamp: ts,
	})
	if sig != nil {
		t.Error("expected nil for pre-market bar")
	}
}

func TestORB_BuildsRange(t *testing.T) {
	cfg := ORBConfig{
		Enabled:          true,
		WindowMinutes:    15,
		BufferPct:        0.001,
		VolumeMultiplier: 1.5,
		MaxGapPct:        0.02,
		TargetMultiplier: 1.5,
	}
	orb := NewORB(cfg)

	loc := markethours.Location()
	// Market opens at 9:30 ET
	marketOpen := time.Date(2026, 3, 20, 9, 30, 0, 0, loc)

	// Feed bars during opening range (first 15 minutes)
	bars := []Bar{
		{Open: 100, High: 102, Low: 99, Close: 101, Volume: 5000, Timestamp: marketOpen.Add(1 * time.Minute)},
		{Open: 101, High: 103, Low: 100, Close: 102, Volume: 4000, Timestamp: marketOpen.Add(5 * time.Minute)},
		{Open: 102, High: 104, Low: 101, Close: 103, Volume: 3000, Timestamp: marketOpen.Add(10 * time.Minute)},
	}

	for _, b := range bars {
		sig := orb.OnBar("AAPL", b)
		if sig != nil {
			t.Error("expected nil during opening range window")
		}
	}

	// Check range
	high, low, set := orb.OpeningRange("AAPL")
	if set {
		t.Error("range should not be set yet (still in window)")
	}
	// The range values should still be tracked internally
	_ = high
	_ = low
}

func TestORB_LongBreakout(t *testing.T) {
	cfg := ORBConfig{
		Enabled:          true,
		WindowMinutes:    5, // short window for test
		BufferPct:        0.001,
		VolumeMultiplier: 1.0, // no volume requirement for test simplicity
		MaxGapPct:        0.05,
		TargetMultiplier: 1.5,
	}
	orb := NewORB(cfg)

	loc := markethours.Location()
	marketOpen := time.Date(2026, 3, 20, 9, 30, 0, 0, loc)

	// Opening range bars (first 5 minutes)
	orb.OnBar("AAPL", Bar{
		Open: 100, High: 102, Low: 99, Close: 101,
		Volume: 5000, Timestamp: marketOpen.Add(1 * time.Minute),
	})
	orb.OnBar("AAPL", Bar{
		Open: 101, High: 103, Low: 100, Close: 102,
		Volume: 4000, Timestamp: marketOpen.Add(3 * time.Minute),
	})

	// Post-range bar that breaks above high (103) + buffer
	sig := orb.OnBar("AAPL", Bar{
		Open: 102, High: 105, Low: 102, Close: 104, // close > 103 + buffer
		Volume: 10000, Timestamp: marketOpen.Add(6 * time.Minute),
	})

	if sig == nil {
		t.Fatal("expected long breakout signal")
	}
	if sig.Direction != DirectionLong {
		t.Errorf("expected long direction, got %s", sig.Direction)
	}
	if sig.Type != SignalTypeORB {
		t.Errorf("expected ORB type, got %s", sig.Type)
	}
}

func TestORB_ShortBreakout(t *testing.T) {
	cfg := ORBConfig{
		Enabled:          true,
		WindowMinutes:    5,
		BufferPct:        0.001,
		VolumeMultiplier: 1.0,
		MaxGapPct:        0.05,
		TargetMultiplier: 1.5,
	}
	orb := NewORB(cfg)

	loc := markethours.Location()
	marketOpen := time.Date(2026, 3, 20, 9, 30, 0, 0, loc)

	// Opening range
	orb.OnBar("AAPL", Bar{
		Open: 100, High: 102, Low: 99, Close: 101,
		Volume: 5000, Timestamp: marketOpen.Add(1 * time.Minute),
	})
	orb.OnBar("AAPL", Bar{
		Open: 101, High: 103, Low: 100, Close: 100,
		Volume: 4000, Timestamp: marketOpen.Add(3 * time.Minute),
	})

	// Short breakout: close < 99 - buffer
	sig := orb.OnBar("AAPL", Bar{
		Open: 100, High: 100, Low: 97, Close: 97, // close < 99 - buffer
		Volume: 10000, Timestamp: marketOpen.Add(6 * time.Minute),
	})

	if sig == nil {
		t.Fatal("expected short breakout signal")
	}
	if sig.Direction != DirectionShort {
		t.Errorf("expected short direction, got %s", sig.Direction)
	}
}

func TestORB_GapFilter(t *testing.T) {
	cfg := ORBConfig{
		Enabled:          true,
		WindowMinutes:    5,
		BufferPct:        0.001,
		VolumeMultiplier: 1.0,
		MaxGapPct:        0.02, // 2% gap filter
		TargetMultiplier: 1.5,
	}
	orb := NewORB(cfg)
	orb.SetPrevClose("AAPL", 100.0) // previous close = 100

	loc := markethours.Location()
	marketOpen := time.Date(2026, 3, 20, 9, 30, 0, 0, loc)

	// Opening range with a big gap open at 105 (5% gap > 2%)
	orb.OnBar("AAPL", Bar{
		Open: 105, High: 107, Low: 104, Close: 106,
		Volume: 5000, Timestamp: marketOpen.Add(1 * time.Minute),
	})

	// Try breakout after range
	sig := orb.OnBar("AAPL", Bar{
		Open: 106, High: 110, Low: 106, Close: 109,
		Volume: 10000, Timestamp: marketOpen.Add(6 * time.Minute),
	})
	if sig != nil {
		t.Error("expected nil due to gap filter")
	}
}

func TestORB_FiresOnlyOnce(t *testing.T) {
	cfg := ORBConfig{
		Enabled:          true,
		WindowMinutes:    5,
		BufferPct:        0.001,
		VolumeMultiplier: 1.0,
		MaxGapPct:        0.05,
		TargetMultiplier: 1.5,
	}
	orb := NewORB(cfg)

	loc := markethours.Location()
	marketOpen := time.Date(2026, 3, 20, 9, 30, 0, 0, loc)

	// Opening range
	orb.OnBar("AAPL", Bar{
		Open: 100, High: 102, Low: 99, Close: 101,
		Volume: 5000, Timestamp: marketOpen.Add(1 * time.Minute),
	})

	// First breakout fires
	sig1 := orb.OnBar("AAPL", Bar{
		Open: 102, High: 105, Low: 102, Close: 104,
		Volume: 10000, Timestamp: marketOpen.Add(6 * time.Minute),
	})
	if sig1 == nil {
		t.Fatal("expected first breakout signal")
	}

	// Second breakout should not fire
	sig2 := orb.OnBar("AAPL", Bar{
		Open: 104, High: 108, Low: 104, Close: 107,
		Volume: 10000, Timestamp: marketOpen.Add(7 * time.Minute),
	})
	if sig2 != nil {
		t.Error("expected nil on second breakout (already fired)")
	}
}

func TestORB_Reset(t *testing.T) {
	cfg := DefaultORBConfig()
	cfg.Enabled = true
	orb := NewORB(cfg)

	loc := markethours.Location()
	ts := time.Date(2026, 3, 20, 9, 31, 0, 0, loc)
	orb.OnBar("AAPL", Bar{Close: 100, Volume: 1000, Timestamp: ts})
	orb.Reset("AAPL")

	_, _, set := orb.OpeningRange("AAPL")
	if set {
		t.Error("expected range not set after reset")
	}
}
