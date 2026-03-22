package portfolio

import (
	"math"
	"sync"
	"time"

	"github.com/edwintcloud/momentum-trading-bot/internal/config"
	"github.com/edwintcloud/momentum-trading-bot/internal/domain"
	"github.com/edwintcloud/momentum-trading-bot/internal/markethours"
)

// Manager tracks positions, exposure, and PnL.
type Manager struct {
	config       config.TradingConfig
	mu           sync.RWMutex
	positions    map[string]domain.Position
	closedTrades []domain.ClosedTrade
	dayKey       string
	dayPnL       float64
	brokerEquity float64
	tradesToday  int
	entriesToday int
	recorder     domain.EventRecorder
}

// NewManager creates a portfolio manager.
func NewManager(cfg config.TradingConfig, recorder domain.EventRecorder) *Manager {
	return &Manager{
		config:       cfg,
		positions:    make(map[string]domain.Position),
		closedTrades: make([]domain.ClosedTrade, 0),
		dayKey:       markethours.TradingDay(time.Now()),
		recorder:     recorder,
	}
}

// SetBrokerEquity sets the broker-reported equity.
func (m *Manager) SetBrokerEquity(equity float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.brokerEquity = equity
}

// GetPosition returns a position by symbol.
func (m *Manager) GetPosition(symbol string) (domain.Position, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.positions[symbol]
	return p, ok
}

// GetPositions returns all open positions.
func (m *Manager) GetPositions() []domain.Position {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Position, 0, len(m.positions))
	for _, p := range m.positions {
		out = append(out, p)
	}
	return out
}

// GetClosedTrades returns all closed trades for the day.
func (m *Manager) GetClosedTrades() []domain.ClosedTrade {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.ClosedTrade, len(m.closedTrades))
	copy(out, m.closedTrades)
	return out
}

// OpenPosition creates or updates a position from an execution report.
func (m *Manager) OpenPosition(report domain.ExecutionReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetDayIfNeeded()

	pos := domain.Position{
		Symbol:           report.Symbol,
		Side:             report.PositionSide,
		Quantity:         report.Quantity,
		AvgPrice:         report.Price,
		StopPrice:        report.StopPrice,
		InitialStopPrice: report.StopPrice,
		RiskPerShare:     report.RiskPerShare,
		EntryATR:         report.EntryATR,
		SetupType:        report.SetupType,
		MarketRegime:     report.MarketRegime,
		RegimeConfidence: report.RegimeConfidence,
		Playbook:         report.Playbook,
		LastPrice:        report.Price,
		HighestPrice:     report.Price,
		LowestPrice:      report.Price,
		MarketValue:      report.Price * float64(report.Quantity),
		OpenedAt:         report.FilledAt,
		UpdatedAt:        report.FilledAt,
	}

	if existing, ok := m.positions[report.Symbol]; ok {
		totalQty := existing.Quantity + report.Quantity
		if totalQty > 0 {
			pos.AvgPrice = (existing.AvgPrice*float64(existing.Quantity) + report.Price*float64(report.Quantity)) / float64(totalQty)
		}
		pos.Quantity = totalQty
		pos.HighestPrice = math.Max(existing.HighestPrice, report.Price)
		pos.LowestPrice = math.Min(existing.LowestPrice, report.Price)
		pos.OpenedAt = existing.OpenedAt
	}

	m.positions[report.Symbol] = pos
	m.entriesToday++
	m.tradesToday++
}

// ClosePosition closes a position and records the trade.
func (m *Manager) ClosePosition(report domain.ExecutionReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetDayIfNeeded()

	pos, ok := m.positions[report.Symbol]
	if !ok {
		return
	}

	var pnl float64
	if domain.IsLong(pos.Side) {
		pnl = (report.Price - pos.AvgPrice) * float64(pos.Quantity)
	} else {
		pnl = (pos.AvgPrice - report.Price) * float64(pos.Quantity)
	}

	rMultiple := 0.0
	if pos.RiskPerShare > 0 {
		rMultiple = pnl / (pos.RiskPerShare * float64(pos.Quantity))
	}

	trade := domain.ClosedTrade{
		Symbol:           pos.Symbol,
		Side:             pos.Side,
		Quantity:         pos.Quantity,
		EntryPrice:       pos.AvgPrice,
		ExitPrice:        report.Price,
		PnL:              pnl,
		RMultiple:        rMultiple,
		SetupType:        pos.SetupType,
		OpenedAt:         pos.OpenedAt,
		ClosedAt:         report.FilledAt,
		ExitReason:       report.Reason,
		MarketRegime:     pos.MarketRegime,
		RegimeConfidence: pos.RegimeConfidence,
		Playbook:         pos.Playbook,
	}

	m.closedTrades = append(m.closedTrades, trade)
	m.dayPnL += pnl
	m.tradesToday++
	delete(m.positions, report.Symbol)

	if m.recorder != nil {
		m.recorder.RecordClosedTrade(trade)
	}
}

// UpdatePrice updates the last known price for a position.
func (m *Manager) UpdatePrice(symbol string, price float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pos, ok := m.positions[symbol]
	if !ok {
		return
	}
	pos.LastPrice = price
	pos.MarketValue = price * float64(pos.Quantity)
	if price > pos.HighestPrice {
		pos.HighestPrice = price
	}
	if price < pos.LowestPrice {
		pos.LowestPrice = price
	}
	if domain.IsLong(pos.Side) {
		pos.UnrealizedPnL = (price - pos.AvgPrice) * float64(pos.Quantity)
	} else {
		pos.UnrealizedPnL = (pos.AvgPrice - price) * float64(pos.Quantity)
	}
	pos.UpdatedAt = time.Now()
	m.positions[symbol] = pos
}

