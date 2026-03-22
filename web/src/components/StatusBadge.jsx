export function StatusBadge({ status }) {
  if (!status) return null;

  const config = {
    running: { label: 'Running', bg: 'bg-gain/15', text: 'text-gain', dot: 'bg-gain' },
    paused: { label: 'Paused', bg: 'bg-warning/15', text: 'text-warning', dot: 'bg-warning' },
    stopped: { label: 'Stopped', bg: 'bg-loss/15', text: 'text-loss', dot: 'bg-loss' },
  };

  let key = 'running';
  if (status.emergencyStop) key = 'stopped';
  else if (status.paused) key = 'paused';

  const { label, bg, text, dot } = config[key];

  return (
    <div className={`inline-flex items-center gap-2 px-3 py-1.5 rounded-full ${bg}`}>
      <span className={`w-2 h-2 rounded-full ${dot} ${key === 'running' ? 'animate-pulse' : ''}`} />
      <span className={`text-sm font-medium ${text}`}>{label}</span>
    </div>
  );
}

export function RegimeIndicator({ regime, confidence }) {
  const regimeColors = {
    bullish: { bg: 'bg-gain/15', text: 'text-gain', bar: 'bg-gain' },
    bearish: { bg: 'bg-loss/15', text: 'text-loss', bar: 'bg-loss' },
    neutral: { bg: 'bg-surface-3', text: 'text-muted', bar: 'bg-muted' },
    mixed: { bg: 'bg-warning/15', text: 'text-warning', bar: 'bg-warning' },
  };

  const { bg, text, bar } = regimeColors[regime] || regimeColors.neutral;
  const pct = Math.min(Math.max((confidence || 0) * 100, 0), 100);

  return (
    <div className={`rounded-lg px-4 py-3 ${bg}`}>
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs text-muted uppercase tracking-wider">Market Regime</span>
        <span className={`text-sm font-medium capitalize ${text}`}>{regime || 'Unknown'}</span>
      </div>
      <div className="h-1.5 bg-surface-3 rounded-full overflow-hidden">
        <div className={`h-full rounded-full ${bar} transition-all duration-500`} style={{ width: `${pct}%` }} />
      </div>
      <span className="text-[10px] text-muted mt-1 block">{pct.toFixed(0)}% confidence</span>
    </div>
  );
}
