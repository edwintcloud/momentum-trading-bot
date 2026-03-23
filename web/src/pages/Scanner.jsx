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
          renderCard={(c) => (
            <div key={c.symbol} className="p-4 space-y-2">
              <div className="flex items-center justify-between">
                <span className="font-semibold text-white text-base">{c.symbol}</span>
                <div className="flex items-center gap-2">
                  <span className={sideBadge(c.direction)}>{c.direction}</span>
                  <span className="text-accent font-mono text-sm">{c.score?.toFixed(1)}</span>
                </div>
              </div>
              <div className="grid grid-cols-2 gap-y-1.5 text-sm">
                <div className="text-muted">Price</div><div className="text-right font-mono text-white">{money.format(c.price)}</div>
                <div className="text-muted">Gap</div><div className={`text-right font-mono ${c.gapPercent >= 0 ? 'text-gain' : 'text-loss'}`}>{c.gapPercent?.toFixed(2)}%</div>
                <div className="text-muted">Rel Vol</div><div className="text-right font-mono text-white">{c.relativeVolume?.toFixed(2)}x</div>
                <div className="text-muted">VWAP %</div><div className="text-right font-mono text-white">{c.priceVsVwapPct?.toFixed(2)}%</div>
                <div className="text-muted">Regime</div><div className="text-right"><span className="badge-info">{c.marketRegime || 'n/a'}</span></div>
                <div className="text-muted">Playbook</div><div className="text-right text-gray-300">{c.playbook || 'n/a'}</div>
              </div>
            </div>
          )}
        />
      </div>
    </div>
  );
}
