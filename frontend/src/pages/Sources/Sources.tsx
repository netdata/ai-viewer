import { CircleCheck, CircleAlert, CircleX, Database, RefreshCw } from 'lucide-react';
import { useSources, useHealth } from '../../api/sources';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState } from '../../components/StatusViews';
import { Button } from '../../components/ui/button';
import { formatNumber, formatTimestamp } from '../../lib/format';
import { cn } from '../../lib/utils';
import type { HealthStatus, SourceItem } from '../../api/types';

// Sources admin / status panel (ui-pages.md §/sources). A per-source table
// (id, format, lifecycle, read models, parse_errors, last_seq, progress) plus
// an overall health badge from /api/health. Live: source_status_changed
// invalidates both ['sources'] and ['health'].

const HEALTH_META: Record<HealthStatus, { Icon: typeof CircleCheck; tone: string; label: string }> = {
  ok:       { Icon: CircleCheck, tone: 'text-status-completed', label: 'Healthy' },
  degraded: { Icon: CircleAlert, tone: 'text-status-running',  label: 'Degraded' },
  down:     { Icon: CircleX,     tone: 'text-status-failed',   label: 'Down' },
};

function sourceColorVar(format: string): string {
  switch (format) {
    case 'aiagent_v3': return '--source-aiagent-v3';
    case 'aiagent_v2': return '--source-aiagent-v2';
    case 'claude-code': return '--source-claude-code';
    case 'codex': return '--source-codex';
    case 'opencode': return '--source-opencode';
    default: return '--border';
  }
}

function metadataSummary(meta: SourceItem['meta']): string {
  const count = meta === undefined ? 0 : Object.keys(meta).length;
  if (count === 0) {
    return '—';
  }
  return `${formatNumber(count)} metadata ${count === 1 ? 'key' : 'keys'}`;
}

function stateLabel(state: string): string {
  if (state === '') {
    return 'Unknown';
  }
  const spaced = state.replaceAll('_', ' ');
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

function lifecycleTone(state: string): string {
  switch (state) {
    case 'tailing':
    case 'scan_complete':
      return 'bg-status-completed/10 text-status-completed';
    case 'scanning':
    case 'tail_starting':
    case 'tail_restarting':
    case 'starting':
      return 'bg-status-running/10 text-status-running';
    case 'start_failed':
    case 'construct_failed':
    case 'scan_failed':
    case 'tail_stale':
    case 'tail_failed':
      return 'bg-status-failed/10 text-status-failed';
    case 'stopped':
      return 'bg-muted text-muted-foreground';
    default:
      return 'bg-muted text-muted-foreground';
  }
}

function readModelTone(state: string): string {
  switch (state) {
    case 'ready':
      return 'bg-status-completed/10 text-status-completed';
    case 'repair_pending':
    case 'repairing':
      return 'bg-status-running/10 text-status-running';
    case 'repair_timeout':
    case 'repair_failed':
      return 'bg-status-failed/10 text-status-failed';
    default:
      return 'bg-muted text-muted-foreground';
  }
}

function StatusPill({ label, tone }: { label: string; tone: string }) {
  return (
    <span className={cn('inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium', tone)}>
      {label}
    </span>
  );
}

function sourceProgressTimestamp(src: SourceItem): number | null {
  return src.progress_updated_at ?? src.updated_at;
}

export function Sources() {
  const sources = useSources();
  const health = useHealth();

  // A source_status_changed frame refreshes both ['sources'] and ['health'].
  useLiveUpdates({});

  const items = sources.data?.items ?? [];
  const enabledCount = items.filter((s) => s.enabled).length;
  const errorCount = items.reduce((acc, s) => acc + s.parse_errors, 0);
  const healthLabel = health.data && !health.isError ? HEALTH_META[health.data.status] : null;

  return (
    <section aria-labelledby="sources-title" className="flex flex-col gap-6 px-6 py-5">
      <div>
        <h1 id="sources-title" className="text-2xl font-semibold tracking-tight">Sources</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Configured ingest sources, their lifecycle, and their current read-model state.
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
                <th scope="col" className="px-4 py-2 text-left font-medium">Lifecycle</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Read models</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Enabled</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Parse errors</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Metadata</th>
                <th scope="col" className="px-4 py-2 text-right font-medium">Last seq</th>
                <th scope="col" className="px-4 py-2 text-left font-medium">Progress</th>
              </tr>
            </thead>
            <tbody>
              {items.map((src, i) => {
                const sourceColor = `var(${sourceColorVar(src.format)})`;
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
                      <div className="flex flex-col gap-1">
                        <StatusPill label={stateLabel(src.lifecycle_state)} tone={lifecycleTone(src.lifecycle_state)} />
                        {src.lifecycle_error ? (
                          <span className="max-w-[18rem] truncate text-xs text-status-failed">
                            {src.lifecycle_error}
                          </span>
                        ) : null}
                      </div>
                    </td>
                    <td className="px-4 py-2">
                      <div className="flex flex-col gap-1">
                        <StatusPill label={stateLabel(src.read_model_state)} tone={readModelTone(src.read_model_state)} />
                        {src.read_model_error ? (
                          <span className="max-w-[18rem] truncate text-xs text-status-failed">
                            {src.read_model_error}
                          </span>
                        ) : null}
                      </div>
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
                    <td className="px-4 py-2 text-xs text-muted-foreground">
                      {metadataSummary(src.meta)}
                    </td>
                    <td className="px-4 py-2 text-right font-mono tabular-nums text-muted-foreground">
                      {formatNumber(src.last_seq)}
                    </td>
                    <td className="px-4 py-2 font-mono text-xs tabular-nums text-muted-foreground">
                      {formatTimestamp(sourceProgressTimestamp(src))}
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
