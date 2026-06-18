import { useCallback, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useAggregate, useTop, useStats } from '../../api/stats';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState, EmptyState } from '../../components/StatusViews';
import type {
  AggregateBucket,
  AggregateResponse,
  StatsBucket,
  StatsMetric,
  StatsResponse,
  StatsTotals,
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

/** Breakdown dimensions for the multi-metric comparison table. */
const BREAKDOWN_DIMS: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'by_model', label: 'Model' },
  { value: 'by_source', label: 'Source' },
  { value: 'by_agent', label: 'Agent' },
  { value: 'by_tool', label: 'Tool' },
  { value: 'by_status', label: 'Status' },
  { value: 'by_error_class', label: 'Error class' },
];

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
  const [breakdownDim, setBreakdownDim] = useState('by_model');

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

      {/* ── Multi-metric comparison table ─────────────────────────────────── */}
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
                value={breakdownDim}
                onChange={(e) => setBreakdownDim(e.target.value)}
              >
                {BREAKDOWN_DIMS.map((d) => (
                  <option key={d.value} value={d.value}>
                    {d.label}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <BreakdownTable data={stats.data} dimension={breakdownDim} totals={stats.data.totals} />
        </section>
      )}

      {/* ── Failure analysis ─────────────────────────────────────────────── */}
      {stats.data && stats.data.by_error_class.length > 0 && (
        <section className={styles.panel} aria-labelledby="stats-failures-title">
          <h2 id="stats-failures-title" className={styles.panelTitle}>
            Failure analysis
          </h2>
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

function aggregateBucketsFrom(data: AggregateResponse | undefined): AggregateBucket[] {
  const buckets = data?.buckets;
  return Array.isArray(buckets) ? buckets : [];
}

function topItemsFrom(data: TopResponse | undefined): TopItem[] {
  const items = data?.items;
  return Array.isArray(items) ? items : [];
}

interface BreakdownRow {
  label: string;
  sublabel?: string;
  sessions: number;
  calls: number;
  failures: number;
  costUsd: number;
  tokensIn: number;
  tokensOut: number;
  failureRate: number;
}

function BreakdownTable({
  data,
  dimension,
  totals,
}: {
  data: StatsResponse;
  dimension: string;
  totals: StatsTotals;
}) {
  const rows: BreakdownRow[] = useMemo(() => {
    const totalSessions = totals.session_count || 1;
    switch (dimension) {
      case 'by_model':
        return data.by_model
          .filter((m) => m.calls > 0)
          .slice(0, 30)
          .map((m) => ({
            label: m.name,
            sublabel: m.provider,
            sessions: 0,
            calls: m.calls,
            failures: m.failures,
            costUsd: m.cost_usd,
            tokensIn: m.tokens_in,
            tokensOut: m.tokens_out,
            failureRate: m.calls > 0 ? (m.failures / m.calls) * 100 : 0,
          }));
      case 'by_source':
        return data.by_source.map((s) => ({
          label: s.source.split(':')[0] ?? s.source,
          sublabel: s.source,
          sessions: s.sessions,
          calls: 0,
          failures: s.failures,
          costUsd: 0,
          tokensIn: 0,
          tokensOut: 0,
          failureRate: s.sessions > 0 ? (s.failures / s.sessions) * 100 : 0,
        }));
      case 'by_agent':
        return data.by_agent
          .filter((a) => a.sessions > 0)
          .slice(0, 30)
          .map((a) => ({
            label: a.name,
            sessions: a.sessions,
            calls: 0,
            failures: a.failures,
            costUsd: a.cost_usd,
            tokensIn: a.tokens_in,
            tokensOut: a.tokens_out,
            failureRate: a.sessions > 0 ? (a.failures / a.sessions) * 100 : 0,
          }));
      case 'by_tool':
        return data.by_tool
          .filter((t) => t.calls > 0)
          .slice(0, 30)
          .map((t) => ({
            label: t.namespace ? `${t.namespace}.${t.name}` : t.name,
            sessions: 0,
            calls: t.calls,
            failures: t.failures,
            costUsd: 0,
            tokensIn: 0,
            tokensOut: 0,
            failureRate: t.calls > 0 ? (t.failures / t.calls) * 100 : 0,
          }));
      case 'by_status':
        return data.by_status.map((s) => ({
          label: s.status,
          sessions: s.count,
          calls: 0,
          failures: s.status === 'failed' ? s.count : 0,
          costUsd: 0,
          tokensIn: 0,
          tokensOut: 0,
          failureRate: s.count > 0 ? ((s.status === 'failed' ? s.count : 0) / s.count) * 100 : 0,
        }));
      case 'by_error_class':
        return data.by_error_class.map((e) => ({
          label: e.error_class,
          sessions: e.sessions,
          calls: 0,
          failures: e.sessions,
          costUsd: e.cost_usd,
          tokensIn: 0,
          tokensOut: 0,
          failureRate: totalSessions > 0 ? (e.sessions / totalSessions) * 100 : 0,
        }));
      default:
        return [];
    }
  }, [data, dimension, totals]);

  if (rows.length === 0) {
    return <EmptyState>No data for this dimension.</EmptyState>;
  }

  return (
    <div className={styles.tableWrap} role="region" aria-label="Comparison table">
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Name</th>
            {dimension === 'by_model' || dimension === 'by_tool' ? (
              <th className={styles.numCol}>Calls</th>
            ) : (
              <th className={styles.numCol}>Sessions</th>
            )}
            <th className={styles.numCol}>Failures</th>
            <th className={styles.numCol}>Failure %</th>
            {(dimension === 'by_model' || dimension === 'by_agent') && (
              <>
                <th className={styles.numCol}>Tokens in</th>
                <th className={styles.numCol}>Tokens out</th>
                <th className={styles.numCol}>Cost</th>
              </>
            )}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.label + (row.sublabel ?? '')}>
              <td>
                {row.label}
                {row.sublabel && row.sublabel !== row.label && (
                  <span className={styles.dimSublabel}>{row.sublabel}</span>
                )}
              </td>
              <td className={styles.numCol}>
                {formatNumber(row.calls || row.sessions)}
              </td>
              <td className={styles.numCol}>{formatNumber(row.failures)}</td>
              <td className={styles.numCol}>
                <span style={{ color: row.failureRate > 10 ? 'var(--status-failed)' : 'inherit' }}>
                  {row.failureRate.toFixed(1)}%
                </span>
              </td>
              {(dimension === 'by_model' || dimension === 'by_agent') && (
                <>
                  <td className={styles.numCol}>{formatNumber(row.tokensIn)}</td>
                  <td className={styles.numCol}>{formatNumber(row.tokensOut)}</td>
                  <td className={styles.numCol}>{formatCost(row.costUsd)}</td>
                </>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
