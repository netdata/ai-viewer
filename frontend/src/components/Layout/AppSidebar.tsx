import { NavLink, useLocation } from 'react-router-dom';
import {
  LayoutGrid,
  Network,
  BarChart3,
  Database,
  Bot,
  Brain,
  Wrench,
  TriangleAlert,
  AlertOctagon,
  BookOpen,
  Settings,
  Eye,
  Search,
} from 'lucide-react';
import { cn } from '../../lib/utils';
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip';
import { Separator } from '../ui/separator';

// AppSidebar — left vertical nav. Lives inside Layout. Brand + primary nav
// at the top, secondary nav + footer (version, health) at the bottom.
// On desktop (≥ lg) it docks permanently; on mobile it collapses into a
// sheet opened from the topbar (see AppTopbar).

type NavItem = {
  to: string;
  label: string;
  Icon: typeof LayoutGrid;
  description: string;
};

const PRIMARY_NAV: readonly NavItem[] = [
  { to: '/', label: 'Sessions', Icon: LayoutGrid, description: 'All sessions, live + history' },
  { to: '/failures', label: 'Recent failures', Icon: TriangleAlert, description: 'Failed, abandoned, interrupted sessions' },
  { to: '/ingest-errors', label: 'Ingest errors', Icon: AlertOctagon, description: 'Parse errors + ingest health per source' },
  { to: '/topology', label: 'Topology', Icon: Network, description: 'Actor + tool call graph' },
  { to: '/stats', label: 'Statistics', Icon: BarChart3, description: 'Cost, tokens, failures over time' },
  { to: '/sources', label: 'Sources', Icon: Database, description: 'Configured source paths and health' },
  { to: '/search', label: 'Search', Icon: Search, description: 'Full-text search across ops, logs, prompts' },
];

const SECONDARY_NAV: readonly NavItem[] = [
  { to: '/agents', label: 'Agents', Icon: Bot, description: 'Agent-name drill-down' },
  { to: '/models', label: 'Models', Icon: Brain, description: 'Model usage breakdown' },
  { to: '/tools', label: 'Tools', Icon: Wrench, description: 'Tool usage breakdown' },
];

// Page title lookup. The topbar reads the current pathname and displays
// this; the sidebar uses it to mark the active item unambiguously.
export function getPageTitle(pathname: string): string {
  if (pathname === '/') return 'Sessions';
  if (pathname.startsWith('/failures')) return 'Recent failures';
  if (pathname.startsWith('/ingest-errors')) return 'Ingest errors';
  if (pathname.startsWith('/topology')) return 'Topology';
  if (pathname.startsWith('/stats')) return 'Statistics';
  if (pathname.startsWith('/sources')) return 'Sources';
  if (pathname.startsWith('/search')) return 'Search';
  if (pathname.startsWith('/agents')) return 'Agents';
  if (pathname.startsWith('/models')) return 'Models';
  if (pathname.startsWith('/tools')) return 'Tools';
  if (pathname.startsWith('/sessions/')) return 'Session detail';
  return 'ai-viewer';
}

