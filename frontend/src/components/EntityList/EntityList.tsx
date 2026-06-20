import { Link } from 'react-router-dom';
import { ArrowRight, Inbox } from 'lucide-react';
import { Skeleton } from '../ui/skeleton';
import { Button } from '../ui/button';
import { cn } from '../../lib/utils';
import { formatCost, formatNumber, formatTimestamp } from '../../lib/format';

// EntityList — a reusable card grid for the per-entity index pages
// (SOW-0081: /agents, /models, /tools). Each card shows the entity's
// primary identifier + a few summary stats + a drill-down link.
//
// The card component is shared; each page passes its own Entity type and
// stats formatters so the visual stays consistent. The page-specific
// summary at the top (4 tiles) is the page's responsibility.
//
// Visual:
//   grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3
//   each card: hover:border-primary hover:bg-card/80 (link affordance)
//   stat tiles inside: monospace tabular-nums

export interface EntityListCardProps<Entity> {
  entity: Entity;
  /** URL to navigate to when the card is clicked. */
  href: string;
  /** Display label (the primary identifier). */
  primaryLabel: string;
  /** Optional secondary line under the primary label. */
  secondaryLabel?: string;
  /** Stat tiles to render inside the card. */
  stats: readonly { label: string; value: string; tone?: 'default' | 'failed' | 'success' }[];
  /** Optional reliability-style percent display. */
  reliability?: { value: number; tone?: 'default' | 'failed' | 'success' } | undefined;
  /** Optional last-seen timestamp. */
  lastSeen?: number | undefined;
}

export function EntityListCard<Entity>({
  href,
  primaryLabel,
  secondaryLabel,
  stats,
  reliability,
  lastSeen,
}: EntityListCardProps<Entity>) {
  return (
    <Link
      to={href}
      className="group block rounded-lg border border-border bg-card p-4 transition-colors hover:border-primary hover:bg-card/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      aria-label={`${primaryLabel} — ${stats.map((s) => `${s.label} ${s.value}`).join(', ')}`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate font-mono text-sm font-semibold text-foreground">
            {primaryLabel}
          </div>
          {secondaryLabel ? (
            <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
              {secondaryLabel}
            </div>
          ) : null}
        </div>
        <ArrowRight
          className="size-3.5 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-foreground"
          aria-hidden
        />
      </div>

      <dl className="mt-3 grid grid-cols-2 gap-x-3 gap-y-2 text-xs">
        {stats.map((s) => (
          <div key={s.label} className="flex flex-col gap-0.5">
            <dt className="text-[10px] uppercase tracking-wider text-muted-foreground">
              {s.label}
            </dt>
            <dd
              className={cn(
                'font-mono text-sm font-medium tabular-nums',
                s.tone === 'failed' && 'text-status-failed',
                s.tone === 'success' && 'text-status-completed',
                (s.tone === 'default' || s.tone === undefined) && 'text-foreground',
              )}
            >
              {s.value}
            </dd>
          </div>
        ))}
        {reliability ? (
          <div className="flex flex-col gap-0.5">
            <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
              Reliability
            </span>
            <span
              className={cn(
                'font-mono text-sm font-medium tabular-nums',
                reliability.tone === 'failed' && 'text-status-failed',
                reliability.tone === 'success' && 'text-status-completed',
                (reliability.tone === 'default' || reliability.tone === undefined) && 'text-foreground',
              )}
            >
              {(reliability.value * 100).toFixed(1)}%
            </span>
          </div>
        ) : null}
      </dl>

      {lastSeen ? (
        <div className="mt-3 text-[10px] text-muted-foreground">
          Last seen{' '}
          <span className="font-mono tabular-nums">{formatTimestamp(lastSeen)}</span>
        </div>
      ) : null}
    </Link>
  );
}

/** EntityListSkeleton renders N skeleton cards in the grid layout. */
export function EntityListSkeleton({ count = 6 }: { count?: number }) {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {Array.from({ length: count }).map((_, i) => (
        <div key={i} className="rounded-lg border border-border bg-card p-4">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="mt-2 h-3 w-20" />
          <div className="mt-4 grid grid-cols-2 gap-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        </div>
      ))}
    </div>
  );
}

/** EntityListEmpty is the shared "no entities in this time window" state. */
export function EntityListEmpty({
  label,
  hasFilters,
  onClearFilters,
}: {
  label: string;
  hasFilters: boolean;
  onClearFilters?: (() => void) | undefined;
}) {
  return (
    <div className="flex flex-col items-center justify-center rounded-lg border border-dashed border-border bg-card/50 px-6 py-16 text-center">
      <div className="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
        <Inbox className="size-6" aria-hidden />
      </div>
      <h2 className="mt-4 text-base font-semibold text-foreground">
        No {label.toLowerCase()} in this window
      </h2>
      <p className="mt-1 max-w-md text-sm text-muted-foreground">
        {hasFilters
          ? 'Active filters are excluding everything. Try widening the time window or clearing a filter.'
          : `Nothing ran with a ${label.toLowerCase()} in the selected time range.`}
      </p>
      {onClearFilters && hasFilters ? (
        <Button variant="outline" size="sm" className="mt-4" onClick={onClearFilters}>
          Clear filters
        </Button>
      ) : null}
    </div>
  );
}

// shared formatter helpers — also used by EntitySummaryStrip
export function fmtCost(usd: number): string {
  return formatCost(usd);
}

export function fmtNum(n: number): string {
  return formatNumber(n);
}

export function fmtTs(us: number | null | undefined): string {
  if (us === null || us === undefined) return '—';
  return formatTimestamp(us);
}
