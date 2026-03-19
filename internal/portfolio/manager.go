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
	mu                sync.RWMutex
	config            config.TradingConfig
	runtime           *runtime.State
	recorder          domain.EventRecorder
	positions         map[string]domain.Position
	closedTrades      []domain.ClosedTrade
	startingCapital   float64
	brokerEquity      float64
	brokerCash        float64
	brokerCashKnown   bool
	dayPnL            float64
	brokerTradesToday int
	brokerTradesKnown bool
	dayRealizedPnL    float64
	realizedPnL       float64
	tradesToday       int
	entriesToday      int
	currentTradeDay   string
}

func unrealizedPnL(position domain.Position, price float64) float64 {
	if domain.IsShort(position.Side) {
		return (position.AvgPrice - price) * float64(position.Quantity)
	}
	return (price - position.AvgPrice) * float64(position.Quantity)
}

func realizedPnL(position domain.Position, exitPrice float64, quantity int64) float64 {
	if domain.IsShort(position.Side) {
		return (position.AvgPrice - exitPrice) * float64(quantity)
	}
	return (exitPrice - position.AvgPrice) * float64(quantity)
}

func updatePositionExtrema(position domain.Position, price float64) domain.Position {
	if price <= 0 {
		return position
	}
	position.HighestPrice = math.Max(position.HighestPrice, price)
	if position.LowestPrice == 0 || price < position.LowestPrice {
		position.LowestPrice = price
	}
	return position
}

func inferExecutionIntent(report domain.ExecutionReport, existing domain.Position, exists bool) domain.ExecutionReport {
	report.Side = domain.NormalizeSide(report.Side)
	if exists && report.PositionSide == "" {
		report.PositionSide = existing.Side
	}
	report.PositionSide = domain.NormalizeDirection(report.PositionSide)
	if report.Intent != "" {
		report.Intent = domain.NormalizeIntent(report.Intent)
		return report
	}
	switch {
	case exists && report.Side == domain.CloseBrokerSide(existing.Side):
		report.Intent = domain.IntentClose
		report.PositionSide = existing.Side
	case exists && report.Side == domain.OpenBrokerSide(existing.Side):
		report.Intent = domain.IntentOpen
		report.PositionSide = existing.Side
	case report.Side == domain.SideSell:
		report.Intent = domain.IntentOpen
		report.PositionSide = domain.DirectionShort
	default:
		report.Intent = domain.IntentOpen
		report.PositionSide = domain.DirectionLong
	}
	return report
}