export function AppSidebar({
  healthOk,
  version,
  onNavigate,
}: {
  healthOk: boolean;
  version: string;
  onNavigate: (() => void) | undefined;
}) {
  const location = useLocation();

  return (
    <aside
      aria-label="Primary"
      className={cn(
        'flex h-full w-60 shrink-0 flex-col border-r border-border bg-card',
        'text-foreground',
      )}
    >
      {/* Brand */}
      <div className="flex h-14 items-center gap-2 border-b border-border px-4">
        <div
          aria-hidden
          className="grid size-7 place-items-center rounded-md bg-primary text-primary-foreground"
        >
          <Eye className="size-4" />
        </div>
        <div className="flex flex-col leading-tight">
          <span className="text-sm font-semibold tracking-tight">ai-viewer</span>
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
            session explorer
          </span>
        </div>
      </div>

      {/* Primary nav */}
      <nav className="flex-1 overflow-y-auto px-2 py-3" aria-label="Primary">
        <NavGroup items={PRIMARY_NAV} pathname={location.pathname} onNavigate={onNavigate} />
        <Separator className="my-3" />
        <p
          className="px-2 pb-1 text-[10px] font-medium uppercase tracking-wider text-muted-foreground"
          aria-hidden
        >
          Drill-down
        </p>
        <NavGroup items={SECONDARY_NAV} pathname={location.pathname} onNavigate={onNavigate} />
      </nav>

      {/* Footer: health + version */}
      <div className="border-t border-border p-3">
        <Tooltip>
          <TooltipTrigger asChild>
            <a
              href="http://127.0.0.1:7710/api/health"
              target="_blank"
              rel="noreferrer"
              className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              <span className="relative flex size-2">
                <span
                  className={cn(
                    'absolute inline-flex size-full rounded-full opacity-75',
                    healthOk ? 'bg-status-completed' : 'bg-status-failed',
                    healthOk && 'animate-ping',
                  )}
                  aria-hidden
                />
                <span
                  className={cn(
                    'relative inline-flex size-2 rounded-full',
                    healthOk ? 'bg-status-completed' : 'bg-status-failed',
                  )}
                  aria-hidden
                />
              </span>
              <span>{healthOk ? 'All sources healthy' : 'Some sources degraded'}</span>
            </a>
          </TooltipTrigger>
          <TooltipContent side="top">Open /api/health</TooltipContent>
        </Tooltip>

        <a
          href="https://github.com/netdata/ai-viewer"
          target="_blank"
          rel="noreferrer"
          className="mt-1 flex items-center gap-2 rounded-md px-2 py-1.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <BookOpen className="size-3.5" aria-hidden />
          <span>Docs</span>
        </a>

        <div className="mt-1 flex items-center gap-2 rounded-md px-2 py-1.5 text-[10px] tabular-nums text-muted-foreground">
          <Settings className="size-3" aria-hidden />
          <span className="font-mono">v{version}</span>
        </div>
      </div>
    </aside>
  );
}

function NavGroup({
  items,
  pathname,
  onNavigate,
}: {
  items: readonly NavItem[];
  pathname: string;
  onNavigate: (() => void) | undefined;
}) {
  return (
    <ul className="flex flex-col gap-0.5">
      {items.map((item) => {
        const isActive =
          item.to === '/'
            ? pathname === '/'
            : pathname === item.to || pathname.startsWith(`${item.to}/`);
        return (
          <li key={item.to}>
            <Tooltip>
              <TooltipTrigger asChild>
                <NavLink
                  to={item.to}
                  end={item.to === '/'}
                  onClick={onNavigate}
                  className={cn(
                    'group relative flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors',
                    isActive
                      // SOW-0078 bugfix: bg-accent + text-foreground in dark mode
                      // resolved to light blue + white, which was unreadable.
                      // bg-primary + text-primary-foreground resolves to the
                      // saturated brand blue + black in dark (high contrast)
                      // and brand blue + white in light (high contrast).
                      ? 'bg-primary text-primary-foreground'
                      : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
                  )}
                  aria-current={isActive ? 'page' : undefined}
                >
                  {isActive ? (
                    <span
                      aria-hidden
                      className="absolute inset-y-1.5 left-0 w-0.5 rounded-r-full bg-foreground/80"
                    />
                  ) : null}
                  <item.Icon
                    aria-hidden
                    className={cn(
                      'size-4 shrink-0',
                      isActive ? 'text-foreground' : 'text-muted-foreground group-hover:text-foreground',
                    )}
                  />
                  <span className="truncate">{item.label}</span>
                </NavLink>
              </TooltipTrigger>
              <TooltipContent side="right">{item.description}</TooltipContent>
            </Tooltip>
          </li>
        );
      })}
    </ul>
  );
}