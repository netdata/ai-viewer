import { Fragment, type ReactElement } from 'react';
import { Link } from 'react-router-dom';
import { StatCard } from '../../../components/StatCard';
import { StatusBadge } from '../../../components/SessionRow';
import { useSessionRelated } from '../../../api/sessions';
import { cacheHitRate, formatCost, formatDuration, formatNumber, formatPct } from '../../../lib/format';
import { cn } from '../../../lib/utils';
import type { ChildSummary, SessionDetailResponse, TurnDetail } from '../../../api/types';
import styles from './OverviewTab.module.css';

// Overview tab (ui-pages.md §/sessions/:id #1). Header (agent/model/status) plus
// per-session aggregate StatCards read from the DETAIL response's session row
// (NOT /api/stats — that is cross-session only; see src/api/stats.ts), plus a
// tools-used summary derived from the response's ops (kind === 'tool').

interface ToolUsage {
  name: string;
  calls: number;
  failures: number;
}

/** toolsUsed aggregates tool ops across all turns, by op name. */
export function toolsUsed(turns: TurnDetail[]): ToolUsage[] {
  const byName = new Map<string, ToolUsage>();
  for (const turn of turns) {
    for (const op of turn.ops) {
      if (op.kind !== 'tool') {
        continue;
      }
      const name = op.name || '(unnamed)';
      const cur = byName.get(name) ?? { name, calls: 0, failures: 0 };
      cur.calls += 1;
      if (op.error_class !== null) {
        cur.failures += 1;
      }
      byName.set(name, cur);
    }
  }
  return Array.from(byName.values()).sort((a, b) => b.calls - a.calls);
}

