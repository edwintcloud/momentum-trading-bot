package signals

import (
	"testing"
	"time"
)

func TestDirection_String(t *testing.T) {
	tests := []struct {
		dir      Direction
		expected string
	}{
		{DirectionLong, "long"},
		{DirectionShort, "short"},
		{DirectionNeutral, "neutral"},
	}
	for _, tt := range tests {
		if tt.dir.String() != tt.expected {
			t.Errorf("expected %s, got %s", tt.expected, tt.dir.String())
		}
	}
}

func TestAggregator_Empty(t *testing.T) {
	agg := NewAggregator()
	sigs := agg.OnBar("AAPL", Bar{Close: 100, Volume: 1000, Timestamp: time.Now()})
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals, got %d", len(sigs))
	}
}

func TestAggregator_OnlyEnabledSources(t *testing.T) {
	enabledCfg := OFIConfig{Enabled: true, WindowBars: 10, ThresholdSigma: 3.0, PersistenceMinBar: 1}
	disabledCfg := OFIConfig{Enabled: false, WindowBars: 10, ThresholdSigma: 3.0, PersistenceMinBar: 1}

	agg := NewAggregator(
		NewOFI(enabledCfg),
		NewOFI(disabledCfg),
	)

	sources := agg.Sources()
	if len(sources) != 1 {
		t.Errorf("expected 1 enabled source, got %d", len(sources))
	}
}

func TestAggregator_Reset(t *testing.T) {
	cfg := OFIConfig{Enabled: true, WindowBars: 10, ThresholdSigma: 3.0, PersistenceMinBar: 1}
	agg := NewAggregator(NewOFI(cfg))

	agg.OnBar("AAPL", Bar{Close: 100, Volume: 1000, Timestamp: time.Now()})
	agg.Reset("AAPL")

	// After reset, the source should have cleared state
	sigs := agg.OnBar("AAPL", Bar{Close: 101, Volume: 2000, Timestamp: time.Now()})
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals after reset, got %d", len(sigs))
	}
}

func TestAggregator_MultipleSources(t *testing.T) {
	ofiCfg := OFIConfig{Enabled: true, WindowBars: 10, ThresholdSigma: 3.0, PersistenceMinBar: 1}
	obvCfg := OBVConfig{Enabled: true, LookbackBars: 10}

	agg := NewAggregator(
		NewOFI(ofiCfg),
		NewOBVDivergence(obvCfg),
	)

	sources := agg.Sources()
	if len(sources) != 2 {
		t.Errorf("expected 2 sources, got %d", len(sources))
	}

	// Feed a bar — neither should fire yet (insufficient data)
	sigs := agg.OnBar("AAPL", Bar{Close: 100, Volume: 1000, Timestamp: time.Now()})
	if len(sigs) != 0 {
		t.Errorf("expected 0 signals with insufficient data, got %d", len(sigs))
	}
}

func TestAggregator_AllDisabled(t *testing.T) {
	agg := NewAggregator(
		NewOFI(OFIConfig{Enabled: false}),
		NewVPIN(VPINConfig{Enabled: false}),
		NewOBVDivergence(OBVConfig{Enabled: false}),
	)

	sources := agg.Sources()
	if len(sources) != 0 {
		t.Errorf("expected 0 sources when all disabled, got %d", len(sources))
	}
}