func normalizedEntryStop(report domain.ExecutionReport) float64 {
	if report.Price <= 0 || report.RiskPerShare <= 0 {
		return round2(report.StopPrice)
	}
	if domain.IsShort(report.PositionSide) {
		return round2(report.Price + report.RiskPerShare)
	}
	return round2(math.Max(0.01, report.Price-report.RiskPerShare))
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

// SyncBrokerCash updates the broker-backed cash balance used for entry sizing.
func (m *Manager) SyncBrokerCash(cash float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cash < 0 {
		return
	}
	m.brokerCash = round2(cash)
	m.brokerCashKnown = true
}

// SyncBrokerTradesToday updates the dashboard-facing trade count from Alpaca.
func (m *Manager) SyncBrokerTradesToday(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if count < 0 {
		count = 0
	}
	m.brokerTradesToday = count
	m.brokerTradesKnown = true
}

// SeedPosition initializes a broker-backed position on startup.
func (m *Manager) SeedPosition(position domain.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positions[position.Symbol] = position
}

// SeedClosedTrades restores today's closed-trade history after a restart so
// the dashboard and risk counters reflect fills that already happened today.
func (m *Manager) SeedClosedTrades(trades []domain.ClosedTrade) {
	if len(trades) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	day := now.In(tradingDayLocation).Format("2006-01-02")

	// trades are ordered newest-first from the DB query
	m.closedTrades = make([]domain.ClosedTrade, len(trades))
	copy(m.closedTrades, trades)

	var totalPnL float64
	for _, t := range trades {
		totalPnL += t.PnL
	}
	m.dayRealizedPnL = round2(totalPnL)
	m.realizedPnL = round2(totalPnL)
	// Each closed trade represents one entry + one exit.
	m.entriesToday = len(trades)
	m.tradesToday = len(trades) * 2
	if m.currentTradeDay == "" {
		m.currentTradeDay = day
	}
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
	report = inferExecutionIntent(report, position, exists)
	if domain.IsOpeningIntent(report.Intent) {
		entryStop := normalizedEntryStop(report)
		isNewEntry := !exists
		if m.brokerCashKnown {
			cashDelta := float64(report.Quantity) * report.Price
			if report.Side == domain.SideBuy {
				m.brokerCash = round2(math.Max(0, m.brokerCash-cashDelta))
			} else {
				m.brokerCash = round2(m.brokerCash + cashDelta)
			}
		}
		if exists {
			totalCost := (float64(position.Quantity) * position.AvgPrice) + (float64(report.Quantity) * report.Price)
			position.Quantity += report.Quantity
			position.AvgPrice = totalCost / float64(position.Quantity)
			position.Side = report.PositionSide
			if entryStop > 0 {
				position.StopPrice = entryStop
			}
			if position.InitialStopPrice == 0 && entryStop > 0 {
				position.InitialStopPrice = entryStop
			}
			if report.RiskPerShare > 0 {
				position.RiskPerShare = report.RiskPerShare
			}
			if report.EntryATR > 0 {
				position.EntryATR = report.EntryATR
			}
			if report.SetupType != "" {
				position.SetupType = report.SetupType
			}
			position.LastPrice = report.Price
			position = updatePositionExtrema(position, report.Price)
			position.MarketValue = float64(position.Quantity) * report.Price
			position.UnrealizedPnL = unrealizedPnL(position, report.Price)
			position.UpdatedAt = now
			m.positions[report.Symbol] = position
		} else {
			newPosition := domain.Position{
				Symbol:           report.Symbol,
				Side:             report.PositionSide,
				Quantity:         report.Quantity,
				AvgPrice:         report.Price,
				StopPrice:        entryStop,
				InitialStopPrice: entryStop,
				RiskPerShare:     report.RiskPerShare,
				EntryATR:         report.EntryATR,
				SetupType:        report.SetupType,
				LastPrice:        report.Price,
				MarketValue:      float64(report.Quantity) * report.Price,
				UnrealizedPnL:    0,
				OpenedAt:         now,
				UpdatedAt:        now,
			}
			newPosition = updatePositionExtrema(newPosition, report.Price)
			m.positions[report.Symbol] = newPosition
		}
		m.tradesToday++
		if isNewEntry {
			m.entriesToday++
		}
		return
	}

	if !exists {
		return
	}

	closeQty := report.Quantity
	if closeQty > position.Quantity {
		closeQty = position.Quantity
	}
	if m.brokerCashKnown {
		cashDelta := float64(closeQty) * report.Price
		if report.Side == domain.SideBuy {
			m.brokerCash = round2(math.Max(0, m.brokerCash-cashDelta))
		} else {
			m.brokerCash = round2(m.brokerCash + cashDelta)
		}
	}
	pnl := realizedPnL(position, report.Price, closeQty)
	m.realizedPnL += pnl
	m.dayRealizedPnL += pnl
	m.tradesToday++
	// Consolidate partial fills into an existing closed-trade record for the
	// same opened position so the dashboard shows one round-trip trade row.
	mergeIndex := m.findMergeableClosedTradeIndex(report, position, now)
	if mergeIndex >= 0 {
		mergedTrade := m.closedTrades[mergeIndex]
		prevQty := mergedTrade.Quantity
		totalQty := prevQty + closeQty
		totalPnL := mergedTrade.PnL + round2(pnl)
		mergedTrade.ExitPrice = round2((mergedTrade.ExitPrice*float64(prevQty) + report.Price*float64(closeQty)) / float64(totalQty))
		mergedTrade.Quantity = totalQty
		mergedTrade.PnL = totalPnL
		mergedTrade.RMultiple = roundRMultiple(totalPnL, position.RiskPerShare, totalQty)
		mergedTrade.ClosedAt = now
		if mergeIndex > 0 {
			m.closedTrades = append(m.closedTrades[:mergeIndex], m.closedTrades[mergeIndex+1:]...)
			m.closedTrades = append([]domain.ClosedTrade{mergedTrade}, m.closedTrades...)
		} else {
			m.closedTrades[0] = mergedTrade
		}
		if m.recorder != nil {
			m.recorder.RecordClosedTrade(mergedTrade)
		}
		position.Quantity -= closeQty
		if position.Quantity == 0 {
			delete(m.positions, report.Symbol)
			return
		}
		position.LastPrice = report.Price
		position = updatePositionExtrema(position, report.Price)
		position.MarketValue = float64(position.Quantity) * report.Price
		position.UnrealizedPnL = unrealizedPnL(position, report.Price)
		position.UpdatedAt = now
		m.positions[report.Symbol] = position
		return
	}
	closed := domain.ClosedTrade{
		Symbol:     report.Symbol,
		Side:       position.Side,
		Quantity:   closeQty,
		EntryPrice: position.AvgPrice,
		ExitPrice:  report.Price,
		PnL:        round2(pnl),
		RMultiple:  roundRMultiple(pnl, position.RiskPerShare, closeQty),
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
	position = updatePositionExtrema(position, report.Price)
	position.MarketValue = float64(position.Quantity) * report.Price
	position.UnrealizedPnL = unrealizedPnL(position, report.Price)
	position.UpdatedAt = now
	m.positions[report.Symbol] = position
}

func (m *Manager) findMergeableClosedTradeIndex(report domain.ExecutionReport, position domain.Position, now time.Time) int {
	for i, existing := range m.closedTrades {
		if existing.Symbol != report.Symbol {
			continue
		}
		if existing.ExitReason != report.Reason {
			continue
		}
		if existing.Side != position.Side {
			continue
		}
		if !existing.OpenedAt.Equal(position.OpenedAt) {
			continue
		}
		if math.Abs(existing.EntryPrice-position.AvgPrice) > 0.011 {
			continue
		}
		if now.Sub(existing.ClosedAt) > 2*time.Minute {
			continue
		}
		return i
	}
	return -1
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
	position = updatePositionExtrema(position, price)
	position.MarketValue = float64(position.Quantity) * price
	position.UnrealizedPnL = unrealizedPnL(position, price)
	position.UpdatedAt = at.UTC()
	m.positions[symbol] = position
}

// ReconcileWithBroker aligns local positions with the broker's current open
// quantities so the dashboard stays in sync when fills complete outside the
// bot's poll window or positions are modified externally.
func (m *Manager) ReconcileWithBroker(brokerPositions map[string]domain.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for symbol := range m.positions {
		brokerPosition, open := brokerPositions[symbol]
		if !open || brokerPosition.Quantity <= 0 {
			delete(m.positions, symbol)
			m.runtime.RecordLog("warn", "portfolio", "removed stale position "+symbol+": no longer open at broker")
			continue
		}
		if domain.NormalizeDirection(brokerPosition.Side) != domain.NormalizeDirection(m.positions[symbol].Side) {
			delete(m.positions, symbol)
			m.runtime.RecordLog("warn", "portfolio", "removed stale position "+symbol+": broker side changed")
			continue
		}
		m.syncPositionQuantityLocked(symbol, brokerPosition.Quantity)
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
	position.UnrealizedPnL = unrealizedPnL(position, position.LastPrice)
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

// Positions returns a slice of all currently open positions.
func (m *Manager) Positions() []domain.Position {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Position, 0, len(m.positions))
	for _, p := range m.positions {
		out = append(out, p)
	}
	return out
}

// OpenPositionCount returns the current number of open positions.
func (m *Manager) OpenPositionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.positions)
}

// Exposure returns gross absolute exposure.
func (m *Manager) Exposure() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return round2(m.longExposureLocked() + m.shortExposureLocked())
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

// LongExposure returns gross long notional.
func (m *Manager) LongExposure() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return round2(m.longExposureLocked())
}

