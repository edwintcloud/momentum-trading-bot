package signals

import (
	"testing"
	"time"
)

func TestOFI_Name(t *testing.T) {
	ofi := NewOFI(DefaultOFIConfig())
	if ofi.Name() != SignalTypeOFI {
		t.Errorf("expected %s, got %s", SignalTypeOFI, ofi.Name())
	}
}

func TestOFI_DisabledReturnsNil(t *testing.T) {
	cfg := DefaultOFIConfig()
	cfg.Enabled = false
	ofi := NewOFI(cfg)

	if ofi.Enabled() {
		t.Error("expected disabled")
	}
}

func TestOFI_EmptyData(t *testing.T) {
	cfg := DefaultOFIConfig()
	cfg.Enabled = true
	ofi := NewOFI(cfg)

	// First bar: no signal (needs previous close)
	sig := ofi.OnBar("AAPL", Bar{
		Close:     100.0,
		Volume:    1000,
		Timestamp: time.Now(),
	})
	if sig != nil {
		t.Error("expected nil on first bar")
	}
}

func TestOFI_SingleBar(t *testing.T) {
	cfg := DefaultOFIConfig()
	cfg.Enabled = true
	ofi := NewOFI(cfg)

	// First bar
	ofi.OnBar("AAPL", Bar{Close: 100.0, Volume: 1000, Timestamp: time.Now()})

	// Second bar: not enough data for signal
	sig := ofi.OnBar("AAPL", Bar{Close: 101.0, Volume: 2000, Timestamp: time.Now()})
	if sig != nil {
		t.Error("expected nil with insufficient data")
	}
}

func TestOFI_StrongBuyImbalance(t *testing.T) {
	cfg := OFIConfig{
		Enabled:           true,
		WindowBars:        10,
		ThresholdSigma:    2.0,
		PersistenceMinBar: 1, // low persistence for test
	}
	ofi := NewOFI(cfg)

	ts := time.Now()

	// Seed with balanced data
	prices := []float64{100, 100.1, 99.9, 100.05, 99.95, 100.02, 99.98, 100.01}
	for _, p := range prices {
		ofi.OnBar("AAPL", Bar{Close: p, Volume: 1000, Timestamp: ts})
		ts = ts.Add(time.Minute)
	}

	// Now inject strong buy imbalance: large volume on up moves
	var lastSig *Signal
	for i := 0; i < 5; i++ {
		lastSig = ofi.OnBar("AAPL", Bar{Close: 100.5 + float64(i)*0.5, Volume: 50000, Timestamp: ts})
		ts = ts.Add(time.Minute)
	}

	if lastSig == nil {
		t.Fatal("expected a signal from strong buy imbalance")
	}
	if lastSig.Direction != DirectionLong {
		t.Errorf("expected long direction, got %s", lastSig.Direction)
	}
	if lastSig.Strength <= 0 || lastSig.Strength > 1.0 {
		t.Errorf("strength out of range: %f", lastSig.Strength)
	}
}

func TestOFI_StrongSellImbalance(t *testing.T) {
	cfg := OFIConfig{
		Enabled:           true,
		WindowBars:        10,
		ThresholdSigma:    2.0,
		PersistenceMinBar: 1,
	}
	ofi := NewOFI(cfg)

	ts := time.Now()

	// Seed with balanced data (small volume, alternating)
	prices := []float64{100, 99.99, 100.01, 99.98, 100.02, 99.97, 100.01, 99.99}
	for _, p := range prices {
		ofi.OnBar("AAPL", Bar{Close: p, Volume: 500, Timestamp: ts})
		ts = ts.Add(time.Minute)
	}

	// Strong sell imbalance: large volume on consecutive down moves
	var lastSig *Signal
	for i := 0; i < 8; i++ {
		lastSig = ofi.OnBar("AAPL", Bar{Close: 99.0 - float64(i)*0.5, Volume: 50000, Timestamp: ts})
		ts = ts.Add(time.Minute)
	}

	if lastSig == nil {
		t.Fatal("expected a signal from strong sell imbalance")
	}
	if lastSig.Direction != DirectionShort {
		t.Errorf("expected short direction, got %s", lastSig.Direction)
	}
}

func TestOFI_PersistenceFilter(t *testing.T) {
	cfg := OFIConfig{
		Enabled:           true,
		WindowBars:        10,
		ThresholdSigma:    2.0,
		PersistenceMinBar: 3, // require 3 consecutive bars
	}
	ofi := NewOFI(cfg)

	ts := time.Now()

	// Seed
	for i := 0; i < 8; i++ {
		ofi.OnBar("AAPL", Bar{Close: 100 + float64(i)*0.01, Volume: 1000, Timestamp: ts})
		ts = ts.Add(time.Minute)
	}

	// Single spike should not fire due to persistence filter
	sig := ofi.OnBar("AAPL", Bar{Close: 105, Volume: 100000, Timestamp: ts})
	if sig != nil {
		t.Error("expected nil due to persistence filter")
	}
}

func TestOFI_Reset(t *testing.T) {
	cfg := DefaultOFIConfig()
	cfg.Enabled = true
	ofi := NewOFI(cfg)

	ofi.OnBar("AAPL", Bar{Close: 100, Volume: 1000, Timestamp: time.Now()})
	ofi.Reset("AAPL")

	// After reset, first bar again
	sig := ofi.OnBar("AAPL", Bar{Close: 101, Volume: 2000, Timestamp: time.Now()})
	if sig != nil {
		t.Error("expected nil after reset")
	}
}
