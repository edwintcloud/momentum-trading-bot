package scanner

import (
	"math"
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

func TestVWAPCumulativeVolume(t *testing.T) {
	// Feed known bars and verify VWAP = sum(typical_price * volume) / sum(volume)
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	bars := []struct {
		high, low, close float64
		volume           int64
	}{
		{11.0, 9.0, 10.0, 100},
		{12.0, 10.0, 11.0, 200},
		{13.0, 11.0, 12.0, 300},
	}

	var expectedDollarFlow, expectedVolume float64
	for _, b := range bars {
		typical := (b.high + b.low + b.close) / 3
		expectedDollarFlow += typical * float64(b.volume)
		expectedVolume += float64(b.volume)
	}
	expectedVWAP := expectedDollarFlow / expectedVolume

	state := &symbolState{day: "2026-03-22", bars: make([]symbolBar, 0)}
	for _, b := range bars {
		tick := domain.Tick{
			Symbol:    "TEST",
			Price:     b.close,
			BarHigh:   b.high,
			BarLow:    b.low,
			Volume:    b.volume,
			Timestamp: time.Now(),
		}
		s.updateBars(state, tick)
	}

	lastBar := state.bars[len(state.bars)-1]
	if math.Abs(lastBar.vwap-expectedVWAP) > 0.001 {
		t.Errorf("VWAP = %.4f, want %.4f", lastBar.vwap, expectedVWAP)
	}
}

func TestContinuousScoreFunction(t *testing.T) {
	// Below threshold -> 0
	if got := continuousScore(1.0, 2.0, 8.0); got != 0 {
		t.Errorf("continuousScore(1,2,8) = %f, want 0", got)
	}

	// At threshold -> 0
	if got := continuousScore(2.0, 2.0, 8.0); got != 0 {
		t.Errorf("continuousScore(2,2,8) = %f, want 0", got)
	}

	// Midpoint -> 0.5
	if got := continuousScore(5.0, 2.0, 8.0); math.Abs(got-0.5) > 0.001 {
		t.Errorf("continuousScore(5,2,8) = %f, want 0.5", got)
	}

	// At saturation -> 1.0
	if got := continuousScore(8.0, 2.0, 8.0); got != 1.0 {
		t.Errorf("continuousScore(8,2,8) = %f, want 1.0", got)
	}

	// Above saturation -> 1.0 (capped)
	if got := continuousScore(100.0, 2.0, 8.0); got != 1.0 {
		t.Errorf("continuousScore(100,2,8) = %f, want 1.0", got)
	}
}

func TestContinuousScoringProducesGradient(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	// All metrics at zero/below threshold should produce score 0
	zeroMetrics := scanMetrics{}
	zeroTick := domain.Tick{RelativeVolume: 0, GapPercent: 0}
	scoreZero := s.scoreCandidate(zeroTick, zeroMetrics, domain.DirectionLong)
	if scoreZero != 0 {
		t.Errorf("zero metrics score = %f, want 0", scoreZero)
	}

	// Moderate metrics should produce score between 0 and max
	modTick := domain.Tick{
		RelativeVolume: 5.0,
		GapPercent:     8.0,
		Price:          50.0,
	}
	modMetrics := scanMetrics{
		volumeRate:      cfg.MinVolumeRate * 2,
		oneMinuteReturn: cfg.MinOneMinuteReturnPct * 1.5,
		breakoutPct:     1.5,
		vwap:            49.0,
		emaFast:         50.5,
		emaSlow:         50.0,
	}
	scoreMod := s.scoreCandidate(modTick, modMetrics, domain.DirectionLong)
	if scoreMod <= 0 {
		t.Errorf("moderate metrics score = %f, want > 0", scoreMod)
	}

	// All metrics at saturation should produce near-max score
	satTick := domain.Tick{
		RelativeVolume: 10.0,
		GapPercent:     20.0,
		Price:          50.0,
	}
	satMetrics := scanMetrics{
		volumeRate:        cfg.MinVolumeRate * 5,
		oneMinuteReturn:   cfg.MinOneMinuteReturnPct * 5,
		threeMinuteReturn: cfg.MinThreeMinuteReturnPct * 5,
		breakoutPct:       5.0,
		vwap:              48.0,
		emaFast:           52.0,
		emaSlow:           50.0,
		rsiMASlope:        3.0,
	}
	scoreSat := s.scoreCandidate(satTick, satMetrics, domain.DirectionLong)
	if scoreSat <= scoreMod {
		t.Errorf("saturated score (%f) should be > moderate score (%f)", scoreSat, scoreMod)
	}
	// Max possible is ~7.5
	if scoreSat < 6.0 {
		t.Errorf("saturated score = %f, expected >= 6.0", scoreSat)
	}
}

func TestComputeRSI(t *testing.T) {
	// Construct bars with known price series: 14 up bars then check RSI should be high
	bars := make([]symbolBar, 16)
	bars[0] = symbolBar{close: 100.0}
	for i := 1; i < 16; i++ {
		bars[i] = symbolBar{close: 100.0 + float64(i)}
	}
	rsi := computeRSI(bars, 14)
	if rsi <= 70 {
		t.Errorf("RSI for 15 consecutive up bars = %.2f, want > 70", rsi)
	}

	// All down bars should give low RSI
	downBars := make([]symbolBar, 16)
	downBars[0] = symbolBar{close: 200.0}
	for i := 1; i < 16; i++ {
		downBars[i] = symbolBar{close: 200.0 - float64(i)}
	}
	rsiDown := computeRSI(downBars, 14)
	if rsiDown >= 30 {
		t.Errorf("RSI for 15 consecutive down bars = %.2f, want < 30", rsiDown)
	}

	// Too few bars returns neutral
	shortBars := []symbolBar{{close: 10}, {close: 11}}
	rsiShort := computeRSI(shortBars, 14)
	if rsiShort != 50.0 {
		t.Errorf("RSI for 2 bars = %.2f, want 50 (neutral default)", rsiShort)
	}
}
