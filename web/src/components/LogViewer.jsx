import { useState, useRef, useEffect } from 'react';

const levels = ['all', 'info', 'warn', 'error'];

export function LogViewer({ logs }) {
  const [filter, setFilter] = useState('all');
  const [autoScroll, setAutoScroll] = useState(true);
  const listRef = useRef(null);

  const filtered = filter === 'all' ? logs : logs.filter((e) => e.level === filter);

  useEffect(() => {
    if (autoScroll && listRef.current) {
      listRef.current.scrollTop = listRef.current.scrollHeight;
    }
  }, [filtered.length, autoScroll]);

  const handleScroll = () => {
    if (!listRef.current) return;
    const { scrollTop, scrollHeight, clientHeight } = listRef.current;
    setAutoScroll(scrollHeight - scrollTop - clientHeight < 50);
  };

  const levelBadge = (level) => {
    switch (level) {
      case 'error': return 'badge-error';
      case 'warn': return 'badge-warn';
      default: return 'badge-info';
    }
  };

  return (
    <div className="panel">
      <div className="panel-header">
        <h2 className="text-sm font-medium text-white">System Logs</h2>
        <div className="flex gap-1">
          {levels.map((level) => (
            <button
              key={level}
              onClick={() => setFilter(level)}
              className={`
                px-2.5 py-1 rounded text-xs font-medium transition-all
                ${filter === level
                  ? 'bg-accent/20 text-accent'
                  : 'text-muted hover:text-gray-300 hover:bg-surface-3'}
              `}
            >
              {level.charAt(0).toUpperCase() + level.slice(1)}
            </button>
          ))}
        </div>
      </div>
      <div
        ref={listRef}
        onScroll={handleScroll}
        className="h-80 overflow-y-auto px-4 py-2 space-y-1"
      >
        {filtered.length === 0 ? (
          <p className="text-muted text-sm text-center py-8">No logs yet.</p>
        ) : (
          filtered.map((entry, i) => (
            <div
              key={`${entry.timestamp}-${i}`}
              className="flex items-start gap-3 py-1.5 text-xs border-b border-surface-3/30 last:border-0"
            >
              <span className="text-muted font-mono shrink-0 w-20">
                {entry.timestamp ? new Date(entry.timestamp).toLocaleTimeString() : '—'}
              </span>
              <span className={levelBadge(entry.level)}>{entry.level}</span>
              <span className="text-gray-400 font-medium shrink-0 w-20">{entry.component}</span>
              <span className="text-gray-300">{entry.message}</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
