import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft } from 'lucide-react';
import { useSessionsInfinite } from '../../api/sessions';
import { useStats } from '../../api/stats';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { Button } from '../../components/ui/button';
import { Skeleton } from '../../components/ui/skeleton';
import { EmptyState, ErrorState } from '../../components/StatusViews';
import { cn } from '../../lib/utils';
import { formatCost, formatNumber, formatTimestamp } from '../../lib/format';
import type { SessionListItem } from '../../api/types';

// ToolDetail — /tools/:name (SOW-0081)
//
// Shows every session that called the given tool. The tool name is the
// `namespace::name` slug from by_tool (the same format used in the tool
// cards on /tools). Same shape as AgentDetail / ModelDetail.

const WINDOWS = [
  { value: '24h', label: '24 hours', microseconds: 24 * 3_600_000_000 },
  { value: '7d',  label: '7 days',    microseconds: 7 * 24 * 3_600_000_000 },
  { value: '30d', label: '30 days',   microseconds: 30 * 24 * 3_600_000_000 },
  { value: 'all', label: 'all time',  microseconds: undefined },
] as const;

type WindowValue = (typeof WINDOWS)[number]['value'];

export function ToolDetail() {
  const { name } = useParams<{ name: string }>();
  const slug = name ?? '';

  return <ToolDetailInner key={slug} slug={slug} />;
}

function ToolDetailInner({ slug }: { slug: string }) {
  const [window, setWindow] = useState<WindowValue>('7d');
  const windowDef = WINDOWS.find((w) => w.value === window) ?? WINDOWS[1];

  const [windowFromUs] = useState<number | undefined>(() =>
    windowDef.microseconds === undefined
      ? undefined
      : Date.now() * 1000 - windowDef.microseconds,
  );

  const { setFilters } = useFilters();

  const scopedFilters = useMemo(() => {
    const base = {
      agents: [] as string[],
      models: [] as string[],
      tools: [slug],
      sources: [] as string[],
      status: [] as string[],
    };
    if (windowFromUs !== undefined) {
      return { ...base, from: windowFromUs };
    }
    return base;
  }, [slug, windowFromUs]);

  useEffect(() => {
    setFilters(scopedFilters);
  }, [scopedFilters, setFilters]);

  const { data, isPending, isError, error, hasNextPage, isFetchingNextPage, fetchNextPage } =
    useSessionsInfinite(scopedFilters, 'root');

  useLiveUpdates(filtersToSubscription(scopedFilters));

  const { data: stats } = useStats(scopedFilters);

  const headerStats = useMemo(() => {
    if (!stats) return null;
    const t = stats.totals;
    return {
      sessions: t.session_count,
      costUsd: t.cost_usd,
      failures: t.failures,
    };
  }, [stats]);

  const items = data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <section aria-labelledby="tool-detail-title" className="flex flex-col gap-6 px-6 py-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0 flex-1">
          <Link
            to="/tools"
            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="size-3" aria-hidden />
            All tools
          </Link>
          <h1
            id="tool-detail-title"
            className="mt-1 truncate font-mono text-2xl font-semibold tracking-tight"
          >
            {slug || '(no tool)'}
          </h1>
          {headerStats ? (
            <p className="mt-1 text-sm text-muted-foreground">
              <span className="font-mono tabular-nums">{formatNumber(headerStats.sessions)}</span>{' '}
              session{headerStats.sessions === 1 ? '' : 's'} used this tool ·{' '}
              <span className="font-mono tabular-nums">{formatCost(headerStats.costUsd)}</span> total ·{' '}
              <span
                className={cn(
                  'font-mono tabular-nums',
                  headerStats.failures === 0
                    ? 'text-status-completed'
                    : 'text-status-failed',
                )}
              >
                {formatNumber(headerStats.failures)}
              </span>{' '}
              failures
            </p>
          ) : (
            <Skeleton className="mt-1 h-4 w-72" />
          )}
        </div>
        <div role="group" aria-label="Time window" className="inline-flex rounded-md border border-border bg-card p-1">
          {WINDOWS.map((w) => (
            <button
              key={w.value}
              type="button"
              aria-pressed={w.value === window}
              onClick={() => { setWindow(w.value); }}
              className={cn(
                'rounded-sm px-3 py-1 text-xs font-medium transition-colors',
                w.value === window
                  ? 'bg-accent text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {w.label}
            </button>
          ))}
        </div>
      </div>

      {isError ? (
        <ErrorState error={error} title="Failed to load sessions" />
      ) : isPending ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <EmptyState>
          <p className="font-semibold text-foreground">
            No sessions called {slug} in this window
          </p>
          <Button asChild variant="outline" size="sm" className="mt-3">
            <Link to="/sessions">All sessions</Link>
          </Button>
        </EmptyState>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-[11px] uppercase tracking-wider text-muted-foreground">
              <tr>
                <th scope="col" className="px-4 py-2 text-left font-medium">Agent</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Model</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Status</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Started</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Cost</th>
              </tr>
            </thead>
            <tbody>
              {items.map((s) => (
                <ToolSessionRow key={s.id} session={s} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {hasNextPage ? (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            disabled={isFetchingNextPage}
            onClick={() => { void fetchNextPage(); }}
          >
            {isFetchingNextPage ? 'Loading…' : `Load more (${items.length} shown)`}
          </Button>
        </div>
      ) : null}
    </section>
  );
}

function ToolSessionRow({ session }: { session: SessionListItem }) {
  return (
    <tr
      className="cursor-pointer border-t border-border/50 transition-colors hover:bg-muted/30"
      onClick={() => {
        window.location.href = `/sessions/${encodeURIComponent(session.id)}`;
      }}
    >
      <td className="px-4 py-2 text-sm text-foreground">{session.agent_name || session.native_id}</td>
      <td className="px-4 py-2 font-mono text-xs text-muted-foreground">{session.model || '—'}</td>
      <td className="px-4 py-2 text-xs">{session.status}</td>
      <td className="px-4 py-2 font-mono text-xs text-muted-foreground tabular-nums">
        {formatTimestamp(session.start_ts)}
      </td>
      <td className="px-4 py-2 text-right font-mono text-xs tabular-nums text-foreground">
        {formatCost(session.cost_usd)}
      </td>
    </tr>
  );
}
