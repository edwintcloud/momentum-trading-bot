package portfolio

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/edwincloud/momentum-trading-bot/internal/config"
	"github.com/edwincloud/momentum-trading-bot/internal/domain"
	"github.com/edwincloud/momentum-trading-bot/internal/runtime"
)

var tradingDayLocation = mustLoadLocation("America/New_York")

// Manager tracks open positions, PnL, and trade history.
type Manager struct {
	mu              sync.RWMutex
	config          config.TradingConfig
	runtime         *runtime.State
	recorder        domain.EventRecorder
	positions       map[string]domain.Position
	closedTrades    []domain.ClosedTrade
	startingCapital float64
	brokerEquity    float64
	dayPnL          float64
	dayRealizedPnL  float64
	realizedPnL     float64
	tradesToday     int
	currentTradeDay string
}

// NewManager creates a new portfolio manager.
func NewManager(cfg config.TradingConfig, runtimeState *runtime.State) *Manager {
	return &Manager{
		config:          cfg,
		runtime:         runtimeState,
		positions:       make(map[string]domain.Position),
		startingCapital: cfg.StartingCapital,
	}
}

// SetRecorder attaches a persistence sink for execution and trade events.
func (m *Manager) SetRecorder(recorder domain.EventRecorder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorder = recorder
}

// SetStartingCapital replaces the displayed starting capital with broker equity.
func (m *Manager) SetStartingCapital(amount float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startingCapital = amount
}

// SyncBrokerAccount updates broker-backed equity and daily PnL shown on the dashboard.
func (m *Manager) SyncBrokerAccount(equity float64, lastEquity float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if equity > 0 {
		m.brokerEquity = round2(equity)
	}
	if lastEquity > 0 && equity > 0 {
		m.dayPnL = round2(equity - lastEquity)
	}
}

// SeedPosition initializes a broker-backed position on startup.
func (m *Manager) SeedPosition(position domain.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positions[position.Symbol] = position
}

// ApplyExecution applies a fill to the portfolio.
func (m *Manager) ApplyExecution(report domain.ExecutionReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recorder != nil {
		m.recorder.RecordExecution(report)
	}

	now := report.FilledAt.UTC()
	m.rollTradingDayLocked(now)
	position, exists := m.positions[report.Symbol]
	if report.Side == "buy" {
		if exists {
			totalCost := (float64(position.Quantity) * position.AvgPrice) + (float64(report.Quantity) * report.Price)
			position.Quantity += report.Quantity
			position.AvgPrice = totalCost / float64(position.Quantity)
			position.LastPrice = report.Price
			position.HighestPrice = math.Max(position.HighestPrice, report.Price)
			position.MarketValue = float64(position.Quantity) * report.Price
			position.UnrealizedPnL = (report.Price - position.AvgPrice) * float64(position.Quantity)
			position.UpdatedAt = now
			m.positions[report.Symbol] = position
		} else {
			m.positions[report.Symbol] = domain.Position{
				Symbol:        report.Symbol,
				Quantity:      report.Quantity,
				AvgPrice:      report.Price,
				LastPrice:     report.Price,
				HighestPrice:  report.Price,
				MarketValue:   float64(report.Quantity) * report.Price,
				UnrealizedPnL: 0,
				OpenedAt:      now,
				UpdatedAt:     now,
			}
		}
		m.tradesToday++
		return
	}

	if !exists {
		return
	}

	closeQty := report.Quantity
	if closeQty > position.Quantity {
		closeQty = position.Quantity
	}
	pnl := (report.Price - position.AvgPrice) * float64(closeQty)
	m.realizedPnL += pnl
	m.dayRealizedPnL += pnl
	m.tradesToday++
	closed := domain.ClosedTrade{
		Symbol:     report.Symbol,
		Quantity:   closeQty,
		EntryPrice: position.AvgPrice,
		ExitPrice:  report.Price,
		PnL:        round2(pnl),
		OpenedAt:   position.OpenedAt,
		ClosedAt:   now,
		ExitReason: report.Reason,
	}
	m.closedTrades = append([]domain.ClosedTrade{closed}, m.closedTrades...)
	if m.recorder != nil {
		m.recorder.RecordClosedTrade(closed)
	}

	position.Quantity -= closeQty
	if position.Quantity == 0 {
		delete(m.positions, report.Symbol)
		return
	}
	position.LastPrice = report.Price
	position.HighestPrice = math.Max(position.HighestPrice, report.Price)
	position.MarketValue = float64(position.Quantity) * report.Price
	position.UnrealizedPnL = (report.Price - position.AvgPrice) * float64(position.Quantity)
	position.UpdatedAt = now
	m.positions[report.Symbol] = position
}

