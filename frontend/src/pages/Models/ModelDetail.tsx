import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft } from 'lucide-react';
import { useSessionsInfinite } from '../../api/sessions';
import { useStats } from '../../api/stats';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { HomeSummaryCard } from '../SessionsList/HomeSummaryCard';
import { Button } from '../../components/ui/button';
import { Skeleton } from '../../components/ui/skeleton';
import { EmptyState, ErrorState } from '../../components/StatusViews';
import { cn } from '../../lib/utils';
import { formatCost, formatNumber, formatTimestamp } from '../../lib/format';
import type { SessionListItem } from '../../api/types';

// ModelDetail — /models/:name (SOW-0081)
//
// Shows every session for the named model. Same shape as AgentDetail.

const WINDOWS = [
  { value: '24h', label: '24 hours', microseconds: 24 * 3_600_000_000 },
  { value: '7d',  label: '7 days',    microseconds: 7 * 24 * 3_600_000_000 },
  { value: '30d', label: '30 days',   microseconds: 30 * 24 * 3_600_000_000 },
  { value: 'all', label: 'all time',  microseconds: undefined },
] as const;

type WindowValue = (typeof WINDOWS)[number]['value'];

export function ModelDetail() {
  const { name } = useParams<{ name: string }>();
  const modelName = name ?? '';

  return <ModelDetailInner key={modelName} modelName={modelName} />;
}

function ModelDetailInner({ modelName }: { modelName: string }) {
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
      models: [modelName],
      tools: [] as string[],
      sources: [] as string[],
      status: [] as string[],
    };
    if (windowFromUs !== undefined) {
      return { ...base, from: windowFromUs };
    }
    return base;
  }, [modelName, windowFromUs]);

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
    const denom = t.session_count - t.failures;
    const reliability =
      t.session_count > 0 && denom > 0 ? denom / t.session_count : null;
    return {
      sessions: t.session_count,
      costUsd: t.cost_usd,
      failures: t.failures,
      reliability,
    };
  }, [stats]);

  const items = data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <section aria-labelledby="model-detail-title" className="flex flex-col gap-6 px-6 py-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0 flex-1">
          <Link
            to="/models"
            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="size-3" aria-hidden />
            All models
          </Link>
          <h1
            id="model-detail-title"
            className="mt-1 truncate font-mono text-2xl font-semibold tracking-tight"
          >
            {modelName || '(no model)'}
          </h1>
          {headerStats ? (
            <p className="mt-1 text-sm text-muted-foreground">
              <span className="font-mono tabular-nums">{formatNumber(headerStats.sessions)}</span>{' '}
              session{headerStats.sessions === 1 ? '' : 's'} ·{' '}
              <span className="font-mono tabular-nums">{formatCost(headerStats.costUsd)}</span> ·{' '}
              <span
                className={cn(
                  'font-mono tabular-nums',
                  headerStats.reliability !== null && headerStats.reliability >= 0.9
                    ? 'text-status-completed'
                    : headerStats.reliability !== null && headerStats.reliability < 0.7
                      ? 'text-status-failed'
                      : 'text-muted-foreground',
                )}
              >
                {headerStats.reliability === null
                  ? '—'
                  : `${(headerStats.reliability * 100).toFixed(1)}%`}
              </span>{' '}
              reliability
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

      <HomeSummaryCard />

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
            No sessions for {modelName} in this window
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
                <th scope="col" className="px-4 py-2 text-left font-medium">Status</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Started</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Cost</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Tokens</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Duration</th>
              </tr>
            </thead>
            <tbody>
              {items.map((s) => (
                <ModelSessionRow key={s.id} session={s} />
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

function ModelSessionRow({ session }: { session: SessionListItem }) {
  return (
    <tr
      className="cursor-pointer border-t border-border/50 transition-colors hover:bg-muted/30"
      onClick={() => {
        window.location.href = `/sessions/${encodeURIComponent(session.id)}`;
      }}
    >
      <td className="px-4 py-2 text-sm text-foreground">{session.agent_name || session.native_id}</td>
      <td className="px-4 py-2 text-xs">{session.status}</td>
      <td className="px-4 py-2 font-mono text-xs text-muted-foreground tabular-nums">
        {formatTimestamp(session.start_ts)}
      </td>
      <td className="px-4 py-2 text-right font-mono text-xs tabular-nums text-foreground">
        {formatCost(session.cost_usd)}
      </td>
      <td className="px-4 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground">
        {formatNumber(session.tokens_in + session.tokens_out)}
      </td>
      <td className="px-4 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground">
        {session.end_ts !== null
          ? formatDuration(session.end_ts - session.start_ts)
          : '—'}
      </td>
    </tr>
  );
}

function formatDuration(microseconds: number): string {
  if (microseconds < 0) return '—';
  const seconds = Math.floor(microseconds / 1_000_000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}
