package strategy

import (
	"math"
	"testing"
)

func TestKellyFraction(t *testing.T) {
	tests := []struct {
		name       string
		winRate    float64
		wlRatio    float64
		wantKelly  float64
		tolerance  float64
	}{
		{
			name:      "55% win rate, 1.5:1 ratio",
			winRate:   0.55,
			wlRatio:   1.5,
			wantKelly: 0.25, // (1.5*0.55 - 0.45) / 1.5 = (0.825 - 0.45) / 1.5 = 0.25
			tolerance: 0.01,
		},
		{
			name:      "50% win rate, 2:1 ratio",
			winRate:   0.50,
			wlRatio:   2.0,
			wantKelly: 0.25, // (2*0.5 - 0.5) / 2 = 0.5 / 2 = 0.25
			tolerance: 0.01,
		},
		{
			name:      "60% win rate, 1:1 ratio",
			winRate:   0.60,
			wlRatio:   1.0,
			wantKelly: 0.20, // (1*0.6 - 0.4) / 1 = 0.2
			tolerance: 0.01,
		},
		{
			name:      "losing system (30% win, 1:1)",
			winRate:   0.30,
			wlRatio:   1.0,
			wantKelly: 0.0, // negative Kelly -> 0
			tolerance: 0.01,
		},
		{
			name:      "zero win rate",
			winRate:   0.0,
			wlRatio:   1.0,
			wantKelly: 0.0,
			tolerance: 0.01,
		},
		{
			name:      "zero ratio",
			winRate:   0.5,
			wlRatio:   0.0,
			wantKelly: 0.0,
			tolerance: 0.01,
		},
		{
			name:      "100% win rate (invalid)",
			winRate:   1.0,
			wlRatio:   2.0,
			wantKelly: 0.0,
			tolerance: 0.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KellyFraction(tt.winRate, tt.wlRatio)
			if math.Abs(got-tt.wantKelly) > tt.tolerance {
				t.Errorf("KellyFraction(%.2f, %.2f) = %.4f, want %.4f", tt.winRate, tt.wlRatio, got, tt.wantKelly)
			}
		})
	}
}

func TestKellyFractionSpecExample(t *testing.T) {
	// From the spec: 55% win rate, 1.5:1 ratio → f* = 0.25
	// Actually: f* = (1.5*0.55 - 0.45) / 1.5 = 0.375/1.5 = 0.25
	kelly := KellyFraction(0.55, 1.5)
	if math.Abs(kelly-0.25) > 0.01 {
		t.Errorf("spec example: KellyFraction(0.55, 1.5) = %.4f, want 0.25", kelly)
	}
}
