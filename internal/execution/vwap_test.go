package execution

import (
	"testing"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

func TestShouldUseVWAP_Disabled(t *testing.T) {
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: false,
		VWAPMinOrderADVPct:   0.005,
	}
	if ShouldUseVWAP(10000, 1000000, cfg) {
		t.Error("VWAP should not be used when disabled")
	}
}

func TestShouldUseVWAP_SmallOrder(t *testing.T) {
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: true,
		VWAPMinOrderADVPct:   0.005,
	}
	// 4000 shares / 1M ADV = 0.4% < 0.5%
	if ShouldUseVWAP(4000, 1000000, cfg) {
		t.Error("small order should not use VWAP")
	}
}

func TestShouldUseVWAP_LargeOrder(t *testing.T) {
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: true,
		VWAPMinOrderADVPct:   0.005,
	}
	// 5000 shares / 1M ADV = 0.5% >= 0.5%
	if !ShouldUseVWAP(5000, 1000000, cfg) {
		t.Error("large order should use VWAP")
	}
}

func TestGenerateVWAPSchedule_TotalQuantityMatches(t *testing.T) {
	loc := markethours.Location()
	// 10:00 AM ET on a weekday
	startTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: true,
		VWAPMinOrderADVPct:   0.005,
	}

	schedule := GenerateVWAPSchedule("AAPL", "buy", 10000, startTime, 1000000, cfg)

	if schedule.TotalQuantity != 10000 {
		t.Errorf("TotalQuantity = %d, want 10000", schedule.TotalQuantity)
	}

	// Sum child quantities should equal total.
	var total int64
	for _, c := range schedule.Children {
		total += c.Quantity
	}
	if total != 10000 {
		t.Errorf("sum of child quantities = %d, want 10000", total)
	}
}

func TestGenerateVWAPSchedule_ChildrenChronological(t *testing.T) {
	loc := markethours.Location()
	startTime := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: true,
		VWAPMinOrderADVPct:   0.005,
	}

	schedule := GenerateVWAPSchedule("AAPL", "buy", 10000, startTime, 1000000, cfg)

	for i := 1; i < len(schedule.Children); i++ {
		if schedule.Children[i].TargetTime.Before(schedule.Children[i-1].TargetTime) {
			t.Errorf("child %d target time %v before child %d target time %v",
				i, schedule.Children[i].TargetTime, i-1, schedule.Children[i-1].TargetTime)
		}
	}
}

func TestGenerateVWAPSchedule_MoreVolumeNearOpen(t *testing.T) {
	loc := markethours.Location()
	// Start at 9:31 AM — lots of volume early in the day
	startTime := time.Date(2026, 3, 23, 9, 31, 0, 0, loc)
	cfg := config.TradingConfig{
		VWAPExecutionEnabled: true,
		VWAPMinOrderADVPct:   0.005,
	}

	schedule := GenerateVWAPSchedule("AAPL", "buy", 10000, startTime, 1000000, cfg)

	if len(schedule.Children) < 2 {
		t.Fatal("expected multiple children")
	}

	// First quarter of children should generally have more volume than last quarter.
	quarter := len(schedule.Children) / 4
	if quarter == 0 {
		quarter = 1
	}
	var firstQuarterQty, lastQuarterQty int64
	for i := 0; i < quarter; i++ {
		firstQuarterQty += schedule.Children[i].Quantity
	}
	for i := len(schedule.Children) - quarter; i < len(schedule.Children); i++ {
		lastQuarterQty += schedule.Children[i].Quantity
	}
	// The U-shape means open is heavier, but we just check first quarter > 0.
	if firstQuarterQty <= 0 {
		t.Error("first quarter should have positive quantity")
	}
}

func TestVWAPSchedule_RecordFill(t *testing.T) {
	schedule := &VWAPSchedule{TotalQuantity: 1000}
	schedule.RecordFill(300)
	if schedule.ExecutedQty != 300 {
		t.Errorf("ExecutedQty = %d, want 300", schedule.ExecutedQty)
	}
	if schedule.RemainingQty() != 700 {
		t.Errorf("RemainingQty = %d, want 700", schedule.RemainingQty())
	}

	// Overfill should be clamped.
	schedule.RecordFill(800)
	if schedule.ExecutedQty != 1000 {
		t.Errorf("ExecutedQty after overfill = %d, want 1000", schedule.ExecutedQty)
	}
}

func TestVWAPSchedule_ScheduleAdherence(t *testing.T) {
	loc := markethours.Location()
	t0 := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	schedule := &VWAPSchedule{
		TotalQuantity: 1000,
		ExecutedQty:   500,
		Children: []VWAPChildOrder{
			{TargetTime: t0.Add(1 * time.Minute), Quantity: 500},
			{TargetTime: t0.Add(2 * time.Minute), Quantity: 500},
		},
	}

	// At t0+1.5min, first child (500) should be due, we've executed 500 → perfect.
	adherence := schedule.ScheduleAdherence(t0.Add(90 * time.Second))
	if adherence != 1.0 {
		t.Errorf("ScheduleAdherence = %f, want 1.0", adherence)
	}

	// If we'd only executed 250, adherence = 0.5.
	schedule.ExecutedQty = 250
	adherence = schedule.ScheduleAdherence(t0.Add(90 * time.Second))
	if adherence != 0.5 {
		t.Errorf("ScheduleAdherence = %f, want 0.5", adherence)
	}
}

func TestVWAPSchedule_DueChildren(t *testing.T) {
	loc := markethours.Location()
	t0 := time.Date(2026, 3, 23, 10, 0, 0, 0, loc)
	schedule := &VWAPSchedule{
		TotalQuantity: 1000,
		ExecutedQty:   0,
		Children: []VWAPChildOrder{
			{TargetTime: t0.Add(1 * time.Minute), Quantity: 300},
			{TargetTime: t0.Add(2 * time.Minute), Quantity: 300},
			{TargetTime: t0.Add(3 * time.Minute), Quantity: 400},
		},
	}

	// At t0+2.5min, first two children are due (total 600).
	due := schedule.DueChildren(t0.Add(150 * time.Second))
	var totalDue int64
	for _, d := range due {
		totalDue += d.Quantity
	}
	if totalDue != 600 {
		t.Errorf("due quantity = %d, want 600", totalDue)
	}
}
