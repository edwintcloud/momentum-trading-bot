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
        />
      </div>
    </div>
  );
}