export function OverviewTab({ detail }: { detail: SessionDetailResponse }) {
  const s = detail.session;
  const tools = toolsUsed(detail.turns);
  // Heuristic cross-harness links (SOW-0071): sessions from a different harness
  // in the same cwd. Rendered only when matches exist. The section is a soft
  // enhancement — a query error does NOT break the Overview, but the error is
  // surfaced (AGENTS.md §6 — no silent failures).
  const related = useSessionRelated(s.id);
  if (related.isError) {
    console.error('Possibly related query failed', related.error);
  }
  // tokens_in is the FRESH/uncached input (canonical token contract); the cache
  // portions are separate. Hit-rate = cache_read / total input (em dash when
  // there is no input at all — cacheHitRate returns null, formatPct → "—").
  const hitRate = cacheHitRate(s.tokens_in, s.tokens_cache_read, s.tokens_cache_write);

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <span className={styles.agent}>{s.agent_name || s.native_id}</span>
        <span className={styles.model}>{s.model || '—'}</span>
        <StatusBadge status={s.status} />
      </header>

      <div className={styles.cards}>
        <StatCard label="Tokens in (fresh)" value={formatNumber(s.tokens_in)} hint="uncached input" />
        <StatCard label="Tokens out" value={formatNumber(s.tokens_out)} />
        <StatCard label="Cache read" value={formatNumber(s.tokens_cache_read)} />
        <StatCard label="Cache write" value={formatNumber(s.tokens_cache_write)} />
        <StatCard
          label="Cache hit rate"
          value={<span data-testid="cache-hit-rate">{formatPct(hitRate)}</span>}
          hint="cache read / total input"
        />
        <StatCard label="Cost" value={formatCost(s.cost_usd)} />
        <StatCard label="Turns" value={formatNumber(s.turn_count)} />
        <StatCard label="Ops" value={formatNumber(s.op_count)} />
        <StatCard label="Failures" value={formatNumber(s.failure_count)} />
      </div>

      {detail.child_sessions.length > 0 ? (
        <section className={styles.children} aria-labelledby="child-sessions-title">
          <h2 id="child-sessions-title" className={styles.childrenTitle}>
            Child sessions ({detail.child_sessions.length})
          </h2>
          <table className={styles.childrenTable}>
            <thead>
              <tr>
                <th>Agent</th>
                <th>Model</th>
                <th>Status</th>
                <th>Duration</th>
                <th className={styles.num}>Ops</th>
                <th className={styles.num}>Tokens in</th>
                <th className={styles.num}>Failures</th>
                <th className={styles.num}>Cost</th>
              </tr>
            </thead>
            <tbody>
              <ChildTreeRows nodes={detail.child_sessions} depth={0} />
            </tbody>
          </table>
        </section>
      ) : null}

      {/* ── Possibly related (SOW-0071 heuristic cross-harness soft links) ── */}
      {related.data && related.data.related.length > 0 ? (
        <section className={styles.related} aria-labelledby="related-title">
          <h2 id="related-title" className={styles.relatedTitle}>
            Possibly related
          </h2>
          <p className={styles.relatedHint}>
            Sessions from a different harness in the same working directory that started during
            this session. These are heuristic soft links — the harnesses do not record the
            parent-child edge.
          </p>
          <ul className={styles.relatedList}>
            {related.data.related.map((r) => (
              <li key={r.id} className={styles.relatedItem}>
                <Link to={`/sessions/${encodeURIComponent(r.id)}`} className={styles.relatedLink}>
                  {r.agent_name || r.id}
                </Link>
                <span className={styles.relatedFormat}>{r.source_format}</span>
                <StatusBadge status={r.status} />
                <span className={styles.relatedReason}>{r.reason}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <section className="mt-6" aria-labelledby="tools-used-title">
        <h2 id="tools-used-title" className="mb-3 text-base font-semibold tracking-tight">
          Tools used
        </h2>
        {tools.length === 0 ? (
          <p className="rounded-md border border-dashed border-border bg-card/50 px-4 py-3 text-sm text-muted-foreground">
            No tool calls in this session.
          </p>
        ) : (
          <div className="overflow-hidden rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-[11px] uppercase tracking-wider text-muted-foreground">
                <tr>
                  <th scope="col" className="px-4 py-2 text-left font-medium">Tool</th>
                  <th scope="col" className="px-4 py-2 text-right font-medium">Calls</th>
                  <th scope="col" className="px-4 py-2 text-right font-medium">Failures</th>
                </tr>
              </thead>
              <tbody>
                {tools.map((t, i) => (
                  <tr
                    key={t.name}
                    className={cn(
                      'border-t border-border/50 transition-colors hover:bg-muted/40',
                      i % 2 === 1 && 'bg-muted/20',
                    )}
                  >
                    <td className="px-4 py-2 font-medium text-foreground">{t.name}</td>
                    <td className="px-4 py-2 text-right font-mono tabular-nums text-foreground">
                      {formatNumber(t.calls)}
                    </td>
                    <td className={cn(
                      'px-4 py-2 text-right font-mono tabular-nums',
                      t.failures > 0 ? 'text-status-failed' : 'text-muted-foreground',
                    )}>
                      {formatNumber(t.failures)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

/**
 * ChildTreeRows renders the child-sessions tree as indented table rows
 * (SOW-0069). The nested `child_sessions` is walked depth-first; each level
 * indents the Agent cell so the full execution tree (parent → children →
 * grandchildren) reads at a glance. Direct children are depth 0 (no indent).
 */
function ChildTreeRows({ nodes, depth }: { nodes: ChildSummary[]; depth: number }): ReactElement {
  return (
    <>
      {nodes.map((c) => {
        const dur = c.end_ts !== null && c.end_ts > c.start_ts ? c.end_ts - c.start_ts : null;
        const kids = c.child_sessions ?? [];
        return (
          <Fragment key={c.id}>
            <tr className={c.status === 'failed' ? styles.childFailed : undefined}>
              <td>
                <span className={styles.childIndent} style={{ ['--depth' as string]: depth }}>
                  {depth > 0 ? <span aria-hidden="true">└ </span> : null}
                  <Link to={`/sessions/${encodeURIComponent(c.id)}`}>
                    {c.agent_name || c.native_id}
                  </Link>
                </span>
              </td>
              <td className={styles.childModel}>{c.model || '—'}</td>
              <td>
                <StatusBadge status={c.status} />
                {c.status === 'failed' && c.error_class ? (
                  <span className={styles.childError}>{c.error_class}</span>
                ) : null}
              </td>
              <td className={styles.childDur}>{dur !== null ? formatDuration(dur) : '—'}</td>
              <td className={styles.num}>{formatNumber(c.op_count)}</td>
              <td className={styles.num}>{formatNumber(c.tokens_in)}</td>
              <td className={styles.num}>{formatNumber(c.failure_count)}</td>
              <td className={styles.num}>{formatCost(c.cost_usd)}</td>
            </tr>
            {kids.length > 0 && <ChildTreeRows nodes={kids} depth={depth + 1} />}
          </Fragment>
        );
      })}
    </>
  );
}
