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
	config        config.TradingConfig
	mu            sync.RWMutex
	positions     map[string]domain.Position
	closedTrades  []domain.ClosedTrade
	dayKey        string
	dayPnL        float64
	brokerEquity  float64
	tradesToday   int
	entriesToday  int
	recorder      domain.EventRecorder
	highWaterMark float64
	maxDrawdown   float64
}

// NewManager creates a portfolio manager.
// The recorder parameter is optional; if omitted, no events are persisted.
func NewManager(cfg config.TradingConfig, recorders ...domain.EventRecorder) *Manager {
	var recorder domain.EventRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &Manager{
		config:        cfg,
		positions:     make(map[string]domain.Position),
		closedTrades:  make([]domain.ClosedTrade, 0),
		dayKey:        markethours.TradingDay(time.Now()),
		recorder:      recorder,
		highWaterMark: cfg.StartingCapital,
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
		Sector:           report.Sector,
		LastPrice:        report.Price,
		HighestPrice:     report.Price,
		LowestPrice:      report.Price,
		MarketValue:      report.Price * float64(report.Quantity),
		OpenedAt:         report.FilledAt,
		UpdatedAt:        report.FilledAt,
	}

	// Phase 3 Change 4: Track original quantity for partial exits
	pos.OriginalQuantity = report.Quantity

	if existing, ok := m.positions[report.Symbol]; ok {
		totalQty := existing.Quantity + report.Quantity
		if totalQty > 0 {
			pos.AvgPrice = (existing.AvgPrice*float64(existing.Quantity) + report.Price*float64(report.Quantity)) / float64(totalQty)
		}
		pos.Quantity = totalQty
		pos.OriginalQuantity = totalQty
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
		Sector:           pos.Sector,
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

// SeedClosedTrades loads previously persisted trades into the in-memory slice.
// Called at startup to restore trade history across restarts.
func (m *Manager) SeedClosedTrades(trades []domain.ClosedTrade) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closedTrades = append(m.closedTrades, trades...)
	// Recompute day PnL from all closed trades.
	m.dayPnL = 0
	for _, t := range m.closedTrades {
		m.dayPnL += t.PnL
	}
	m.tradesToday = len(m.closedTrades)
}

// SeedBrokerPosition adds a position from the broker.
func (m *Manager) SeedBrokerPosition(pos domain.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pos.BrokerSeeded = true
	m.positions[pos.Symbol] = pos
}

// UpdateSeededPositionRisk sets stop price, risk per share, original quantity,
// entry ATR, and playbook for a broker-seeded position that was missing these values.
// Only zero-valued fields are updated; non-zero values are preserved.
func (m *Manager) UpdateSeededPositionRisk(symbol string, stopPrice, riskPerShare float64, originalQty int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pos, ok := m.positions[symbol]
	if !ok {
		return
	}
	if pos.StopPrice == 0 {
		pos.StopPrice = stopPrice
		pos.InitialStopPrice = stopPrice
	}
	if pos.RiskPerShare == 0 {
		pos.RiskPerShare = riskPerShare
	}
	if pos.OriginalQuantity == 0 {
		pos.OriginalQuantity = originalQty
	}
	if pos.EntryATR == 0 {
		pos.EntryATR = riskPerShare // Use risk per share as ATR proxy
	}
	if pos.Playbook == "" {
		pos.Playbook = "breakout" // Safe default
	}
	pos.UpdatedAt = time.Now()
	m.positions[symbol] = pos
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

// UpdateStopPrice updates the stop price for a position (trailing stop).
func (m *Manager) UpdateStopPrice(symbol string, newStop float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pos, ok := m.positions[symbol]
	if !ok {
		return
	}
	pos.StopPrice = newStop
	pos.UpdatedAt = time.Now()
	m.positions[symbol] = pos
}

// ReducePosition partially closes a position, reducing quantity and recording a partial trade.
func (m *Manager) ReducePosition(report domain.ExecutionReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetDayIfNeeded()

	pos, ok := m.positions[report.Symbol]
	if !ok {
		return
	}

	exitQty := report.Quantity
	if exitQty >= pos.Quantity {
		// Full close — delegate to ClosePosition
		m.mu.Unlock()
		m.ClosePosition(report)
		m.mu.Lock()
		return
	}

	var pnl float64
	if domain.IsLong(pos.Side) {
		pnl = (report.Price - pos.AvgPrice) * float64(exitQty)
	} else {
		pnl = (pos.AvgPrice - report.Price) * float64(exitQty)
	}

	rMultiple := 0.0
	if pos.RiskPerShare > 0 {
		rMultiple = pnl / (pos.RiskPerShare * float64(exitQty))
	}

	trade := domain.ClosedTrade{
		Symbol:           pos.Symbol,
		Side:             pos.Side,
		Quantity:         exitQty,
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
		Sector:           pos.Sector,
	}

	m.closedTrades = append(m.closedTrades, trade)
	m.dayPnL += pnl

	pos.Quantity -= exitQty
	pos.PartialsExecuted++
	pos.MarketValue = pos.LastPrice * float64(pos.Quantity)
	if domain.IsLong(pos.Side) {
		pos.UnrealizedPnL = (pos.LastPrice - pos.AvgPrice) * float64(pos.Quantity)
	} else {
		pos.UnrealizedPnL = (pos.AvgPrice - pos.LastPrice) * float64(pos.Quantity)
	}
	pos.UpdatedAt = report.FilledAt
	m.positions[report.Symbol] = pos

	if m.recorder != nil {
		m.recorder.RecordClosedTrade(trade)
	}
}

// ApplyExecution processes an execution report, opening or closing positions as appropriate.
func (m *Manager) ApplyExecution(report domain.ExecutionReport) {
	if domain.IsOpeningIntent(report.Intent) {
		m.OpenPosition(report)
	} else if domain.IsPartialIntent(report.Intent) {
		m.ReducePosition(report)
	} else {
		m.ClosePosition(report)
	}
}

// HasPosition returns true if the manager holds a position in the given symbol.
func (m *Manager) HasPosition(symbol string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.positions[symbol]
	return ok
}

// SymbolHadLossToday returns true if the symbol has any closed trade with negative PnL today.
func (m *Manager) SymbolHadLossToday(symbol string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, t := range m.closedTrades {
		if t.Symbol == symbol && t.PnL < 0 {
			return true
		}
	}
	return false
}

// RemoveStalePosition removes a position that no longer exists at the broker,
// recording it as a closed trade with the last known price.
func (m *Manager) RemoveStalePosition(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetDayIfNeeded()

	pos, ok := m.positions[symbol]
	if !ok {
		return
	}

	exitPrice := pos.LastPrice
	if exitPrice == 0 {
		exitPrice = pos.AvgPrice
	}

	var pnl float64
	if domain.IsLong(pos.Side) {
		pnl = (exitPrice - pos.AvgPrice) * float64(pos.Quantity)
	} else {
		pnl = (pos.AvgPrice - exitPrice) * float64(pos.Quantity)
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
		ExitPrice:        exitPrice,
		PnL:              pnl,
		RMultiple:        rMultiple,
		SetupType:        pos.SetupType,
		OpenedAt:         pos.OpenedAt,
		ClosedAt:         time.Now(),
		ExitReason:       "reconcile-stale",
		MarketRegime:     pos.MarketRegime,
		RegimeConfidence: pos.RegimeConfidence,
		Playbook:         pos.Playbook,
		Sector:           pos.Sector,
	}

	m.closedTrades = append(m.closedTrades, trade)
	m.dayPnL += pnl
	m.tradesToday++
	delete(m.positions, symbol)

	if m.recorder != nil {
		m.recorder.RecordClosedTrade(trade)
	}
}

// MarkPriceAt updates the position's price tracking at a specific time.
func (m *Manager) MarkPriceAt(symbol string, price float64, at time.Time) {
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
	if price < pos.LowestPrice || pos.LowestPrice == 0 {
		pos.LowestPrice = price
	}
	if domain.IsLong(pos.Side) {
		pos.UnrealizedPnL = (price - pos.AvgPrice) * float64(pos.Quantity)
	} else {
		pos.UnrealizedPnL = (pos.AvgPrice - price) * float64(pos.Quantity)
	}
	pos.UpdatedAt = at
	m.positions[symbol] = pos
}

// RealizedPnL returns the total realized PnL from closed trades.
func (m *Manager) RealizedPnL() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total float64
	for _, t := range m.closedTrades {
		total += t.PnL
	}
	return total
}

// OpenPositionCount returns the number of open positions.
func (m *Manager) OpenPositionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.positions)
}

