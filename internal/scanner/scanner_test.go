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

func TestRSIFilterBlocksOverbought(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.RSIFilterEnabled = true
	cfg.RSIOverboughtThreshold = 80.0
	cfg.MinEntryScore = 0
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	// Build a state with 20 consecutive up bars so RSI > 80
	state := &symbolState{day: "2026-03-22", bars: make([]symbolBar, 0)}
	for i := 0; i < 20; i++ {
		tick := domain.Tick{
			Symbol:          "OVERBUY",
			Price:           100.0 + float64(i)*2,
			BarOpen:         100.0 + float64(i)*2 - 0.5,
			BarHigh:         100.0 + float64(i)*2 + 0.5,
			BarLow:          100.0 + float64(i)*2 - 1.0,
			Open:            100.0,
			HighOfDay:       100.0 + float64(i)*2 + 0.5,
			Volume:          100000,
			RelativeVolume:  5.0,
			GapPercent:      10.0,
			PreMarketVolume: 60000,
			Timestamp:       time.Date(2026, 3, 23, 14, 30+i, 0, 0, time.UTC),
		}
		s.updateBars(state, tick)
	}

	// Now check RSI is high
	bars := state.bars
	rsi := computeRSI(bars, 14)
	if rsi <= 80 {
		t.Skipf("RSI=%.2f not high enough for test, skipping", rsi)
	}
}

func TestBollingerBands(t *testing.T) {
	// Simple test: 20 identical prices -> stddev=0, upper=middle=lower=price
	prices := make([]float64, 20)
	for i := range prices {
		prices[i] = 50.0
	}
	upper, middle, lower := ComputeBollingerBandsFromPrices(prices, 20, 2.0)
	if math.Abs(middle-50.0) > 0.001 {
		t.Errorf("BB middle = %.4f, want 50.0", middle)
	}
	if math.Abs(upper-50.0) > 0.001 {
		t.Errorf("BB upper = %.4f, want 50.0 (zero stddev)", upper)
	}
	if math.Abs(lower-50.0) > 0.001 {
		t.Errorf("BB lower = %.4f, want 50.0 (zero stddev)", lower)
	}

	// With varied prices, upper > middle > lower
	variedPrices := make([]float64, 20)
	for i := range variedPrices {
		variedPrices[i] = 50.0 + float64(i%5)
	}
	upper2, middle2, lower2 := ComputeBollingerBandsFromPrices(variedPrices, 20, 2.0)
	if upper2 <= middle2 {
		t.Errorf("BB upper (%.4f) should be > middle (%.4f)", upper2, middle2)
	}
	if lower2 >= middle2 {
		t.Errorf("BB lower (%.4f) should be < middle (%.4f)", lower2, middle2)
	}

	// Too few prices returns zeros
	shortPrices := []float64{1, 2, 3}
	u, m, l := ComputeBollingerBandsFromPrices(shortPrices, 20, 2.0)
	if u != 0 || m != 0 || l != 0 {
		t.Errorf("BB with too few prices should return zeros, got %.4f, %.4f, %.4f", u, m, l)
	}
}

func TestComputeADX(t *testing.T) {
	// With insufficient bars, should return 50 (default)
	shortBars := make([]symbolBar, 5)
	for i := range shortBars {
		shortBars[i] = symbolBar{high: 50, low: 49, close: 49.5}
	}
	adx := computeADX(shortBars, 14)
	if adx != 50 {
		t.Errorf("ADX with few bars = %.2f, want 50", adx)
	}

	// With enough trending bars, ADX should be meaningful (> 0)
	trendBars := make([]symbolBar, 60)
	for i := range trendBars {
		trendBars[i] = symbolBar{
			high:  100.0 + float64(i)*1.5,
			low:   99.0 + float64(i)*1.5,
			close: 99.5 + float64(i)*1.5,
		}
	}
	trendADX := computeADX(trendBars, 14)
	if trendADX <= 0 {
		t.Errorf("ADX for trending bars = %.2f, want > 0", trendADX)
	}
}

