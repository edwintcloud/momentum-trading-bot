package risk

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/portfolio"
	"github.com/edwintcloud/momentum-trading-bot/internal/runtime"
)

type stubBorrowChecker struct {
	allowed bool
}

func (s stubBorrowChecker) IsEasyToBorrow(string) bool {
	return s.allowed
}

func TestEvaluateBlocksShortWhenNotEasyToBorrow(t *testing.T) {
	cfg := testTradingConfig()
	book := portfolio.NewManager(cfg)
	book.SetBrokerEquity(cfg.StartingCapital)
	engine := NewEngine(cfg, book, runtime.NewState(), stubBorrowChecker{allowed: false})

	_, approved, reason := engine.Evaluate(testShortSignal())
	if approved {
		t.Fatalf("expected short signal to be blocked")
	}
	if reason != "not-easy-to-borrow" {
		t.Fatalf("expected reason not-easy-to-borrow, got %q", reason)
	}
}

func TestEvaluateApprovesShortWhenEasyToBorrow(t *testing.T) {
	cfg := testTradingConfig()
	book := portfolio.NewManager(cfg)
	book.SetBrokerEquity(cfg.StartingCapital)
	engine := NewEngine(cfg, book, runtime.NewState(), stubBorrowChecker{allowed: true})

	order, approved, reason := engine.Evaluate(testShortSignal())
	if !approved {
		t.Fatalf("expected short signal to be approved, got reason %q", reason)
	}
	if order.PositionSide != domain.DirectionShort {
		t.Fatalf("expected short position side, got %q", order.PositionSide)
	}
}

func testShortSignal() domain.TradeSignal {
	return domain.TradeSignal{
		Symbol:       "AAPL",
		Side:         domain.SideSell,
		Intent:       domain.IntentOpen,
		PositionSide: domain.DirectionShort,
		Price:        25,
		Quantity:     100,
		Timestamp:    time.Date(2026, time.March, 27, 10, 0, 0, 0, markethours.Location()),
	}
}

func testTradingConfig() config.TradingConfig {
	cfg := config.DefaultTradingConfig()
	cfg.MaxTradesPerDay = 10
	cfg.MaxOpenPositions = 5
	cfg.MaxExposurePct = 1.0
	cfg.MaxShortOpenPositions = 3
	cfg.MaxShortExposurePct = 1.0
	cfg.MaxEntriesPerMinute = 5
	cfg.DailyLossLimitPct = 1.0
	return cfg
}
