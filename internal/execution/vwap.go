package execution

import (
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
	"github.com/edwintcloud/momentum-trading-bot/internal/volumeprofile"
)

// VWAPChildOrder represents a single child order in a VWAP execution schedule.
type VWAPChildOrder struct {
	TargetTime time.Time
	Quantity   int64
}

// VWAPSchedule holds the full VWAP execution plan and tracks execution state.
type VWAPSchedule struct {
	Symbol          string
	Side            string
	TotalQuantity   int64
	ExecutedQty     int64
	Children        []VWAPChildOrder
	StartTime       time.Time
	ADV             float64
	MinOrderADVPct  float64
}

// ShouldUseVWAP returns true if the order is large enough to warrant VWAP execution.
func ShouldUseVWAP(orderShares int64, adv float64, cfg config.TradingConfig) bool {
	if !cfg.VWAPExecutionEnabled || adv <= 0 {
		return false
	}
	return float64(orderShares)/adv >= cfg.VWAPMinOrderADVPct
}

// GenerateVWAPSchedule creates a VWAP execution schedule that distributes
// the order across remaining market minutes proportional to the historical
// volume profile (HVP).
func GenerateVWAPSchedule(symbol, side string, totalQty int64, startTime time.Time, adv float64, cfg config.TradingConfig) *VWAPSchedule {
	loc := markethours.Location()
	start := startTime.In(loc)
	close := markethours.MarketClose(start)

	// If market already closed or past, return single immediate child.
	if !start.Before(close) {
		return &VWAPSchedule{
			Symbol:        symbol,
			Side:          side,
			TotalQuantity: totalQty,
			StartTime:     startTime,
			ADV:           adv,
			MinOrderADVPct: cfg.VWAPMinOrderADVPct,
			Children: []VWAPChildOrder{
				{TargetTime: startTime, Quantity: totalQty},
			},
		}
	}

	// Build 1-minute interval schedule from start to close.
	// HVP[t] = cumulative fraction at time t.
	// Target quantity at t = totalQty * (HVP[t] - HVP[start]) / (HVP[close] - HVP[start])
	startFrac := volumeprofile.ExpectedCumulativeShare(start)
	closeFrac := volumeprofile.ExpectedCumulativeShare(close)
	fracRange := closeFrac - startFrac
	if fracRange <= 0 {
		return &VWAPSchedule{
			Symbol:        symbol,
			Side:          side,
			TotalQuantity: totalQty,
			StartTime:     startTime,
			ADV:           adv,
			MinOrderADVPct: cfg.VWAPMinOrderADVPct,
			Children: []VWAPChildOrder{
				{TargetTime: startTime, Quantity: totalQty},
			},
		}
	}

	var children []VWAPChildOrder
	var allocated int64

	// Generate child orders at 1-minute intervals.
	cursor := start.Truncate(time.Minute).Add(time.Minute)
	for cursor.Before(close) || cursor.Equal(close) {
		cursorFrac := volumeprofile.ExpectedCumulativeShare(cursor)
		targetCumQty := int64(float64(totalQty) * (cursorFrac - startFrac) / fracRange)
		if targetCumQty > totalQty {
			targetCumQty = totalQty
		}

		childQty := targetCumQty - allocated
		if childQty > 0 {
			children = append(children, VWAPChildOrder{
				TargetTime: cursor,
				Quantity:   childQty,
			})
			allocated += childQty
		}
		cursor = cursor.Add(time.Minute)
	}

	// If rounding left unallocated shares, add to last child or create one.
	if allocated < totalQty {
		remainder := totalQty - allocated
		if len(children) > 0 {
			children[len(children)-1].Quantity += remainder
		} else {
			children = append(children, VWAPChildOrder{
				TargetTime: startTime,
				Quantity:   remainder,
			})
		}
	}

	return &VWAPSchedule{
		Symbol:        symbol,
		Side:          side,
		TotalQuantity: totalQty,
		StartTime:     startTime,
		ADV:           adv,
		MinOrderADVPct: cfg.VWAPMinOrderADVPct,
		Children:      children,
	}
}

// RemainingQty returns the quantity still to be executed.
func (s *VWAPSchedule) RemainingQty() int64 {
	return s.TotalQuantity - s.ExecutedQty
}

// RecordFill updates the schedule with a completed fill.
func (s *VWAPSchedule) RecordFill(qty int64) {
	s.ExecutedQty += qty
	if s.ExecutedQty > s.TotalQuantity {
		s.ExecutedQty = s.TotalQuantity
	}
}

// ScheduleAdherence returns how well execution tracks the target schedule.
// 1.0 = perfectly on schedule, <1.0 = behind, >1.0 = ahead.
func (s *VWAPSchedule) ScheduleAdherence(now time.Time) float64 {
	if s.TotalQuantity <= 0 || len(s.Children) == 0 {
		return 1.0
	}

	// Compute what the target executed qty should be by now.
	var targetQty int64
	for _, c := range s.Children {
		if !now.Before(c.TargetTime) {
			targetQty += c.Quantity
		}
	}
	if targetQty <= 0 {
		return 1.0
	}
	return float64(s.ExecutedQty) / float64(targetQty)
}

// DueChildren returns child orders whose target time has passed but haven't
// been accounted for yet, given the current executed quantity.
func (s *VWAPSchedule) DueChildren(now time.Time) []VWAPChildOrder {
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
	return []VWAPChildOrder{
		{TargetTime: now, Quantity: needed},
	}
}
