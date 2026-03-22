import { useState } from 'react';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { StatusBadge } from '../components/StatusBadge';
import { money, number } from '../lib/format';
import { Pause, Play, X, AlertOctagon } from 'lucide-react';

export function Controls({ status, post, setError }) {
  const [confirm, setConfirm] = useState(null);

  const action = async (path) => {
    try {
      await post(path);
      setConfirm(null);
    } catch (err) {
      setError(err.message);
      setConfirm(null);
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-semibold text-white">Trading Controls</h2>
        <p className="text-sm text-muted mt-1">Manage the live trading session</p>
      </div>

      <div className="flex items-center gap-4">
        <StatusBadge status={status} />
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div className="panel p-6 space-y-4">
          <h3 className="text-sm font-medium text-white">Session Controls</h3>
          <div className="flex gap-3">
            <button
              onClick={() => action('/api/pause')}
              disabled={status.paused || status.emergencyStop}
              className="btn-warning flex items-center gap-2"
            >
              <Pause className="w-4 h-4" /> Pause Trading
            </button>
            <button
              onClick={() => action('/api/resume')}
              disabled={!status.paused || status.emergencyStop}
              className="btn-primary flex items-center gap-2"
            >
              <Play className="w-4 h-4" /> Resume Trading
            </button>
          </div>
        </div>

        <div className="panel p-6 space-y-4">
          <h3 className="text-sm font-medium text-white">Position Management</h3>
          <div className="flex gap-3">
            <button
              onClick={() => setConfirm({
                title: 'Close All Positions',
                message: 'This will close all open positions at market price. This action cannot be undone.',
                action: '/api/close-all',
                label: 'Close All',
                cls: 'btn-warning',
              })}
              disabled={status.emergencyStop}
              className="btn-warning flex items-center gap-2"
            >
              <X className="w-4 h-4" /> Close All Positions
            </button>
          </div>
        </div>

        <div className="panel p-6 space-y-4 md:col-span-2 border-red-600/20">
          <h3 className="text-sm font-medium text-loss">Emergency Controls</h3>
          <p className="text-xs text-gray-400">
            Emergency stop halts all trading permanently, closes all positions, and prevents any new entries.
            This cannot be reversed without a system restart.
          </p>
          <button
            onClick={() => setConfirm({
              title: 'Emergency Stop',
              message: 'This will immediately stop all trading, close all positions, and prevent any new entries. The system must be restarted to resume. Are you sure?',
              action: '/api/emergency-stop',
              label: 'Emergency Stop',
              cls: 'btn-danger',
            })}
            disabled={status.emergencyStop}
            className="btn-danger flex items-center gap-2"
          >
            <AlertOctagon className="w-4 h-4" /> Emergency Stop
          </button>
        </div>
      </div>

      <div className="panel">
        <div className="panel-header">
          <h3 className="text-sm font-medium text-white">System Details</h3>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-0 divide-y md:divide-y-0">
          {[
            ['Starting Capital', money.format(status.startingCapital)],
            ['Broker Equity', money.format(status.brokerEquity)],
            ['Realized PnL', money.format(status.realizedPnL)],
            ['Unrealized PnL', money.format(status.unrealizedPnL)],
            ['Net PnL', money.format(status.netPnL)],
            ['Positions', `${status.openPositions}/${status.maxOpenPositions}`],
            ['Entries Today', `${status.entriesToday}/${status.maxTradesPerDay}`],
            ['Broker Fills', number.format(status.tradesToday)],
            ['Regime', status.currentRegime || 'n/a'],
            ['Regime Confidence', status.regimeConfidence ? status.regimeConfidence.toFixed(2) : 'n/a'],
            ['Active Profile', status.activeProfile || 'Built-in baseline'],
            ['Profile Version', status.activeVersion || 'n/a'],
            ['Last Optimizer', status.lastOptimizerRun && !status.lastOptimizerRun.startsWith('0001')
              ? new Date(status.lastOptimizerRun).toLocaleString()
              : 'Not run'],
            ['Paper Validation', status.paperValidation || 'n/a'],
          ].map(([label, value]) => (
            <div key={label} className="px-4 py-3 flex flex-col">
              <span className="text-xs text-muted">{label}</span>
              <span className="text-sm text-white font-mono mt-0.5">{value}</span>
            </div>
          ))}
        </div>
      </div>

      <ConfirmDialog
        open={!!confirm}
        title={confirm?.title || ''}
        message={confirm?.message || ''}
        confirmLabel={confirm?.label}
        confirmClass={confirm?.cls}
        onConfirm={() => confirm && action(confirm.action)}
        onCancel={() => setConfirm(null)}
      />
    </div>
  );
}
