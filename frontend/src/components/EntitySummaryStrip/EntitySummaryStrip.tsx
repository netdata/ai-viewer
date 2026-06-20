import { cn } from '../../lib/utils';
import { Skeleton } from '../ui/skeleton';
import { formatNumber } from '../../lib/format';

// EntitySummaryStrip — a 4-tile summary shown at the top of each per-entity
// list page (/agents, /models, /tools — SOW-0081). It shows the AGGREGATE
// stats across all entities in the current view: total entities, total
// sessions, total cost, average reliability.
//
// The per-entity drill page (e.g. /agents/:name) uses a 5-tile strip that
// is just the home summary card scoped to a single entity's filter; that
// page lives inline in its file because it needs the useStats hook with
// the entity filter applied.

export interface EntitySummaryTile {
  label: string;
  value: number | string | null;
  format: 'number' | 'cost' | 'percent' | 'text';
  tone?: 'default' | 'failed' | 'success' | undefined;
}

export function EntitySummaryStrip({ tiles }: { tiles: readonly EntitySummaryTile[] }) {
  return (
    <div className="grid grid-cols-2 gap-3 rounded-lg border border-border bg-card p-4 sm:grid-cols-4">
      {tiles.map((t) => (
        <Tile key={t.label} tile={t} />
      ))}
    </div>
  );
}

function Tile({ tile }: { tile: EntitySummaryTile }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
        {tile.label}
      </span>
      {tile.value === null ? (
        <Skeleton className="h-7 w-24" />
      ) : (
        <span
          className={cn(
            'font-mono text-lg font-semibold tabular-nums',
            tile.tone === 'failed' && 'text-status-failed',
            tile.tone === 'success' && 'text-status-completed',
            (tile.tone === 'default' || tile.tone === undefined) && 'text-foreground',
          )}
        >
          {tile.format === 'percent'
            ? `${(Number(tile.value) * 100).toFixed(1)}%`
            : tile.format === 'cost'
              ? `$${Number(tile.value).toFixed(2)}`
              : tile.format === 'text'
                ? String(tile.value)
                : formatNumber(Number(tile.value))}
        </span>
      )}
    </div>
  );
}

/** EntitySummaryStripSkeleton renders the strip with skeleton tiles. */
export function EntitySummaryStripSkeleton() {
  return (
    <div className="grid grid-cols-2 gap-3 rounded-lg border border-border bg-card p-4 sm:grid-cols-4">
      {Array.from({ length: 4 }).map((_, i) => (
        <div key={i} className="flex flex-col gap-0.5">
          <span className="h-3 w-16 rounded bg-muted" />
          <Skeleton className="h-7 w-24" />
        </div>
      ))}
    </div>
  );
}
