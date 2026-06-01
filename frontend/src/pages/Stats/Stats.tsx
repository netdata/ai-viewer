import { useState } from 'react';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useAggregate, useTop } from '../../api/stats';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState } from '../../components/StatusViews';
import type { StatsBucket, StatsMetric, TopDimension } from '../../api/types';
import { LineChart } from './charts/LineChart';
import { BarChart } from './charts/BarChart';
import { SearchBox } from './SearchBox';
import styles from './Stats.module.css';

// Statistics dashboard (ui-pages.md §/stats). Three sections over the global
// FilterBar's filters: per-bucket TREND line chart (cost/tokens/…), a TOP-N
// horizontal bar chart by a selectable dimension, and a deep full-text SEARCH
// box whose hits link to the matching session. The FilterBar (in Layout) drives
// `filters`; this page only reads them and adds its own chart controls as
// page-LOCAL state (NOT global filters — they tune the charts, not the data set).
//
// LIVE: useLiveUpdates(subscription) keeps one SSE subscription open; a
// stats_invalidated frame invalidates the ['stats'] key wholesale (api/sse.ts
// onStatsInvalidated), so the aggregate + top queries (both keyed under
// ['stats', …]) auto-refresh. SEARCH is deliberately NOT invalidated by SSE: a
// result list jumping on every ingest is poor UX (it refetches on q/filter
// change only — its key is ['search', …], outside the invalidated prefix).

/** The trend-metric options (StatsMetric is the closed server enum). */
const METRIC_OPTIONS: ReadonlyArray<{ value: StatsMetric; label: string }> = [
  { value: 'cost', label: 'Cost' },
  { value: 'tokens_in', label: 'Tokens in' },
  { value: 'tokens_out', label: 'Tokens out' },
  { value: 'calls', label: 'Calls' },
  { value: 'failures', label: 'Failures' },
  { value: 'duration_us', label: 'Duration' },
];

/** The top-N ranking dimensions (TopDimension excludes total/source_format). */
const DIMENSION_OPTIONS: ReadonlyArray<{ value: TopDimension; label: string }> = [
  { value: 'model', label: 'Model' },
  { value: 'provider', label: 'Provider' },
  { value: 'tool', label: 'Tool' },
  { value: 'agent', label: 'Agent' },
  { value: 'cwd', label: 'Working dir' },
];

/** Fixed top-N size; the server clamps to [1,200] (rest-api.md §GET /api/stats/top). */
const TOP_N = 20;

export function Stats() {
  const { filters } = useFilters();

  // Page-local chart controls (NOT global filters). The trend chart and the
  // top-N chart carry SEPARATE metrics on purpose: a user typically compares a
  // cost trend over time against, say, a failures-by-tool ranking.
  const [trendMetric, setTrendMetric] = useState<StatsMetric>('cost');
  const [bucket, setBucket] = useState<StatsBucket>('daily');
  const [topDimension, setTopDimension] = useState<TopDimension>('model');
  const [topMetric, setTopMetric] = useState<StatsMetric>('cost');

  const aggregate = useAggregate(filters, { bucket, groupBy: 'total', metric: trendMetric });
  const ranking = useTop(filters, { dimension: topDimension, metric: topMetric, n: TOP_N });

  // One live subscription for the active filter; stats_invalidated refreshes the
  // ['stats']-keyed aggregate + top queries (search stays put — see file header).
  useLiveUpdates(filtersToSubscription(filters));

  return (
    <section aria-labelledby="stats-title">
      <h1 id="stats-title">Statistics</h1>

      {/* ── Trends over time (line chart) ─────────────────────────────────── */}
      <section className={styles.panel} aria-labelledby="stats-trends-title">
        <div className={styles.panelHeader}>
          <h2 id="stats-trends-title" className={styles.panelTitle}>
            Trends over time
          </h2>
          <div className={styles.controls}>
            <label className={styles.control}>
              <span className={styles.controlLabel}>Trend metric</span>
              <select
                className={styles.select}
                value={trendMetric}
                onChange={(e) => setTrendMetric(e.target.value as StatsMetric)}
              >
                {METRIC_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
            <label className={styles.control}>
              <span className={styles.controlLabel}>Time bucket</span>
              <select
                className={styles.select}
                value={bucket}
                onChange={(e) => setBucket(e.target.value as StatsBucket)}
              >
                <option value="daily">Daily</option>
                <option value="hourly">Hourly</option>
              </select>
            </label>
          </div>
        </div>

        {aggregate.isPending ? (
          <LoadingState label="Loading trends…" />
        ) : aggregate.isError ? (
          <ErrorState error={aggregate.error} title="Failed to load trends" />
        ) : (
          <LineChart
            buckets={aggregate.data?.buckets ?? []}
            metric={trendMetric}
            bucket={bucket}
          />
        )}
      </section>

      {/* ── Top-N breakdown (horizontal bars) ─────────────────────────────── */}
      <section className={styles.panel} aria-labelledby="stats-top-title">
        <div className={styles.panelHeader}>
          <h2 id="stats-top-title" className={styles.panelTitle}>
            Top {TOP_N} breakdown
          </h2>
          <div className={styles.controls}>
            <label className={styles.control}>
              <span className={styles.controlLabel}>Breakdown dimension</span>
              <select
                className={styles.select}
                value={topDimension}
                onChange={(e) => setTopDimension(e.target.value as TopDimension)}
              >
                {DIMENSION_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
            <label className={styles.control}>
              <span className={styles.controlLabel}>Breakdown metric</span>
              <select
                className={styles.select}
                value={topMetric}
                onChange={(e) => setTopMetric(e.target.value as StatsMetric)}
              >
                {METRIC_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </div>

        {ranking.isPending ? (
          <LoadingState label="Loading breakdown…" />
        ) : ranking.isError ? (
          <ErrorState error={ranking.error} title="Failed to load breakdown" />
        ) : (
          <BarChart
            items={ranking.data?.items ?? []}
            dimension={topDimension}
            metric={topMetric}
          />
        )}
      </section>

      {/* ── Deep search (ops + logs) ──────────────────────────────────────── */}
      <section className={styles.panel} aria-labelledby="stats-search-title">
        <h2 id="stats-search-title" className={styles.panelTitle}>
          Search ops &amp; logs
        </h2>
        <SearchBox filters={filters} />
      </section>
    </section>
  );
}
