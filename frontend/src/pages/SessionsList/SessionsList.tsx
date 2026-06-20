import { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useSessionsInfinite } from '../../api/sessions';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { SessionRowBody } from '../../components/SessionRow';
import { ErrorState } from '../../components/StatusViews';
import { DurationBar, Sparkline } from '../../components/Sparkline';
void Sparkline; // SOW-0087: wired into a 'Last 24h' column in a follow-up commit
import { HomeSummaryCard } from './HomeSummaryCard';
import type { SessionListItem } from '../../api/types';
import {
  formatCost,
  formatDuration,
  formatNumber,
  formatTimestamp,
} from '../../lib/format';
import { StatusBadge } from '../../components/StatusBadge';
import { Button } from '../../components/ui/button';
import { ToggleGroup, ToggleGroupItem } from '../../components/ui/toggle-group';
import { Tooltip, TooltipContent, TooltipTrigger } from '../../components/ui/tooltip';
import { Skeleton } from '../../components/ui/skeleton';
import {
  ChevronRight,
  CircleAlert,
  Filter,
  RefreshCw,
  ArrowDownAZ,
  ArrowUpAZ,
  Inbox,
} from 'lucide-react';
import { cn } from '../../lib/utils';

// Sessions list page (SOW-0073 redesign). The home view of the app.
//
// Data contract is unchanged from SOW-0068: root sessions for the active
// filter, keyset-paginated, live-refreshed over SSE, with a "Show
// secondary" toggle that widens the query to all kinds. The visual
// treatment is fully rewritten — proper header, redesigned table with
// sticky header / hover rows / zebra / tabular-nums / source-color rail,
// empty/loading/error states with illustrations, and a toolbar that
// re-homes every affordance (Show secondary as a segmented control,
// sort, density).

type SortKey =
  | 'start_ts'
  | 'agent_name'
  | 'status'
  | 'tokens_in'
  | 'tokens_out'
  | 'cost_usd'
  | 'failure_count';

type SortDir = 'asc' | 'desc';

