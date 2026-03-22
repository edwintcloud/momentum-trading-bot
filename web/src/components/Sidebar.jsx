import {
  LayoutDashboard, Crosshair, Target, BarChart3,
  ScrollText, Settings, Activity,
} from 'lucide-react';

const navItems = [
  { id: 'overview', label: 'Overview', icon: LayoutDashboard },
  { id: 'positions', label: 'Positions', icon: Target },
  { id: 'scanner', label: 'Scanner', icon: Crosshair },
  { id: 'trades', label: 'Trades', icon: BarChart3 },
  { id: 'logs', label: 'Logs', icon: ScrollText },
  { id: 'controls', label: 'Controls', icon: Settings },
];

export function Sidebar({ currentPage, setPage }) {
  return (
    <aside className="w-56 shrink-0 bg-surface-1 border-r border-surface-3 flex flex-col">
      <div className="px-4 py-5 flex items-center gap-3">
        <div className="w-8 h-8 rounded-lg bg-accent/20 flex items-center justify-center">
          <Activity className="w-4 h-4 text-accent" />
        </div>
        <div>
          <p className="text-sm font-semibold text-white leading-none">MTB</p>
          <p className="text-[10px] text-muted mt-0.5">Operator Console</p>
        </div>
      </div>

      <nav className="flex-1 px-2 py-2 space-y-0.5">
        {navItems.map(({ id, label, icon: Icon }) => {
          const active = currentPage === id;
          return (
            <button
              key={id}
              onClick={() => setPage(id)}
              className={`
                w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm transition-all duration-150
                ${active
                  ? 'bg-accent/15 text-white font-medium'
                  : 'text-gray-400 hover:text-gray-200 hover:bg-surface-2'}
              `}
            >
              <Icon className={`w-4 h-4 ${active ? 'text-accent' : ''}`} />
              {label}
            </button>
          );
        })}
      </nav>

      <div className="px-4 py-4 border-t border-surface-3">
        <p className="text-[10px] text-muted">
          Built with Go + React
        </p>
      </div>
    </aside>
  );
}
