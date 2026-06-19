import { useLocation } from 'react-router-dom';
import { Search, Command, Sun, Moon, Monitor, Activity, Menu } from 'lucide-react';
import { Button } from '../ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '../ui/dropdown-menu';
import { Tooltip, TooltipContent, TooltipTrigger } from '../ui/tooltip';
import { cn } from '../../lib/utils';
import { useTheme } from '../../state/theme';
import { getPageTitle } from './AppSidebar';
import { LiveIndicator } from '../LiveIndicator/LiveIndicator';
import type { ConnectionStatus } from '../../api/sse';

// AppTopbar — sticky top header. Shows the page title on the left, the live
// indicator + theme switcher + command palette trigger on the right. The
// search input here is the same as the active filter bar's "agent name…"
// input; it focuses the filter bar via the global ⌘K command palette.

export function AppTopbar({
  sseStatus,
  onOpenMobileSidebar,
  onOpenCommandPalette,
  rightSlot,
}: {
  sseStatus: ConnectionStatus;
  onOpenMobileSidebar: (() => void) | undefined;
  onOpenCommandPalette: (() => void) | undefined;
  rightSlot?: React.ReactNode;
}) {
  const location = useLocation();
  const title = getPageTitle(location.pathname);

  return (
    <header
      className={cn(
        'sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-background/80 px-4 backdrop-blur-md',
        'supports-[backdrop-filter]:bg-background/60',
      )}
    >
      {/* Mobile-only sidebar trigger */}
      <div className="lg:hidden">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              onClick={onOpenMobileSidebar}
              aria-label="Open navigation"
            >
              <Menu className="size-4" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Navigation</TooltipContent>
        </Tooltip>
      </div>

      {/* Page title */}
      <div className="flex min-w-0 flex-col leading-tight">
        <span className="truncate text-sm font-semibold tracking-tight">{title}</span>
        <span className="truncate text-[10px] uppercase tracking-wider text-muted-foreground">
          {humanizePath(location.pathname)}
        </span>
      </div>

      <div className="flex-1" />

      {/* Search — opens the command palette with focus on the search field */}
      <div className="hidden md:block md:w-72">
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              onClick={onOpenCommandPalette}
              className={cn(
                'group flex h-9 w-full items-center gap-2 rounded-md border border-border bg-card px-3 text-left text-sm text-muted-foreground',
                'hover:border-border/80 hover:text-foreground',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              )}
            >
              <Search className="size-3.5 shrink-0" aria-hidden />
              <span className="flex-1 truncate">Search sessions, ops, payloads…</span>
              <kbd className="hidden items-center gap-0.5 rounded border border-border bg-background px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground sm:inline-flex">
                <Command className="size-2.5" />K
              </kbd>
            </button>
          </TooltipTrigger>
          <TooltipContent>Open command palette</TooltipContent>
        </Tooltip>
      </div>

      <LiveIndicator status={sseStatus} compact />

      {rightSlot}

      <ThemeMenu />
    </header>
  );
}

// humanizePath turns the URL into a quiet breadcrumb-style secondary label
// shown under the page title. e.g. /sessions/abc123 → /sessions/abc123.
function humanizePath(pathname: string): string {
  if (pathname === '/') return 'home';
  return pathname.replace(/^\//, '').replace(/\//g, ' / ');
}

// ThemeMenu — three-button switcher in a dropdown. Auto follows the OS,
// Dark + Light are explicit locks.
function ThemeMenu() {
  const { preference, setPreference } = useTheme();
  // Suppress hydration mismatch: the no-flash inline script in index.html sets
  // the theme before React mounts, so during SSR there's no mismatch — but in
  // client-only code paths (Vitest jsdom without the inline script) the
  // initial preference may differ. Read from document directly so the
  // displayed icon matches whatever was actually applied.
  const resolved: 'auto' | 'dark' | 'light' =
    typeof document === 'undefined'
      ? preference
      : (document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark'
        : document.documentElement.getAttribute('data-theme') === 'light' ? 'light'
        : preference);

  const Icon = resolved === 'dark' ? Moon : resolved === 'light' ? Sun : Monitor;
  const label = resolved === 'auto' ? 'Auto' : resolved === 'dark' ? 'Dark' : 'Light';

  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" aria-label={`Theme: ${label}`}>
              <Icon className="size-4" />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>Theme</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end" className="w-40">
        <ThemeItem
          Icon={Monitor}
          label="Auto"
          description="Follow OS"
          active={resolved === 'auto'}
          onSelect={() => {
            setPreference('auto');
          }}
          testId="theme-auto"
        />
        <ThemeItem
          Icon={Moon}
          label="Dark"
          active={resolved === 'dark'}
          onSelect={() => {
            setPreference('dark');
          }}
          testId="theme-dark"
        />
        <ThemeItem
          Icon={Sun}
          label="Light"
          active={resolved === 'light'}
          onSelect={() => {
            setPreference('light');
          }}
          testId="theme-light"
        />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function ThemeItem({
  Icon,
  label,
  description,
  active,
  onSelect,
  testId,
}: {
  Icon: typeof Sun;
  label: string;
  description?: string;
  active: boolean;
  onSelect: () => void;
  testId: string;
}) {
  return (
    <DropdownMenuItem
      onSelect={onSelect}
      data-testid={testId}
      className={cn(
        'flex items-center gap-2 px-2 py-1.5 text-sm',
        active && 'bg-accent text-foreground',
      )}
    >
      <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span className="flex-1 truncate">{label}</span>
      {description ? (
        <span className="hidden text-[10px] uppercase tracking-wider text-muted-foreground lg:inline">
          {description}
        </span>
      ) : null}
      {active ? <span aria-hidden className="text-[10px] text-primary">●</span> : null}
    </DropdownMenuItem>
  );
}

// Re-export the ConnectionStatus type so consumers (Layout) can pass it down.
export type { ConnectionStatus };

// Convenience icon for the topbar LiveIndicator placement.
export const LiveIcon = Activity;