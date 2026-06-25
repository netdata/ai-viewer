// /compare — multi-session diff comparison (SOW-0095).
//
// Compares 2-4 sessions side-by-side. Top: summary cards (one per
// session, in request order). Below: 4 tabs (Overview / Tools /
// Errors / Kinds) rendering the structured diff. The id set lives
// in ?ids=<csv>; the URL is the source of truth and the view is
// shareable.
//
// The diff payload is single-shot: one /api/sessions/compare
// request. The page does not re-fetch on tab changes.

import { useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useCompareSessions } from '../../api/sessions';
import { EmptyState, ErrorState, LoadingState } from '../../components/StatusViews';
import { Skeleton } from '../../components/ui/skeleton';
import { formatDuration, formatNumber, formatCost, formatTimestamp } from '../../lib/format';
import type { CompareErrorRef, CompareResponse, SessionListItem } from '../../api/types';
import styles from './Compare.module.css';

type Tab = 'overview' | 'tools' | 'errors' | 'kinds';

const TABS: { id: Tab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'tools', label: 'Tools' },
  { id: 'errors', label: 'Errors' },
  { id: 'kinds', label: 'Kinds' },
];

/** parseIdsFromURL extracts the `?ids=...` query parameter and splits
 *  on commas. Empty / missing / 1-id / 5+-id shapes are flagged so
 *  the page can render the empty state instead of firing a request. */
function parseIdsFromURL(raw: string | null): { ok: true; ids: string[] } | { ok: false; reason: string } {
  if (raw === null || raw.trim() === '') {
    return { ok: false, reason: 'Pick 2-4 sessions to compare' };
  }
  const ids = raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
  if (ids.length < 2) {
    return { ok: false, reason: 'Pick 2-4 sessions to compare' };
  }
  if (ids.length > 4) {
    return { ok: false, reason: 'Compare supports 2-4 sessions; pick fewer' };
  }
  return { ok: true, ids };
}

export function Compare() {
  const [searchParams] = useSearchParams();
  const parsed = parseIdsFromURL(searchParams.get('ids'));
  const ids = parsed.ok ? parsed.ids : [];
  const query = useCompareSessions(ids);
  const [tab, setTab] = useState<Tab>('overview');

  if (!parsed.ok) {
    return (
      <div className={styles.page}>
        <EmptyState>
          <div className={styles.emptyContent}>
            <p>{parsed.reason}</p>
            <Link to="/sessions" className={styles.cta}>
              Browse sessions →
            </Link>
          </div>
        </EmptyState>
      </div>
    );
  }

  if (query.isLoading) {
    return (
      <div className={styles.page}>
        <h1 className={styles.title}>Comparing {ids.length} sessions</h1>
        <div className={styles.cardsRow}>
          {ids.map((id) => (
            <div key={id} className={styles.card}>
              <Skeleton className={styles.skelLine} />
              <Skeleton className={styles.skelLine} />
              <Skeleton className={styles.skelLine} />
            </div>
          ))}
        </div>
        <LoadingState />
      </div>
    );
  }

  if (query.isError) {
    return (
      <div className={styles.page}>
        <ErrorState
          error={query.error}
          title="Could not load comparison"
        />
      </div>
    );
  }

  const data = query.data;
  if (!data) {
    return <div className={styles.page}><LoadingState /></div>;
  }

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>
        Comparing {data.sessions.length} sessions
        <span className={styles.subtitle}>
          <Link to="/sessions" className={styles.cta}>← Sessions</Link>
        </span>
      </h1>
      <CardsRow data={data} />
      <nav className={styles.tabs}>
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            className={tab === t.id ? styles.tabActive : styles.tab}
            onClick={() => {
              setTab(t.id);
            }}
          >
            {t.label}
          </button>
        ))}
      </nav>
      <div className={styles.tabBody}>
        {tab === 'overview' && <OverviewTab data={data} />}
        {tab === 'tools' && <ToolsTab data={data} />}
        {tab === 'errors' && <ErrorsTab data={data} />}
        {tab === 'kinds' && <KindsTab data={data} />}
      </div>
    </div>
  );
}

/** CardsRow renders one summary card per session, in the request order.
 *  Each metric gets a small ✓/✗ indicator for the directional metrics
 *  (lower is better). The neutral metrics (op_count, tokens) are
 *  unmarked. */
