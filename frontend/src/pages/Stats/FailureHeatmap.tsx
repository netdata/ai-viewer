import { useEffect, useMemo, useState } from 'react';
import { Heatmap } from '../../components/Heatmap';
import { Skeleton } from '../../components/ui/skeleton';
import { cn } from '../../lib/utils';
import { formatNumber } from '../../lib/format';
import type { SessionListItem, SessionListResponse } from '../../api/types';
import type { Filters } from '../../state/filters';

// FailureHeatmap — SOW-0087 chunk 2.
//
// Renders a Heatmap showing failed sessions bucketed by
// (day-of-week, hour-of-day) of their start_ts. The data is
// fetched from /api/sessions with a status filter that
// captures all failure modes (failed + abandoned + interrupted)
// over the last 7 days.
//
// This is intentionally client-side aggregation: the
// /api/stats/aggregate endpoint is time-bucketed but does not
// expose a day-of-week × hour-of-day cross-cut. A future
// /api/stats/hourly endpoint could pre-aggregate this server-side
// (and would scale better past ~10k failures). For now,
// ~7 days × status IN (failed, abandoned, interrupted) is bounded.
//
// We walk pages (paginated) until we have either hit `hardCap`
// rows or `next_cursor` is null. Sessions is keyset-paginated.

const ONE_WEEK_US = 7 * 24 * 3_600_000_000;
const HARD_CAP = 10_000;
const PAGE_LIMIT = 200;
const FAILURE_STATUSES = ['failed', 'abandoned', 'interrupted'];

async function fetchAllFailedSessions(
  fromUs: number,
  signal: AbortSignal,
): Promise<SessionListItem[]> {
  const all: SessionListItem[] = [];
  let cursor: string | undefined;
  for (let i = 0; i < 100; i += 1) {
    const params = new URLSearchParams();
    params.set('from', String(fromUs));
    for (const s of FAILURE_STATUSES) params.append('status', s);
    if (cursor !== undefined && cursor !== '') params.set('cursor', cursor);
    params.set('limit', String(PAGE_LIMIT));
    const res = await fetch(`/api/sessions?${params.toString()}`, { signal });
    if (!res.ok) {
      throw new Error(`status ${res.status}`);
    }
    const body = (await res.json()) as SessionListResponse;
    all.push(...body.items);
    const next = body.next_cursor;
    if (next === undefined || next === '' || body.items.length < PAGE_LIMIT) break;
    if (all.length >= HARD_CAP) break;
    cursor = next;
  }
  return all;
}

export function FailureHeatmap({ filters }: { filters: Filters }) {
  const [rows, setRows] = useState<SessionListItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  // The heatmap uses local-time, last-7-days. It deliberately
  // ignores the user's filter (from/to) so the picture is always the
  // same — an observability heatmap is comparable across visits.
  useEffect(() => {
    const ctl = new AbortController();
    const fromUs = Date.now() * 1000 - ONE_WEEK_US;
    fetchAllFailedSessions(fromUs, ctl.signal)
      .then((items) => {
        if (!ctl.signal.aborted) setRows(items);
      })
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) {
          setError(err instanceof Error ? err.message : 'unknown error');
        }
      });
    return () => { ctl.abort(); };
  }, []);

  // The "filters" prop is unused but kept in the signature so the page
  // can pass it as an extensibility hook (e.g. to surface user-controlled
  // time-window selection later).
  void filters;

  const counts = useMemo(() => buildCounts(rows ?? []), [rows]);

  if (error !== null) {
    return (
      <p className="text-xs text-status-failed">
        Heatmap unavailable: {error}
      </p>
    );
  }
  if (rows === null) {
    return (
      <div className="space-y-2" aria-busy="true">
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <Heatmap counts={counts} />
      <p
        className={cn(
          'text-[10px] uppercase tracking-wider text-muted-foreground',
        )}
      >
        {formatNumber(rows.length)} failed sessions in the last 7 days · local time
      </p>
    </div>
  );
}

function buildCounts(rows: readonly SessionListItem[]): Record<string, number> {
  const out: Record<string, number> = {};
  for (const r of rows) {
    if (r.start_ts <= 0) continue;
    const d = new Date(r.start_ts / 1000);
    const key = `${d.getDay()}:${d.getHours()}`;
    out[key] = (out[key] ?? 0) + 1;
  }
  return out;
}
