import { DataTable } from '../components/DataTable';
import { StatCard } from '../components/StatCard';
import { money, number, pnlColor, sideBadge, duration } from '../lib/format';

export function Trades({ closedTrades }) {
  const trades = closedTrades;
  const wins = trades.filter((t) => t.pnl >= 0);
  const losses = trades.filter((t) => t.pnl < 0);
  const totalPnl = trades.reduce((sum, t) => sum + (t.pnl || 0), 0);
  const winRate = trades.length > 0 ? (wins.length / trades.length) * 100 : 0;
  const avgWin = wins.length > 0 ? wins.reduce((s, t) => s + t.pnl, 0) / wins.length : 0;
  const avgLoss = losses.length > 0 ? losses.reduce((s, t) => s + Math.abs(t.pnl), 0) / losses.length : 0;
  const profitFactor = avgLoss > 0 ? (wins.reduce((s, t) => s + t.pnl, 0)) / losses.reduce((s, t) => s + Math.abs(t.pnl), 0) : 0;

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-semibold text-white">Closed Trades</h2>
        <p className="text-sm text-muted mt-1">{trades.length} trade{trades.length !== 1 ? 's' : ''} today</p>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
        <StatCard label="Total PnL" value={money.format(totalPnl)} tone={totalPnl >= 0 ? 'good' : 'danger'} />
        <StatCard label="Win Rate" value={`${winRate.toFixed(1)}%`} tone={winRate >= 50 ? 'good' : 'danger'} />
        <StatCard label="Avg Win" value={money.format(avgWin)} tone="good" />
        <StatCard label="Avg Loss" value={money.format(avgLoss)} tone="danger" />
        <StatCard label="Profit Factor" value={profitFactor.toFixed(2)} tone={profitFactor >= 1 ? 'good' : 'danger'} />
      </div>

      <div className="panel">
        <DataTable
          columns={['Symbol', 'Side', 'Qty', 'Entry', 'Exit', 'PnL', 'R-Multiple', 'Setup', 'Exit Reason', 'Duration']}
          rows={trades}
          emptyMessage="No trades closed yet."
          renderRow={(trade, index) => (
            <tr key={`${trade.symbol}-${index}`}>
              <td className="font-semibold text-white">{trade.symbol}</td>
              <td><span className={sideBadge(trade.side)}>{trade.side}</span></td>
              <td>{number.format(trade.quantity)}</td>
              <td>{money.format(trade.entryPrice)}</td>
              <td>{money.format(trade.exitPrice)}</td>
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
                <div className="text-muted">Entry / Exit</div><div className="text-right font-mono text-white">{money.format(trade.entryPrice)} → {money.format(trade.exitPrice)}</div>
                <div className="text-muted">R-Multiple</div><div className={`text-right font-mono ${pnlColor(trade.rMultiple)}`}>{trade.rMultiple ? `${trade.rMultiple >= 0 ? '+' : ''}${trade.rMultiple.toFixed(2)}R` : '—'}</div>
                <div className="text-muted">Setup</div><div className="text-right"><span className="badge-info">{trade.setupType || 'n/a'}</span></div>
                <div className="text-muted">Exit</div><div className="text-right text-gray-300">{trade.exitReason || '—'}</div>
                <div className="text-muted">Qty</div><div className="text-right font-mono text-white">{number.format(trade.quantity)}</div>
              </div>
            </div>
          )}
        />
      </div>
    </div>
  );
}
