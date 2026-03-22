import { Sidebar } from './Sidebar';

export function Layout({ children, currentPage, setPage, connected }) {
  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar currentPage={currentPage} setPage={setPage} />
      <main className="flex-1 overflow-y-auto">
        <header className="sticky top-0 z-10 flex items-center justify-between px-6 py-3 bg-surface-1/80 backdrop-blur-md border-b border-surface-3">
          <h1 className="text-lg font-semibold text-white tracking-tight">
            Momentum Trading Bot
          </h1>
          <div className="flex items-center gap-3">
            <div className={`flex items-center gap-2 text-xs ${connected ? 'text-gain' : 'text-muted'}`}>
              <span className={`w-2 h-2 rounded-full ${connected ? 'bg-gain animate-pulse' : 'bg-muted'}`} />
              {connected ? 'Live' : 'Disconnected'}
            </div>
          </div>
        </header>
        <div className="p-6">{children}</div>
      </main>
    </div>
  );
}
