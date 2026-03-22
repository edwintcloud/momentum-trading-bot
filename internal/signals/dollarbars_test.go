package signals

import (
	"testing"
	"time"
)

func TestDollarBarBuilder_Name(t *testing.T) {
	d := NewDollarBarBuilder(DefaultDollarBarConfig())
	if d.Name() != SignalTypeDollarBar {
		t.Errorf("expected %s, got %s", SignalTypeDollarBar, d.Name())
	}
}

func TestDollarBarBuilder_Disabled(t *testing.T) {
	cfg := DefaultDollarBarConfig()
	cfg.Enabled = false
	d := NewDollarBarBuilder(cfg)
	if d.Enabled() {
		t.Error("expected disabled")
	}
}

func TestDollarBarBuilder_EmptyData(t *testing.T) {
	cfg := DollarBarConfig{Enabled: true, Threshold: 100000}
	d := NewDollarBarBuilder(cfg)

	// Small bar that doesn't reach threshold
	sig := d.OnBar("AAPL", Bar{
		Open: 100, High: 101, Low: 99, Close: 100,
		Volume: 500, Timestamp: time.Now(),
	})
	if sig != nil {
		t.Error("expected nil when threshold not reached")
	}
}

func TestDollarBarBuilder_ThresholdReached(t *testing.T) {
	cfg := DollarBarConfig{Enabled: true, Threshold: 10000} // low threshold
	d := NewDollarBarBuilder(cfg)

	ts := time.Now()

	// bar: 100 * 200 = $20k > $10k threshold
	sig := d.OnBar("AAPL", Bar{
		Open: 100, High: 101, Low: 99, Close: 101,
		Volume: 200, Timestamp: ts,
	})
	if sig == nil {
		t.Fatal("expected signal when dollar threshold reached")
	}
	if sig.Type != SignalTypeDollarBar {
		t.Errorf("expected dollar_bar type, got %s", sig.Type)
	}
	if sig.Direction != DirectionLong {
		t.Errorf("expected long direction (close > open), got %s", sig.Direction)
	}
}

func TestDollarBarBuilder_MultipleBarAccumulation(t *testing.T) {
	cfg := DollarBarConfig{Enabled: true, Threshold: 50000}
	d := NewDollarBarBuilder(cfg)

	ts := time.Now()

	// First bar: 100 * 200 = $20k
	sig := d.OnBar("AAPL", Bar{
		Open: 100, High: 101, Low: 99, Close: 100,
		Volume: 200, Timestamp: ts,
	})
	if sig != nil {
		t.Error("expected nil, threshold not reached yet")
	}

	// Second bar: 100 * 400 = $40k, total = $60k > $50k
	sig = d.OnBar("AAPL", Bar{
		Open: 100, High: 102, Low: 99, Close: 99,
		Volume: 400, Timestamp: ts.Add(time.Minute),
	})
	if sig == nil {
		t.Fatal("expected signal after accumulation exceeds threshold")
	}
	if sig.Direction != DirectionShort {
		t.Errorf("expected short direction (close < open), got %s", sig.Direction)
	}
}

func TestDollarBarBuilder_AddBar(t *testing.T) {
	cfg := DollarBarConfig{Enabled: true, Threshold: 10000}
	d := NewDollarBarBuilder(cfg)

	ts := time.Now()

	// Below threshold
	result := d.AddBar("AAPL", Bar{
		Open: 100, High: 100, Low: 100, Close: 100,
		Volume: 50, Timestamp: ts,
	})
	if result != nil {
		t.Error("expected nil below threshold")
	}

	// Exceeds threshold
	result = d.AddBar("AAPL", Bar{
		Open: 100, High: 105, Low: 99, Close: 103,
		Volume: 200, Timestamp: ts.Add(time.Minute),
	})
	if result == nil {
		t.Fatal("expected completed bar")
	}
	if result.Volume != 250 { // 50 + 200
		t.Errorf("expected volume 250, got %d", result.Volume)
	}
}

func TestDollarBarBuilder_Reset(t *testing.T) {
	cfg := DollarBarConfig{Enabled: true, Threshold: 50000}
	d := NewDollarBarBuilder(cfg)

	d.OnBar("AAPL", Bar{Close: 100, Volume: 200, Timestamp: time.Now()})
	d.Reset("AAPL")

	// After reset, accumulator should be cleared
	sig := d.OnBar("AAPL", Bar{
		Open: 100, High: 100, Low: 100, Close: 100,
		Volume: 100, Timestamp: time.Now(),
	})
	if sig != nil {
		t.Error("expected nil after reset with small bar")
	}
}

// Volume Bar tests

func TestVolumeBarBuilder_Name(t *testing.T) {
	v := NewVolumeBarBuilder(DefaultVolumeBarConfig())
	if v.Name() != SignalTypeVolumeBar {
		t.Errorf("expected %s, got %s", SignalTypeVolumeBar, v.Name())
	}
}

func TestVolumeBarBuilder_Disabled(t *testing.T) {
	cfg := DefaultVolumeBarConfig()
	cfg.Enabled = false
	v := NewVolumeBarBuilder(cfg)
	if v.Enabled() {
		t.Error("expected disabled")
	}
}

func TestVolumeBarBuilder_ThresholdReached(t *testing.T) {
	cfg := VolumeBarConfig{Enabled: true, Threshold: 1000}
	v := NewVolumeBarBuilder(cfg)

	ts := time.Now()

	// Volume 500: below threshold
	sig := v.OnBar("AAPL", Bar{
		Open: 100, High: 101, Low: 99, Close: 100.5,
		Volume: 500, Timestamp: ts,
	})
	if sig != nil {
		t.Error("expected nil below threshold")
	}

	// Volume 600: total 1100 > 1000
	sig = v.OnBar("AAPL", Bar{
		Open: 100, High: 102, Low: 99, Close: 101,
		Volume: 600, Timestamp: ts.Add(time.Minute),
	})
	if sig == nil {
		t.Fatal("expected signal when volume threshold reached")
	}
	if sig.Type != SignalTypeVolumeBar {
		t.Errorf("expected volume_bar type, got %s", sig.Type)
	}
}

func TestVolumeBarBuilder_AddBar(t *testing.T) {
	cfg := VolumeBarConfig{Enabled: true, Threshold: 1000}
	v := NewVolumeBarBuilder(cfg)

	ts := time.Now()

	result := v.AddBar("AAPL", Bar{
		Open: 100, High: 100, Low: 100, Close: 100,
		Volume: 500, Timestamp: ts,
	})
	if result != nil {
		t.Error("expected nil below threshold")
	}

	result = v.AddBar("AAPL", Bar{
		Open: 100, High: 105, Low: 99, Close: 103,
		Volume: 800, Timestamp: ts.Add(time.Minute),
	})
	if result == nil {
		t.Fatal("expected completed bar")
	}
	if result.Volume != 1300 {
		t.Errorf("expected volume 1300, got %d", result.Volume)
	}
}

func TestVolumeBarBuilder_Reset(t *testing.T) {
	cfg := VolumeBarConfig{Enabled: true, Threshold: 1000}
	v := NewVolumeBarBuilder(cfg)

	v.OnBar("AAPL", Bar{Close: 100, Volume: 500, Timestamp: time.Now()})
	v.Reset("AAPL")

	sig := v.OnBar("AAPL", Bar{
		Open: 100, High: 100, Low: 100, Close: 100,
		Volume: 100, Timestamp: time.Now(),
	})
	if sig != nil {
		t.Error("expected nil after reset with small bar")
	}
}