// ShortExposure returns gross short notional.
func (m *Manager) ShortExposure() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return round2(m.shortExposureLocked())
}

// PositionCountBySide returns the number of open positions for a direction.
func (m *Manager) PositionCountBySide(side string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, position := range m.positions {
		if domain.NormalizeDirection(position.Side) == domain.NormalizeDirection(side) {
			count++
		}
	}
	return count
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

// EffectiveCapital returns the account's cash value for non-levered risk sizing.
func (m *Manager) EffectiveCapital() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.effectiveCapitalLocked()
}

// AvailableCash returns cash currently available for new entries.
func (m *Manager) AvailableCash() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.availableCashLocked()
}

// TradesToday returns the count of fills processed today.
func (m *Manager) TradesToday() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tradesToday
}

// EntriesToday returns the count of new entries opened today.
func (m *Manager) EntriesToday() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entriesToday
}

// PendingCloseAll returns exit orders for all open positions.
func (m *Manager) PendingCloseAll(reason string) []domain.OrderRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()

	orders := make([]domain.OrderRequest, 0, len(m.positions))
	for _, position := range m.positions {
		orders = append(orders, domain.OrderRequest{
			Symbol:       position.Symbol,
			Side:         domain.CloseBrokerSide(position.Side),
			Intent:       domain.IntentClose,
			PositionSide: position.Side,
			Price:        position.LastPrice,
			Quantity:     position.Quantity,
			Reason:       reason,
			Timestamp:    time.Now().UTC(),
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
	return m.statusSnapshotLocked()
}

func (m *Manager) statusSnapshotLocked() domain.StatusSnapshot {
	var unrealized float64
	for _, position := range m.positions {
		unrealized += position.UnrealizedPnL
	}
	longExposure := m.longExposureLocked()
	shortExposure := m.shortExposureLocked()

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
		Exposure:         round2(longExposure + shortExposure),
		LongExposure:     round2(longExposure),
		ShortExposure:    round2(shortExposure),
		OpenPositions:    len(m.positions),
		TradesToday:      m.tradesToday,
		EntriesToday:     m.entriesToday,
		DailyLossLimit:   round2(m.effectiveCapitalLocked() * m.config.DailyLossLimitPct),
		MaxOpenPositions: m.config.MaxOpenPositions,
		MaxTradesPerDay:  m.config.MaxTradesPerDay,
	}
	optimizerStatus := m.runtime.OptimizerStatus()
	status.ActiveProfile = optimizerStatus.ActiveProfileName
	status.ActiveVersion = optimizerStatus.ActiveProfileVersion
	status.PendingProfile = optimizerStatus.PendingProfileName
	status.PendingVersion = optimizerStatus.PendingProfileVersion
	status.LastOptimizerRun = optimizerStatus.LastOptimizerRun
	status.PaperValidation = optimizerStatus.LastPaperValidationResult
	if m.brokerEquity > 0 {
		status.DayPnL = round2(m.dayPnL)
	}
	if m.brokerTradesKnown {
		status.TradesToday = m.brokerTradesToday
	}
	return status
}

func (m *Manager) effectiveCapitalLocked() float64 {
	return round2(m.availableCashLocked() + m.openLongCostBasisLocked())
}

func (m *Manager) availableCashLocked() float64 {
	shortCostBasis := m.openShortCostBasisLocked()
	if m.brokerCashKnown {
		return round2(math.Max(0, m.brokerCash-shortCostBasis))
	}
	cash := m.startingCapital + m.realizedPnL - m.openLongCostBasisLocked()
	if cash < 0 {
		cash = 0
	}
	return round2(cash)
}

func (m *Manager) openLongCostBasisLocked() float64 {
	var basis float64
	for _, position := range m.positions {
		if domain.IsShort(position.Side) {
			continue
		}
		basis += float64(position.Quantity) * position.AvgPrice
	}
	return round2(basis)
}

func (m *Manager) openShortCostBasisLocked() float64 {
	var basis float64
	for _, position := range m.positions {
		if !domain.IsShort(position.Side) {
			continue
		}
		basis += float64(position.Quantity) * position.AvgPrice
	}
	return round2(basis)
}

func (m *Manager) longExposureLocked() float64 {
	var exposure float64
	for _, position := range m.positions {
		if domain.IsShort(position.Side) {
			continue
		}
		exposure += position.MarketValue
	}
	return round2(exposure)
}

func (m *Manager) shortExposureLocked() float64 {
	var exposure float64
	for _, position := range m.positions {
		if !domain.IsShort(position.Side) {
			continue
		}
		exposure += position.MarketValue
	}
	return round2(exposure)
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func roundRMultiple(pnl, riskPerShare float64, quantity int64) float64 {
	if riskPerShare <= 0 || quantity <= 0 {
		return 0
	}
	totalRisk := riskPerShare * float64(quantity)
	if totalRisk <= 0 {
		return 0
	}
	return math.Round((pnl/totalRisk)*100) / 100
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
	m.brokerTradesToday = 0
	m.tradesToday = 0
	m.entriesToday = 0
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