function CardsRow({ data }: { data: CompareResponse }) {
  const durBest = data.summary.duration_us.best;
  const costBest = data.summary.cost_usd.best;
  return (
    <div className={styles.cardsRow}>
      {data.sessions.map((s) => (
        <article key={s.id} className={styles.card}>
          <header className={styles.cardHeader}>
            <Link to={`/sessions/${encodeURIComponent(s.id)}`} className={styles.cardLink}>
              {s.agent_name || s.kind}
            </Link>
            <span className={`${styles.statusBadge} ${badgeClassFor(s.effective_status)}`}>
              {s.effective_status}
            </span>
          </header>
          <dl className={styles.cardMeta}>
            <dt>Model</dt>
            <dd>{s.model || '—'}</dd>
            <dt>Ops</dt>
            <dd>{formatNumber(s.op_count)}</dd>
            <dt>Duration</dt>
            <dd className={directionalClass(s.id, durBest, data.summary.duration_us.worst)}>
              {formatDuration(durationMicros(s))}
            </dd>
            <dt>Cost</dt>
            <dd className={directionalClass(s.id, costBest, data.summary.cost_usd.worst)}>
              {formatCost(s.cost_usd)}
            </dd>
            <dt>Tokens</dt>
            <dd>{formatNumber(s.tokens_in + s.tokens_out)}</dd>
            <dt>Started</dt>
            <dd>{formatTimestamp(s.start_ts)}</dd>
          </dl>
        </article>
      ))}
    </div>
  );
}

function durationMicros(s: SessionListItem): number {
  if (s.end_ts == null) return 0;
  const d = s.end_ts - s.start_ts;
  return d > 0 ? d : 0;
}

function directionalClass(id: string, best: string | undefined, worst: string | undefined): string {
  if (best === undefined || worst === undefined) return '';
  if (best === worst) return ''; // only one session; nothing to compare
  if (id === best) return styles.metricBest ?? '';
  if (id === worst) return styles.metricWorst ?? '';
  return '';
}

function badgeClassFor(status: string): string {
  switch (status) {
    case 'completed':
      return styles.statusCompleted ?? '';
    case 'failed':
      return styles.statusFailed ?? '';
    case 'running':
      return styles.statusRunning ?? '';
    default:
      return styles.statusOther ?? '';
  }
}

/** OverviewTab renders a side-by-side metric table: rows = metrics,
 *  columns = sessions. The best cell per row gets a green tint; the
 *  worst gets a red tint (for the directional metrics). */
