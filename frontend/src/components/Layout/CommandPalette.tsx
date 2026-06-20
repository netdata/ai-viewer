import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  LayoutGrid,
  Network,
  BarChart3,
  Database,
  Bot,
  Brain,
  Wrench,
  Moon,
  Sun,
  Monitor,
  Eye,
  CornerDownLeft,
  Search,
  FileText,
} from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from '../ui/dialog';
import { Input } from '../ui/input';
import { Skeleton } from '../ui/skeleton';
import { useSearch } from '../../api/stats';
import { cn } from '../../lib/utils';
import { THEME_PREFERENCE_STORAGE_NAME } from '../../state/theme';

// CommandPalette — global ⌘K palette (SOW-0074.1).
//
// Built without cmdk because cmdk 1.1.1 has a runtime incompatibility with
// the radix-ui 1.6.0 unified package (the dialog open-state subscription
// crashes with "Cannot read properties of undefined (reading 'subscribe')")
// that was first caught during SOW-0073. This is a custom Dialog + Input
// implementation that gives us the same UX with zero new dependencies.
//
// Keyboard shortcuts:
//   - ⌘K / Ctrl-K: toggle the palette
//   - Arrow Up / Arrow Down: move the active row
//   - Enter: invoke the active row
//   - Esc: close (handled by Radix Dialog primitive)

type ThemePref = 'auto' | 'dark' | 'light';

interface Command {
  id: string;
  label: string;
  hint?: string | undefined;
  group: 'Navigate' | 'Theme' | 'About';
  Icon: typeof LayoutGrid;
  shortcut?: string | undefined;
  run: () => void;
}

