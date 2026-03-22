export function StatCard({ label, value, tone = 'neutral', subtitle }) {
  const toneColors = {
    good: 'border-gain/30 bg-gain/5',
    danger: 'border-loss/30 bg-loss/5',
    warn: 'border-warning/30 bg-warning/5',
    neutral: 'border-surface-3 bg-surface-2',
  };

  const valueColors = {
    good: 'text-gain',
    danger: 'text-loss',
    warn: 'text-warning',
    neutral: 'text-white',
  };

  return (
    <div className={`stat-card ${toneColors[tone] || toneColors.neutral}`}>
      <span className="text-xs text-muted font-medium uppercase tracking-wider">{label}</span>
      <strong className={`text-xl font-semibold font-mono ${valueColors[tone] || valueColors.neutral}`}>
        {value}
      </strong>
      {subtitle && <span className="text-[11px] text-muted">{subtitle}</span>}
    </div>
  );
}
