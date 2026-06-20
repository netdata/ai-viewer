import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Activity, AlertOctagon, CircleCheck, Inbox, TriangleAlert } from 'lucide-react';
import { useSources, useHealth } from '../../api/sources';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { EntityListCard, EntityListSkeleton, EntityListEmpty } from '../../components/EntityList';
import { EntitySummaryStrip, EntitySummaryStripSkeleton } from '../../components/EntitySummaryStrip';
import { Skeleton } from '../../components/ui/skeleton';
import { ErrorState } from '../../components/StatusViews';
import { cn } from '../../lib/utils';
import { formatDuration, formatNumber, formatTimestamp } from '../../lib/format';

// IngestErrors — /ingest-errors (SOW-0082)
//
// Surfaces silent ingest errors per source. The data layer exposes
// per-source parse_errors count and lag via /api/health and /api/sources;
// this page ranks sources by error count so the operator sees
// "codex has 2042 errors" at a glance without curling /api/health.
//
// The /ingest-errors page also shows a lag indicator per source (green
// < 60s, yellow < 5min, red >= 5min) so the operator can spot a stalled
// ingest even when its parse_errors count is 0.
//
// Recent log entries with severity IN (WRN, ERR) would be the obvious
// follow-up — would need a cross-source /api/logs endpoint — and is
// tracked as a separate SOW.

const LAG_GREEN_US = 60 * 1_000_000;          // < 60s → green
const LAG_YELLOW_US = 5 * 60 * 1_000_000;     // < 5min → yellow
                                                  // >= 5min → red

// Stable empty array for the useMemo deps (avoids re-memo churn when
// sources is still pending).
const EMPTY_ITEMS: readonly NonNullable<ReturnType<typeof useSources>['data']>['items'][number][] = Object.freeze([]);

type LagTone = 'green' | 'yellow' | 'red' | 'unknown';

function lagTone(lagUs: number | null): LagTone {
  if (lagUs === null) return 'unknown';
  if (lagUs < LAG_GREEN_US) return 'green';
  if (lagUs < LAG_YELLOW_US) return 'yellow';
  return 'red';
}

function lagToneClass(tone: LagTone): string {
  switch (tone) {
    case 'green': return 'text-status-completed';
    case 'yellow': return 'text-status-running';
    case 'red': return 'text-status-failed';
    case 'unknown': return 'text-muted-foreground';
  }
}

function lagLabel(lagUs: number | null): string {
  if (lagUs === null) return '—';
  return formatDuration(lagUs);
}

export function IngestErrors() {
  const sources = useSources();
  const health = useHealth();

  // source_status_changed invalidates both ['sources'] and ['health'].
  useLiveUpdates({});

  const lagBySource = useMemo(() => {
    if (health.isError || !health.data) return new Map<string, number>();
    return new Map(health.data.sources.map((s) => [s.id, s.lag_us]));
  }, [health.data, health.isError]);

  const rawItems = sources.data?.items;
  // Stable reference for the empty case so the useMemo deps don't churn
  // when sources is still pending.
  const items = useMemo(() => rawItems ?? EMPTY_ITEMS, [rawItems]);

  const rankedSources = useMemo(() => {
    return [...items].sort((a, b) => {
      if (a.parse_errors !== b.parse_errors) {
        return b.parse_errors - a.parse_errors;
      }
      const aLag = lagBySource.get(a.id) ?? -1;
      const bLag = lagBySource.get(b.id) ?? -1;
      return bLag - aLag;
    });
  }, [items, lagBySource]);

  const totalErrors = items.reduce((acc, s) => acc + s.parse_errors, 0);
  const erroredSources = items.filter((s) => s.parse_errors > 0).length;
  const laggingSources = Array.from(lagBySource.values()).filter(
    (lag) => lag >= LAG_YELLOW_US,
  ).length;
  const healthLabel =
    health.data && !health.isError
      ? health.data.status
      : null;

  const summaryTiles = [
    { label: 'Total parse errors', value: totalErrors, format: 'number' as const,
      tone: totalErrors > 0 ? ('failed' as const) : ('default' as const) },
    { label: 'Sources with errors', value: erroredSources, format: 'number' as const },
    { label: 'Sources lagging ≥5m', value: laggingSources, format: 'number' as const,
      tone: laggingSources > 0 ? ('failed' as const) : ('default' as const) },
    { label: 'Health', value: healthLabel === null ? null : healthLabel.toUpperCase(),
      format: 'text' as const,
      tone:
        healthLabel === 'ok' ? ('success' as const) :
        healthLabel === 'degraded' ? ('default' as const) :
        healthLabel === 'down' ? ('failed' as const) :
        ('default' as const) },
  ];

  return (
    <section aria-labelledby="ingest-errors-title" className="flex flex-col gap-6 px-6 py-5">
      <div>
        <h1 id="ingest-errors-title" className="text-2xl font-semibold tracking-tight">
          Ingest errors
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Parse errors per source, ranked by error count. Recent log entries with
          severity IN (WRN, ERR) are tracked in a follow-up SOW.
        </p>
      </div>

      {sources.isPending || health.isPending ? (
        <EntitySummaryStripSkeleton />
      ) : (
        <EntitySummaryStrip tiles={summaryTiles} />
      )}

      {sources.isError ? (
        <ErrorState error={sources.error} title="Failed to load sources" />
      ) : sources.isPending ? (
        <EntityListSkeleton />
      ) : items.length === 0 ? (
        <EntityListEmpty
          label="Sources"
          hasFilters={false}
          onClearFilters={undefined}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-[11px] uppercase tracking-wider text-muted-foreground">
              <tr>
                <th scope="col" className="px-4 py-2 text-left font-medium">Source</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Format</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Parse errors</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Lag</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Last seen</th>
                <th scope="col" className="px-4 py-2 text-left font-medium" />
              </tr>
            </thead>
            <tbody>
              {rankedSources.map((src, i) => {
                const lagUs = lagBySource.get(src.id) ?? null;
                const tone = lagTone(lagUs);
                return (
                  <tr
                    key={src.id}
                    className={cn(
                      'border-t border-border/50 transition-colors hover:bg-muted/30',
                      i % 2 === 1 && 'bg-muted/20',
                    )}
                  >
                    <td className="px-4 py-2 font-mono text-xs text-foreground">{src.id}</td>
                    <td className="px-4 py-2 font-mono text-xs text-muted-foreground">
                      {src.format}
                    </td>
                    <td className={cn(
                      'px-4 py-2 text-right font-mono tabular-nums',
                      src.parse_errors > 0 ? 'text-status-failed font-semibold' : 'text-muted-foreground',
                    )}>
                      {formatNumber(src.parse_errors)}
                    </td>
                    <td className={cn(
                      'px-4 py-2 text-right font-mono tabular-nums',
                      lagToneClass(tone),
                    )}>
                      {lagLabel(lagUs)}
                    </td>
                    <td className="px-4 py-2 font-mono text-xs tabular-nums text-muted-foreground">
                      {formatTimestamp(src.last_seen_at)}
                    </td>
                    <td className="px-4 py-2 text-right">
                      <Link
                        to={`/sessions?sources=${encodeURIComponent(src.id)}`}
                        className="text-xs text-muted-foreground hover:text-foreground"
                        aria-label={`Open sessions for ${src.id}`}
                      >
                        View sessions →
                      </Link>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// silence unused-import warnings for icons kept for future use
void Activity;
void AlertOctagon;
void CircleCheck;
void Inbox;
void TriangleAlert;
void EntityListCard;
void Skeleton;
