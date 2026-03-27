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
	mu           sync.Mutex
	nextID       atomic.Int64
	pending      map[string]*paperOrder // orderID → order
	bars         map[string]domain.Bar  // latest bar per symbol
	easyToBorrow map[string]bool
	maxBarsFill  int // orders expire after this many bars (default 2)
	expiries     int // count of expired entry orders
}

type paperOrder struct {
	request      domain.OrderRequest
	barsLeft     int
	fillPrice    float64   // set when filled
	status       string    // "new", "filled", "expired"
	submittedBar time.Time // bar timestamp at submission (entry orders skip this bar)
	lastEvalBar  time.Time // last bar timestamp used to advance this order
}

// NewPaperBroker creates a paper broker for simulated execution.
func NewPaperBroker(easyToBorrow map[string]bool) *PaperBroker {
	normalized := make(map[string]bool, len(easyToBorrow))
	for symbol, allowed := range easyToBorrow {
		normalized[symbol] = allowed
	}
	return &PaperBroker{
		pending:      make(map[string]*paperOrder),
		bars:         make(map[string]domain.Bar),
		easyToBorrow: normalized,
		maxBarsFill:  2,
	}
}

// UpdateBar feeds the latest bar for a symbol. Must be called before
// PollOrderStatus so fill decisions use current bar data.
func (pb *PaperBroker) UpdateBar(bar domain.Bar) {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	prevBar, hadPrev := pb.bars[bar.Symbol]
	pb.bars[bar.Symbol] = bar
	if hadPrev && prevBar.Timestamp.Equal(bar.Timestamp) {
		return
	}
	pb.advancePendingOrders(bar)
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

// PollOrderStatus reports the order's bar-driven status.
func (pb *PaperBroker) PollOrderStatus(_ context.Context, orderID string) (string, float64, int64, error) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	po, ok := pb.pending[orderID]
	if !ok {
		return "canceled", 0, 0, nil
	}

	if po.status == "filled" {
		price := po.fillPrice
		qty := po.request.Quantity
		delete(pb.pending, orderID)
		return "filled", price, qty, nil
	}
	if po.status == "expired" {
		delete(pb.pending, orderID)
		return "expired", 0, 0, nil
	}
	return "new", 0, 0, nil
}

// CancelOrder removes a pending order.
func (pb *PaperBroker) CancelOrder(_ context.Context, orderID string) error {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	delete(pb.pending, orderID)
	return nil
}

// IsEasyToBorrow reports whether a symbol is eligible for short entries in backtests.
func (pb *PaperBroker) IsEasyToBorrow(symbol string) bool {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return pb.easyToBorrow[symbol]
}

// Expiries returns the number of entry orders that expired without filling.
func (pb *PaperBroker) Expiries() int {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return pb.expiries
}

func (pb *PaperBroker) advancePendingOrders(bar domain.Bar) {
	for _, po := range pb.pending {
		if po.status != "new" || !domain.IsOpeningIntent(po.request.Intent) || po.request.Symbol != bar.Symbol {
			continue
		}
		if !po.submittedBar.IsZero() && bar.Timestamp.Equal(po.submittedBar) {
			continue
		}
		if !po.lastEvalBar.IsZero() && !bar.Timestamp.After(po.lastEvalBar) {
			continue
		}
		pb.advanceOrderOnBar(po, bar)
		po.lastEvalBar = bar.Timestamp
	}
}

func (pb *PaperBroker) advanceOrderOnBar(po *paperOrder, bar domain.Bar) {
	// Volume constraint: order qty must be ≤ 80% of bar volume
	maxShares := int64(float64(bar.Volume) * 0.80)
	if po.request.Quantity > maxShares {
		pb.expireOrCarry(po)
		return
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
		po.status = "filled"
		po.fillPrice = math.Round(fillPrice*100) / 100
		return
	}

	pb.expireOrCarry(po)
}

func (pb *PaperBroker) expireOrCarry(po *paperOrder) {
	po.barsLeft--
	if po.barsLeft <= 0 {
		po.status = "expired"
		pb.expiries++
	}
}
