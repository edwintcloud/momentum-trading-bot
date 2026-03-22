import { LogViewer } from '../components/LogViewer';

export function Logs({ logs }) {
  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-semibold text-white">System Logs</h2>
        <p className="text-sm text-muted mt-1">{logs.length} log entries</p>
      </div>
      <LogViewer logs={logs} />
    </div>
  );
}