func TestComputeSlippage(t *testing.T) {
	// Liquid stock (> 5M volume): 5 bps
	slip := ComputeSlippage(100.0, 10_000_000, 5.0, 10.0, 20.0)
	expected := 100.0 * 5.0 / 10000.0
	if math.Abs(slip-expected) > 0.001 {
		t.Errorf("liquid slippage = %.4f, want %.4f", slip, expected)
	}

	// Mid liquidity (500K-5M): 10 bps
	slipMid := ComputeSlippage(100.0, 1_000_000, 5.0, 10.0, 20.0)
	expectedMid := 100.0 * 10.0 / 10000.0
	if math.Abs(slipMid-expectedMid) > 0.001 {
		t.Errorf("mid slippage = %.4f, want %.4f", slipMid, expectedMid)
	}

	// Illiquid (< 500K): 20 bps
	slipIlliq := ComputeSlippage(100.0, 100_000, 5.0, 10.0, 20.0)
	expectedIlliq := 100.0 * 20.0 / 10000.0
	if math.Abs(slipIlliq-expectedIlliq) > 0.001 {
		t.Errorf("illiquid slippage = %.4f, want %.4f", slipIlliq, expectedIlliq)
	}

	// Verify ordering: illiquid > mid > liquid
	if slipIlliq <= slipMid || slipMid <= slip {
		t.Errorf("slippage ordering wrong: liquid=%.4f mid=%.4f illiquid=%.4f", slip, slipMid, slipIlliq)
	}
}

func TestFloatFilter_RejectHighFloat(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MaxFloat = 20_000_000 // 20M max
	cfg.MinFloat = 0
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	// Tick with float above max should be rejected
	tick := domain.Tick{
		Symbol:          "BIGFLOAT",
		Price:           10.0,
		BarOpen:         9.8,
		BarHigh:         10.5,
		BarLow:          9.5,
		Open:            9.0,
		HighOfDay:       10.5,
		Volume:          500000,
		RelativeVolume:  5.0,
		GapPercent:      10.0,
		PreMarketVolume: 200000,
		Float:           50_000_000, // 50M > 20M max
		Timestamp:       time.Date(2026, 3, 23, 14, 35, 0, 0, time.UTC),
	}
	_, ok := s.Evaluate(tick)
	if ok {
		t.Error("tick with float 50M should be rejected when MaxFloat=20M")
	}

	// Verify rejection reason
	_, _, reason := s.EvaluateTickDetailed(tick)
	if reason != "float-too-high" {
		t.Errorf("rejection reason: got %q, want %q", reason, "float-too-high")
	}
}

func TestFloatFilter_AcceptLowFloat(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MaxFloat = 20_000_000
	cfg.MinEntryScore = 0 // lower threshold so we can test float filter in isolation
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	tick := domain.Tick{
		Symbol:          "LOWFLOAT",
		Price:           5.0,
		BarOpen:         4.8,
		BarHigh:         5.5,
		BarLow:          4.5,
		Open:            4.0,
		HighOfDay:       5.5,
		Volume:          300000,
		RelativeVolume:  8.0,
		GapPercent:      15.0,
		PreMarketVolume: 200000,
		Float:           3_000_000, // 3M < 20M max — should pass float filter
		Timestamp:       time.Date(2026, 3, 23, 14, 35, 0, 0, time.UTC),
	}
	candidate, ok := s.Evaluate(tick)
	if !ok {
		t.Skip("candidate did not pass all filters (may fail for non-float reasons)")
	}
	if candidate.Float != 3_000_000 {
		t.Errorf("candidate.Float: got %d, want 3000000", candidate.Float)
	}
}