export function SessionsList() {
  const { filters } = useFilters();
  const navigate = useNavigate();
  const [showSecondary, setShowSecondary] = useState(false);
  const [sortKey, setSortKey] = useState<SortKey>('start_ts');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  const [density, setDensity] = useState<'comfortable' | 'compact' | 'minimal'>('comfortable');

  const group = showSecondary ? 'all' : 'root';
  const { data, isPending, isError, error, hasNextPage, isFetchingNextPage, fetchNextPage, refetch } =
    useSessionsInfinite(filters, group);

  useLiveUpdates(filtersToSubscription(filters));

  const items = data?.pages.flatMap((p) => p.items) ?? [];
  const sorted = sortItems(items, sortKey, sortDir);
  const stats = summarize(items);

  return (
    <div className="flex min-h-full flex-col">
      {/* Home summary card (SOW-0079 P0.1). 5 tiles, each clickable to a
         filtered view. Loads in parallel with the sessions list; shows
         skeleton placeholders while pending. Answers scenario #1
         ('What\'s happening right now?') in 5 seconds. Implemented as a
         sibling component (HomeSummaryCard.tsx) so its tests + coverage
         floor are isolated from this page. */}
      <div className="px-6 pt-5">
        <HomeSummaryCard />
      </div>

      {/* Page header */}
      <div className="border-b border-border bg-background px-6 py-5">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <div className="flex items-baseline gap-3">
              <h1 className="text-2xl font-semibold tracking-tight">Sessions</h1>
              <span className="text-sm text-muted-foreground tabular-nums">
                {items.length > 0
                  ? `${formatNumber(items.length)}${hasNextPage ? '+' : ''} sessions`
                  : 'No sessions'}
              </span>
            </div>
            <p className="mt-1 text-sm text-muted-foreground">
              Live snapshot of every AI coding-agent session across all configured sources.
            </p>
          </div>

          {/* Stats summary (visible page) */}
          {items.length > 0 ? (
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
              <StatPill label="Active" value={stats.running} tone="running" />
              <StatPill label="Failed" value={stats.failed} tone="failed" />
              <StatPill label="Completed" value={stats.completed} tone="completed" />
              <StatPill
                label="Reliability"
                value={stats.reliabilityPct === null ? '—' : `${stats.reliabilityPct.toFixed(0)}%`}
                tone={
                  stats.reliabilityPct === null
                    ? undefined
                    : stats.reliabilityPct >= 90
                      ? 'completed'
                      : stats.reliabilityPct >= 70
                        ? undefined
                        : 'failed'
                }
                title={
                  stats.reliabilityPct === null
                    ? 'No completed or failed sessions in this view yet'
                    : `${stats.completed} completed of ${stats.completed + stats.failed} ended sessions (${stats.reliabilityPct.toFixed(1)}%)`
                }
              />
              <StatPill label="Tokens" value={formatNumber(stats.tokensIn + stats.tokensOut)} />
              <StatPill label="Cost" value={formatCost(stats.costUsd)} />
            </div>
          ) : null}
        </div>

        {/* Toolbar */}
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <ToggleGroup
            type="single"
            value={showSecondary ? 'all' : 'root'}
            onValueChange={(v) => { if (v) setShowSecondary(v === 'all'); }}
            aria-label="Session kind filter"
            size="sm"
          >
            <ToggleGroupItem value="root" aria-label="Roots only — hide sub-agents and forks">
              Roots only
            </ToggleGroupItem>
            <ToggleGroupItem value="all" aria-label="All — show sub-agents and forks too">
              Sub-agents and forks
            </ToggleGroupItem>
          </ToggleGroup>

          <ToggleGroup
            type="single"
            value={sortDir}
            onValueChange={(v) => { if (v) setSortDir(v as SortDir); }}
            aria-label="Sort direction"
            size="sm"
          >
            <Tooltip>
              <TooltipTrigger asChild>
                <ToggleGroupItem value="desc" aria-label="Newest first">
                  <ArrowDownAZ className="size-3.5" />
                </ToggleGroupItem>
              </TooltipTrigger>
              <TooltipContent>Newest first</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger asChild>
                <ToggleGroupItem value="asc" aria-label="Oldest first">
                  <ArrowUpAZ className="size-3.5" />
                </ToggleGroupItem>
              </TooltipTrigger>
              <TooltipContent>Oldest first</TooltipContent>
            </Tooltip>
          </ToggleGroup>

          <ToggleGroup
            type="single"
            value={density}
            onValueChange={(v) => { if (v) setDensity(v as 'comfortable' | 'compact' | 'minimal'); }}
            aria-label="Row density"
            size="sm"
          >
            <ToggleGroupItem value="comfortable" aria-label="Comfortable row density">
              Comfortable
            </ToggleGroupItem>
            <ToggleGroupItem value="compact" aria-label="Compact row density">
              Compact
            </ToggleGroupItem>
            <ToggleGroupItem value="minimal" aria-label="Minimal row density">
              Minimal
            </ToggleGroupItem>
          </ToggleGroup>

          <div className="flex-1" />

          <span className="hidden items-center gap-1.5 text-xs text-muted-foreground sm:inline-flex">
            <Filter className="size-3.5" aria-hidden />
            {activeFilterCount(filters)} active filter{activeFilterCount(filters) === 1 ? '' : 's'}
          </span>

          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                onClick={() => { void refetch(); }}
                aria-label="Refresh"
              >
                <RefreshCw className="size-3.5" aria-hidden />
                Refresh
              </Button>
            </TooltipTrigger>
            <TooltipContent>Re-fetch the first page</TooltipContent>
          </Tooltip>
        </div>
      </div>

      {/* Content */}
      <div className="min-h-0 flex-1 overflow-y-auto px-6 py-4">
        {isPending ? (
          <SessionsSkeleton />
        ) : isError ? (
          <ErrorState error={error} title="Failed to load sessions" />
        ) : items.length === 0 ? (
          <SessionsEmpty hasFilters={activeFilterCount(filters) > 0} />
        ) : (
          <SessionsTable
            items={sorted}
            sortKey={sortKey}
            sortDir={sortDir}
            onSort={(k) => {
              if (k === sortKey) {
                setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
              } else {
                setSortKey(k);
                setSortDir('desc');
              }
            }}
            density={density}
            onRowClick={(id) => { void navigate(`/sessions/${encodeURIComponent(id)}`); }}
          />
        )}

        {/* Load more */}
        {!isPending && !isError && items.length > 0 && hasNextPage ? (
          <div className="mt-4 flex justify-center">
            <Button
              variant="outline"
              size="sm"
              disabled={isFetchingNextPage}
              onClick={() => { void fetchNextPage(); }}
            >
              {isFetchingNextPage ? 'Loading…' : 'Load more'}
            </Button>
          </div>
        ) : null}
      </div>
    </div>
  );
}