// CurrentEquity returns the current equity: starting capital + realized PnL + unrealized PnL.
func (m *Manager) CurrentEquity() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	equity := m.config.StartingCapital + m.dayPnL
	for _, pos := range m.positions {
		equity += pos.UnrealizedPnL
	}
	return equity
}

// DayPnL returns the realized day PnL.
func (m *Manager) DayPnL() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dayPnL
}

// PortfolioHeat returns the total dollar risk across all open positions.
func (m *Manager) PortfolioHeat() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var totalRisk float64
	for _, pos := range m.positions {
		totalRisk += pos.RiskPerShare * float64(pos.Quantity)
	}
	return totalRisk
}

// PortfolioHeatPct returns total open risk as a fraction of current equity.
func (m *Manager) PortfolioHeatPct() float64 {
	equity := m.CurrentEquity()
	if equity <= 0 {
		return 0
	}
	return m.PortfolioHeat() / equity
}

// UpdateEquityTracking updates the high-water mark and max drawdown.
func (m *Manager) UpdateEquityTracking() {
	m.mu.Lock()
	defer m.mu.Unlock()
	equity := m.currentEquityLocked()
	if equity > m.highWaterMark {
		m.highWaterMark = equity
	}
	if m.highWaterMark > 0 {
		dd := (m.highWaterMark - equity) / m.highWaterMark
		if dd > m.maxDrawdown {
			m.maxDrawdown = dd
		}
	}
}

