import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { ChevronDown, CircleAlert, ExternalLink, Inbox } from 'lucide-react';
import { useSessionsInfinite } from '../../api/sessions';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { Button } from '../../components/ui/button';
import { Skeleton } from '../../components/ui/skeleton';
import { cn } from '../../lib/utils';
import { formatCost, formatNumber, formatTimestamp } from '../../lib/format';
import type { SessionListItem } from '../../api/types';

// /failures (SOW-0079 P0.2) — the operator's most common audit question:
// "what failed in the last N days?". Default window = 7 days. Default
// status filter = (failed, abandoned, interrupted). Time-range selector at
// the top swaps the window. Each row drills to the session detail where
// the failing op is highlighted (deferred — current SOW).

const WINDOWS = [
  { value: '24h', label: '24 hours', microseconds: 24 * 3_600_000_000 },
  { value: '7d',  label: '7 days',    microseconds: 7 * 24 * 3_600_000_000 },
  { value: '30d', label: '30 days',   microseconds: 30 * 24 * 3_600_000_000 },
  { value: 'all', label: 'all time',  microseconds: undefined },
] as const;

type WindowValue = (typeof WINDOWS)[number]['value'];

const FAILURE_STATUSES: string[] = ['failed', 'abandoned', 'interrupted'];

export function Failures() {
  const [windowValue, setWindowValue] = useState<WindowValue>('7d');

  return (
    <FailuresInner
      key={windowValue}
      windowValue={windowValue}
      onWindowChange={setWindowValue}
    />
  );
}

interface FailuresInnerProps {
  windowValue: WindowValue;
  onWindowChange: (_w: WindowValue) => void;
}