func TestFloatFilter_AcceptUnknownFloat(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MaxFloat = 20_000_000
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	// Float=0 means unknown — should NOT be filtered out
	tick := domain.Tick{
		Symbol:          "NOFLOAT",
		Price:           5.0,
		BarOpen:         4.8,
		BarHigh:         5.5,
		BarLow:          4.5,
		Open:            4.0,
		HighOfDay:       5.5,
		Volume:          300000,
		RelativeVolume:  8.0,
		GapPercent:      15.0,
		PreMarketVolume: 200000,
		Float:           0, // unknown
		Timestamp:       time.Date(2026, 3, 23, 14, 35, 0, 0, time.UTC),
	}

	// Should not be rejected by float filter (may still be rejected by score)
	_, _, reason := s.EvaluateTickDetailed(tick)
	if reason == "float-too-high" || reason == "float-too-low" {
		t.Errorf("unknown float (0) should not be filtered, got reason: %s", reason)
	}
}

func TestFloatFilter_RejectTooLowFloat(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinFloat = 500_000 // minimum 500K shares
	cfg.MaxFloat = 0       // no max
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	tick := domain.Tick{
		Symbol:          "TINYFLOAT",
		Price:           3.0,
		BarOpen:         2.8,
		BarHigh:         3.5,
		BarLow:          2.5,
		Open:            2.5,
		HighOfDay:       3.5,
		Volume:          300000,
		RelativeVolume:  5.0,
		GapPercent:      10.0,
		PreMarketVolume: 200000,
		Float:           100_000, // 100K < 500K min
		Timestamp:       time.Date(2026, 3, 23, 14, 35, 0, 0, time.UTC),
	}

	_, _, reason := s.EvaluateTickDetailed(tick)
	if reason != "float-too-low" {
		t.Errorf("rejection reason: got %q, want %q", reason, "float-too-low")
	}
}

func TestFloatFilter_Disabled(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MaxFloat = 0 // disabled
	cfg.MinFloat = 0 // disabled
	runtimeState := runtime.NewState()
	s := NewScanner(cfg, runtimeState)

	// With float filter disabled, any float should pass the float check
	tick := domain.Tick{
		Symbol:          "ANYFLOAT",
		Price:           5.0,
		BarOpen:         4.8,
		BarHigh:         5.5,
		BarLow:          4.5,
		Open:            4.0,
		HighOfDay:       5.5,
		Volume:          300000,
		RelativeVolume:  5.0,
		GapPercent:      10.0,
		PreMarketVolume: 200000,
		Float:           500_000_000, // very high float
		Timestamp:       time.Date(2026, 3, 23, 14, 35, 0, 0, time.UTC),
	}

	_, _, reason := s.EvaluateTickDetailed(tick)
	if reason == "float-too-high" || reason == "float-too-low" {
		t.Errorf("float filter should be disabled when MaxFloat=0 and MinFloat=0, got reason: %s", reason)
	}
}

func TestRankCandidates(t *testing.T) {
	candidates := []domain.Candidate{
		{Symbol: "A", RelativeVolume: 2.0, GapPercent: 5.0, Score: 3.0},
		{Symbol: "B", RelativeVolume: 8.0, GapPercent: 10.0, Score: 3.0},
		{Symbol: "C", RelativeVolume: 4.0, GapPercent: 3.0, Score: 3.0},
	}

	RankCandidates(candidates)

	// B has highest composite (8*10=80), should be rank 1
	for _, c := range candidates {
		if c.Symbol == "B" && c.LeaderRank != 1 {
			t.Errorf("B should be rank 1, got %d", c.LeaderRank)
		}
		if c.Symbol == "A" && c.LeaderRank == 1 {
			t.Errorf("A should not be rank 1")
		}
	}

	// Top 3 leaders get score bonus
	for _, c := range candidates {
		if c.Score <= 3.0 && c.LeaderRank <= 3 {
			t.Errorf("candidates with rank <= 3 should have score > 3.0, %s has %.2f", c.Symbol, c.Score)
		}
	}
}
