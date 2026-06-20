import { Link } from 'react-router-dom';
import { useHomeSummary, todayMidnightUs } from '../../api/home';
import { Skeleton } from '../../components/ui/skeleton';
import { cn } from '../../lib/utils';
import { formatCost, formatNumber } from '../../lib/format';

// HomeSummaryCard — SOW-0079 P0.1.
//
// Renders a 5-tile summary card above the Sessions table that answers the
// operator's #1 question ("what's happening right now?") in under five
// seconds. Each tile is clickable and navigates to a filtered view of the
// underlying data.
//
// The card uses the existing /api/stats endpoint with two parallel filter
// sets (today's totals + currently-running count), reuses the project's
// useQuery machinery so the SSE stats_invalidated event auto-refreshes the
// numbers, and renders skeleton placeholders while the query is pending so
// the page opens immediately.
//
// The card is rendered above the Sessions page header; it is mounted
// in-place rather than in a route so its data shares the query cache
// with the table that follows.

export function HomeSummaryCard() {
  const home = useHomeSummary();

  // The four navigable tiles point at filtered routes. Each target is
  // a URL the rest of the app already understands:
  //   /?status=running                       — Sessions filtered by status
  //   /stats?from=today                     — Stats since today's local midnight
  //   /failures?from=today                  — Recent failures since today
  // We default the time window to today's local midnight for cost/reliability.
  const fromToday = todayMidnightUs();

  const data = home.data;
  const today = data?.today ?? null;
  const running = data?.running ?? null;
  const reliability =
    today !== null && today.sessionCount > 0
      ? (today.sessionCount - today.failures) / today.sessionCount
      : null;
  const reliabilityTone: 'default' | 'failed' | 'success' =
    reliability === null
      ? 'default'
      : reliability >= 0.9
        ? 'success'
        : reliability >= 0.7
          ? 'default'
          : 'failed';

  const tiles: readonly {
    label: string;
    value: number | null;
    sublabel: string;
    href: string | undefined;
    tone?: 'default' | 'failed' | 'success';
    format: 'number' | 'cost' | 'percent';
  }[] = [
    {
      label: 'Active',
      value: running?.sessionCount ?? null,
      sublabel: 'Sessions running right now',
      href: '/?status=running',
      format: 'number',
    },
    {
      label: 'Today\u2019s spend',
      value: today?.costUsd ?? null,
      sublabel: 'Since local midnight',
      href: `/stats?from=${fromToday}`,
      format: 'cost',
    },
    {
      label: 'Failed today',
      value: today?.failures ?? null,
      sublabel: 'Failed, abandoned, or interrupted',
      href: `/failures?from=${fromToday}`,
      format: 'number',
      tone: 'failed',
    },
    {
      label: 'Sessions today',
      value: today?.sessionCount ?? null,
      sublabel: 'Total sessions since midnight',
      href: `/?from=${fromToday}`,
      format: 'number',
    },
    {
      label: 'Reliability',
      value: reliability,
      sublabel: 'Successful / total sessions',
      href: `/stats?from=${fromToday}`,
      format: 'percent',
      tone: reliabilityTone,
    },
  ];

  return (
    <section
      aria-label="Home summary"
      className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5"
    >
      {tiles.map((t) => (
        <HomeTile key={t.label} {...t} />
      ))}
    </section>
  );
}

interface TileProps {
  label: string;
  value: number | null;
  sublabel: string;
  href: string | undefined;
  format: 'number' | 'cost' | 'percent';
  tone?: 'default' | 'failed' | 'success';
}

function HomeTile({ label, value, sublabel, href, format, tone = 'default' }: TileProps) {
  const body = (
    <div
      className={cn(
        'flex flex-col gap-1 rounded-lg border bg-card p-4 transition-colors',
        href !== undefined ? 'hover:border-primary hover:bg-card/80' : '',
        tone === 'failed' ? 'border-status-failed/30' : 'border-border',
      )}
    >
      <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      {value === null ? (
        <Skeleton className="h-7 w-24" />
      ) : (
        <span
          className={cn(
            'font-mono text-xl font-semibold tabular-nums',
            tone === 'failed' && 'text-status-failed',
            tone === 'success' && 'text-status-completed',
            tone === 'default' && 'text-foreground',
          )}
        >
          {format === 'number'
            ? formatNumber(value)
            : format === 'cost'
              ? formatCost(value)
              : `${(value * 100).toFixed(1)}%`}
        </span>
      )}
      <span className="text-[10px] text-muted-foreground">{sublabel}</span>
    </div>
  );
  if (href === undefined) {
    return body;
  }
  return (
    <Link
      to={href}
      className="block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background rounded-lg"
      aria-label={`${label} — ${sublabel}. Open filtered view.`}
    >
      {body}
    </Link>
  );
}
