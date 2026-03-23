import { DataTable } from '../components/DataTable';
import { money, number, pnlColor, sideBadge, duration } from '../lib/format';

export function Positions({ positions }) {
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-semibold text-white">Open Positions</h2>
        <p className="text-sm text-muted mt-1">{positions.length} position{positions.length !== 1 ? 's' : ''} open</p>
      </div>

      <div className="panel">
        <DataTable
          columns={['Symbol', 'Side', 'Qty', 'Avg Price', 'Last', 'Stop', 'Market Value', 'Unrealized PnL', 'Setup', 'Held']}
          rows={positions}
          emptyMessage="No open positions."
          renderRow={(pos) => (
            <tr key={pos.symbol}>
              <td className="font-semibold text-white">{pos.symbol}</td>
              <td><span className={sideBadge(pos.side)}>{pos.side}</span></td>
              <td>{number.format(pos.quantity)}</td>
              <td>{money.format(pos.avgPrice)}</td>
              <td>{money.format(pos.lastPrice)}</td>
              <td className="text-warning">{money.format(pos.stopPrice)}</td>
              <td>{money.format(pos.marketValue)}</td>
              <td className={pnlColor(pos.unrealizedPnL)}>{money.format(pos.unrealizedPnL)}</td>
              <td><span className="badge-info">{pos.setupType || 'n/a'}</span></td>
              <td className="text-muted">{pos.openedAt ? duration(Date.now() - new Date(pos.openedAt).getTime()) : '—'}</td>
            </tr>
          )}
          renderCard={(pos) => (
            <div key={pos.symbol} className="p-4 space-y-2">
              <div className="flex items-center justify-between">
                <span className="font-semibold text-white text-base">{pos.symbol}</span>
                <span className={sideBadge(pos.side)}>{pos.side}</span>
              </div>
              <div className="grid grid-cols-2 gap-y-1.5 text-sm">
                <div className="text-muted">Qty</div><div className="text-right font-mono text-white">{number.format(pos.quantity)}</div>
                <div className="text-muted">Entry</div><div className="text-right font-mono text-white">{money.format(pos.avgPrice)}</div>
                <div className="text-muted">Last</div><div className="text-right font-mono text-white">{money.format(pos.lastPrice)}</div>
                <div className="text-muted">PnL</div><div className={`text-right font-mono ${pnlColor(pos.unrealizedPnL)}`}>{money.format(pos.unrealizedPnL)}</div>
                <div className="text-muted">Stop</div><div className="text-right font-mono text-warning">{money.format(pos.stopPrice)}</div>
                <div className="text-muted">Setup</div><div className="text-right"><span className="badge-info">{pos.setupType || 'n/a'}</span></div>
              </div>
            </div>
          )}
        />
      </div>
    </div>
  );
}
