import { useEffect, useRef, useState, useCallback } from 'react';

const emptySnapshot = {
  status: {
    running: true, paused: false, emergencyStop: false,
    startingCapital: 0, brokerEquity: 0, dayPnL: 0, realizedPnL: 0,
    unrealizedPnL: 0, netPnL: 0, exposure: 0, longExposure: 0,
    shortExposure: 0, openPositions: 0, tradesToday: 0, entriesToday: 0,
    dailyLossLimit: 0, maxOpenPositions: 0, maxTradesPerDay: 0,
    activeProfile: '', activeVersion: '', pendingProfile: '', pendingVersion: '',
    lastOptimizerRun: '', paperValidation: '', currentRegime: '', regimeConfidence: 0,
  },
  marketRegime: { regime: '', confidence: 0, benchmarks: [], timestamp: '' },
  candidates: [],
  positions: [],
  closedTrades: [],
  logs: [],
  updatedAt: '',
};

function normalize(next = {}) {
  return {
    ...emptySnapshot,
    ...next,
    status: { ...emptySnapshot.status, ...(next.status || {}) },
    marketRegime: {
      ...emptySnapshot.marketRegime,
      ...(next.marketRegime || {}),
      benchmarks: Array.isArray(next.marketRegime?.benchmarks) ? next.marketRegime.benchmarks : [],
    },
    candidates: Array.isArray(next.candidates) ? next.candidates : [],
    positions: Array.isArray(next.positions) ? next.positions : [],
    closedTrades: Array.isArray(next.closedTrades) ? next.closedTrades : [],
    logs: Array.isArray(next.logs) ? next.logs : [],
  };
}

export function useWebSocket() {
  const [snapshot, setSnapshot] = useState(emptySnapshot);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState('');
  const socketRef = useRef(null);

  useEffect(() => {
    let cancelled = false;
    let reconnectTimer;

    function connect() {
      if (cancelled) return;
      const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws';
      const socket = new WebSocket(`${protocol}://${window.location.host}/ws`);
      socketRef.current = socket;

      socket.onopen = () => {
        setConnected(true);
        setError('');
      };

      socket.onmessage = (event) => {
        const next = JSON.parse(event.data);
        setSnapshot(normalize(next));
      };

      socket.onerror = () => {
        setConnected(false);
        setError('Connection lost. Reconnecting…');
      };

      socket.onclose = () => {
        setConnected(false);
        if (!cancelled) {
          reconnectTimer = setTimeout(connect, 3000);
        }
      };
    }

    // Initial REST fetch
    fetch('/api/dashboard')
      .then((r) => r.json())
      .then((data) => { if (!cancelled) setSnapshot(normalize(data)); })
      .catch((err) => { if (!cancelled) setError(err.message); });

    connect();

    return () => {
      cancelled = true;
      clearTimeout(reconnectTimer);
      if (socketRef.current) socketRef.current.close();
    };
  }, []);

  const post = useCallback(async (path) => {
    const response = await fetch(path, { method: 'POST' });
    if (!response.ok) throw new Error(`Request failed: ${response.status}`);
    return response.json();
  }, []);

  return { snapshot, connected, error, setError, post };
}
