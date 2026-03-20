package scanner

import (
	"math"
	"testing"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

func testScannerConfig() config.TradingConfig {
	cfg := config.DefaultTradingConfig()
	cfg.MinRelativeVolume = 6.0
	cfg.MinGapPercent = 10.0
	cfg.MinPremarketVolume = 500_000
	cfg.MaxPrice = 40.0
	return cfg
}

func TestScannerFiltersMomentumCandidates(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(testScannerConfig(), runtimeState)

	engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           4.00,
		Open:            3.85,
		HighOfDay:       4.01,
		GapPercent:      17.8,
		RelativeVolume:  15.9,
		PreMarketVolume: 780_000,
		Volume:          100_000,
		VolumeSpike:     true,
		Timestamp:       time.Now().UTC().Add(-3 * time.Minute),
	})
	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           4.15,
		Open:            3.85,
		HighOfDay:       4.16,
		GapPercent:      18.4,
		RelativeVolume:  16.3,
		PreMarketVolume: 800_000,
		Volume:          260_000,
		VolumeSpike:     true,
		Catalyst:        "FDA update",
		Timestamp:       time.Now().UTC(),
	})
	if !ok {
		t.Fatal("expected tick to pass scanner filters")
	}
	if candidate.Symbol != "APVO" {
		t.Fatalf("unexpected symbol: %s", candidate.Symbol)
	}
	if candidate.OneMinuteReturnPct <= 0 {
		t.Fatalf("expected scanner to compute positive short-term momentum, got %+v", candidate)
	}
	if candidate.VolumeRate <= 1 {
		t.Fatalf("expected scanner to compute accelerating volume, got %+v", candidate)
	}
}

func TestScannerUsesEarlierRejectedTicksForMomentumContext(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(testScannerConfig(), runtimeState)
	base := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)

	_, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           11.24,
		Open:            11.10,
		HighOfDay:       11.30,
		GapPercent:      12.3,
		RelativeVolume:  16.0,
		PreMarketVolume: 200_000,
		Volume:          200_000,
		VolumeSpike:     true,
		Timestamp:       base,
	})
	if ok {
		t.Fatal("expected first tick to be rejected on premarket volume")
	}

	_, ok = engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           11.46,
		Open:            11.10,
		HighOfDay:       11.48,
		GapPercent:      14.5,
		RelativeVolume:  16.4,
		PreMarketVolume: 450_000,
		Volume:          450_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(time.Minute),
	})
	if ok {
		t.Fatal("expected second tick to be rejected on premarket volume")
	}

	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "APVO",
		Price:           11.70,
		Open:            11.10,
		HighOfDay:       11.72,
		GapPercent:      16.9,
		RelativeVolume:  17.1,
		PreMarketVolume: 850_000,
		Volume:          850_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(2 * time.Minute),
	})
	if !ok {
		t.Fatal("expected first qualifying tick to pass scanner filters")
	}
	if candidate.OneMinuteReturnPct <= 0 {
		t.Fatalf("expected first qualifying candidate to retain one-minute context, got %+v", candidate)
	}
	if candidate.VolumeRate <= 1 {
		t.Fatalf("expected first qualifying candidate to retain volume-rate context, got %+v", candidate)
	}
}

