import { Link } from 'react-router-dom';
import { StatCard } from '../../../components/StatCard';
import { StatusBadge } from '../../../components/SessionRow';
import { formatCost, formatNumber } from '../../../lib/format';
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

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <span className={styles.agent}>{s.agent_name || s.native_id}</span>
        <span className={styles.model}>{s.model || '—'}</span>
        <StatusBadge status={s.status} />
      </header>

      <div className={styles.cards}>
        <StatCard label="Tokens in" value={formatNumber(s.tokens_in)} />
        <StatCard label="Tokens out" value={formatNumber(s.tokens_out)} />
        <StatCard label="Cost" value={formatCost(s.cost_usd)} />
        <StatCard label="Turns" value={formatNumber(s.turn_count)} />
        <StatCard label="Ops" value={formatNumber(s.op_count)} />
        <StatCard label="Failures" value={formatNumber(s.failure_count)} />
      </div>

      {detail.child_sessions.length > 0 ? (
        <section className={styles.children} aria-labelledby="child-sessions-title">
          <h2 id="child-sessions-title" className={styles.childrenTitle}>
            Child sessions
          </h2>
          <table className={styles.childrenTable}>
            <thead>
              <tr>
                <th>Agent</th>
                <th>Model</th>
                <th>Status</th>
                <th className={styles.num}>Ops</th>
                <th className={styles.num}>Failures</th>
                <th className={styles.num}>Cost</th>
              </tr>
            </thead>
            <tbody>
              {detail.child_sessions.map((c) => (
                <tr key={c.id}>
                  <td>
                    <Link to={`/sessions/${encodeURIComponent(c.id)}`}>
                      {c.agent_name || c.native_id}
                    </Link>
                  </td>
                  <td className={styles.childModel}>{c.model || '—'}</td>
                  <td>
                    <StatusBadge status={c.status} />
                  </td>
                  <td className={styles.num}>{formatNumber(c.op_count)}</td>
                  <td className={styles.num}>{formatNumber(c.failure_count)}</td>
                  <td className={styles.num}>{formatCost(c.cost_usd)}</td>
                </tr>
              ))}
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