export function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (next: boolean) => void;
}) {
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  // ⌘K / Ctrl-K toggles the palette (Esc is handled by Radix Dialog).
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        onOpenChange(!open);
      }
    };
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('keydown', onKey);
    };
  }, [open, onOpenChange]);

  // Focus the input on open + reset the query/active row.
  // Reset query/active row + focus the input when the dialog toggles. The
  // explicit `open` change handler is the cleanest place for this: we don't
  // need a useEffect for state derived from another piece of state.
  const onOpenChangeWithReset = (next: boolean): void => {
    if (next) {
      setQuery('');
      setActiveIndex(0);
      // Focus the input after the dialog mounts.
      setTimeout(() => inputRef.current?.focus(), 30);
    } else {
      setQuery('');
      setActiveIndex(0);
    }
    onOpenChange(next);
  };

  // The set of commands. Navigation runs the route push; theme commands write
  // the data-theme + localStorage mirror (so the no-flash inline script keeps
  // finding the preference on next load).
  const commands: Command[] = useMemo<Command[]>(() => {
    const setTheme = (theme: ThemePref): (() => void) => {
      return (): void => {
        if (typeof window === 'undefined') return;
        try {
          if (theme === 'auto') {
            window.localStorage.removeItem(THEME_PREFERENCE_STORAGE_NAME);
          } else {
            window.localStorage.setItem(THEME_PREFERENCE_STORAGE_NAME, theme);
          }
        } catch {
          /* storage disabled */
        }
        const dark = theme === 'dark' || (theme === 'auto' && window.matchMedia('(prefers-color-scheme: dark)').matches);
        document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
      };
    };
    const go = (path: string) => (): void => {
      onOpenChange(false);
      void navigate(path);
    };
    const noop = (): void => { /* informational */ };
    return [
      { id: 'nav-sessions',  label: 'Sessions',  hint: 'All sessions, live + history',     group: 'Navigate', Icon: LayoutGrid, shortcut: 'G S', run: go('/') },
      { id: 'nav-topology',  label: 'Topology',  hint: 'Actor + tool call graph',         group: 'Navigate', Icon: Network,    shortcut: 'G T', run: go('/topology') },
      { id: 'nav-stats',     label: 'Statistics', hint: 'Cost, tokens, failures over time', group: 'Navigate', Icon: BarChart3,   shortcut: 'G A', run: go('/stats') },
      { id: 'nav-sources',   label: 'Sources',   hint: 'Configured source paths + health', group: 'Navigate', Icon: Database,   shortcut: 'G O', run: go('/sources') },
      { id: 'nav-agents',    label: 'Agents',    hint: 'Agent-name drill-down',            group: 'Navigate', Icon: Bot,        run: go('/agents') },
      { id: 'nav-models',    label: 'Models',    hint: 'Model usage breakdown',            group: 'Navigate', Icon: Brain,      run: go('/models') },
      { id: 'nav-tools',     label: 'Tools',     hint: 'Tool usage breakdown',             group: 'Navigate', Icon: Wrench,     run: go('/tools') },
      { id: 'theme-auto',    label: 'Auto (follow OS)',                          group: 'Theme',    Icon: Monitor,    run: () => { setTheme('auto')(); onOpenChange(false); } },
      { id: 'theme-dark',    label: 'Dark',                                     group: 'Theme',    Icon: Moon,       run: () => { setTheme('dark')(); onOpenChange(false); } },
      { id: 'theme-light',   label: 'Light',                                    group: 'Theme',    Icon: Sun,        run: () => { setTheme('light')(); onOpenChange(false); } },
      { id: 'about',         label: 'ai-viewer', hint: 'Read-only AI session explorer',  group: 'About',    Icon: Eye,        run: noop },
    ];
  }, [navigate, onOpenChange]);

  // Filter by case-insensitive substring match on label + hint. Empty query
  // shows everything. The active-row index resets when the filter changes so
  // arrow-key navigation starts at the top.
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (q === '') return commands;
    return commands.filter(
      (c) => c.label.toLowerCase().includes(q) || (c.hint ?? '').toLowerCase().includes(q),
    );
  }, [commands, query]);

  // Live search results (SOW-0084 D4): when the query has non-whitespace
  // content, fetch ranked matches from /api/search and show them above the
  // command list. The search endpoint is disabled until q has content.
  const trimmedQuery = query.trim();
  const search = useSearch(
    // Empty filter object — the palette is global; filters would constrain.
    { agents: [], models: [], tools: [], status: [], sources: [] },
    trimmedQuery,
    { limit: 8 },
  );

  // Keep the active row in view as the user arrows through.

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>): void => {
    if (filtered.length === 0) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActiveIndex((i) => (i + 1) % filtered.length);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActiveIndex((i) => (i - 1 + filtered.length) % filtered.length);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const target = filtered[activeIndex];
      if (target !== undefined) {
        target.run();
      }
    }
  };

  // Group the filtered commands by their `group` key, preserving the original
  // command order within each group (so Navigate comes first, then Theme, then
  // About).
  const grouped = useMemo(() => {
    const order: Command['group'][] = ['Navigate', 'Theme', 'About'];
    return order
      .map((g) => ({ group: g, items: filtered.filter((c) => c.group === g) }))
      .filter((g) => g.items.length > 0);
  }, [filtered]);

  // Keep the active row in view as the user arrows through.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-cmd-index="${activeIndex}"]`);
    el?.scrollIntoView({ block: 'nearest' });
  }, [activeIndex]);

  return (
    <Dialog open={open} onOpenChange={onOpenChangeWithReset}>
      <DialogContent
        className="max-w-xl gap-0 overflow-hidden p-0 sm:rounded-xl"
        onKeyDown={onKeyDown}
        aria-label="Command palette"
      >
        <DialogTitle className="sr-only">Command palette</DialogTitle>
        <DialogDescription className="sr-only">
          Navigate, switch theme, or invoke a quick action. Press arrow keys to move,
          Enter to select, Esc to close.
        </DialogDescription>

        <div className="flex items-center gap-2 border-b border-border px-4 py-3">
          <span aria-hidden className="text-muted-foreground">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="11" cy="11" r="8" />
              <path d="m21 21-4.3-4.3" />
            </svg>
          </span>
          <Input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => { setQuery(e.target.value); }}
            placeholder="Type a command or search…"
            className="flex-1 border-0 bg-transparent px-0 text-sm shadow-none focus-visible:ring-0"
            aria-label="Command query"
            aria-autocomplete="list"
            aria-controls="command-palette-list"
            aria-activedescendant={filtered[activeIndex] !== undefined ? `command-${filtered[activeIndex].id}` : undefined}
            autoComplete="off"
            spellCheck={false}
          />
          <kbd className="hidden items-center gap-0.5 rounded border border-border bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground sm:inline-flex">
            <CornerDownLeft className="size-2.5" />Enter
          </kbd>
        </div>

        <div
          ref={listRef}
          id="command-palette-list"
          role="listbox"
          aria-label="Commands"
          className="max-h-[60vh] overflow-y-auto p-2"
        >
          {/* SOW-0084 D4: live search results, shown above commands when the
             query has non-whitespace content. Each result links to the
             matching session. */}
          {trimmedQuery !== '' ? (
            <div className="mb-2">
              <p className="px-2 pb-1 pt-1 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
                <Search className="mr-1 inline size-3" aria-hidden /> Search results
              </p>
              {search.isPending ? (
                <div className="space-y-2 px-2 py-1">
                  <Skeleton className="h-4 w-3/4" />
                  <Skeleton className="h-4 w-2/3" />
                  <Skeleton className="h-4 w-1/2" />
                </div>
              ) : search.isError ? (
                <p className="px-2 py-2 text-xs text-muted-foreground">
                  Search failed.
                </p>
              ) : search.data.ops.length === 0 && search.data.logs.length === 0 ? (
                <p className="px-2 py-2 text-xs text-muted-foreground">
                  No ops or log entries match.
                </p>
              ) : (
                <ul role="none">
                  {[...search.data.ops.slice(0, 4).map((o) => ({
                    key: 'op:' + o.op_id,
                    sessionId: o.session_id,
                    kind: o.kind,
                    name: o.name,
                    snippet: o.snippet,
                  })), ...search.data.logs.slice(0, 4).map((l) => ({
                    key: 'log:' + l.log_id,
                    sessionId: l.session_id,
                    kind: l.severity,
                    name: '',
                    snippet: l.snippet,
                  }))].map((r) => {
                    const snippet = r.snippet.trim();
                    return (
                      <li key={r.key} role="presentation">
                        <button
                          type="button"
                          onClick={() => {
                            onOpenChange(false);
                            void navigate(`/sessions/${encodeURIComponent(r.sessionId)}`);
                          }}
                          className="flex w-full items-start gap-2 rounded-md px-2.5 py-1.5 text-left text-sm transition-colors hover:bg-muted"
                          aria-label={`Open session ${r.sessionId}`}
                        >
                          <FileText className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                          <span className="min-w-0 flex-1">
                            <span className="block font-mono text-xs text-foreground">
                              {r.sessionId}
                              {r.kind ? (
                                <span className="ml-2 text-muted-foreground">{r.kind}</span>
                              ) : null}
                            </span>
                            {snippet ? (
                              <span className="block truncate text-xs text-muted-foreground">
                                {snippet}
                              </span>
                            ) : null}
                          </span>
                        </button>
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
          ) : null}

          {filtered.length === 0 ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">
              No results.
            </p>
          ) : (
            grouped.map((section) => (
              <div key={section.group} className="mb-1">
                <p className="px-2 pb-1 pt-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
                  {section.group}
                </p>
                <ul role="none">
                  {section.items.map((cmd) => {
                    // The original flat index for active-row tracking.
                    const flatIndex = filtered.indexOf(cmd);
                    const isActive = flatIndex === activeIndex;
                    return (
                      <li key={cmd.id} role="presentation">
                        <button
                          type="button"
                          role="option"
                          id={`command-${cmd.id}`}
                          data-cmd-index={flatIndex}
                          aria-selected={isActive}
                          onMouseEnter={() => { setActiveIndex(flatIndex); }}
                          onClick={() => { cmd.run(); }}
                          className={cn(
                            'flex w-full items-center gap-3 rounded-md px-2.5 py-1.5 text-left text-sm transition-colors',
                            isActive ? 'bg-accent text-foreground' : 'text-foreground/80 hover:bg-muted',
                          )}
                        >
                          <cmd.Icon
                            className={cn(
                              'size-4 shrink-0',
                              isActive ? 'text-foreground' : 'text-muted-foreground',
                            )}
                            aria-hidden
                          />
                          <span className="flex-1 truncate">{cmd.label}</span>
                          {cmd.hint ? (
                            <span className="hidden truncate text-xs text-muted-foreground sm:inline">
                              {cmd.hint}
                            </span>
                          ) : null}
                          {cmd.shortcut ? (
                            <kbd className="hidden rounded border border-border bg-background px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground sm:inline">
                              {cmd.shortcut}
                            </kbd>
                          ) : null}
                        </button>
                      </li>
                    );
                  })}
                </ul>
              </div>
            ))
          )}
        </div>

        <div className="flex items-center gap-3 border-t border-border bg-muted/30 px-4 py-2 text-[10px] uppercase tracking-wider text-muted-foreground">
          <span className="inline-flex items-center gap-1">
            <kbd className="rounded border border-border bg-background px-1 py-0.5 font-mono">↑↓</kbd>
            navigate
          </span>
          <span className="inline-flex items-center gap-1">
            <kbd className="rounded border border-border bg-background px-1 py-0.5 font-mono">↵</kbd>
            select
          </span>
          <span className="inline-flex items-center gap-1">
            <kbd className="rounded border border-border bg-background px-1 py-0.5 font-mono">esc</kbd>
            close
          </span>
          <span className="ml-auto inline-flex items-center gap-1">
            <Eye className="size-3" aria-hidden />
            ai-viewer
          </span>
        </div>
      </DialogContent>
    </Dialog>
  );
}