// ---------- Pieces ----------

function SessionsTable({
  items,
  sortKey,
  sortDir,
  onSort,
  density,
  onRowClick,
}: {
  items: SessionListItem[];
  sortKey: SortKey;
  sortDir: SortDir;
  onSort: (key: SortKey) => void;
  density: 'comfortable' | 'compact' | 'minimal';
  onRowClick: (id: string) => void;
}) {
  // Minimal density hides 7 of the 11 columns; keeps Agent / Status /
  // Started / Duration / Cost (the columns the operator uses to scan
  // the table at a glance). Comfortable + Compact show everything.
  const isMinimal = density === 'minimal';
  // Computed once per render so all rows can size their DurationBar the
  // same way (max duration in the current view).
  const maxDurationUs: number = useMemo(() => {
    let m = 0;
    for (const s of items) {
      const d = s.end_ts !== null && s.start_ts > 0 ? s.end_ts - s.start_ts : 0;
      if (d > m) m = d;
    }
    return m;
  }, [items]);
  const pad = density === 'compact' ? 'py-1.5' : 'py-2.5';
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card">
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 z-10 border-b border-border bg-muted/60 backdrop-blur">
            <tr>
              <th
                scope="col"
                className={cn('w-10 px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}
              >
                <span className="sr-only">Expand</span>
              </th>
              <th scope="col" className={cn('px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                <SortHeader
                  label="Agent"
                  active={sortKey === 'agent_name'}
                  dir={sortDir}
                  onClick={() => { onSort('agent_name'); }}
                />
              </th>
              {isMinimal ? null : (
                <th scope="col" className={cn('px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                  Model
                </th>
              )}
              {isMinimal ? null : (
                <th scope="col" className={cn('px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                  Source
                </th>
              )}
              <th scope="col" className={cn('px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                <SortHeader
                  label="Started"
                  active={sortKey === 'start_ts'}
                  dir={sortDir}
                  onClick={() => { onSort('start_ts'); }}
                />
              </th>
              <th scope="col" className={cn('px-3 py-2 text-right text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                Duration
              </th>
              <th scope="col" className={cn('px-3 py-2 text-left text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                <SortHeader
                  label="Status"
                  active={sortKey === 'status'}
                  dir={sortDir}
                  onClick={() => { onSort('status'); }}
                />
              </th>
              {isMinimal ? null : (
                <th scope="col" className={cn('px-3 py-2 text-right text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                  <SortHeader
                    label="Tokens in"
                    align="right"
                    active={sortKey === 'tokens_in'}
                    dir={sortDir}
                    onClick={() => { onSort('tokens_in'); }}
                  />
                </th>
              )}
              {isMinimal ? null : (
                <th scope="col" className={cn('px-3 py-2 text-right text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                  <SortHeader
                    label="Tokens out"
                    align="right"
                    active={sortKey === 'tokens_out'}
                    dir={sortDir}
                    onClick={() => { onSort('tokens_out'); }}
                  />
                </th>
              )}
              <th scope="col" className={cn('px-3 py-2 text-right text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                <SortHeader
                  label="Cost"
                  align="right"
                  active={sortKey === 'cost_usd'}
                  dir={sortDir}
                  onClick={() => { onSort('cost_usd'); }}
                />
              </th>
              <th scope="col" className={cn('px-3 py-2 text-right text-[11px] font-medium uppercase tracking-wider text-muted-foreground')}>
                <SortHeader
                  label="Failures"
                  align="right"
                  active={sortKey === 'failure_count'}
                  dir={sortDir}
                  onClick={() => { onSort('failure_count'); }}
                />
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((s, idx) => (
              <SessionTableRow
                key={s.id}
                session={s}
                zebra={idx % 2 === 1}
                rowPad={pad}
                isMinimal={isMinimal}
                maxDurationUs={maxDurationUs}
                onRowClick={() => { onRowClick(s.id); }}
              />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function SessionTableRow({
  session,
  zebra,
  rowPad,
  isMinimal,
  maxDurationUs,
  onRowClick,
}: {
  session: SessionListItem;
  zebra: boolean;
  rowPad: string;
  isMinimal: boolean;
  maxDurationUs: number;
  onRowClick: () => void;
}) {
  const durationUs: number | null = session.end_ts === null
    ? null
    : session.end_ts - session.start_ts;
  const safeDurationUs: number = durationUs === null ? 0 : durationUs;
  const badge = kindLabel(session.kind);
  const sourceColor = sourceColorVar(session.source_id);

  return (
    <tr
      className={cn(
        'group cursor-pointer border-b border-border/50 transition-colors last:border-b-0',
        'hover:bg-accent/40',
        zebra && 'bg-muted/20',
      )}
      onClick={onRowClick}
      data-testid={`session-row-${session.id}`}
    >
      <td className={cn('w-2 px-0', rowPad)} aria-hidden>
        <span
          className="block h-6 w-1 rounded-r-full"
          style={{ backgroundColor: `var(${sourceColor})` }}
        />
      </td>
      <td className={cn('px-3 align-middle', rowPad)}>
        <div className="flex items-center gap-2">
          <ChildExpander session={session} />
          <Link
            to={`/sessions/${encodeURIComponent(session.id)}`}
            onClick={(e) => { e.stopPropagation(); }}
            className="truncate font-medium text-foreground hover:underline"
          >
            {session.agent_name || session.native_id}
          </Link>
          {badge ? <KindBadge label={badge} /> : null}
        </div>
      </td>
      {isMinimal ? null : (
        <td className={cn('px-3 align-middle font-mono text-xs text-muted-foreground', rowPad)}>
          <span className="truncate">{session.model || '—'}</span>
        </td>
      )}
      {isMinimal ? null : (
        <td className={cn('px-3 align-middle text-xs', rowPad)}>
          <span
            className="inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-[11px] font-medium"
            style={{
              color: `var(${sourceColor})`,
              borderColor: `color-mix(in oklch, var(${sourceColor}) 30%, transparent)`,
              backgroundColor: `color-mix(in oklch, var(${sourceColor}) 8%, transparent)`,
            }}
          >
            <span
              aria-hidden
              className="inline-block size-1.5 rounded-full"
              style={{ backgroundColor: `var(${sourceColor})` }}
            />
            {sourceLabel(session.source_id)}
          </span>
        </td>
      )}
      <td className={cn('px-3 align-middle font-mono text-xs text-muted-foreground tabular-nums', rowPad)}>
        {formatTimestamp(session.start_ts)}
      </td>
      <td className={cn('px-3 text-right align-middle font-mono text-xs tabular-nums text-muted-foreground', rowPad)}>
        <span className="inline-flex items-center gap-2 justify-end">
          <DurationBar
            durationUs={safeDurationUs}
            maxDurationUs={maxDurationUs}
          />
          <span>{formatDuration(durationUs)}</span>
        </span>
      </td>
      <td className={cn('px-3 align-middle', rowPad)}>
        <div className="flex items-center gap-1.5">
          <StatusBadge status={session.status} />
          {session.status === 'failed' && session.error_class ? (
            <span className="inline-flex items-center gap-1 rounded-md bg-status-failed/10 px-1.5 py-0.5 text-[11px] text-status-failed">
              <CircleAlert className="size-3" aria-hidden />
              {session.error_class}
            </span>
          ) : null}
        </div>
      </td>
      {isMinimal ? null : (
        <td className={cn('px-3 text-right align-middle font-mono text-xs tabular-nums', rowPad)}>
          {formatNumber(session.tokens_in)}
        </td>
      )}
      {isMinimal ? null : (
        <td className={cn('px-3 text-right align-middle font-mono text-xs tabular-nums', rowPad)}>
          {formatNumber(session.tokens_out)}
        </td>
      )}
      <td className={cn('px-3 text-right align-middle font-mono text-xs tabular-nums', rowPad)}>
        {formatCost(session.cost_usd)}
      </td>
      <td className={cn('px-3 text-right align-middle font-mono text-xs tabular-nums', rowPad)}>
        {session.failure_count > 0 ? (
          <span className="text-status-failed">{formatNumber(session.failure_count)}</span>
        ) : (
          <span className="text-muted-foreground">{formatNumber(session.failure_count)}</span>
        )}
      </td>
    </tr>
  );
}

function ChildExpander({ session }: { session: SessionListItem }) {
  if (session.child_session_count <= 0) {
    return <span className="inline-block size-4 text-muted-foreground/30">·</span>;
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Link
          to={`/sessions/${encodeURIComponent(session.id)}`}
          onClick={(e) => { e.stopPropagation(); }}
          className={cn(
            'inline-flex h-5 min-w-5 items-center justify-center gap-0.5 rounded-md px-1 text-[11px] font-medium tabular-nums',
            'border border-border bg-background text-muted-foreground hover:border-primary hover:text-primary',
          )}
          aria-label={`${session.child_session_count} child sessions`}
        >
          <ChevronRight className="size-3" aria-hidden />
          {formatNumber(session.child_session_count)}
        </Link>
      </TooltipTrigger>
      <TooltipContent>
        {session.child_session_count} child session{session.child_session_count === 1 ? '' : 's'}
      </TooltipContent>
    </Tooltip>
  );
}

function KindBadge({ label }: { label: string }) {
  return (
    <span className="inline-flex items-center rounded-md border border-border bg-muted px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
      {label}
    </span>
  );
}

function SortHeader({
  label,
  active,
  dir,
  align,
  onClick,
}: {
  label: string;
  active: boolean;
  dir: SortDir;
  align?: 'right';
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1 rounded -mx-1 px-1 hover:text-foreground',
        align === 'right' && 'ml-auto',
      )}
    >
      {label}
      {active ? (
        dir === 'asc' ? <ArrowUpAZ className="size-3" aria-hidden /> : <ArrowDownAZ className="size-3" aria-hidden />
      ) : null}
    </button>
  );
}

function StatPill({
  label,
  value,
  tone,
  title,
}: {
  label: string;
  value: string | number;
  tone?: 'running' | 'completed' | 'failed' | undefined;
  title?: string | undefined;
}) {
  const toneColor =
    tone === 'running'
      ? 'text-status-running'
      : tone === 'failed'
        ? 'text-status-failed'
        : tone === 'completed'
          ? 'text-status-completed'
          : 'text-foreground';
  return (
    <div className="flex items-baseline gap-1.5" title={title}>
      <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      <span className={cn('text-sm font-semibold tabular-nums', toneColor)}>{value}</span>
    </div>
  );
}

function SessionsEmpty({ hasFilters }: { hasFilters: boolean }) {
  return (
    <div className="flex flex-col items-center justify-center rounded-lg border border-dashed border-border bg-card/50 px-6 py-16 text-center">
      <div className="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
        <Inbox className="size-6" aria-hidden />
      </div>
      <h2 className="mt-4 text-base font-semibold text-foreground">
        {hasFilters ? 'No sessions match these filters' : 'No sessions yet'}
      </h2>
      <p className="mt-1 max-w-md text-sm text-muted-foreground">
        {hasFilters
          ? 'Try clearing one or more filters — agents, models, tools, time range, status — or wait for a new session to start.'
          : 'Once an AI coding agent runs in a configured source, its sessions will appear here in real time.'}
      </p>
    </div>
  );
}

function SessionsSkeleton() {
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card">
      <div className="border-b border-border bg-muted/60 p-3">
        <Skeleton className="h-4 w-32" />
      </div>
      {Array.from({ length: 8 }).map((_, i) => (
        <div
          key={i}
          className="flex items-center gap-4 border-b border-border/50 px-4 py-3 last:border-b-0"
        >
          <Skeleton className="size-2 rounded-full" />
          <Skeleton className="h-4 w-1/4" />
          <Skeleton className="h-3 w-20" />
          <Skeleton className="h-5 w-16 rounded-md" />
          <Skeleton className="ml-auto h-4 w-20" />
        </div>
      ))}
    </div>
  );
}

// ---------- Helpers ----------

function activeFilterCount(filters: { agents: string[]; models: string[]; tools: string[]; sources: string[]; status: string[]; q?: string; from?: number; to?: number }): number {
  return (
    filters.agents.length +
    filters.models.length +
    filters.tools.length +
    filters.sources.length +
    filters.status.length +
    (filters.q && filters.q.length > 0 ? 1 : 0) +
    (filters.from !== undefined ? 1 : 0) +
    (filters.to !== undefined ? 1 : 0)
  );
}

function summarize(items: SessionListItem[]) {
  let running = 0;
  let failed = 0;
  let completed = 0;
  let abandoned = 0;
  let interrupted = 0;
  let tokensIn = 0;
  let tokensOut = 0;
  let costUsd = 0;
  for (const s of items) {
    if (s.status === 'running') running++;
    else if (s.status === 'failed') failed++;
    else if (s.status === 'completed') completed++;
    else if (s.status === 'abandoned') abandoned++;
    else if (s.status === 'interrupted') interrupted++;
    tokensIn += s.tokens_in;
    tokensOut += s.tokens_out;
    costUsd += s.cost_usd;
  }
  // Reliability: completed / (completed + failed). Abandoned and interrupted
  // are NOT counted in the denominator — they reflect operator action, not
  // agent reliability. Returns null if there are no completed-or-failed
  // sessions in the current view (avoids 0/0 producing Infinity%).
  const reliabilityDenom = completed + failed;
  const reliabilityPct =
    reliabilityDenom > 0 ? (completed / reliabilityDenom) * 100 : null;
  return {
    running,
    failed,
    completed,
    abandoned,
    interrupted,
    tokensIn,
    tokensOut,
    costUsd,
    reliabilityPct,
  };
}

function sortItems(items: SessionListItem[], key: SortKey, dir: SortDir): SessionListItem[] {
  const copy = [...items];
  copy.sort((a, b) => {
    const av = sortValue(a, key);
    const bv = sortValue(b, key);
    if (av === bv) return 0;
    const cmp = av > bv ? 1 : -1;
    return dir === 'asc' ? cmp : -cmp;
  });
  return copy;
}

function sortValue(s: SessionListItem, key: SortKey): string | number {
  switch (key) {
    case 'start_ts': return s.start_ts;
    case 'agent_name': return s.agent_name || s.native_id;
    case 'status': return s.status;
    case 'tokens_in': return s.tokens_in;
    case 'tokens_out': return s.tokens_out;
    case 'cost_usd': return s.cost_usd;
    case 'failure_count': return s.failure_count;
  }
}

function sourceLabel(sourceID: string): string {
  const fmt = sourceID.split(':')[0] ?? '';
  switch (fmt) {
    case 'aiagent_v3': return 'ai-agent v3';
    case 'aiagent_v2': return 'ai-agent v2';
    case 'claude-code': return 'claude-code';
    case 'codex': return 'codex';
    case 'opencode': return 'opencode';
    default: return fmt;
  }
}

function sourceColorVar(sourceID: string): string {
  const fmt = sourceID.split(':')[0] ?? '';
  switch (fmt) {
    case 'aiagent_v3': return '--source-aiagent-v3';
    case 'aiagent_v2': return '--source-aiagent-v2';
    case 'claude-code': return '--source-claude-code';
    case 'codex': return '--source-codex';
    case 'opencode': return '--source-opencode';
    default: return '--border';
  }
}

function kindLabel(kind: SessionListItem['kind']): string | null {
  switch (kind) {
    case 'sub_agent': return 'sub-agent';
    case 'tool_internal': return 'internal';
    case 'fork': return 'fork';
    default: return null;
  }
}

// Re-exported for tests that consume the page.
export const _testHelpers = { sortItems, summarize, sourceLabel, sourceColorVar, kindLabel };
// Use the body export so existing tests that import SessionRowBody keep working.
export { SessionRowBody };

