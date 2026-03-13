import React, { useEffect, useRef, useState } from 'react';

const emptySnapshot = {
  status: {
    running: true,
    paused: false,
    emergencyStop: false,
    startingCapital: 0,
    brokerEquity: 0,
    dayPnL: 0,
    realizedPnL: 0,
    unrealizedPnL: 0,
    netPnL: 0,
    exposure: 0,
    openPositions: 0,
    tradesToday: 0,
    dailyLossLimit: 0,
    maxOpenPositions: 0,
    maxTradesPerDay: 0,
  },
  candidates: [],
  positions: [],
  closedTrades: [],
  logs: [],
  updatedAt: '',
};

const money = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 2 });
const number = new Intl.NumberFormat('en-US');

function compactVolume(value) {
  if (value >= 1_000_000_000) return (value / 1_000_000_000).toFixed(2) + ' B';
  if (value >= 1_000_000) return (value / 1_000_000).toFixed(2) + ' M';
  if (value >= 1_000) return (value / 1_000).toFixed(1) + ' K';
  return String(value);
}

async function post(path) {
  const response = await fetch(path, { method: 'POST' });
  if (!response.ok) {
    throw new Error(`Request failed: ${response.status}`);
  }
  return response.json();
}

function StatCard({ label, value, tone = 'neutral' }) {
  return (
    <section className={`stat-card tone-${tone}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </section>
  );
}

function TableSection({ title, columns, rows, renderRow, emptyMessage }) {
  return (
    <section className="panel table-panel">
      <div className="panel-header">
        <h2>{title}</h2>
      </div>
      {rows.length === 0 ? (
        <p className="empty-copy">{emptyMessage}</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                {columns.map((column) => (
                  <th key={column}>{column}</th>
                ))}
              </tr>
            </thead>
            <tbody>{rows.map(renderRow)}</tbody>
          </table>
        </div>
      )}
    </section>
  );
}

export function App() {
  const [snapshot, setSnapshot] = useState(emptySnapshot);
  const [error, setError] = useState('');
  const [logFilter, setLogFilter] = useState('all');
  const socketRef = useRef(null);

  useEffect(() => {
    let cancelled = false;

    fetch('/api/dashboard')
      .then((response) => response.json())
      .then((data) => {
        if (!cancelled) {
          setSnapshot(data);
        }
      })
      .catch((fetchError) => {
        if (!cancelled) {
          setError(fetchError.message);
        }
      });

    const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws';
    const socket = new WebSocket(`${protocol}://${window.location.host}/ws`);
    socketRef.current = socket;

    socket.onmessage = (event) => {
      const next = JSON.parse(event.data);
      setSnapshot(next);
      setError('');
    };

    socket.onerror = () => {
      setError('Live dashboard stream disconnected.');
    };

    return () => {
      cancelled = true;
      socket.close();
    };
  }, []);

  const statusTone = snapshot.status.emergencyStop ? 'danger' : snapshot.status.paused ? 'warn' : 'good';

  return (
    <main className="app-shell">
      <div className="hero-backdrop" />
      <header className="hero">
        <div>
          <p className="eyebrow">Momentum Trading Bot</p>
          <h1>Operator Console</h1>
          <p className="hero-copy">
            Real-time breakout trading with live candidates, active positions, PnL,
            structured logs, and hard trading controls.
          </p>
        </div>
        <div className="control-strip">
          <button className="ghost" onClick={() => post('/api/pause').catch((postError) => setError(postError.message))}>Pause</button>
          <button className="ghost" onClick={() => post('/api/resume').catch((postError) => setError(postError.message))}>Resume</button>
          <button className="warning" onClick={() => post('/api/close-all').catch((postError) => setError(postError.message))}>Close All</button>
          <button className="danger" onClick={() => post('/api/emergency-stop').catch((postError) => setError(postError.message))}>Emergency Stop</button>
        </div>
      </header>

      {error ? <div className="banner-error">{error}</div> : null}

      <section className="stats-grid">
        <StatCard label="System" value={snapshot.status.emergencyStop ? 'Stopped' : snapshot.status.paused ? 'Paused' : 'Running'} tone={statusTone} />
        <StatCard label="Today's PnL" value={money.format(snapshot.status.dayPnL)} tone={snapshot.status.dayPnL >= 0 ? 'good' : 'danger'} />
        <StatCard label="Exposure" value={money.format(snapshot.status.exposure)} />
        <StatCard label="Open Positions" value={number.format(snapshot.status.openPositions)} />
        <StatCard label="Trades Today" value={number.format(snapshot.status.tradesToday)} />
        <StatCard label="Daily Loss Limit" value={money.format(snapshot.status.dailyLossLimit)} tone="warn" />
      </section>

      <section className="panel status-panel">
        <div className="panel-header">
          <h2>System Status</h2>
          <span>Updated {snapshot.updatedAt ? new Date(snapshot.updatedAt).toLocaleTimeString() : 'n/a'}</span>
        </div>
        <div className="status-grid">
          <div>
            <span>Starting Capital</span>
            <strong>{money.format(snapshot.status.startingCapital)}</strong>
          </div>
          <div>
            <span>Broker Equity</span>
            <strong>{money.format(snapshot.status.brokerEquity)}</strong>
          </div>
          <div>
            <span>Realized PnL</span>
            <strong>{money.format(snapshot.status.realizedPnL)}</strong>
          </div>
          <div>
            <span>Unrealized PnL</span>
            <strong>{money.format(snapshot.status.unrealizedPnL)}</strong>
          </div>
          <div>
            <span>Local Net PnL</span>
            <strong>{money.format(snapshot.status.netPnL)}</strong>
          </div>
          <div>
            <span>Position Limit</span>
            <strong>{snapshot.status.openPositions}/{snapshot.status.maxOpenPositions}</strong>
          </div>
          <div>
            <span>Trade Limit</span>
            <strong>{snapshot.status.tradesToday}/{snapshot.status.maxTradesPerDay}</strong>
          </div>
          <div>
            <span>Emergency Stop</span>
            <strong>{snapshot.status.emergencyStop ? 'Active' : 'Inactive'}</strong>
          </div>
        </div>
      </section>

      <div className="panel-grid">
        <TableSection
          title="Scanner Candidates"
          columns={['Symbol', 'Price', 'Gap %', 'Rel Vol', 'Premarket Vol', 'Catalyst']}
          rows={snapshot.candidates}
          emptyMessage="No symbols currently satisfy the scanner filters."
          renderRow={(candidate) => (
            <tr key={candidate.symbol}>
              <td>{candidate.symbol}</td>
              <td>{money.format(candidate.price)}</td>
              <td>{candidate.gapPercent.toFixed(2)}%</td>
              <td>{candidate.relativeVolume.toFixed(2)}x</td>
              <td>{compactVolume(candidate.premarketVolume)}</td>
              <td>
                {candidate.catalystUrl ? (
                  <a href={candidate.catalystUrl} target="_blank" rel="noopener noreferrer" className="catalyst-link">
                    {candidate.catalyst || 'News'}
                  </a>
                ) : (
                  candidate.catalyst || '—'
                )}
              </td>
            </tr>
          )}
        />

        <TableSection
          title="Open Positions"
          columns={['Symbol', 'Qty', 'Avg', 'Last', 'Market Value', 'Unrealized']}
          rows={snapshot.positions}
          emptyMessage="No open positions."
          renderRow={(position) => (
            <tr key={position.symbol}>
              <td>{position.symbol}</td>
              <td>{number.format(position.quantity)}</td>
              <td>{money.format(position.avgPrice)}</td>
              <td>{money.format(position.lastPrice)}</td>
              <td>{money.format(position.marketValue)}</td>
              <td className={position.unrealizedPnL >= 0 ? 'gain' : 'loss'}>{money.format(position.unrealizedPnL)}</td>
            </tr>
          )}
        />
      </div>

      <div className="panel-grid bottom-grid">
        <TableSection
          title="Closed Trades"
          columns={['Symbol', 'Qty', 'Entry', 'Exit', 'PnL', 'Reason']}
          rows={snapshot.closedTrades.slice(0, 8)}
          emptyMessage="No trades closed yet."
          renderRow={(trade, index) => (
            <tr key={`${trade.symbol}-${index}`}>
              <td>{trade.symbol}</td>
              <td>{number.format(trade.quantity)}</td>
              <td>{money.format(trade.entryPrice)}</td>
              <td>{money.format(trade.exitPrice)}</td>
              <td className={trade.pnl >= 0 ? 'gain' : 'loss'}>{money.format(trade.pnl)}</td>
              <td>{trade.exitReason}</td>
            </tr>
          )}
        />

        <section className="panel logs-panel">
          <div className="panel-header">
            <h2>System Logs</h2>
            <div className="log-filters">
              {['all', 'info', 'warn', 'error'].map((level) => (
                <button
                  key={level}
                  className={`log-filter-btn${logFilter === level ? ' active' : ''} filter-${level}`}
                  onClick={() => setLogFilter(level)}
                >
                  {level === 'all' ? 'All' : level.charAt(0).toUpperCase() + level.slice(1)}
                </button>
              ))}
            </div>
          </div>
          <div className="log-list">
            {snapshot.logs.length === 0 ? (
              <p className="empty-copy">No logs yet.</p>
            ) : (
              snapshot.logs
                .filter((entry) => logFilter === 'all' || entry.level === logFilter)
                .map((entry, index) => (
                <article key={`${entry.timestamp}-${index}`} className={`log-entry level-${entry.level}`}>
                  <div>
                    <strong>{entry.component}</strong>
                    <span className={`log-badge level-badge-${entry.level}`}>{entry.level}</span>
                    <span>{new Date(entry.timestamp).toLocaleTimeString()}</span>
                  </div>
                  <p>{entry.message}</p>
                </article>
              ))
            )}
          </div>
        </section>
      </div>
    </main>
  );
}
