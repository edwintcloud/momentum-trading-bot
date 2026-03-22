package execution

import (
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
)

// TWAPChildOrder represents a single child order in a TWAP execution schedule.
type TWAPChildOrder struct {
	TargetTime time.Time
	Quantity   int64
}

// TWAPSchedule holds the full TWAP execution plan and tracks execution state.
type TWAPSchedule struct {
	Symbol        string
	Side          string
	TotalQuantity int64
	ExecutedQty   int64
	Children      []TWAPChildOrder
	StartTime     time.Time
	NumSlices     int
	WindowSeconds int
}

// ShouldUseTWAP returns true if TWAP execution is enabled. TWAP is used for
// medium-sized orders that don't meet the VWAP threshold.
func ShouldUseTWAP(cfg config.TradingConfig) bool {
	return cfg.TWAPExecutionEnabled
}

// GenerateTWAPSchedule creates a TWAP execution schedule that divides the
// total order into N equal time slices at regular intervals.
func GenerateTWAPSchedule(symbol, side string, totalQty int64, startTime time.Time, cfg config.TradingConfig) *TWAPSchedule {
	numSlices := cfg.TWAPSlices
	if numSlices <= 0 {
		numSlices = 10
	}
	windowSec := cfg.TWAPWindowSeconds
	if windowSec <= 0 {
		windowSec = 300
	}

	// Don't create more slices than shares.
	if int64(numSlices) > totalQty {
		numSlices = int(totalQty)
	}
	if numSlices <= 0 {
		numSlices = 1
	}

	intervalDuration := time.Duration(windowSec/numSlices) * time.Second
	if intervalDuration < time.Second {
		intervalDuration = time.Second
	}

	baseChildSize := totalQty / int64(numSlices)
	extraShares := totalQty - baseChildSize*int64(numSlices)

	children := make([]TWAPChildOrder, numSlices)
	cursor := startTime

	for i := 0; i < numSlices; i++ {
		qty := baseChildSize
		// Distribute remainder across final slices for rounding.
		if int64(numSlices-i) <= extraShares {
			qty++
			extraShares--
		}
		children[i] = TWAPChildOrder{
			TargetTime: cursor,
			Quantity:   qty,
		}
		cursor = cursor.Add(intervalDuration)
	}

	return &TWAPSchedule{
		Symbol:        symbol,
		Side:          side,
		TotalQuantity: totalQty,
		StartTime:     startTime,
		NumSlices:     numSlices,
		WindowSeconds: windowSec,
		Children:      children,
	}
}

// RemainingQty returns the quantity still to be executed.
func (s *TWAPSchedule) RemainingQty() int64 {
	return s.TotalQuantity - s.ExecutedQty
}

// RecordFill updates the schedule with a completed fill.
func (s *TWAPSchedule) RecordFill(qty int64) {
	s.ExecutedQty += qty
	if s.ExecutedQty > s.TotalQuantity {
		s.ExecutedQty = s.TotalQuantity
	}
}

// DueChildren returns child orders whose target time has passed and haven't
// been filled yet based on current executed quantity.
func (s *TWAPSchedule) DueChildren(now time.Time) []TWAPChildOrder {
	var cumTarget int64
	for _, c := range s.Children {
		if now.Before(c.TargetTime) {
			break
		}
		cumTarget += c.Quantity
	}

	needed := cumTarget - s.ExecutedQty
	if needed <= 0 {
		return nil
	}
	if needed > s.RemainingQty() {
		needed = s.RemainingQty()
	}
	return []TWAPChildOrder{
		{TargetTime: now, Quantity: needed},
	}
}