// MarkPrice updates the latest price for an open position.
func (m *Manager) MarkPrice(symbol string, price float64) {
	m.MarkPriceAt(symbol, price, time.Now().UTC())
}

// MarkPriceAt updates the latest price for an open position using the provided timestamp.
func (m *Manager) MarkPriceAt(symbol string, price float64, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollTradingDayLocked(at.UTC())

	position, exists := m.positions[symbol]
	if !exists {
		return
	}
	position.LastPrice = price
	position.HighestPrice = math.Max(position.HighestPrice, price)
	position.MarketValue = float64(position.Quantity) * price
	position.UnrealizedPnL = (price - position.AvgPrice) * float64(position.Quantity)
	position.UpdatedAt = at.UTC()
	m.positions[symbol] = position
}

// ReconcileWithBroker aligns local positions with the broker's current open
// quantities so the dashboard stays in sync when fills complete outside the
// bot's poll window or positions are modified externally.
func (m *Manager) ReconcileWithBroker(brokerQuantities map[string]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for symbol := range m.positions {
		brokerQty, open := brokerQuantities[symbol]
		if !open || brokerQty <= 0 {
			delete(m.positions, symbol)
			m.runtime.RecordLog("warn", "portfolio", "removed stale position "+symbol+": no longer open at broker")
			continue
		}
		m.syncPositionQuantityLocked(symbol, brokerQty)
	}
}

// SyncPositionQuantity updates a local position to match the broker's current
// share count for a symbol.
func (m *Manager) SyncPositionQuantity(symbol string, quantity int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncPositionQuantityLocked(symbol, quantity)
	if quantity <= 0 {
		delete(m.positions, symbol)
	}
}

func (m *Manager) syncPositionQuantityLocked(symbol string, quantity int64) {
	position, exists := m.positions[symbol]
	if !exists {
		return
	}
	if quantity <= 0 {
		return
	}
	if position.Quantity == quantity {
		return
	}
	previous := position.Quantity
	position.Quantity = quantity
	position.MarketValue = float64(position.Quantity) * position.LastPrice
	position.UnrealizedPnL = (position.LastPrice - position.AvgPrice) * float64(position.Quantity)
	position.UpdatedAt = time.Now().UTC()
	m.positions[symbol] = position
	m.runtime.RecordLog("warn", "portfolio", fmt.Sprintf("reconciled %s quantity from %d to %d based on broker position", symbol, previous, quantity))
}

// GetPositions returns sorted open positions.
func (m *Manager) GetPositions() []domain.Position {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]domain.Position, 0, len(m.positions))
	for _, position := range m.positions {
		out = append(out, position)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// GetClosedTrades returns the most recent closed trades.
func (m *Manager) GetClosedTrades() []domain.ClosedTrade {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.ClosedTrade, len(m.closedTrades))
	copy(out, m.closedTrades)
	return out
}

// HasPosition reports whether a symbol is already open.
func (m *Manager) HasPosition(symbol string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.positions[symbol]
	return exists
}

