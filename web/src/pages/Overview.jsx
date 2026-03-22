import { StatCard } from '../components/StatCard';
import { StatusBadge, RegimeIndicator } from '../components/StatusBadge';
import { PnlChart } from '../components/PnlChart';
import { money, number } from '../lib/format';

export function Overview({ status, marketRegime, closedTrades, updatedAt }) {
  const statusTone = status.emergencyStop ? 'danger' : status.paused ? 'warn' : 'good';

  // Build PnL chart data from closed trades
  const chartData = closedTrades.reduce((acc, trade) => {
    const prev = acc.length > 0 ? acc[acc.length - 1].pnl : 0;
    acc.push({
      time: trade.closedAt ? new Date(trade.closedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : `T${acc.length}`,
      pnl: prev + (trade.pnl || 0),
    });
    return acc;
  }, []);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-semibold text-white">Dashboard Overview</h2>
          <p className="text-sm text-muted mt-1">
            Last updated {updatedAt ? new Date(updatedAt).toLocaleTimeString() : 'n/a'}
          </p>
        </div>
        <StatusBadge status={status} />
      </div>

      <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-3">
        <StatCard
          label="Day PnL"
          value={money.format(status.dayPnL)}
          tone={status.dayPnL >= 0 ? 'good' : 'danger'}
        />
        <StatCard
          label="Net PnL"
          value={money.format(status.netPnL)}
          tone={status.netPnL >= 0 ? 'good' : 'danger'}
        />
        <StatCard
          label="Broker Equity"
          value={money.format(status.brokerEquity)}
        />
        <StatCard
          label="Exposure"
          value={money.format(status.exposure)}
          subtitle={`L: ${money.format(status.longExposure)} / S: ${money.format(status.shortExposure)}`}
        />
        <StatCard
          label="Open Positions"
          value={`${number.format(status.openPositions)} / ${status.maxOpenPositions}`}
        />
      </div>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <StatCard label="Realized PnL" value={money.format(status.realizedPnL)} tone={status.realizedPnL >= 0 ? 'good' : 'danger'} />
        <StatCard label="Unrealized PnL" value={money.format(status.unrealizedPnL)} tone={status.unrealizedPnL >= 0 ? 'good' : 'danger'} />
        <StatCard label="Trades Today" value={`${number.format(status.tradesToday)} / ${status.maxTradesPerDay}`} />
        <StatCard label="Daily Loss Limit" value={money.format(status.dailyLossLimit)} tone="warn" />
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <div className="lg:col-span-2 panel">
          <div className="panel-header">
            <h3 className="text-sm font-medium text-white">Intraday PnL</h3>
          </div>
          <div className="p-4">
            <PnlChart data={chartData} />
          </div>
        </div>

        <div className="space-y-4">
          <RegimeIndicator regime={marketRegime.regime} confidence={marketRegime.confidence} />

          <div className="panel">
            <div className="panel-header">
              <h3 className="text-sm font-medium text-white">Trading Profile</h3>
            </div>
            <div className="p-4 space-y-3 text-sm">
              <div className="flex justify-between">
                <span className="text-muted">Active Profile</span>
                <span className="text-white font-medium font-mono">
                  {status.activeProfile || 'Built-in baseline'}
                </span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted">Version</span>
                <span className="text-gray-300 font-mono">{status.activeVersion || 'n/a'}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted">Pending Candidate</span>
                <span className="text-gray-300 font-mono">
                  {status.pendingProfile ? `${status.pendingProfile} (${status.pendingVersion})` : 'None'}
                </span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted">Paper Validation</span>
                <span className="text-gray-300">{status.paperValidation || 'n/a'}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      {marketRegime.benchmarks && marketRegime.benchmarks.length > 0 && (
        <div className="panel">
          <div className="panel-header">
            <h3 className="text-sm font-medium text-white">Market Regime Benchmarks</h3>
            <span className="text-xs text-muted">
              {marketRegime.timestamp ? new Date(marketRegime.timestamp).toLocaleTimeString() : ''}
            </span>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-0 divide-y md:divide-y-0 md:divide-x divide-surface-3">
            {marketRegime.benchmarks.map((b) => (
              <div key={b.symbol} className="p-4">
                <span className="text-white font-medium">{b.symbol}</span>
                <div className="mt-2 space-y-1 text-xs text-gray-400 font-mono">
                  <div>VWAP: <span className="text-gray-200">{b.priceVsVwapPct?.toFixed(2)}%</span></div>
                  <div>EMA: <span className="text-gray-200">{b.emaFast?.toFixed(2)} / {b.emaSlow?.toFixed(2)}</span></div>
                  <div>30m Return: <span className="text-gray-200">{b.returnLookbackPct?.toFixed(2)}%</span></div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
