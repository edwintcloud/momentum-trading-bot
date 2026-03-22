package signals

import (
	"testing"
	"time"
)

func TestVPIN_Name(t *testing.T) {
	v := NewVPIN(DefaultVPINConfig())
	if v.Name() != SignalTypeVPIN {
		t.Errorf("expected %s, got %s", SignalTypeVPIN, v.Name())
	}
}

func TestVPIN_DisabledReturnsNil(t *testing.T) {
	cfg := DefaultVPINConfig()
	cfg.Enabled = false
	v := NewVPIN(cfg)

	if v.Enabled() {
		t.Error("expected disabled")
	}
}

func TestVPIN_EmptyData(t *testing.T) {
	cfg := DefaultVPINConfig()
	cfg.Enabled = true
	v := NewVPIN(cfg)

	sig := v.OnBar("AAPL", Bar{Close: 100.0, Volume: 1000, Timestamp: time.Now()})
	if sig != nil {
		t.Error("expected nil on first bar")
	}
}

func TestVPIN_SingleBar(t *testing.T) {
	cfg := DefaultVPINConfig()
	cfg.Enabled = true
	v := NewVPIN(cfg)

	v.OnBar("AAPL", Bar{Close: 100.0, Volume: 1000, Timestamp: time.Now()})
	sig := v.OnBar("AAPL", Bar{Close: 101.0, Volume: 2000, Timestamp: time.Now()})
	if sig != nil {
		t.Error("expected nil with insufficient buckets")
	}
}

func TestVPIN_SetADV(t *testing.T) {
	cfg := VPINConfig{
		Enabled:         true,
		BucketDivisor:   50,
		LookbackBuckets: 5,
		HighThreshold:   0.7,
		LowThreshold:    0.3,
	}
	v := NewVPIN(cfg)
	v.SetADV("AAPL", 1000000)

	v.mu.Lock()
	st := v.getState("AAPL")
	v.mu.Unlock()

	expectedBucket := int64(1000000 / 50)
	if st.bucketSize != expectedBucket {
		t.Errorf("expected bucket size %d, got %d", expectedBucket, st.bucketSize)
	}
}

func TestVPIN_HighVPIN_MomentumSignal(t *testing.T) {
	cfg := VPINConfig{
		Enabled:         true,
		BucketDivisor:   50,
		LookbackBuckets: 5, // small lookback for test
		HighThreshold:   0.7,
		LowThreshold:    0.3,
	}
	v := NewVPIN(cfg)
	v.SetADV("AAPL", 50000) // bucket size = 1000

	ts := time.Now()

	// First bar to initialize
	v.OnBar("AAPL", Bar{Close: 100, Volume: 500, Open: 100, Timestamp: ts})
	ts = ts.Add(time.Minute)

	// Generate highly imbalanced buy volume (all up moves) to create high VPIN
	var lastSig *Signal
	for i := 0; i < 50; i++ {
		lastSig = v.OnBar("AAPL", Bar{
			Open:      100 + float64(i)*0.1,
			Close:     100.1 + float64(i)*0.1, // always up → all buy volume
			Volume:    500,
			Timestamp: ts,
		})
		ts = ts.Add(time.Minute)
	}

	if lastSig == nil {
		t.Fatal("expected high VPIN signal")
	}
	if lastSig.Type != SignalTypeVPIN {
		t.Errorf("expected VPIN type, got %s", lastSig.Type)
	}
}

func TestVPIN_Reset(t *testing.T) {
	cfg := DefaultVPINConfig()
	cfg.Enabled = true
	v := NewVPIN(cfg)

	v.OnBar("AAPL", Bar{Close: 100, Volume: 1000, Timestamp: time.Now()})
	v.Reset("AAPL")

	// After reset, should be back to initial state
	sig := v.OnBar("AAPL", Bar{Close: 101, Volume: 2000, Timestamp: time.Now()})
	if sig != nil {
		t.Error("expected nil after reset")
	}
}

func TestVPIN_ZeroBucketDivisor(t *testing.T) {
	cfg := VPINConfig{
		Enabled:         true,
		BucketDivisor:   0,
		LookbackBuckets: 5,
		HighThreshold:   0.7,
		LowThreshold:    0.3,
	}
	v := NewVPIN(cfg)
	v.SetADV("AAPL", 1000000)

	v.mu.Lock()
	st := v.getState("AAPL")
	v.mu.Unlock()

	// Should default to 1 to avoid division by zero
	if st.bucketSize <= 0 {
		t.Errorf("bucket size should be positive, got %d", st.bucketSize)
	}
}
