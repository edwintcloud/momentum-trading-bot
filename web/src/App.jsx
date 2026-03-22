import { useState } from 'react';
import { useWebSocket } from './hooks/useWebSocket';
import { Layout } from './components/Layout';
import { Overview } from './pages/Overview';
import { Positions } from './pages/Positions';
import { Scanner } from './pages/Scanner';
import { Trades } from './pages/Trades';
import { Logs } from './pages/Logs';
import { Controls } from './pages/Controls';

export function App() {
  const [page, setPage] = useState('overview');
  const { snapshot, connected, error, setError, post } = useWebSocket();

  const { status, marketRegime, candidates, positions, closedTrades, logs, updatedAt } = snapshot;

  function renderPage() {
    switch (page) {
      case 'overview':
        return (
          <Overview
            status={status}
            marketRegime={marketRegime}
            closedTrades={closedTrades}
            updatedAt={updatedAt}
          />
        );
      case 'positions':
        return <Positions positions={positions} />;
      case 'scanner':
        return <Scanner candidates={candidates} />;
      case 'trades':
        return <Trades closedTrades={closedTrades} />;
      case 'logs':
        return <Logs logs={logs} />;
      case 'controls':
        return <Controls status={status} post={post} />;
      default:
        return null;
    }
  }

  return (
    <Layout currentPage={page} setPage={setPage} connected={connected}>
      {error && (
        <div className="mb-4 px-4 py-3 rounded-lg bg-loss/10 border border-loss/30 text-loss text-sm flex items-center justify-between">
          <span>{error}</span>
          <button
            onClick={() => setError('')}
            className="ml-4 text-loss/60 hover:text-loss transition-colors"
          >
            ✕
          </button>
        </div>
      )}
      {renderPage()}
    </Layout>
  );
}