function OverviewTab({ data }: { data: CompareResponse }) {
  const rows: {
    label: string;
    values: (s: SessionListItem) => string;
    best: string | undefined;
    worst: string | undefined;
    pickId: (s: SessionListItem) => string;
  }[] = [
    { label: 'Agent', values: (s) => s.agent_name || '—', best: undefined, worst: undefined, pickId: (s) => s.id },
    { label: 'Model', values: (s) => s.model || '—', best: undefined, worst: undefined, pickId: (s) => s.id },
    { label: 'Status', values: (s) => s.effective_status, best: undefined, worst: undefined, pickId: (s) => s.id },
    { label: 'Ops', values: (s) => formatNumber(s.op_count), best: undefined, worst: undefined, pickId: (s) => s.id },
    {
      label: 'Duration',
      values: (s) => formatDuration(durationMicros(s)),
      best: data.summary.duration_us.best,
      worst: data.summary.duration_us.worst,
      pickId: (s) => s.id,
    },
    {
      label: 'Cost',
      values: (s) => formatCost(s.cost_usd),
      best: data.summary.cost_usd.best,
      worst: data.summary.cost_usd.worst,
      pickId: (s) => s.id,
    },
    {
      label: 'Tokens (in + out)',
      values: (s) => formatNumber(s.tokens_in + s.tokens_out),
      best: undefined,
      worst: undefined,
      pickId: (s) => s.id,
    },
    { label: 'Started', values: (s) => formatTimestamp(s.start_ts), best: undefined, worst: undefined, pickId: (s) => s.id },
    { label: 'Children', values: (s) => formatNumber(s.child_session_count), best: undefined, worst: undefined, pickId: (s) => s.id },
  ];
  return (
    <div className={styles.overviewWrap}>
      <table className={styles.overview}>
        <thead>
          <tr>
            <th />
            {data.sessions.map((s) => (
              <th key={s.id}>{shortId(s.id)}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.label}>
              <th scope="row">{row.label}</th>
              {data.sessions.map((s) => {
                const cls = directionalClass(row.pickId(s), row.best, row.worst);
                return (
                  <td key={s.id} className={cls}>
                    {row.values(s)}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** ToolsTab renders the per-tool histogram diff. Layout: one column
 *  for "Common" (tools used by every session), plus one column per
 *  session for "Only in X". */
function ToolsTab({ data }: { data: CompareResponse }) {
  return (
    <div className={styles.diffColumns}>
      <div className={styles.diffCol}>
        <h3 className={styles.diffColTitle}>Common</h3>
        {data.tool_usage.common.length === 0 ? (
          <p className={styles.empty}>No tools used by every session.</p>
        ) : (
          <ul className={styles.list}>
            {data.tool_usage.common.map((name) => (
              <li key={name} className={styles.listItem}>
                <span className={styles.toolName}>{name}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
      {data.sessions.map((s) => {
        const added = data.tool_usage.added[s.id] ?? [];
        const perSession = data.tool_usage.per_session[s.id] ?? {};
        return (
          <div key={s.id} className={styles.diffCol}>
            <h3 className={styles.diffColTitle}>Only in {shortId(s.id)}</h3>
            {added.length === 0 ? (
              <p className={styles.empty}>No tools unique to this session.</p>
            ) : (
              <ul className={styles.list}>
                {added.map((name) => (
                  <li key={name} className={styles.listItem}>
                    <span className={styles.toolName}>{name}</span>
                    <span className={styles.count}>{formatNumber(perSession[name] ?? 0)} calls</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** ErrorsTab renders the per-session error diff. The same column
 *  layout as ToolsTab: "Common" + "Only in X". */
function ErrorsTab({ data }: { data: CompareResponse }) {
  return (
    <div className={styles.diffColumns}>
      <div className={styles.diffCol}>
        <h3 className={styles.diffColTitle}>Common</h3>
        {data.errors.common.length === 0 ? (
          <p className={styles.empty}>No errors shared by every session.</p>
        ) : (
          <ul className={styles.list}>
            {data.errors.common.map((e) => (
              <ErrorRow key={e.op_id} err={e} />
            ))}
          </ul>
        )}
      </div>
      {data.sessions.map((s) => {
        const only = data.errors.only_in[s.id] ?? [];
        return (
          <div key={s.id} className={styles.diffCol}>
            <h3 className={styles.diffColTitle}>Only in {shortId(s.id)}</h3>
            {only.length === 0 ? (
              <p className={styles.empty}>No errors unique to this session.</p>
            ) : (
              <ul className={styles.list}>
                {only.slice(0, 50).map((e) => (
                  <ErrorRow key={e.op_id} err={e} />
                ))}
                {only.length > 50 && (
                  <li className={styles.moreNote}>+{formatNumber(only.length - 50)} more</li>
                )}
              </ul>
            )}
          </div>
        );
      })}
    </div>
  );
}

function ErrorRow({ err }: { err: CompareErrorRef }) {
  return (
    <li className={styles.listItem}>
      <span className={styles.errorClass}>{err.error_class}</span>
      <span className={styles.errorKind}>
        {err.kind} · {err.name}
      </span>
      <span className={styles.errorTs}>+{formatDuration(err.started_at_us)}</span>
    </li>
  );
}

/** KindsTab renders the per-session op-kind histogram as a simple
 *  bar chart. Each row is a kind, each column is a session, the bar
 *  width is the relative count. */
function KindsTab({ data }: { data: CompareResponse }) {
  // Union of all kinds across all sessions, sorted alphabetically.
  const kinds = new Set<string>();
  for (const s of data.sessions) {
    for (const k of Object.keys(data.kind_distribution.per_session[s.id] ?? {})) {
      kinds.add(k);
    }
  }
  const kindList = Array.from(kinds).sort();
  if (kindList.length === 0) {
    return <p className={styles.empty}>No ops recorded for these sessions.</p>;
  }
  // Max count per row (for relative bar widths).
  return (
    <div className={styles.kinds}>
      {kindList.map((k) => {
        const counts = data.sessions.map((s) => data.kind_distribution.per_session[s.id]?.[k] ?? 0);
        const max = Math.max(1, ...counts);
        return (
          <div key={k} className={styles.kindRow}>
            <span className={styles.kindLabel}>{k}</span>
            {data.sessions.map((s, i) => {
              const c = counts[i] ?? 0;
              const pct = (c / max) * 100;
              return (
                <div key={s.id} className={styles.kindCell}>
                  <div
                    className={styles.kindBar}
                    style={{ width: `${pct}%` }}
                    aria-label={`${c} ${k} ops`}
                  />
                  <span className={styles.kindCount}>{formatNumber(c)}</span>
                </div>
              );
            })}
          </div>
        );
      })}
    </div>
  );
}

function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}