func TestScannerAllowsIntradaySqueezeWithoutPremarketGap(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(testScannerConfig(), runtimeState)
	base := time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC)

	engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.45,
		BarOpen:         5.00,
		BarHigh:         5.50,
		BarLow:          4.95,
		Open:            4.90,
		HighOfDay:       5.50,
		GapPercent:      1.0,
		RelativeVolume:  15.6,
		PreMarketVolume: 0,
		Volume:          150_000,
		VolumeSpike:     true,
		Timestamp:       base,
	})
	engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.20,
		BarOpen:         5.05,
		BarHigh:         5.25,
		BarLow:          5.00,
		Open:            4.90,
		HighOfDay:       5.25,
		GapPercent:      1.0,
		RelativeVolume:  16.0,
		PreMarketVolume: 0,
		Volume:          250_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(time.Minute),
	})
	engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.32,
		BarOpen:         5.18,
		BarHigh:         5.35,
		BarLow:          5.18,
		Open:            4.90,
		HighOfDay:       5.35,
		GapPercent:      1.0,
		RelativeVolume:  16.0,
		PreMarketVolume: 0,
		Volume:          400_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(2 * time.Minute),
	})
	engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.28,
		BarOpen:         5.30,
		BarHigh:         5.34,
		BarLow:          5.24,
		Open:            4.90,
		HighOfDay:       5.35,
		GapPercent:      1.4,
		RelativeVolume:  16.6,
		PreMarketVolume: 0,
		Volume:          500_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(3 * time.Minute),
	})
	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "SQUEEZE",
		Price:           5.58,
		BarOpen:         5.30,
		BarHigh:         5.60,
		BarLow:          5.26,
		Open:            4.90,
		HighOfDay:       5.60,
		GapPercent:      1.8,
		RelativeVolume:  17.2,
		PreMarketVolume: 0,
		Volume:          820_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(4 * time.Minute),
	})
	if !ok {
		t.Fatal("expected intraday squeeze to pass scanner without gap profile")
	}
	if candidate.GapPercent >= testScannerConfig().MinGapPercent {
		t.Fatalf("expected test candidate to validate non-gap path, got %+v", candidate)
	}
}

func TestScannerEmitsParabolicFailedReclaimShort(t *testing.T) {
	cfg := testScannerConfig()
	cfg.EnableShorts = true
	cfg.ShortPeakExtensionMinPct = 12
	cfg.ShortVWAPBreakMinPct = -0.75
	runtimeState := runtime.NewState()
	engine := NewScanner(cfg, runtimeState)
	base := time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC)

	ticks := []domain.Tick{
		{Symbol: "GOAI", Price: 12.90, BarOpen: 12.00, BarHigh: 13.10, BarLow: 11.95, Open: 12.00, HighOfDay: 13.10, GapPercent: 18.0, RelativeVolume: 12.0, PreMarketVolume: 750_000, Volume: 200_000, VolumeSpike: true, Timestamp: base},
		{Symbol: "GOAI", Price: 14.60, BarOpen: 12.90, BarHigh: 14.80, BarLow: 12.80, Open: 12.00, HighOfDay: 14.80, GapPercent: 21.0, RelativeVolume: 13.5, PreMarketVolume: 780_000, Volume: 500_000, VolumeSpike: true, Timestamp: base.Add(time.Minute)},
		{Symbol: "GOAI", Price: 14.10, BarOpen: 14.60, BarHigh: 15.60, BarLow: 14.00, Open: 12.00, HighOfDay: 15.60, GapPercent: 21.0, RelativeVolume: 14.2, PreMarketVolume: 800_000, Volume: 850_000, VolumeSpike: true, Timestamp: base.Add(2 * time.Minute)},
		{Symbol: "GOAI", Price: 13.88, BarOpen: 14.10, BarHigh: 14.20, BarLow: 13.70, Open: 12.00, HighOfDay: 15.60, GapPercent: 21.0, RelativeVolume: 14.6, PreMarketVolume: 820_000, Volume: 1_000_000, VolumeSpike: true, Timestamp: base.Add(3 * time.Minute)},
		{Symbol: "GOAI", Price: 14.20, BarOpen: 13.88, BarHigh: 14.45, BarLow: 13.80, Open: 12.00, HighOfDay: 15.60, GapPercent: 21.0, RelativeVolume: 14.8, PreMarketVolume: 830_000, Volume: 1_150_000, VolumeSpike: true, Timestamp: base.Add(4 * time.Minute)},
		{Symbol: "GOAI", Price: 13.62, BarOpen: 14.20, BarHigh: 14.00, BarLow: 13.50, Open: 12.00, HighOfDay: 15.60, GapPercent: 21.0, RelativeVolume: 15.0, PreMarketVolume: 835_000, Volume: 1_250_000, VolumeSpike: true, Timestamp: base.Add(5 * time.Minute)},
	}
	for _, tick := range ticks {
		engine.evaluateTick(tick)
	}

	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "GOAI",
		Price:           12.72,
		BarOpen:         14.20,
		BarHigh:         14.25,
		BarLow:          12.50,
		Open:            12.00,
		HighOfDay:       15.60,
		GapPercent:      21.0,
		RelativeVolume:  15.4,
		PreMarketVolume: 840_000,
		Volume:          1_450_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(6 * time.Minute),
	})
	if !ok {
		t.Fatal("expected short breakdown candidate to pass scanner")
	}
	if candidate.Direction != domain.DirectionShort || candidate.SetupType != "parabolic-failed-reclaim-short" {
		t.Fatalf("expected short setup candidate, got %+v", candidate)
	}
	if candidate.BreakoutPct >= 0 || candidate.PriceVsVWAPPct >= 0 {
		t.Fatalf("expected bearish breakdown metrics, got %+v", candidate)
	}
}

