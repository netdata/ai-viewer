import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { applyPatch, filtersToSubscription, useFilters, type FilterPatch } from '../../state/filters';
import { useAggregate, useTop, useStats } from '../../api/stats';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState, EmptyState } from '../../components/StatusViews';
import type {
  AggregateBucket,
  AggregateGroupBy,
  AggregateSeriesPoint,
  StatsBucket,
  StatsMetric,
  StatsResponse,
  TopDimension,
  TopItem,
  TopResponse,
} from '../../api/types';
import { LineChart } from './charts/LineChart';
import { BarChart } from './charts/BarChart';
import { SearchBox } from './SearchBox';
import { formatCost, formatDuration, formatNumber } from '../../lib/format';
import {
  applyStatPatch,
  readStatControls,
  type StatControlsPatch,
  type TrendMetric,
} from './shareState';
import styles from './Stats.module.css';

// Statistics dashboard (ui-pages.md §/stats, redesigned SOW-0067). Sections
// over the global FilterBar's filters: a multi-series TREND line chart (cost /
// tokens / … / failure-rate, group-by overlay), a TOP-N horizontal bar chart,
// a multi-metric COMPARISON table (honest stable columns + drill-down), a
// FAILURE-analysis section, and a deep SEARCH box. The FilterBar drives
// `filters`; this page reads them and adds its OWN chart controls.
//
// SHAREABLE (ui-pages.md §/stats "Copy-share-link"): the chart controls live
// in the URL under own param names (shareState.ts), DISTINCT from the global
// filter keys, so the whole view (filters + chart controls) survives a reload /
// bookmark / paste. A "Copy link" button copies window.location.href.
//
// LIVE: useLiveUpdates(subscription) keeps one SSE subscription open; a
// stats_invalidated frame invalidates the ['stats'] key wholesale, so the
// aggregate + top + stats queries auto-refresh. SEARCH is deliberately NOT
// invalidated by SSE (a result list jumping on every ingest is poor UX).

/** The trend-metric options — the server enum PLUS the client-derived failure_rate. */
const TREND_METRIC_OPTIONS: ReadonlyArray<{ value: TrendMetric; label: string }> = [
  { value: 'cost', label: 'Cost' },
  { value: 'tokens_in', label: 'Tokens in' },
  { value: 'tokens_out', label: 'Tokens out' },
  { value: 'calls', label: 'Calls' },
  { value: 'failures', label: 'Failures' },
  { value: 'failure_rate', label: 'Failure rate' },
  { value: 'duration_us', label: 'Duration' },
  { value: 'sessions', label: 'Sessions' },
];

/** The top-N ranking metric options (plain server enum — no derived rate). */
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

/** The trend group-by overlay dimensions (includes total/source_format). */
const GROUP_BY_OPTIONS: ReadonlyArray<{ value: AggregateGroupBy; label: string }> = [
  { value: 'total', label: 'Total' },
  { value: 'model', label: 'Model' },
  { value: 'provider', label: 'Provider' },
  { value: 'tool', label: 'Tool' },
  { value: 'agent', label: 'Agent' },
  { value: 'cwd', label: 'Working dir' },
  { value: 'source_format', label: 'Source' },
];

/** Fixed top-N size; the server clamps to [1,200] (rest-api.md §GET /api/stats/top). */
const TOP_N = 20;

/** Breakdown dimensions for the multi-metric comparison table. */
const BREAKDOWN_DIMS: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'by_model', label: 'Model' },
  { value: 'by_source', label: 'Source' },
  { value: 'by_agent', label: 'Agent' },
  { value: 'by_tool', label: 'Tool' },
  { value: 'by_status', label: 'Status' },
  { value: 'by_error_class', label: 'Error class' },
];

/** Maximum series drawn on the trend chart; the rest roll into an "other" line. */
const SERIES_LIMIT = 8;