// Position returns the current position for a symbol.
func (m *Manager) Position(symbol string) (domain.Position, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	position, exists := m.positions[symbol]
	return position, exists
}

// OpenPositionCount returns the current number of open positions.
func (m *Manager) OpenPositionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.positions)
}

// Exposure returns gross long exposure.
func (m *Manager) Exposure() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var exposure float64
	for _, position := range m.positions {
		exposure += position.MarketValue
	}
	return round2(exposure)
}

// UnrealizedPnL returns aggregate unrealized PnL.
func (m *Manager) UnrealizedPnL() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var pnl float64
	for _, position := range m.positions {
		pnl += position.UnrealizedPnL
	}
	return round2(pnl)
}

// RealizedPnL returns realized PnL.
func (m *Manager) RealizedPnL() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return round2(m.realizedPnL)
}

// DayPnL returns the broker-backed day PnL when available, otherwise falls
// back to the local bot ledger.
func (m *Manager) DayPnL() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.brokerEquity > 0 {
		return round2(m.dayPnL)
	}
	var unrealized float64
	for _, position := range m.positions {
		unrealized += position.UnrealizedPnL
	}
	return round2(m.dayRealizedPnL + unrealized)
}

// EffectiveCapital returns broker equity when available, otherwise configured
// starting capital.
func (m *Manager) EffectiveCapital() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.brokerEquity > 0 {
		return round2(m.brokerEquity)
	}
	return round2(m.startingCapital)
}

// TradesToday returns the count of fills processed today.
func (m *Manager) TradesToday() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tradesToday
}

// PendingCloseAll returns exit orders for all open positions.
func (m *Manager) PendingCloseAll(reason string) []domain.OrderRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()

	orders := make([]domain.OrderRequest, 0, len(m.positions))
	for _, position := range m.positions {
		orders = append(orders, domain.OrderRequest{
			Symbol:    position.Symbol,
			Side:      "sell",
			Price:     position.LastPrice,
			Quantity:  position.Quantity,
			Reason:    reason,
			Timestamp: time.Now().UTC(),
		})
	}
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].Symbol < orders[j].Symbol
	})
	return orders
}

// StatusSnapshot builds the API status payload.
func (m *Manager) StatusSnapshot() domain.StatusSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var exposure float64
	var unrealized float64
	for _, position := range m.positions {
		exposure += position.MarketValue
		unrealized += position.UnrealizedPnL
	}

	status := domain.StatusSnapshot{
		Running:          true,
		Paused:           m.runtime.IsPaused(),
		EmergencyStop:    m.runtime.IsEmergencyStopped(),
		LastUpdate:       m.runtime.LastUpdate(),
		StartingCapital:  m.startingCapital,
		BrokerEquity:     m.brokerEquity,
		DayPnL:           round2(m.dayRealizedPnL + unrealized),
		RealizedPnL:      round2(m.realizedPnL),
		UnrealizedPnL:    round2(unrealized),
		NetPnL:           round2(m.realizedPnL + unrealized),
		Exposure:         round2(exposure),
		OpenPositions:    len(m.positions),
		TradesToday:      m.tradesToday,
		DailyLossLimit:   round2(m.EffectiveCapital() * m.config.DailyLossLimitPct),
		MaxOpenPositions: m.config.MaxOpenPositions,
		MaxTradesPerDay:  m.config.MaxTradesPerDay,
	}
	if m.brokerEquity > 0 {
		status.DayPnL = round2(m.dayPnL)
	}
	return status
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func (m *Manager) rollTradingDayLocked(now time.Time) {
	day := now.In(tradingDayLocation).Format("2006-01-02")
	if m.currentTradeDay == "" {
		m.currentTradeDay = day
		return
	}
	if m.currentTradeDay == day {
		return
	}
	m.currentTradeDay = day
	m.tradesToday = 0
	m.dayRealizedPnL = 0
	if m.brokerEquity == 0 {
		m.dayPnL = 0
	}
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
