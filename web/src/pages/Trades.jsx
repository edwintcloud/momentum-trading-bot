import { useState, useEffect } from 'react';
import { DataTable } from '../components/DataTable';
import { StatCard } from '../components/StatCard';
import { money, number, pnlColor, sideBadge, duration } from '../lib/format';
import { Download, ChevronLeft, ChevronRight } from 'lucide-react';

export function Trades({ closedTrades }) {
  const [selectedDate, setSelectedDate] = useState(''); // '' = today (live)
  const [historicalTrades, setHistoricalTrades] = useState(null);
  const [availableDates, setAvailableDates] = useState([]);
  const [loading, setLoading] = useState(false);

  // Load available dates on mount
  useEffect(() => {
    fetch('/api/trades/dates')
      .then(r => r.json())
      .then(dates => setAvailableDates(dates || []))
      .catch(() => {});
  }, []);

  // Load trades for selected date
  useEffect(() => {
    if (!selectedDate) {
      setHistoricalTrades(null); // Use live trades
      return;
    }
    setLoading(true);
    fetch(`/api/trades/history?date=${selectedDate}`)
      .then(r => r.json())
      .then(data => { setHistoricalTrades(data || []); setLoading(false); })
      .catch(() => { setHistoricalTrades([]); setLoading(false); });
  }, [selectedDate]);

  const trades = historicalTrades !== null ? historicalTrades : closedTrades;
  const isLive = historicalTrades === null;

  const wins = trades.filter((t) => t.pnl >= 0);
  const losses = trades.filter((t) => t.pnl < 0);
  const totalPnl = trades.reduce((sum, t) => sum + (t.pnl || 0), 0);
  const winRate = trades.length > 0 ? (wins.length / trades.length) * 100 : 0;
  const avgWin = wins.length > 0 ? wins.reduce((s, t) => s + t.pnl, 0) / wins.length : 0;
  const avgLoss = losses.length > 0 ? losses.reduce((s, t) => s + Math.abs(t.pnl), 0) / losses.length : 0;
  const profitFactor = avgLoss > 0 ? (wins.reduce((s, t) => s + t.pnl, 0)) / losses.reduce((s, t) => s + Math.abs(t.pnl), 0) : 0;

  // Date navigation
  const today = new Date().toISOString().split('T')[0];
  const displayDate = selectedDate || today;

  const navigateDate = (offset) => {
    const d = new Date(displayDate + 'T12:00:00');
    d.setDate(d.getDate() + offset);
    const newDate = d.toISOString().split('T')[0];
    if (newDate === today) {
      setSelectedDate('');
    } else if (newDate <= today) {
      setSelectedDate(newDate);
    }
  };

  const handleExport = () => {
    const date = selectedDate || today;
    window.open(`/api/trades/export?date=${date}`, '_blank');
  };

  const formatTime = (ts) => {
    if (!ts) return '';
    return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  };

  return (
    <div className="space-y-6">
      {/* Header with date picker and export */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-semibold text-white">Closed Trades</h2>
          <p className="text-sm text-muted mt-1">
            {trades.length} trade{trades.length !== 1 ? 's' : ''}
            {isLive ? ' today (live)' : ` on ${displayDate}`}
          </p>
        </div>

        <div className="flex items-center gap-2">
          {/* Date navigation */}
          <div className="flex items-center gap-1 bg-surface-2 rounded-lg border border-surface-3 p-1">
            <button onClick={() => navigateDate(-1)} className="p-1.5 rounded hover:bg-surface-3 text-gray-400 hover:text-white transition-colors">
              <ChevronLeft className="w-4 h-4" />
            </button>
            <input
              type="date"
              value={displayDate}
              max={today}
              onChange={(e) => {
                const val = e.target.value;
                setSelectedDate(val === today ? '' : val);
              }}
              className="bg-transparent text-white text-sm font-mono px-2 py-1 border-0 outline-none [color-scheme:dark]"
            />
            <button onClick={() => navigateDate(1)} disabled={displayDate >= today} className="p-1.5 rounded hover:bg-surface-3 text-gray-400 hover:text-white disabled:opacity-30 transition-colors">
              <ChevronRight className="w-4 h-4" />
            </button>
            {selectedDate && (
              <button onClick={() => setSelectedDate('')} className="px-2 py-1 text-xs text-blue-400 hover:text-white transition-colors">
                Today
              </button>
            )}
          </div>

          {/* Export CSV */}
          <button onClick={handleExport} className="btn-ghost flex items-center gap-2 text-xs">
            <Download className="w-3.5 h-3.5" /> CSV
          </button>
        </div>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
        <StatCard label="Total PnL" value={money.format(totalPnl)} tone={totalPnl >= 0 ? 'good' : 'danger'} />
        <StatCard label="Win Rate" value={`${winRate.toFixed(1)}%`} tone={winRate >= 50 ? 'good' : 'danger'} />
        <StatCard label="Avg Win" value={money.format(avgWin)} tone="good" />
        <StatCard label="Avg Loss" value={money.format(avgLoss)} tone="danger" />
        <StatCard label="Profit Factor" value={profitFactor.toFixed(2)} tone={profitFactor >= 1 ? 'good' : 'danger'} />
      </div>

      {/* Trades table */}
      {loading ? (
        <div className="panel p-8 text-center text-muted text-sm">Loading trades...</div>
      ) : (
        <div className="panel">
          <DataTable
            columns={['Symbol', 'Side', 'Qty', 'Entry', 'Exit', 'PnL', 'R-Multiple', 'Setup', 'Exit Reason', 'Duration']}
            rows={trades}
            emptyMessage={isLive ? "No trades closed yet." : `No trades on ${displayDate}.`}
            renderRow={(trade, index) => (
              <tr key={`${trade.symbol}-${index}`}>
                <td className="font-semibold text-white">{trade.symbol}</td>
                <td><span className={sideBadge(trade.side)}>{trade.side}</span></td>
                <td>{number.format(trade.quantity)}</td>
                <td title={trade.openedAt ? `Entered: ${formatTime(trade.openedAt)}` : ''} className="cursor-help">
                  {money.format(trade.entryPrice)}
                </td>
                <td title={trade.closedAt ? `Exited: ${formatTime(trade.closedAt)}` : ''} className="cursor-help">
                  {money.format(trade.exitPrice)}
                </td>
                <td className={pnlColor(trade.pnl)}>{money.format(trade.pnl)}</td>
                <td className={pnlColor(trade.rMultiple)}>
                  {trade.rMultiple ? `${trade.rMultiple >= 0 ? '+' : ''}${trade.rMultiple.toFixed(2)}R` : '—'}
                </td>
                <td><span className="badge-info">{trade.setupType || 'n/a'}</span></td>
                <td className="text-gray-300">{trade.exitReason || '—'}</td>
                <td className="text-muted">
                  {trade.openedAt && trade.closedAt
                    ? duration(new Date(trade.closedAt).getTime() - new Date(trade.openedAt).getTime())
                    : '—'}
                </td>
              </tr>
            )}
            renderCard={(trade, index) => (
              <div key={`${trade.symbol}-${index}`} className="p-4 space-y-2">
                <div className="flex items-center justify-between">
                  <span className="font-semibold text-white text-base">{trade.symbol}</span>
                  <span className={`font-mono text-sm ${pnlColor(trade.pnl)}`}>{money.format(trade.pnl)}</span>
                </div>
                <div className="grid grid-cols-2 gap-y-1.5 text-sm">
                  <div className="text-muted">Side</div><div className="text-right"><span className={sideBadge(trade.side)}>{trade.side}</span></div>
                  <div className="text-muted">Entry</div><div className="text-right font-mono text-white">{money.format(trade.entryPrice)} <span className="text-muted text-xs">{formatTime(trade.openedAt)}</span></div>
                  <div className="text-muted">Exit</div><div className="text-right font-mono text-white">{money.format(trade.exitPrice)} <span className="text-muted text-xs">{formatTime(trade.closedAt)}</span></div>
                  <div className="text-muted">R-Multiple</div><div className={`text-right font-mono ${pnlColor(trade.rMultiple)}`}>{trade.rMultiple ? `${trade.rMultiple >= 0 ? '+' : ''}${trade.rMultiple.toFixed(2)}R` : '—'}</div>
                  <div className="text-muted">Setup</div><div className="text-right"><span className="badge-info">{trade.setupType || 'n/a'}</span></div>
                  <div className="text-muted">Exit</div><div className="text-right text-gray-300">{trade.exitReason || '—'}</div>
                  <div className="text-muted">Duration</div>
                  <div className="text-right text-muted">
                    {trade.openedAt && trade.closedAt
                      ? duration(new Date(trade.closedAt).getTime() - new Date(trade.openedAt).getTime())
                      : '—'}
                  </div>
                </div>
              </div>
            )}
          />
        </div>
      )}
    </div>
  );
}