// SeedBrokerPosition adds a position from the broker.
func (m *Manager) SeedBrokerPosition(pos domain.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pos.BrokerSeeded = true
	m.positions[pos.Symbol] = pos
}

// Exposure returns total long and short exposure.
func (m *Manager) Exposure() (total, long, short float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.positions {
		value := math.Abs(p.MarketValue)
		total += value
		if domain.IsLong(p.Side) {
			long += value
		} else {
			short += value
		}
	}
	return
}

// UnrealizedPnL returns the total unrealized PnL across all positions.
func (m *Manager) UnrealizedPnL() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total float64
	for _, p := range m.positions {
		total += p.UnrealizedPnL
	}
	return total
}

// PendingCloseAll returns close orders for all positions.
func (m *Manager) PendingCloseAll(reason string) []domain.OrderRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	orders := make([]domain.OrderRequest, 0, len(m.positions))
	for _, p := range m.positions {
		orders = append(orders, domain.OrderRequest{
			Symbol:       p.Symbol,
			Side:         domain.CloseBrokerSide(p.Side),
			Intent:       domain.IntentClose,
			PositionSide: p.Side,
			Price:        p.LastPrice,
			Quantity:     p.Quantity,
			Reason:       reason,
			Timestamp:    time.Now(),
		})
	}
	return orders
}

// StatusSnapshot returns a dashboard status snapshot.
func (m *Manager) StatusSnapshot() domain.StatusSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.resetDayIfNeededLocked()

	exposure, longExposure, shortExposure := float64(0), float64(0), float64(0)
	var unrealized float64
	for _, p := range m.positions {
		value := math.Abs(p.MarketValue)
		exposure += value
		unrealized += p.UnrealizedPnL
		if domain.IsLong(p.Side) {
			longExposure += value
		} else {
			shortExposure += value
		}
	}

	return domain.StatusSnapshot{
		Running:          true,
		Paused:           false,
		EmergencyStop:    false,
		LastUpdate:       time.Now(),
		StartingCapital:  m.config.StartingCapital,
		BrokerEquity:     m.brokerEquity,
		DayPnL:           m.dayPnL,
		RealizedPnL:      m.dayPnL,
		UnrealizedPnL:    unrealized,
		NetPnL:           m.dayPnL + unrealized,
		Exposure:         exposure,
		LongExposure:     longExposure,
		ShortExposure:    shortExposure,
		OpenPositions:    len(m.positions),
		TradesToday:      m.tradesToday,
		EntriesToday:     m.entriesToday,
		DailyLossLimit:   m.config.StartingCapital * m.config.DailyLossLimitPct,
		MaxOpenPositions: m.config.MaxOpenPositions,
		MaxTradesPerDay:  m.config.MaxTradesPerDay,
		ActiveProfile:    m.config.StrategyProfileName,
		ActiveVersion:    m.config.StrategyProfileVersion,
	}
}

// PerformanceMetrics computes aggregate performance stats from closed trades.
func (m *Manager) PerformanceMetrics() domain.PerformanceMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.closedTrades) == 0 {
		return domain.PerformanceMetrics{}
	}

	var wins, losses int
	var totalWin, totalLoss float64
	var totalR float64
	var largest, smallest float64
	var holdTime int64

	for _, t := range m.closedTrades {
		totalR += t.RMultiple
		holdTime += t.ClosedAt.Sub(t.OpenedAt).Milliseconds()
		if t.PnL >= 0 {
			wins++
			totalWin += t.PnL
			if t.PnL > largest {
				largest = t.PnL
			}
		} else {
			losses++
			totalLoss += math.Abs(t.PnL)
			if t.PnL < smallest {
				smallest = t.PnL
			}
		}
	}

	n := len(m.closedTrades)
	avgWin := float64(0)
	avgLoss := float64(0)
	if wins > 0 {
		avgWin = totalWin / float64(wins)
	}
	if losses > 0 {
		avgLoss = totalLoss / float64(losses)
	}
	profitFactor := float64(0)
	if totalLoss > 0 {
		profitFactor = totalWin / totalLoss
	}

	return domain.PerformanceMetrics{
		TotalTrades:   n,
		WinRate:       float64(wins) / float64(n),
		AvgWin:        avgWin,
		AvgLoss:       avgLoss,
		ProfitFactor:  profitFactor,
		AvgRMultiple:  totalR / float64(n),
		LargestWin:    largest,
		LargestLoss:   smallest,
		AvgHoldTimeMs: holdTime / int64(n),
	}
}

func (m *Manager) resetDayIfNeeded() {
	today := markethours.TradingDay(time.Now())
	if today != m.dayKey {
		m.dayKey = today
		m.dayPnL = 0
		m.tradesToday = 0
		m.entriesToday = 0
		m.closedTrades = m.closedTrades[:0]
	}
}

func (m *Manager) resetDayIfNeededLocked() {
	today := markethours.TradingDay(time.Now())
	if today != m.dayKey {
		m.dayKey = today
		m.dayPnL = 0
		m.tradesToday = 0
		m.entriesToday = 0
		m.closedTrades = m.closedTrades[:0]
	}
}
