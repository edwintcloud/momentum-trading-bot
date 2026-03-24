package scanner

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

func scannerTestTick(prevDayVolume int64) domain.Tick {
	loc := markethours.Location()
	ts := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	return domain.Tick{
		Symbol:          "TEST",
		Price:           10.0,
		BarOpen:         9.5,
		BarHigh:         10.5,
		BarLow:          9.0,
		Open:            9.0,
		HighOfDay:       10.5,
		Volume:          500000,
		RelativeVolume:  5.0,
		GapPercent:      10.0,
		PreMarketVolume: 100000,
		Float:           5000000,
		PrevDayVolume:   prevDayVolume,
		Timestamp:       ts,
	}
}

func TestMinPrevDayVolumeFilter_Rejected(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinPrevDayVolume = 500000
	cfg.MinEntryScore = 0 // lower bar so we can isolate volume filter

	rt := runtime.NewState()
	s := NewScanner(cfg, rt)

	tick := scannerTestTick(100000) // below minimum
	_, ok := s.Evaluate(tick)
	if ok {
		t.Error("expected tick with low prevDayVolume to be rejected")
	}
}

func TestMinPrevDayVolumeFilter_Passes(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinPrevDayVolume = 500000
	cfg.MinEntryScore = 0

	rt := runtime.NewState()
	s := NewScanner(cfg, rt)

	tick := scannerTestTick(1000000) // above minimum
	_, ok := s.Evaluate(tick)
	if !ok {
		t.Error("expected tick with sufficient prevDayVolume to pass")
	}
}

func TestMinPrevDayVolumeFilter_UnknownVolumePasses(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinPrevDayVolume = 500000
	cfg.MinEntryScore = 0

	rt := runtime.NewState()
	s := NewScanner(cfg, rt)

	tick := scannerTestTick(0) // unknown volume — should not block
	_, ok := s.Evaluate(tick)
	if !ok {
		t.Error("expected tick with unknown prevDayVolume (0) to pass")
	}
}

func TestMinPrevDayVolumeFilter_DisabledPasses(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinPrevDayVolume = 0 // disabled
	cfg.MinEntryScore = 0

	rt := runtime.NewState()
	s := NewScanner(cfg, rt)

	tick := scannerTestTick(100) // very low volume, but filter disabled
	_, ok := s.Evaluate(tick)
	if !ok {
		t.Error("expected tick to pass when MinPrevDayVolume is disabled")
	}
}

func TestClassifyTickRejection_DailyVolume(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinPrevDayVolume = 500000

	tick := scannerTestTick(100000)
	reason := classifyTickRejection(tick, cfg)
	if reason != "daily-volume" {
		t.Errorf("expected rejection reason 'daily-volume', got %q", reason)
	}
}

func TestClassifyTickRejection_DailyVolumeUnknown(t *testing.T) {
	cfg := config.DefaultTradingConfig()
	cfg.MinPrevDayVolume = 500000

	tick := scannerTestTick(0) // unknown
	reason := classifyTickRejection(tick, cfg)
	// Should NOT be daily-volume since volume is unknown
	if reason == "daily-volume" {
		t.Error("expected unknown volume to not be rejected as daily-volume")
	}
}
