import { CircleCheck, CircleAlert, CircleX, Database, RefreshCw } from 'lucide-react';
import { useSources, useHealth } from '../../api/sources';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState } from '../../components/StatusViews';
import { Button } from '../../components/ui/button';
import { formatDuration, formatNumber, formatTimestamp } from '../../lib/format';
import { cn } from '../../lib/utils';
import type { HealthStatus, SourceItem } from '../../api/types';

// Sources admin / status panel (ui-pages.md §/sources). A per-source table
// (id, format, enabled, parse_errors, lag, last_seq, last_seen) plus an overall
// health badge from /api/health. Live: source_status_changed invalidates both
// ['sources'] and ['health'].

const HEALTH_META: Record<HealthStatus, { Icon: typeof CircleCheck; tone: string; label: string }> = {
  ok:       { Icon: CircleCheck, tone: 'text-status-completed', label: 'Healthy' },
  degraded: { Icon: CircleAlert, tone: 'text-status-running',  label: 'Degraded' },
  down:     { Icon: CircleX,     tone: 'text-status-failed',   label: 'Down' },
};

/** lagFor reads a source's ingest lag from the health snapshot (the sources
 *  list carries no lag field; lag is a health observability metric). */
function lagFor(lagBySource: Map<string, number>, source: SourceItem): string {
  const lag = lagBySource.get(source.id);
  return lag === undefined ? '—' : formatDuration(lag);
}

export function Sources() {
  const sources = useSources();
  const health = useHealth();

  // A source_status_changed frame refreshes both ['sources'] and ['health'].
  useLiveUpdates({});

  // On a health error, ignore any stale health.data: TanStack Query keeps the
  // last successful payload across a failed background refetch (the live
  // source_status_changed path), so lag must fall back to '—' via lagFor — not
  // show stale numbers beside the error banner. (ui-pages.md §/sources)
  const lagBySource = new Map<string, number>(
    (health.isError ? [] : (health.data?.sources ?? [])).map((s) => [s.id, s.lag_us]),
  );
  const items = sources.data?.items ?? [];
  const enabledCount = items.filter((s) => s.enabled).length;
  const errorCount = items.reduce((acc, s) => acc + s.parse_errors, 0);
  const healthLabel = health.data && !health.isError ? HEALTH_META[health.data.status] : null;

  return (
    <section aria-labelledby="sources-title" className="flex flex-col gap-6 px-6 py-5">
      <div>
        <h1 id="sources-title" className="text-2xl font-semibold tracking-tight">Sources</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Configured ingest sources, their health, and their current parse-error + lag posture.
        </p>
      </div>

      {/* Stat summary */}
      {items.length > 0 ? (
        <div className="grid grid-cols-2 gap-3 rounded-lg border border-border bg-card p-4 sm:grid-cols-4">
          <SummaryStat label="Sources" value={formatNumber(items.length)} />
          <SummaryStat label="Enabled" value={formatNumber(enabledCount)} />
          <SummaryStat
            label="Parse errors"
            value={formatNumber(errorCount)}
            accent={errorCount > 0 ? 'text-status-failed' : 'text-foreground'}
          />
          <div className="flex flex-col gap-0.5">
            <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
              Health
            </span>
            {healthLabel !== null ? (
              <span className={cn('inline-flex items-center gap-1.5 text-sm font-semibold', healthLabel.tone)}>
                <healthLabel.Icon className="size-4" aria-hidden />
                {healthLabel.label}
              </span>
            ) : (
              <span className="text-sm font-semibold text-muted-foreground">—</span>
            )}
          </div>
        </div>
      ) : null}

      {health.isError ? (
        <div className="rounded-lg border border-border bg-card p-4">
          <ErrorState error={health.error} title="Health unavailable" />
        </div>
      ) : null}

      {sources.isPending ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <LoadingState label="Loading sources…" />
        </div>
      ) : sources.isError ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <ErrorState error={sources.error} title="Failed to load sources" />
        </div>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border bg-card/50 p-12">
          <div className="flex flex-col items-center text-center">
            <div className="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
              <Database className="size-6" aria-hidden />
            </div>
            <h2 className="mt-4 text-base font-semibold text-foreground">No sources configured</h2>
            <p className="mt-1 max-w-md text-sm text-muted-foreground">
              Once you start the ingest daemon with at least one --source flag, the
              configured sources will appear here in real time.
            </p>
          </div>
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-[11px] uppercase tracking-wider text-muted-foreground">
              <tr>
                <th scope="col" className="px-4 py-2 text-left font-medium">ID</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Format</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Enabled</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Parse errors</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Lag</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Last seq</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Last seen</th>
              </tr>
            </thead>
            <tbody>
              {items.map((src, i) => {
                const sourceColor = `var(--source-${src.format.replace(/[^a-z0-9]/gi, '').toLowerCase()}, var(--border))`;
                return (
                  <tr
                    key={src.id}
                    className={cn(
                      'border-t border-border/50 transition-colors hover:bg-muted/40',
                      i % 2 === 1 && 'bg-muted/20',
                    )}
                  >
                    <td className="px-4 py-2 font-mono text-xs text-foreground">
                      <span className="flex items-center gap-2">
                        <span
                          aria-hidden
                          className="inline-block size-1.5 shrink-0 rounded-full"
                          style={{ backgroundColor: sourceColor }}
                        />
                        {src.id}
                      </span>
                    </td>
                    <td className="px-4 py-2 font-mono text-xs text-muted-foreground">
                      <span
                        className="inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-[11px] font-medium"
                        style={{
                          color: sourceColor,
                          borderColor: `color-mix(in oklch, ${sourceColor} 30%, transparent)`,
                          backgroundColor: `color-mix(in oklch, ${sourceColor} 8%, transparent)`,
                        }}
                      >
                        {src.format}
                      </span>
                    </td>
                    <td className="px-4 py-2">
                      <span className={cn(
                        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium',
                        src.enabled
                          ? 'bg-status-completed/10 text-status-completed'
                          : 'bg-muted text-muted-foreground',
                      )}>
                        <span aria-hidden className={cn('size-1.5 rounded-full', src.enabled ? 'bg-status-completed' : 'bg-muted-foreground')} />
                        {src.enabled ? 'enabled' : 'disabled'}
                      </span>
                    </td>
                    <td className={cn(
                      'px-4 py-2 text-right font-mono tabular-nums',
                      src.parse_errors > 0 ? 'text-status-failed' : 'text-muted-foreground',
                    )}>
                      {formatNumber(src.parse_errors)}
                    </td>
                    <td className="px-4 py-2 text-right font-mono tabular-nums text-muted-foreground">
                      {lagFor(lagBySource, src)}
                    </td>
                    <td className="px-4 py-2 text-right font-mono tabular-nums text-muted-foreground">
                      {formatNumber(src.last_seq)}
                    </td>
                    <td className="px-4 py-2 font-mono text-xs tabular-nums text-muted-foreground">
                      {formatTimestamp(src.last_seen_at)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <div className="flex justify-end">
        <Button
          variant="outline"
          size="sm"
          onClick={() => { void sources.refetch(); void health.refetch(); }}
          aria-label="Refresh sources and health"
        >
          <RefreshCw className="size-3.5" aria-hidden />
          Refresh
        </Button>
      </div>
    </section>
  );
}

function SummaryStat({ label, value, accent }: { label: string; value: string; accent?: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      <span className={cn('font-mono text-lg font-semibold tabular-nums', accent ?? 'text-foreground')}>
        {value}
      </span>
    </div>
  );
}