func TestScannerTracksCurrentVolumeLeader(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(testScannerConfig(), runtimeState)
	base := time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)

	engine.evaluateTick(domain.Tick{
		Symbol:          "LEADER",
		Price:           6.10,
		Open:            5.80,
		HighOfDay:       6.12,
		GapPercent:      11.0,
		RelativeVolume:  18.0,
		PreMarketVolume: 900_000,
		Volume:          1_000_000,
		VolumeSpike:     true,
		Timestamp:       base,
	})
	candidate, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "FOLLOWER",
		Price:           4.80,
		Open:            4.30,
		HighOfDay:       4.82,
		GapPercent:      10.5,
		RelativeVolume:  17.5,
		PreMarketVolume: 850_000,
		Volume:          500_000,
		VolumeSpike:     true,
		Timestamp:       base.Add(time.Minute),
	})
	if !ok {
		t.Fatal("expected follower candidate to pass scanner")
	}
	if math.Abs(candidate.VolumeLeaderPct-0.38) > 0.01 {
		t.Fatalf("expected follower to carry 0.38 leader share, got %.2f", candidate.VolumeLeaderPct)
	}
	if candidate.LeaderRank != 2 {
		t.Fatalf("expected follower to rank second among momentum leaders, got %d", candidate.LeaderRank)
	}
}

func TestScannerRejectsLowGap(t *testing.T) {
	runtimeState := runtime.NewState()
	engine := NewScanner(testScannerConfig(), runtimeState)

	_, ok := engine.evaluateTick(domain.Tick{
		Symbol:          "LCID",
		Price:           2.95,
		HighOfDay:       2.98,
		GapPercent:      3.0,
		RelativeVolume:  6.0,
		PreMarketVolume: 900_000,
		VolumeSpike:     true,
		Timestamp:       time.Now().UTC(),
	})
	if ok {
		t.Fatal("expected low-gap symbol to be rejected")
	}
}

func TestScannerScoreCapsExtremeRelativeVolume(t *testing.T) {
	cfg := testScannerConfig()
	runtimeState := runtime.NewState()
	engine := NewScanner(cfg, runtimeState)

	base := engine.momentumScore(domain.Tick{
		GapPercent:     12,
		RelativeVolume: cfg.MinRelativeVolume + 15,
	}, 18, 0.5, 0.9, 1, scanMetrics{
		oneMinuteReturn:   1.2,
		threeMinuteReturn: 2.0,
		volumeRate:        1.8,
	})
	extreme := engine.momentumScore(domain.Tick{
		GapPercent:     12,
		RelativeVolume: 4_000,
	}, 18, 0.5, 0.9, 1, scanMetrics{
		oneMinuteReturn:   1.2,
		threeMinuteReturn: 2.0,
		volumeRate:        1.8,
	})

	if base != extreme {
		t.Fatalf("expected extreme relative volume to cap at the same score contribution, base=%.2f extreme=%.2f", base, extreme)
	}
}

