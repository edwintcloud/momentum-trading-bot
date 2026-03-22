package execution

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func TestShouldUseTWAP_Disabled(t *testing.T) {
	cfg := config.TradingConfig{TWAPExecutionEnabled: false}
	if ShouldUseTWAP(cfg) {
		t.Error("TWAP should not be used when disabled")
	}
}

func TestShouldUseTWAP_Enabled(t *testing.T) {
	cfg := config.TradingConfig{TWAPExecutionEnabled: true}
	if !ShouldUseTWAP(cfg) {
		t.Error("TWAP should be used when enabled")
	}
}

func TestGenerateTWAPSchedule_EqualSlices(t *testing.T) {
	loc := markethours.Location()
	startTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		TWAPExecutionEnabled: true,
		TWAPSlices:           5,
		TWAPWindowSeconds:    300,
	}

	schedule := GenerateTWAPSchedule("AAPL", "buy", 1000, startTime, cfg)

	if len(schedule.Children) != 5 {
		t.Fatalf("expected 5 children, got %d", len(schedule.Children))
	}

	// Each slice should be 200 shares.
	for i, c := range schedule.Children {
		if c.Quantity != 200 {
			t.Errorf("child %d quantity = %d, want 200", i, c.Quantity)
		}
	}
}

func TestGenerateTWAPSchedule_RoundingHandled(t *testing.T) {
	loc := markethours.Location()
	startTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		TWAPExecutionEnabled: true,
		TWAPSlices:           3,
		TWAPWindowSeconds:    300,
	}

	// 10 shares / 3 slices = 3 + 3 + 4
	schedule := GenerateTWAPSchedule("AAPL", "buy", 10, startTime, cfg)

	var total int64
	for _, c := range schedule.Children {
		total += c.Quantity
	}
	if total != 10 {
		t.Errorf("sum of child quantities = %d, want 10", total)
	}
}

func TestGenerateTWAPSchedule_Intervals(t *testing.T) {
	loc := markethours.Location()
	startTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		TWAPExecutionEnabled: true,
		TWAPSlices:           4,
		TWAPWindowSeconds:    200, // 50 seconds per slice
	}

	schedule := GenerateTWAPSchedule("AAPL", "buy", 400, startTime, cfg)

	if len(schedule.Children) != 4 {
		t.Fatalf("expected 4 children, got %d", len(schedule.Children))
	}

	expectedInterval := 50 * time.Second
	for i := 1; i < len(schedule.Children); i++ {
		gap := schedule.Children[i].TargetTime.Sub(schedule.Children[i-1].TargetTime)
		if gap != expectedInterval {
			t.Errorf("interval %d = %v, want %v", i, gap, expectedInterval)
		}
	}
}

func TestGenerateTWAPSchedule_MoreSlicesThanShares(t *testing.T) {
	loc := markethours.Location()
	startTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		TWAPExecutionEnabled: true,
		TWAPSlices:           10,
		TWAPWindowSeconds:    300,
	}

	// Only 3 shares — should clamp slices to 3.
	schedule := GenerateTWAPSchedule("AAPL", "buy", 3, startTime, cfg)

	if len(schedule.Children) != 3 {
		t.Fatalf("expected 3 children (clamped), got %d", len(schedule.Children))
	}

	var total int64
	for _, c := range schedule.Children {
		total += c.Quantity
	}
	if total != 3 {
		t.Errorf("sum of child quantities = %d, want 3", total)
	}
}

func TestTWAPSchedule_RecordFill(t *testing.T) {
	schedule := &TWAPSchedule{TotalQuantity: 500}
	schedule.RecordFill(200)
	if schedule.RemainingQty() != 300 {
		t.Errorf("RemainingQty = %d, want 300", schedule.RemainingQty())
	}
}

func TestTWAPSchedule_DueChildren(t *testing.T) {
	loc := markethours.Location()
	t0 := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	schedule := &TWAPSchedule{
		TotalQuantity: 300,
		ExecutedQty:   0,
		Children: []TWAPChildOrder{
			{TargetTime: t0, Quantity: 100},
			{TargetTime: t0.Add(60 * time.Second), Quantity: 100},
			{TargetTime: t0.Add(120 * time.Second), Quantity: 100},
		},
	}

	// After 1 minute, first two children are due.
	due := schedule.DueChildren(t0.Add(61 * time.Second))
	var totalDue int64
	for _, d := range due {
		totalDue += d.Quantity
	}
	if totalDue != 200 {
		t.Errorf("due quantity = %d, want 200", totalDue)
	}
}
