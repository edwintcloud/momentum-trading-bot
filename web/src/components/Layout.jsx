import { useState, useEffect } from 'react';
import { Sidebar } from './Sidebar';
import { Menu, X } from 'lucide-react';

export function Layout({ children, currentPage, setPage, connected }) {
  const [sidebarOpen, setSidebarOpen] = useState(false);

  // Close sidebar on navigation (mobile)
  const handleSetPage = (page) => {
    setPage(page);
    setSidebarOpen(false);
  };

  // Close on Escape key
  useEffect(() => {
    const handler = (e) => { if (e.key === 'Escape') setSidebarOpen(false); };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, []);

  return (
    <div className="flex h-screen overflow-hidden bg-surface-0">
      {/* Mobile backdrop */}
      {sidebarOpen && (
        <div
          className="fixed inset-0 z-30 bg-black/50 md:hidden"
          onClick={() => setSidebarOpen(false)}
        />
      )}

      {/* Sidebar — overlay on mobile, static on desktop */}
      <div className={`
        fixed inset-y-0 left-0 z-40 w-56 transform transition-transform duration-200 ease-in-out
        md:relative md:translate-x-0 md:z-auto
        ${sidebarOpen ? 'translate-x-0' : '-translate-x-full'}
      `}>
        <Sidebar currentPage={currentPage} setPage={handleSetPage} />
      </div>

      {/* Main content */}
      <main className="flex-1 overflow-y-auto min-w-0">
        <header className="sticky top-0 z-20 flex items-center justify-between px-4 md:px-6 py-3 bg-surface-1/80 backdrop-blur-md border-b border-surface-3">
          <div className="flex items-center gap-3">
            {/* Hamburger — mobile only */}
            <button
              onClick={() => setSidebarOpen(!sidebarOpen)}
              className="md:hidden p-1.5 rounded-lg text-gray-400 hover:text-white hover:bg-surface-2 transition-colors"
            >
              {sidebarOpen ? <X className="w-5 h-5" /> : <Menu className="w-5 h-5" />}
            </button>
            <h1 className="text-lg font-semibold text-white tracking-tight">
              Momentum Trading Bot
            </h1>
          </div>
          <div className="flex items-center gap-3">
            <div className={`flex items-center gap-2 text-xs ${connected ? 'text-gain' : 'text-muted'}`}>
              <span className={`w-2 h-2 rounded-full ${connected ? 'bg-gain animate-pulse' : 'bg-muted'}`} />
              {connected ? 'Live' : 'Disconnected'}
            </div>
          </div>
        </header>
        <div className="p-4 md:p-6">{children}</div>
      </main>
    </div>
  );
}
