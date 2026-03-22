import { DataTable } from '../components/DataTable';
import { money, compactVolume, sideBadge } from '../lib/format';

export function Scanner({ candidates }) {
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-semibold text-white">Scanner Candidates</h2>
        <p className="text-sm text-muted mt-1">{candidates.length} candidate{candidates.length !== 1 ? 's' : ''} passing filters</p>
      </div>

      <div className="panel">
        <DataTable
          columns={['Symbol', 'Direction', 'Score', 'Price', 'Gap %', 'Rel Vol', 'PM Vol', 'VWAP %', 'Regime', 'Playbook', 'Catalyst']}
          rows={candidates}
          emptyMessage="No symbols currently satisfy the scanner filters."
          renderRow={(c) => (
            <tr key={c.symbol}>
              <td className="font-semibold text-white">{c.symbol}</td>
              <td><span className={sideBadge(c.direction)}>{c.direction}</span></td>
              <td>
                <div className="flex items-center gap-2">
                  <div className="w-12 h-1.5 bg-surface-3 rounded-full overflow-hidden">
                    <div
                      className="h-full bg-accent rounded-full"
                      style={{ width: `${Math.min((c.score / 6) * 100, 100)}%` }}
                    />
                  </div>
                  <span className="text-accent">{c.score?.toFixed(1)}</span>
                </div>
              </td>
              <td>{money.format(c.price)}</td>
              <td className={c.gapPercent >= 0 ? 'text-gain' : 'text-loss'}>{c.gapPercent?.toFixed(2)}%</td>
              <td>{c.relativeVolume?.toFixed(2)}x</td>
              <td>{compactVolume(c.premarketVolume)}</td>
              <td>{c.priceVsVwapPct?.toFixed(2)}%</td>
              <td><span className="badge-info">{c.marketRegime || 'n/a'}</span></td>
              <td className="text-gray-300">{c.playbook || 'n/a'}</td>
              <td>
                {c.catalystUrl ? (
                  <a href={c.catalystUrl} target="_blank" rel="noopener noreferrer" className="text-accent hover:underline">
                    {c.catalyst || 'News'}
                  </a>
                ) : (
                  <span className="text-muted">{c.catalyst || '—'}</span>
                )}
              </td>
            </tr>
          )}
        />
      </div>
    </div>
  );
}