export function Stats() {
  const { filters } = useFilters();
  const navigate = useNavigate();

  // Chart controls live in the URL (shareState.ts), NOT page-local state, so the
  // view is shareable. The trend chart carries a metric, a group-by overlay, and
  // a bucket; the top-N chart carries separate dimension + metric.
  const [searchParams, setSearchParams] = useSearchParams();
  const { trendMetric, bucket, trendGroupBy, topDimension, topMetric } = useMemo(
    () => readStatControls(searchParams),
    [searchParams],
  );

  // breakdownDim + comparison-table sort are LOCAL (not shareable) — they are
  // view tuning, not analysis state.
  const [breakdownDim, setBreakdownDim] = useState('by_model');
  const [sortKey, setSortKey] = useState<SortKey>('cost');
  const [sortDir, setSortDir] = useState<SortDir>('desc');

  const setControl = useCallback(
    (patch: StatControlsPatch): void => {
      setSearchParams((prev) => applyStatPatch(prev, patch), { replace: true });
    },
    [setSearchParams],
  );

  // Failure-rate is a CLIENT-derived metric: the server cannot SUM a ratio, so
  // the page fetches the failures AND calls aggregates and divides per bucket.
  // For absolute metrics, baseMetric === secondaryMetric (=== trendMetric), so
  // the two useAggregate calls share an identical query key and TanStack dedupes
  // them to a single request (no over-fetch outside the rate case).
  const isRate = trendMetric === 'failure_rate';
  const baseMetric: StatsMetric = isRate ? 'failures' : trendMetric;
  const secondaryMetric: StatsMetric = isRate ? 'calls' : trendMetric;
  const aggFailures = useAggregate(filters, { bucket, groupBy: trendGroupBy, metric: baseMetric });
  const aggCalls = useAggregate(filters, { bucket, groupBy: trendGroupBy, metric: secondaryMetric });
  const ranking = useTop(filters, { dimension: topDimension, metric: topMetric, n: TOP_N });
  const stats = useStats(filters);

  useLiveUpdates(filtersToSubscription(filters));

  // Build the trend buckets: for absolute metrics, cap+roll the single
  // aggregate; for failure_rate, divide failures/calls per (bucket,key) first.
  const { trendBuckets, trendTruncated } = useMemo(() => {
    const fail = aggFailures.data?.buckets ?? [];
    if (!isRate) {
      const r = capAndRoll(fail);
      return { trendBuckets: r.buckets, trendTruncated: r.truncated };
    }
    const calls = aggCalls.data?.buckets ?? [];
    const r = capAndRollRate(fail, calls);
    return { trendBuckets: r.buckets, trendTruncated: r.truncated };
  }, [aggFailures.data, aggCalls.data, isRate]);

  const trendPending = aggFailures.isPending || (isRate && aggCalls.isPending);
  const trendError = aggFailures.isError || (isRate && aggCalls.isError);
  const trendErrorObj = aggFailures.isError ? aggFailures.error : aggCalls.error;

  // Copy-share-link: copy the current URL (filters + chart controls) and
  // announce the outcome through the polite live region below. A rejected
  // clipboard promise surfaces a failure message — never a silent failure.
  const [copyStatus, setCopyStatus] = useState<string>('');
  const handleCopyLink = useCallback(async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(window.location.href);
      setCopyStatus('Link copied');
    } catch {
      setCopyStatus('Copy failed');
    }
  }, []);

  // drillDown applies a filter patch and navigates to the sessions list, so a
  // click on a table row bridges "what's broken / expensive" to "show me those
  // sessions". Filters live in the URL, so the patched search carries over.
  const drillDown = useCallback(
    (patch: FilterPatch): void => {
      const next = applyPatch(searchParams, patch);
      const qs = next.toString();
      void navigate(qs ? `/?${qs}` : '/');
    },
    [navigate, searchParams],
  );

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
          {/* Polite live region announces the copy outcome; visually hidden. */}
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

      {/* ── Trends over time (multi-series line chart) ────────────────────── */}
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
                onChange={(e) => setControl({ trendMetric: e.target.value as TrendMetric })}
              >
                {TREND_METRIC_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
            <label className={styles.control}>
              <span className={styles.controlLabel}>Group by</span>
              <select
                className={styles.select}
                value={trendGroupBy}
                onChange={(e) => setControl({ trendGroupBy: e.target.value as AggregateGroupBy })}
              >
                {GROUP_BY_OPTIONS.map((o) => (
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
            <button
              type="button"
              className={styles.copyButton}
              onClick={() => downloadCSV('trend', trendMetric, trendBucketsToRows(trendBuckets))}
            >
              Export CSV
            </button>
          </div>
        </div>

        {trendPending ? (
          <LoadingState label="Loading trends…" />
        ) : trendError ? (
          <ErrorState error={trendErrorObj} title="Failed to load trends" />
        ) : (
          <>
            <LineChart buckets={trendBuckets} metric={trendMetric} bucket={bucket} />
            {trendTruncated > 0 && (
              <p className={styles.note}>
                Showing the top {SERIES_LIMIT} of {SERIES_LIMIT + trendTruncated} series; the rest are summed into <em>other</em>.
              </p>
            )}
          </>
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
                downloadCSV(`top-${topDimension}`, topMetric, items.map((i) => ({ key: i.key, value: i.value })));
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

      {/* ── Multi-metric comparison table (SOW-0067) ──────────────────────── */}
      {stats.data && (
        <section className={styles.panel} aria-labelledby="stats-breakdown-title">
          <div className={styles.panelHeader}>
            <h2 id="stats-breakdown-title" className={styles.panelTitle}>
              Comparison table
            </h2>
            <div className={styles.controls}>
              <span className={styles.controlLabel}>Dimension</span>
              <select
                className={styles.select}
                aria-label="Comparison dimension"
                value={breakdownDim}
                onChange={(e) => setBreakdownDim(e.target.value)}
              >
                {BREAKDOWN_DIMS.map((d) => (
                  <option key={d.value} value={d.value}>
                    {d.label}
                  </option>
                ))}
              </select>
              <button
                type="button"
                className={styles.copyButton}
                onClick={() =>
                  downloadCSV(
                    `comparison-${breakdownDim}`,
                    'metrics',
                    comparisonToRows(buildBreakdownRows(stats.data, breakdownDim)),
                  )
                }
              >
                Export CSV
              </button>
            </div>
          </div>
          <BreakdownTable
            data={stats.data}
            dimension={breakdownDim}
            sortKey={sortKey}
            sortDir={sortDir}
            onSort={(key) => {
              if (key === sortKey) {
                setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
              } else {
                setSortKey(key);
                setSortDir('desc');
              }
            }}
            onDrillDown={drillDown}
          />
        </section>
      )}

      {/* ── Failure analysis ─────────────────────────────────────────────── */}
      {stats.data && stats.data.by_error_class.length > 0 && (
        <section className={styles.panel} aria-labelledby="stats-failures-title">
          <div className={styles.panelHeader}>
            <h2 id="stats-failures-title" className={styles.panelTitle}>
              Failure analysis
            </h2>
            <button
              type="button"
              className={styles.copyButton}
              onClick={() =>
                downloadCSV(
                  'failure-analysis',
                  'metrics',
                  stats.data.by_error_class.map((row) => ({
                    key: row.error_class,
                    sessions: row.sessions,
                    ops: row.ops,
                    cost: row.cost_usd,
                  })),
                )
              }
            >
              Export CSV
            </button>
          </div>
          <div className={styles.tableWrap} role="region" aria-label="Error class breakdown">
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>Error class</th>
                  <th className={styles.numCol}>Failed sessions</th>
                  <th className={styles.numCol}>Ops</th>
                  <th className={styles.numCol}>Cost</th>
                </tr>
              </thead>
              <tbody>
                {stats.data.by_error_class.map((row) => (
                  <tr key={row.error_class}>
                    <td>{row.error_class}</td>
                    <td className={styles.numCol}>{formatNumber(row.sessions)}</td>
                    <td className={styles.numCol}>{formatNumber(row.ops)}</td>
                    <td className={styles.numCol}>{formatCost(row.cost_usd)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className={styles.failureRateBar}>
            {(() => {
              const totalFailed = stats.data.totals.failures || 1;
              return stats.data.by_error_class.slice(0, 8).map((row) => {
                const pct = (row.sessions / totalFailed) * 100;
                return (
                  <div key={row.error_class} className={styles.failureRateRow}>
                    <span className={styles.failureRateLabel}>{row.error_class}</span>
                    <div className={styles.failureRateTrack}>
                      <div
                        className={styles.failureRateFill}
                        style={{ width: `${Math.max(pct, 2)}%` }}
                      />
                    </div>
                    <span className={styles.failureRatePct}>{pct.toFixed(1)}%</span>
                  </div>
                );
              });
            })()}
          </div>
          {stats.data.by_error_class.length > 8 && (
            <p className={styles.note}>
              Showing the top 8 of {stats.data.by_error_class.length} error classes in the bar above; the table lists them all.
            </p>
          )}
        </section>
      )}

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

// ── trend bucket helpers ────────────────────────────────────────────────────

/**
 * rateBuckets divides failures by calls per (bucket, key) to produce a
 * failure-RATE time series. A rate cannot be SUM-ed by the rollup fast path
 * (ratios do not aggregate), so the page fetches failures AND calls and divides
 * client-side (SOW-0067). A key with zero calls yields rate 0 (never NaN).
 */
export function rateBuckets(failures: AggregateBucket[], calls: AggregateBucket[]): AggregateBucket[] {
  const callsByBucket = new Map<number, Map<string, number>>();
  for (const cb of calls) {
    const m = new Map<string, number>();
    for (const s of cb.series) m.set(s.key, s.value);
    callsByBucket.set(cb.bucket_ts, m);
  }
  return failures.map((fb) => {
    const callMap = callsByBucket.get(fb.bucket_ts);
    const series: AggregateSeriesPoint[] = fb.series.map((fs) => {
      const denom = callMap?.get(fs.key) ?? 0;
      return { key: fs.key, value: denom > 0 ? fs.value / denom : 0 };
    });
    return { bucket_ts: fb.bucket_ts, series };
  });
}

/**
 * capAndRollRate is the FAILURE-RATE-aware cap+roll. A rate cannot be summed
 * (summing per-bucket ratios is meaningless), so this ranks series by total CALL
 * volume (the rate's denominator — the natural prominence measure), keeps the
 * top `limit`, and computes the rolled `other` line as Σ(dropped failures) /
 * Σ(dropped calls) PER BUCKET — the only mathematically correct combined rate.
 * The kept series are divided as failures/calls. The '' key (group_by=total) is
 * never ranked/dropped. (SOW-0067 round-2 correctness fix.)
 */
export function capAndRollRate(
  failures: AggregateBucket[],
  calls: AggregateBucket[],
  limit = SERIES_LIMIT,
): { buckets: AggregateBucket[]; truncated: number } {
  const callsByKey = new Map<string, number>();
  for (const b of calls) {
    for (const s of b.series) {
      if (s.key === '') continue;
      callsByKey.set(s.key, (callsByKey.get(s.key) ?? 0) + s.value);
    }
  }
  const ranked = [...callsByKey.entries()].sort((a, b) => b[1] - a[1]);
  if (ranked.length <= limit) {
    return { buckets: rateBuckets(failures, calls), truncated: 0 };
  }
  const keep = new Set(ranked.slice(0, limit).map(([k]) => k));
  const callsByBucket = new Map<number, Map<string, number>>();
  for (const cb of calls) {
    const m = new Map<string, number>();
    for (const s of cb.series) m.set(s.key, s.value);
    callsByBucket.set(cb.bucket_ts, m);
  }
  const buckets = failures.map((fb) => {
    const callMap = callsByBucket.get(fb.bucket_ts);
    let otherFail = 0;
    let otherCalls = 0;
    const kept: AggregateSeriesPoint[] = [];
    for (const fs of fb.series) {
      const f = fs.value;
      const c = callMap?.get(fs.key) ?? 0;
      if (fs.key !== '' && !keep.has(fs.key)) {
        otherFail += f;
        otherCalls += c;
      } else {
        kept.push({ key: fs.key, value: c > 0 ? f / c : 0 });
      }
    }
    kept.push({ key: 'other', value: otherCalls > 0 ? otherFail / otherCalls : 0 });
    return { bucket_ts: fb.bucket_ts, series: kept };
  });
  return { buckets, truncated: ranked.length - limit };
}

/**
 * capAndRoll keeps the top `limit` series (by summed value across buckets) and
 * rolls the remainder into a single "other" series per bucket, so multi-series
 * trends never overflow the chart while losing no data. The '' key (group_by
 * total) is never ranked/dropped — there is only one series in that case. Used
 * for ABSOLUTE metrics; failure_rate uses capAndRollRate (rates do not sum).
 */
export function capAndRoll(
  buckets: AggregateBucket[],
  limit = SERIES_LIMIT,
): { buckets: AggregateBucket[]; truncated: number } {
  const totals = new Map<string, number>();
  for (const b of buckets) {
    for (const s of b.series) {
      if (s.key === '') continue;
      totals.set(s.key, (totals.get(s.key) ?? 0) + s.value);
    }
  }
  const ranked = [...totals.entries()].sort((a, b) => b[1] - a[1]);
  if (ranked.length <= limit) {
    return { buckets, truncated: 0 };
  }
  const keep = new Set(ranked.slice(0, limit).map(([k]) => k));
  const out = buckets.map((b) => {
    let other = 0;
    const kept: AggregateSeriesPoint[] = [];
    for (const s of b.series) {
      if (s.key !== '' && !keep.has(s.key)) {
        other += s.value;
      } else {
        kept.push(s);
      }
    }
    kept.push({ key: 'other', value: other });
    return { bucket_ts: b.bucket_ts, series: kept };
  });
  return { buckets: out, truncated: ranked.length - limit };
}

function topItemsFrom(data: TopResponse | undefined): TopItem[] {
  const items = data?.items;
  return Array.isArray(items) ? items : [];
}

// ── comparison table ────────────────────────────────────────────────────────

type SortKey = 'name' | 'count' | 'failures' | 'failureRate' | 'cost' | 'tokensIn' | 'tokensOut' | 'cacheRead' | 'cacheHit' | 'duration';
type SortDir = 'asc' | 'desc';

/** A comparison row carries only the metrics its dimension owns; absent fields
 *  are `undefined` and render as "—" so the table is honest (no silently-hidden
 *  columns, SOW-0067). Fields are typed `T | undefined` (not optional `?`) so
 *  they can be assigned `undefined` explicitly under exactOptionalPropertyTypes. */
export interface BreakdownRow {
  label: string;
  sublabel: string | undefined;
  count: number;
  countLabel: 'Calls' | 'Sessions';
  failures: number;
  failureRate: number; // %
  cost: number | undefined;
  tokensIn: number | undefined;
  tokensOut: number | undefined;
  cacheRead: number | undefined;
  cacheHit: number | undefined; // %
  duration: number | undefined; // us
  /** The filter to apply on a click drill-down. undefined = the dimension has
   *  no honest URL filter, so the row is non-interactive (e.g. by_error_class,
   *  which has no error_class filter — drilling to status=failed would be a
   *  misleading wider result). */
  drill: FilterPatch | undefined;
}

const ROW_CAP = 30;

/** cacheHitPct is cache_read / (cache_read + tokens_in) as a percent, or
 *  undefined when the inputs are absent/zero (rendered as "—"). */
function cacheHitPct(cacheRead: number, tokensIn: number): number | undefined {
  const denom = cacheRead + tokensIn;
  if (denom <= 0) return undefined;
  return (cacheRead / denom) * 100;
}

function buildBreakdownRows(data: StatsResponse, dimension: string): BreakdownRow[] {
  switch (dimension) {
    case 'by_model':
      return data.by_model
        .filter((m) => m.calls > 0)
        .map((m) => ({
          label: m.name,
          sublabel: m.provider || undefined,
          count: m.calls,
          countLabel: 'Calls' as const,
          failures: m.failures,
          failureRate: m.calls > 0 ? (m.failures / m.calls) * 100 : 0,
          cost: m.cost_usd,
          tokensIn: m.tokens_in,
          tokensOut: m.tokens_out,
          cacheRead: m.tokens_cache_read,
          cacheHit: cacheHitPct(m.tokens_cache_read, m.tokens_in),
          duration: m.duration_us,
          drill: { models: [m.name] },
        }));
    case 'by_source':
      return data.by_source.map((s) => ({
        label: s.format || s.source,
        sublabel: s.source,
        count: s.sessions,
        countLabel: 'Sessions' as const,
        failures: s.failures,
        failureRate: s.sessions > 0 ? (s.failures / s.sessions) * 100 : 0,
        cost: s.cost_usd,
        tokensIn: s.tokens_in,
        tokensOut: s.tokens_out,
        cacheRead: s.tokens_cache_read,
        cacheHit: cacheHitPct(s.tokens_cache_read, s.tokens_in),
        duration: undefined,
        drill: { sources: [s.source] },
      }));
    case 'by_agent':
      return data.by_agent
        .filter((a) => a.sessions > 0)
        .map((a) => ({
          label: a.name,
          sublabel: undefined,
          count: a.sessions,
          countLabel: 'Sessions' as const,
          failures: a.failures,
          failureRate: a.sessions > 0 ? (a.failures / a.sessions) * 100 : 0,
          cost: a.cost_usd,
          tokensIn: a.tokens_in,
          tokensOut: a.tokens_out,
          cacheRead: a.tokens_cache_read,
          cacheHit: cacheHitPct(a.tokens_cache_read, a.tokens_in),
          duration: undefined,
          drill: { agents: [a.name] },
        }));
    case 'by_tool':
      return data.by_tool
        .filter((t) => t.calls > 0)
        .map((t) => ({
          label: t.namespace ? `${t.namespace}.${t.name}` : t.name,
          sublabel: undefined,
          count: t.calls,
          countLabel: 'Calls' as const,
          failures: t.failures,
          failureRate: t.calls > 0 ? (t.failures / t.calls) * 100 : 0,
          cost: undefined,
          tokensIn: undefined,
          tokensOut: undefined,
          cacheRead: undefined,
          cacheHit: undefined,
          duration: t.total_us,
          drill: { tools: [t.name] },
        }));
    case 'by_status':
      return data.by_status.map((s) => ({
        label: s.status,
        sublabel: undefined,
        count: s.count,
        countLabel: 'Sessions' as const,
        failures: s.status === 'failed' ? s.count : 0,
        failureRate: s.count > 0 ? ((s.status === 'failed' ? s.count : 0) / s.count) * 100 : 0,
        cost: s.cost_usd,
        tokensIn: s.tokens_in,
        tokensOut: s.tokens_out,
        cacheRead: undefined,
        cacheHit: undefined,
        duration: undefined,
        drill: { status: [s.status] },
      }));
    case 'by_error_class':
      return data.by_error_class.map((e) => ({
        label: e.error_class,
        sublabel: undefined,
        count: e.sessions,
        countLabel: 'Sessions' as const,
        failures: e.sessions,
        failureRate: 100,
        cost: e.cost_usd,
        tokensIn: undefined,
        tokensOut: undefined,
        cacheRead: undefined,
        cacheHit: undefined,
        duration: undefined,
        // No error_class URL filter exists; drilling to status=failed would be a
        // wider-than-requested result, so the row is informational only.
        drill: undefined,
      }));
    default:
      return [];
  }
}

export function sortRows(rows: BreakdownRow[], key: SortKey, dir: SortDir): BreakdownRow[] {
  const mul = dir === 'asc' ? 1 : -1;
  const val = (r: BreakdownRow): number | string | null => {
    switch (key) {
      case 'name':
        return r.label.toLowerCase();
      case 'count':
        return r.count;
      case 'failures':
        return r.failures;
      case 'failureRate':
        return r.failureRate;
      case 'cost':
        return r.cost ?? null;
      case 'tokensIn':
        return r.tokensIn ?? null;
      case 'tokensOut':
        return r.tokensOut ?? null;
      case 'cacheRead':
        return r.cacheRead ?? null;
      case 'cacheHit':
        return r.cacheHit ?? null;
      case 'duration':
        return r.duration ?? null;
    }
  };
  // Nulls-last regardless of direction: an absent (N/A) value sorts to the
  // bottom on BOTH asc and desc, so it never floats misleadingly to the top.
  return [...rows].sort((a, b) => {
    const va = val(a);
    const vb = val(b);
    if (va === null && vb === null) return 0;
    if (va === null) return 1;
    if (vb === null) return -1;
    if (typeof va === 'string' || typeof vb === 'string') {
      return String(va).localeCompare(String(vb)) * mul;
    }
    return (va - vb) * mul;
  });
}

function BreakdownTable({
  data,
  dimension,
  sortKey,
  sortDir,
  onSort,
  onDrillDown,
}: {
  data: StatsResponse;
  dimension: string;
  sortKey: SortKey;
  sortDir: SortDir;
  onSort: (key: SortKey) => void;
  onDrillDown: (patch: FilterPatch) => void;
}) {
  const allRows = useMemo(() => buildBreakdownRows(data, dimension), [data, dimension]);
  const sorted = useMemo(() => sortRows(allRows, sortKey, sortDir), [allRows, sortKey, sortDir]);
  // Cap the display at ROW_CAP for readability; report an honest count of the
  // dropped rows so the truncation is disclosed (never silent).
  const rows = sorted.slice(0, ROW_CAP);
  const dropped = sorted.length > ROW_CAP ? sorted.length - ROW_CAP : 0;

  if (rows.length === 0) {
    return <EmptyState>No data for this dimension.</EmptyState>;
  }

  const countLabel = rows[0]?.countLabel ?? 'Sessions';
  const th = (key: SortKey, label: string, numeric = true): ReactNode => (
    <th
      key={key}
      className={numeric ? styles.numCol : undefined}
      aria-sort={sortKey === key ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}
    >
      <button type="button" className={styles.sortBtn} onClick={() => onSort(key)}>
        {label}
        {sortKey === key ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}
      </button>
    </th>
  );

  return (
    <div className={styles.tableWrap} role="region" aria-label="Comparison breakdown">
      <table className={styles.table}>
        <thead>
          <tr>
            {th('name', 'Name', false)}
            {th('count', countLabel)}
            {th('failures', 'Failures')}
            {th('failureRate', 'Failure %')}
            {th('cost', 'Cost')}
            {th('tokensIn', 'Tokens in')}
            {th('tokensOut', 'Tokens out')}
            {th('cacheRead', 'Cache read')}
            {th('cacheHit', 'Cache-hit %')}
            {th('duration', 'Duration')}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            // Drill-down is a real <button> on the label cell (keyboard + screen
            // reader reachable), ONLY when the row has an honest filter to apply.
            // Non-drillable rows (e.g. by_error_class) render a plain cell so the
            // affordance is never misleading.
            return (
              <tr key={row.label + (row.sublabel ?? '')} className={row.drill ? styles.drillRow : undefined}>
                <td>
                  {row.drill ? (
                    <button
                      type="button"
                      className={styles.drillLink}
                      onClick={() => onDrillDown(row.drill as FilterPatch)}
                    >
                      {row.label}
                    </button>
                  ) : (
                    <span>{row.label}</span>
                  )}
                  {row.sublabel && row.sublabel !== row.label && (
                    <span className={styles.dimSublabel}>{row.sublabel}</span>
                  )}
                </td>
                <td className={styles.numCol}>{formatNumber(row.count)}</td>
                <td className={styles.numCol}>{formatNumber(row.failures)}</td>
                <td className={styles.numCol}>
                  <span style={{ color: row.failureRate > 10 ? 'var(--status-failed)' : 'inherit' }}>
                    {row.failureRate.toFixed(1)}%
                  </span>
                </td>
                <td className={styles.numCol}>{row.cost !== undefined ? formatCost(row.cost) : '—'}</td>
                <td className={styles.numCol}>{row.tokensIn !== undefined ? formatNumber(row.tokensIn) : '—'}</td>
                <td className={styles.numCol}>{row.tokensOut !== undefined ? formatNumber(row.tokensOut) : '—'}</td>
                <td className={styles.numCol}>{row.cacheRead !== undefined ? formatNumber(row.cacheRead) : '—'}</td>
                <td className={styles.numCol}>{row.cacheHit !== undefined ? `${row.cacheHit.toFixed(1)}%` : '—'}</td>
                <td className={styles.numCol}>{row.duration !== undefined ? formatDuration(row.duration) : '—'}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {dropped > 0 && (
        <p className={styles.note}>
          Showing the top {ROW_CAP} of {ROW_CAP + dropped} rows for this dimension.
        </p>
      )}
    </div>
  );
}

// ── CSV export ──────────────────────────────────────────────────────────────

type CSVRow = Record<string, string | number>;

/** downloadCSV builds a CSV blob from rows and triggers a download. The header
 *  row is the object keys of the first row, in insertion order (standard CSV —
 *  no leading comment line, so strict parsers incl. Excel read it cleanly). */
function downloadCSV(name: string, metric: string, rows: CSVRow[]): void {
  if (rows.length === 0) return;
  const headers = Object.keys(rows[0] ?? {});
  const esc = (v: string | number): string => {
    const s = String(v);
    return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  const header = headers.join(',');
  const body = rows.map((r) => headers.map((h) => esc(r[h] ?? '')).join(',')).join('\n');
  const csv = `${header}\n${body}`;
  try {
    const blob = new Blob([csv], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${name}-${metric}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  } catch (err) {
    // Best-effort client-side export; surface the failure rather than swallow
    // it silently (AGENTS.md §6 — no silent failures).
    console.error('CSV export failed', err);
  }
}

/** trendBucketsToRows flattens the trend into one CSV row per (bucket, key). */
function trendBucketsToRows(buckets: AggregateBucket[]): CSVRow[] {
  const rows: CSVRow[] = [];
  for (const b of buckets) {
    for (const s of b.series) {
      rows.push({ bucket_ts: b.bucket_ts, key: s.key || 'total', value: s.value });
    }
  }
  return rows;
}

/** comparisonToRows flattens comparison rows for CSV (absent fields omitted). */
function comparisonToRows(rows: BreakdownRow[]): CSVRow[] {
  return rows.map((r) => {
    const out: CSVRow = { name: r.label, [r.countLabel.toLowerCase()]: r.count, failures: r.failures, failure_pct: r.failureRate.toFixed(1) };
    if (r.cost !== undefined) out.cost_usd = r.cost;
    if (r.tokensIn !== undefined) out.tokens_in = r.tokensIn;
    if (r.tokensOut !== undefined) out.tokens_out = r.tokensOut;
    if (r.cacheRead !== undefined) out.cache_read = r.cacheRead;
    if (r.cacheHit !== undefined) out.cache_hit_pct = r.cacheHit.toFixed(1);
    if (r.duration !== undefined) out.duration_us = r.duration;
    return out;
  });
}
