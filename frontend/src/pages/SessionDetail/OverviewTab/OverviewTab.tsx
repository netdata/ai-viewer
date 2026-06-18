import { Link } from 'react-router-dom';
import { StatCard } from '../../../components/StatCard';
import { StatusBadge } from '../../../components/SessionRow';
import { cacheHitRate, formatCost, formatDuration, formatNumber, formatPct } from '../../../lib/format';
import type { SessionDetailResponse, TurnDetail } from '../../../api/types';
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
              {detail.child_sessions.map((c) => {
                const dur = c.end_ts !== null && c.end_ts > c.start_ts ? c.end_ts - c.start_ts : null;
                return (
                  <tr key={c.id} className={c.status === 'failed' ? styles.childFailed : undefined}>
                    <td>
                      <Link to={`/sessions/${encodeURIComponent(c.id)}`}>
                        {c.agent_name || c.native_id}
                      </Link>
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
                );
              })}
            </tbody>
          </table>
        </section>
      ) : null}

      <section className={styles.tools} aria-labelledby="tools-used-title">
        <h2 id="tools-used-title" className={styles.toolsTitle}>
          Tools used
        </h2>
        {tools.length === 0 ? (
          <p className={styles.noTools}>No tool calls in this session.</p>
        ) : (
          <table className={styles.toolsTable}>
            <thead>
              <tr>
                <th>Tool</th>
                <th className={styles.num}>Calls</th>
                <th className={styles.num}>Failures</th>
              </tr>
            </thead>
            <tbody>
              {tools.map((t) => (
                <tr key={t.name}>
                  <td className={styles.toolName}>{t.name}</td>
                  <td className={styles.num}>{formatNumber(t.calls)}</td>
                  <td className={styles.num}>{formatNumber(t.failures)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
