import { useCallback, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useAggregate, useTop, useStats } from '../../api/stats';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState } from '../../components/StatusViews';
import type {
  AggregateBucket,
  AggregateResponse,
  StatsBucket,
  StatsMetric,
  TopDimension,
  TopItem,
  TopResponse,
} from '../../api/types';
import { LineChart } from './charts/LineChart';
import { BarChart } from './charts/BarChart';
import { SearchBox } from './SearchBox';
import { formatCost, formatNumber } from '../../lib/format';
import {
  applyStatPatch,
  readStatControls,
  type StatControlsPatch,
} from './shareState';
import styles from './Stats.module.css';

// Statistics dashboard (ui-pages.md §/stats). Three sections over the global
// FilterBar's filters: per-bucket TREND line chart (cost/tokens/…), a TOP-N
// horizontal bar chart by a selectable dimension, and a deep full-text SEARCH
// box whose hits link to the matching session. The FilterBar (in Layout) drives
// `filters`; this page reads them and adds its OWN chart controls.
//
// SHAREABLE (ui-pages.md §/stats "Copy-share-link"): the 4 chart controls live
// in the URL under own param names (shareState.ts), DISTINCT from the global
// filter keys, so the whole view (filters + chart controls) survives a reload /
// bookmark / paste. A "Copy link" button copies window.location.href and
// announces the result through a polite live region. Both the controls and the
// filters write via the functional setSearchParams MERGE form, so changing a
// chart control preserves the filter params and vice-versa.
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
  { value: 'sessions', label: 'Sessions' },
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

  // Chart controls live in the URL (shareState.ts), NOT page-local state, so the
  // view is shareable. The trend chart and the top-N chart carry SEPARATE
  // metrics on purpose: a user typically compares a cost trend over time against,
  // say, a failures-by-tool ranking. Reads CLAMP unknown values to the default.
  const [searchParams, setSearchParams] = useSearchParams();
  const { trendMetric, bucket, topDimension, topMetric } = useMemo(
    () => readStatControls(searchParams),
    [searchParams],
  );

  // setControl merges one control patch into the URL (functional form), so the
  // global filter params — and any control NOT in the patch — are preserved.
  // replace:true keeps control tweaks out of the history stack (they are view
  // tuning, not navigation), matching the SessionDetail ?tab= convention.
  const setControl = useCallback(
    (patch: StatControlsPatch): void => {
      setSearchParams((prev) => applyStatPatch(prev, patch), { replace: true });
    },
    [setSearchParams],
  );

  const aggregate = useAggregate(filters, { bucket, groupBy: 'total', metric: trendMetric });
  const ranking = useTop(filters, { dimension: topDimension, metric: topMetric, n: TOP_N });
  const stats = useStats(filters);

  // One live subscription for the active filter; stats_invalidated refreshes the
  // ['stats']-keyed aggregate + top queries (search stays put — see file header).
  useLiveUpdates(filtersToSubscription(filters));

  // Copy-share-link: copy the current URL (filters + chart controls) and
  // announce the outcome through the polite live region below. A rejected
  // clipboard promise surfaces a failure message — never a silent failure
  // (AGENTS.md §6). The outcome is the announced text; a later copy replaces it.
  const [copyStatus, setCopyStatus] = useState<string>('');
  const handleCopyLink = useCallback(async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(window.location.href);
      setCopyStatus('Link copied');
    } catch {
      setCopyStatus('Copy failed');
    }
  }, []);

  return (
    <section aria-labelledby="stats-title">
      <div className={styles.header}>
        <h1 id="stats-title">Statistics</h1>
        <div className={styles.toolbar}>
          <button
            type="button"
            className={styles.copyButton}
            onClick={() => {
              void handleCopyLink();
            }}
          >
            Copy link
          </button>
          {/* Polite live region announces the copy outcome; visually hidden so
              it never shifts layout (mirrors the ThemeToggle .srOnly recipe). */}
          <span role="status" aria-live="polite" className={styles.srOnly}>
            {copyStatus}
          </span>
        </div>
      </div>

      {/* ── Summary metrics bar ─────────────────────────────────────────── */}
      {stats.data && (
        <div className={styles.summaryBar}>
          <div className={styles.summaryItem}>
            <span className={styles.summaryValue}>{formatCost(stats.data.totals.cost_usd)}</span>
            <span className={styles.summaryLabel}>Total cost</span>
          </div>
          <div className={styles.summaryItem}>
            <span className={styles.summaryValue}>{formatNumber(stats.data.totals.session_count)}</span>
            <span className={styles.summaryLabel}>Sessions</span>
          </div>
          <div className={styles.summaryItem}>
            <span className={styles.summaryValue}>{formatNumber(stats.data.totals.op_count)}</span>
            <span className={styles.summaryLabel}>Ops</span>
          </div>
          <div className={styles.summaryItem}>
            <span className={styles.summaryValue}>{formatNumber(stats.data.totals.tokens_in)}</span>
            <span className={styles.summaryLabel}>Tokens in</span>
          </div>
          <div className={styles.summaryItem}>
            <span className={styles.summaryValue}>{formatNumber(stats.data.totals.tokens_out)}</span>
            <span className={styles.summaryLabel}>Tokens out</span>
          </div>
          <div className={styles.summaryItem}>
            <span className={styles.summaryValue}>{formatNumber(stats.data.totals.failures)}</span>
            <span className={styles.summaryLabel}>Failures</span>
          </div>
        </div>
      )}

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
                onChange={(e) => setControl({ trendMetric: e.target.value as StatsMetric })}
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
                onChange={(e) => setControl({ bucket: e.target.value as StatsBucket })}
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
            buckets={aggregateBucketsFrom(aggregate.data)}
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
                onChange={(e) => setControl({ topDimension: e.target.value as TopDimension })}
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
                onChange={(e) => setControl({ topMetric: e.target.value as StatsMetric })}
              >
                {METRIC_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              className={styles.copyButton}
              onClick={() => {
                const items = topItemsFrom(ranking.data);
                if (items.length === 0) return;
                const header = `${topDimension},${topMetric}\n`;
                const rows = items.map((i) => `"${i.key.replace(/"/g, '""')}",${i.value}`).join('\n');
                const csv = header + rows;
                try {
                  const blob = new Blob([csv], { type: 'text/csv' });
                  const url = URL.createObjectURL(blob);
                  const a = document.createElement('a');
                  a.href = url;
                  a.download = `top-${topDimension}-${topMetric}.csv`;
                  a.click();
                  URL.revokeObjectURL(url);
                } catch {
                  // Best-effort; no silent failure path needed for a download
                }
              }}
            >
              Export CSV
            </button>
          </div>
        </div>

        {ranking.isPending ? (
          <LoadingState label="Loading breakdown…" />
        ) : ranking.isError ? (
          <ErrorState error={ranking.error} title="Failed to load breakdown" />
        ) : (
          <>
            <BarChart
              items={topItemsFrom(ranking.data)}
              dimension={topDimension}
              metric={topMetric}
            />
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{DIMENSION_OPTIONS.find((d) => d.value === topDimension)?.label ?? topDimension}</th>
                  <th className={styles.numCol}>{METRIC_OPTIONS.find((m) => m.value === topMetric)?.label ?? topMetric}</th>
                </tr>
              </thead>
              <tbody>
                {topItemsFrom(ranking.data).map((item) => (
                  <tr key={item.key}>
                    <td>{item.key}</td>
                    <td className={styles.numCol}>
                      {topMetric === 'cost' ? formatCost(item.value) : formatNumber(item.value)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
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

function aggregateBucketsFrom(data: AggregateResponse | undefined): AggregateBucket[] {
  const buckets = data?.buckets;
  return Array.isArray(buckets) ? buckets : [];
}

function topItemsFrom(data: TopResponse | undefined): TopItem[] {
  const items = data?.items;
  return Array.isArray(items) ? items : [];
}