// FailuresInner is re-mounted whenever windowValue changes (via key on the
// parent). That makes the initial state initializer run a fresh Date.now()
// without violating the no-impure-render rule (initializers run during the
// render phase but they are explicitly designed to seed state from a single
// impure call at mount).
function FailuresInner({ windowValue, onWindowChange }: FailuresInnerProps) {
  const windowDef = WINDOWS.find((w) => w.value === windowValue) ?? WINDOWS[1];

  // from bound for the selected window. Captured once per window change so
  // the same window doesn't drift while the operator is looking at it.
  const [windowFromUs] = useState<number | undefined>(() =>
    windowDef.microseconds === undefined
      ? undefined
      : Date.now() * 1000 - windowDef.microseconds,
  );

  const { setFilters, filters } = useFilters();

  // The /failures page's defaults: status IN failure set + a from bound from
  // the selected window. These are URL-synced so the operator can share a
  // /failures?window=24h URL with a colleague. exactOptionalPropertyTypes
  // forbids assigning `undefined` to an optional property, so we only add
  // `from` when it's defined.
  const computedFilters = useMemo(() => {
    const base: {
      agents: string[];
      models: string[];
      tools: string[];
      sources: string[];
      status: string[];
      from?: number;
      q?: string;
    } = {
      agents: filters.agents,
      models: filters.models,
      tools: filters.tools,
      sources: filters.sources,
      status: FAILURE_STATUSES,
    };
    if (windowFromUs !== undefined) {
      base.from = windowFromUs;
    }
    if (filters.q !== undefined) {
      base.q = filters.q;
    }
    return base;
  }, [filters, windowFromUs]);

  // Push the computed filter into the URL so the URL is the source of truth.
  if (filters.status.join(',') !== computedFilters.status.join(',')
      || filters.from !== computedFilters.from) {
    setFilters({ status: computedFilters.status, from: computedFilters.from });
  }

  const { data, isPending, isError, error, hasNextPage, isFetchingNextPage, fetchNextPage } =
    useSessionsInfinite(computedFilters, 'all');

  useLiveUpdates(filtersToSubscription(computedFilters));

  const items = useMemo(() => data?.pages.flatMap((p) => p.items) ?? [], [data]);
  const summary = useMemo(() => summarize(items), [items]);
  const errorClassCounts = useMemo(() => countErrorClasses(items), [items]);

  return (
    <section aria-labelledby="failures-title" className="flex flex-col gap-6 px-6 py-5">
      {/* Page header */}
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 id="failures-title" className="text-2xl font-semibold tracking-tight">
            Recent failures
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Sessions that ended with a failure, an abandonment, or an interrupt.
            Click a row to see the full trace.
          </p>
        </div>
        <div role="group" aria-label="Time window" className="inline-flex rounded-md border border-border bg-card p-1">
          {WINDOWS.map((w) => (
            <button
              key={w.value}
              type="button"
              aria-pressed={w.value === windowValue}
              onClick={() => { onWindowChange(w.value); }}
              className={cn(
                'rounded-sm px-3 py-1 text-xs font-medium transition-colors',
                w.value === windowValue
                  ? 'bg-accent text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {w.label}
            </button>
          ))}
        </div>
      </div>

      {/* Summary stat strip */}
      <div className="grid grid-cols-2 gap-3 rounded-lg border border-border bg-card p-4 sm:grid-cols-4">
        <FailuresStat
          label="Failures"
          value={summary.count}
          isLoading={isPending}
          tone="failed"
        />
        <FailuresStat
          label="Cost"
          value={summary.costUsd}
          isLoading={isPending}
          format="cost"
        />
        <FailuresStat
          label="Tokens"
          value={summary.tokensIn + summary.tokensOut}
          isLoading={isPending}
          format="number"
        />
        <FailuresStat
          label="Top error class"
          value={summary.topErrorClass}
          isLoading={isPending}
          format="text"
          tone={summary.topErrorClass === null ? 'foreground' : 'failed'}
        />
      </div>

      {/* Error class chip strip */}
      {errorClassCounts.length > 0 ? (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
            By error class
          </span>
          {errorClassCounts.map((ec) => (
            <Link
              key={ec.name}
              to={`?${buildQuery({ ...computedFilters, q: ec.name })}`}
              className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-0.5 text-xs font-medium hover:border-primary hover:text-primary"
            >
              <CircleAlert className="size-3 text-status-failed" aria-hidden />
              <span className="truncate">{ec.name}</span>
              <span className="font-mono tabular-nums text-muted-foreground">
                {formatNumber(ec.count)}
              </span>
            </Link>
          ))}
        </div>
      ) : null}

      {/* Failures table */}
      {isError ? (
        <div className="rounded-lg border border-border bg-card p-12 text-sm text-status-failed">
          Failed to load failures: {error instanceof Error ? error.message : 'unknown error'}
        </div>
      ) : items.length === 0 && !isPending ? (
        <div className="flex flex-col items-center justify-center rounded-lg border border-dashed border-border bg-card/50 px-6 py-16 text-center">
          <div className="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
            <Inbox className="size-6" aria-hidden />
          </div>
          <h2 className="mt-4 text-base font-semibold text-foreground">No failures in this window</h2>
          <p className="mt-1 max-w-md text-sm text-muted-foreground">
            That&apos;s good — nothing ended in failure, abandonment, or interrupt in
            the selected time range. Try a longer window if you expected to see
            something.
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-[11px] uppercase tracking-wider text-muted-foreground">
              <tr>
                <th scope="col" className="px-4 py-2 text-left font-medium">Agent</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Model</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Error class</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Started</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Cost</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Tokens</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Status</th>
                <th scope="col" className="px-4 py-2 text-right font-medium" />
              </tr>
            </thead>
            <tbody>
              {isPending && items.length === 0 ? (
                Array.from({ length: 6 }).map((_, i) => (
                  <tr key={i} className="border-t border-border/50">
                    <td className="px-4 py-2"><Skeleton className="h-4 w-32" /></td>
                    <td className="px-4 py-2"><Skeleton className="h-4 w-24" /></td>
                    <td className="px-4 py-2"><Skeleton className="h-4 w-40" /></td>
                    <td className="px-4 py-2"><Skeleton className="h-4 w-32" /></td>
                    <td className="px-4 py-2 text-right"><Skeleton className="ml-auto h-4 w-16" /></td>
                    <td className="px-4 py-2 text-right"><Skeleton className="ml-auto h-4 w-20" /></td>
                    <td className="px-4 py-2"><Skeleton className="h-5 w-20 rounded-full" /></td>
                    <td className="px-4 py-2" />
                  </tr>
                ))
              ) : (
                items.map((s) => <FailureRow key={s.id} session={s} />)
              )}
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

function FailureRow({ session }: { session: SessionListItem }) {
  const sessionHref = `/sessions/${encodeURIComponent(session.id)}`;
  return (
    <tr className="border-t border-border/50 transition-colors hover:bg-muted/30">
      <td className="px-4 py-2 font-medium text-foreground">{session.agent_name || session.native_id}</td>
      <td className="px-4 py-2 font-mono text-xs text-muted-foreground">{session.model || '—'}</td>
      <td className="px-4 py-2">
        {session.error_class ? (
          <span className="inline-flex items-center gap-1.5 rounded-md bg-status-failed/10 px-2 py-0.5 font-mono text-xs text-status-failed">
            <CircleAlert className="size-3" aria-hidden />
            {session.error_class}
          </span>
        ) : (
          <span className="font-mono text-xs text-muted-foreground">—</span>
        )}
      </td>
      <td className="px-4 py-2 font-mono text-xs text-muted-foreground tabular-nums">
        {formatTimestamp(session.start_ts)}
      </td>
      <td className="px-4 py-2 text-right font-mono text-xs tabular-nums text-foreground">
        {formatCost(session.cost_usd)}
      </td>
      <td className="px-4 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground">
        {formatNumber(session.tokens_in + session.tokens_out)}
      </td>
      <td className="px-4 py-2">
        <span className={cn(
          'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium',
          session.status === 'failed' && 'bg-status-failed/10 text-status-failed',
          session.status === 'abandoned' && 'bg-muted text-muted-foreground',
          session.status === 'interrupted' && 'bg-status-running/10 text-status-running',
        )}>
          {session.status}
        </span>
      </td>
      <td className="px-4 py-2 text-right">
        <Link
          to={sessionHref}
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          aria-label={`Open session ${session.agent_name || session.native_id}`}
        >
          Open
          <ExternalLink className="size-3" aria-hidden />
        </Link>
      </td>
    </tr>
  );
}

function FailuresStat({
  label,
  value,
  isLoading,
  format,
  tone = 'foreground',
}: {
  label: string;
  value: number | string | null;
  isLoading: boolean;
  format?: 'number' | 'cost' | 'text';
  tone?: 'foreground' | 'failed';
}) {
  const display = (() => {
    if (isLoading || value == null) {
      return <span aria-hidden className="inline-block h-5 w-20 rounded bg-muted" />;
    }
    if (format === 'number') return formatNumber(value as number);
    if (format === 'cost') return formatCost(value as number);
    return String(value);
  })();
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      <span
        className={cn(
          'font-mono text-lg font-semibold tabular-nums',
          tone === 'failed' ? 'text-status-failed' : 'text-foreground',
        )}
      >
        {display}
      </span>
    </div>
  );
}

function summarize(items: SessionListItem[]): {
  count: number;
  costUsd: number;
  tokensIn: number;
  tokensOut: number;
  topErrorClass: string | null;
} {
  const acc = items.reduce(
    (a, s) => ({
      count: a.count + 1,
      costUsd: a.costUsd + s.cost_usd,
      tokensIn: a.tokensIn + s.tokens_in,
      tokensOut: a.tokensOut + s.tokens_out,
    }),
    { count: 0, costUsd: 0, tokensIn: 0, tokensOut: 0 },
  );
  // Top error class: most common error_class in the failure set.
  const counts = new Map<string, number>();
  for (const s of items) {
    const ec = s.error_class;
    if (ec) {
      counts.set(ec, (counts.get(ec) ?? 0) + 1);
    }
  }
  let top: string | null = null;
  let topCount = 0;
  for (const [ec, c] of counts) {
    if (c > topCount) {
      top = ec;
      topCount = c;
    }
  }
  return { ...acc, topErrorClass: top };
}

function countErrorClasses(items: SessionListItem[]): { name: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const s of items) {
    const ec = s.error_class;
    if (ec) {
      counts.set(ec, (counts.get(ec) ?? 0) + 1);
    }
  }
  return Array.from(counts.entries())
    .sort((a, b) => b[1] - a[1])
    .slice(0, 8)
    .map(([name, count]) => ({ name, count }));
}

// buildQuery is a tiny URL builder for the error-class chip filter link.
// Uses URLSearchParams so it handles encoding consistently.
function buildQuery(params: Record<string, string | number | string[] | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined) continue;
    if (Array.isArray(v)) {
      if (v.length > 0) sp.set(k, v.join(','));
    } else {
      sp.set(k, String(v));
    }
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

// silence unused-import warning for ChevronDown (kept for future UX)
void ChevronDown;