// CurrentDrawdown returns the current drawdown from the high-water mark as a fraction.
func (m *Manager) CurrentDrawdown() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	equity := m.currentEquityLocked()
	if m.highWaterMark <= 0 {
		return 0
	}
	dd := (m.highWaterMark - equity) / m.highWaterMark
	if dd < 0 {
		return 0
	}
	return dd
}

// currentEquityLocked computes equity without acquiring the lock (caller must hold lock).
func (m *Manager) currentEquityLocked() float64 {
	equity := m.config.StartingCapital + m.dayPnL
	for _, pos := range m.positions {
		equity += pos.UnrealizedPnL
	}
	return equity
}

// OpenSymbols returns a list of symbols with open positions.
func (m *Manager) OpenSymbols() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	symbols := make([]string, 0, len(m.positions))
	for sym := range m.positions {
		symbols = append(symbols, sym)
	}
	return symbols
}

// RollingTradeStats returns win rate and avg win/loss ratio over the last windowSize trades.
func (m *Manager) RollingTradeStats(windowSize int) (winRate float64, avgWinLossRatio float64, tradeCount int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	trades := m.closedTrades
	if len(trades) > windowSize {
		trades = trades[len(trades)-windowSize:]
	}

	if len(trades) < 10 {
		return 0, 0, len(trades)
	}

	var wins, losses int
	var totalWin, totalLoss float64
	for _, t := range trades {
		if t.PnL > 0 {
			wins++
			totalWin += t.PnL
		} else if t.PnL < 0 {
			losses++
			totalLoss += math.Abs(t.PnL)
		}
	}

	if wins+losses == 0 {
		return 0, 0, 0
	}

	winRate = float64(wins) / float64(wins+losses)

	avgWin := 0.0
	avgLoss := 0.0
	if wins > 0 {
		avgWin = totalWin / float64(wins)
	}
	if losses > 0 {
		avgLoss = totalLoss / float64(losses)
	}

	if avgLoss > 0 {
		avgWinLossRatio = avgWin / avgLoss
	}

	return winRate, avgWinLossRatio, wins + losses
}
