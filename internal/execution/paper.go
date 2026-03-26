package execution

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/scanner"
)

// PaperBroker implements BrokerClient for backtests and optimizers.
// It simulates order fills using OHLC bar data fed via UpdateBar.
//
// Exit orders (close/partial) fill immediately at the order price.
// Entry orders use OHLC fill logic on subsequent bars (not the bar
// that generated the signal).
type PaperBroker struct {
	mu          sync.Mutex
	nextID      atomic.Int64
	pending     map[string]*paperOrder // orderID → order
	bars        map[string]domain.Bar  // latest bar per symbol
	maxBarsFill int                    // orders expire after this many bars (default 2)
	expiries    int                    // count of expired entry orders
}

type paperOrder struct {
	request      domain.OrderRequest
	barsLeft     int
	fillPrice    float64   // set when filled
	status       string    // "new", "filled", "expired"
	submittedBar time.Time // bar timestamp at submission (entry orders skip this bar)
}

// NewPaperBroker creates a paper broker for simulated execution.
func NewPaperBroker() *PaperBroker {
	return &PaperBroker{
		pending:     make(map[string]*paperOrder),
		bars:        make(map[string]domain.Bar),
		maxBarsFill: 2,
	}
}

// UpdateBar feeds the latest bar for a symbol. Must be called before
// PollOrderStatus so fill decisions use current bar data.
func (pb *PaperBroker) UpdateBar(bar domain.Bar) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	pb.bars[bar.Symbol] = bar
}

// SubmitOrder stores a pending order and returns a synthetic order ID.
// Exit orders (close/partial intent) are pre-filled immediately at the order price.
// Entry orders go through OHLC fill logic on subsequent bars.
func (pb *PaperBroker) SubmitOrder(_ context.Context, order domain.OrderRequest) (string, error) {
	id := fmt.Sprintf("paper-%d", pb.nextID.Add(1))
	pb.mu.Lock()
	defer pb.mu.Unlock()

	// Exit orders fill immediately at the order price (simulates aggressive market order).
	if !domain.IsOpeningIntent(order.Intent) {
		pb.pending[id] = &paperOrder{
			request:   order,
			status:    "filled",
			fillPrice: order.Price,
		}
		return id, nil
	}

	// Entry orders: record current bar so we skip same-bar fills.
	var subBar time.Time
	if bar, ok := pb.bars[order.Symbol]; ok {
		subBar = bar.Timestamp
	}
	pb.pending[id] = &paperOrder{
		request:      order,
		barsLeft:     pb.maxBarsFill,
		status:       "new",
		submittedBar: subBar,
	}
	return id, nil
}

// PollOrderStatus checks if the latest bar for the order's symbol crosses the limit price.
func (pb *PaperBroker) PollOrderStatus(_ context.Context, orderID string) (string, float64, error) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	po, ok := pb.pending[orderID]
	if !ok {
		return "canceled", 0, nil
	}

	if po.status == "filled" {
		price := po.fillPrice
		delete(pb.pending, orderID)
		return "filled", price, nil
	}
	if po.status == "expired" {
		delete(pb.pending, orderID)
		return "expired", 0, nil
	}

	bar, hasBar := pb.bars[po.request.Symbol]
	if !hasBar {
		return "new", 0, nil
	}

	// Entry orders: skip the bar they were submitted on (can't fill
	// on the same bar that generated the signal).
	if !po.submittedBar.IsZero() && bar.Timestamp.Equal(po.submittedBar) {
		return "new", 0, nil
	}

	// Volume constraint: order qty must be ≤ 80% of bar volume
	maxShares := int64(float64(bar.Volume) * 0.80)
	if po.request.Quantity > maxShares {
		po.barsLeft--
		if po.barsLeft <= 0 {
			po.status = "expired"
			pb.expiries++
			delete(pb.pending, orderID)
			return "expired", 0, nil
		}
		return "new", 0, nil
	}

	// OHLC fill logic
	fillPrice := 0.0
	switch {
	case po.request.Side == domain.SideBuy && bar.Open > 0 && bar.Open <= po.request.Price:
		fillPrice = bar.Open
	case po.request.Side == domain.SideBuy && bar.Low > 0 && bar.Low <= po.request.Price:
		fillPrice = po.request.Price
	case po.request.Side == domain.SideSell && bar.Open > 0 && bar.Open >= po.request.Price:
		fillPrice = bar.Open
	case po.request.Side == domain.SideSell && bar.High > 0 && bar.High >= po.request.Price:
		fillPrice = po.request.Price
	}

	if fillPrice > 0 {
		// Slippage model
		penalty := scanner.ComputeSlippage(fillPrice, po.request.AvgDailyVolume, 5.0, 10.0, 20.0)
		if penalty < 0.01 {
			spread := bar.High - bar.Low
			if spread < 0 {
				spread = 0
			}
			penalty = spread * 0.05
		}
		if po.request.Side == domain.SideSell {
			fillPrice = math.Max(po.request.Price, fillPrice-penalty)
		} else {
			fillPrice = math.Min(po.request.Price, fillPrice+penalty)
		}
		fillPrice = math.Round(fillPrice*100) / 100

		po.status = "filled"
		po.fillPrice = fillPrice
		delete(pb.pending, orderID)
		return "filled", fillPrice, nil
	}

	po.barsLeft--
	if po.barsLeft <= 0 {
		po.status = "expired"
		pb.expiries++
		delete(pb.pending, orderID)
		return "expired", 0, nil
	}
	return "new", 0, nil
}

// CancelOrder removes a pending order.
func (pb *PaperBroker) CancelOrder(_ context.Context, orderID string) error {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	delete(pb.pending, orderID)
	return nil
}

// IsShortable always returns true for paper trading.
func (pb *PaperBroker) IsShortable(_ string) bool {
	return true
}

// Expiries returns the number of entry orders that expired without filling.
func (pb *PaperBroker) Expiries() int {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return pb.expiries
}