func TestScannerExcludesBenchmarkSymbolsFromCandidates(t *testing.T) {
	cfg := testScannerConfig()
	cfg.EnableMarketRegime = true
	runtimeState := runtime.NewState()
	engine := NewScanner(cfg, runtimeState)

	_, ok, reason := engine.EvaluateTickDetailed(domain.Tick{
		Symbol:          "SPY",
		Price:           510.25,
		BarOpen:         509.80,
		BarHigh:         510.40,
		BarLow:          509.70,
		Open:            509.50,
		HighOfDay:       510.40,
		GapPercent:      1.2,
		RelativeVolume:  8.0,
		PreMarketVolume: 0,
		Volume:          2_000_000,
		VolumeSpike:     true,
		Timestamp:       time.Now().UTC(),
	})
	if ok {
		t.Fatal("expected benchmark symbol to be excluded from scanner candidates")
	}
	if reason != "market-benchmark" {
		t.Fatalf("expected benchmark reject reason, got %s", reason)
	}
}

func TestScannerEmitsLowerHighBreakdownShort(t *testing.T) {
	cfg := testScannerConfig()
	cfg.EnableShorts = true
	cfg.EnableMarketRegime = true
	cfg.ShortVWAPBreakMinPct = -0.05
	base := time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC)

	bars := []symbolBar{
		{timestamp: base, open: 10.00, high: 10.20, low: 9.95, close: 10.10, volume: 300_000, cumulativeVolume: 300_000, vwap: 10.08},
		{timestamp: base.Add(time.Minute), open: 10.10, high: 10.60, low: 10.05, close: 10.55, volume: 400_000, cumulativeVolume: 700_000, vwap: 10.35},
		{timestamp: base.Add(2 * time.Minute), open: 10.55, high: 10.90, low: 10.50, close: 10.80, volume: 500_000, cumulativeVolume: 1_200_000, vwap: 10.56},
		{timestamp: base.Add(3 * time.Minute), open: 10.80, high: 10.85, low: 10.10, close: 10.18, volume: 150_000, cumulativeVolume: 1_350_000, vwap: 10.51},
		{timestamp: base.Add(4 * time.Minute), open: 10.18, high: 10.20, low: 10.00, close: 10.05, volume: 150_000, cumulativeVolume: 1_500_000, vwap: 10.47},
		{timestamp: base.Add(5 * time.Minute), open: 10.05, high: 10.28, low: 10.02, close: 10.22, volume: 140_000, cumulativeVolume: 1_640_000, vwap: 10.45},
		{timestamp: base.Add(6 * time.Minute), open: 10.22, high: 10.05, low: 9.90, close: 9.92, volume: 560_000, cumulativeVolume: 2_200_000, vwap: 10.30},
	}

	metrics := deriveMetrics(bars, cfg)
	if metrics.setupType != "lower-high-breakdown-short" {
		t.Fatalf("expected lower-high setup metrics, got %+v", metrics)
	}

	engine := NewScanner(cfg, runtime.NewState())
	current := bars[len(bars)-1]
	tick := domain.Tick{
		Symbol:         "WEAK",
		Price:          current.close,
		BarOpen:        current.open,
		BarHigh:        current.high,
		BarLow:         current.low,
		Open:           10.00,
		HighOfDay:      10.90,
		GapPercent:     4.0,
		RelativeVolume: 12.0,
		Volume:         current.cumulativeVolume,
		VolumeSpike:    true,
		Timestamp:      current.timestamp,
	}
	if !engine.qualifiesShortMomentumProfile(tick, percentChange(tick.Open, tick.Price), metrics) {
		t.Fatalf("expected lower-high setup to satisfy short momentum profile, metrics=%+v", metrics)
	}
}
