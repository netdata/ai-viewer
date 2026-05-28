import { Link } from 'react-router-dom';
import type { SessionListItem } from '../../api/types';
import {
  formatCost,
  formatDuration,
  formatNumber,
  formatTimestamp,
} from '../../lib/format';
import { StatusBadge } from './StatusBadge';
import styles from './SessionRow.module.css';

// Presentational session row used by the SessionsList table (Chunk 15 wires the
// live data + expander). Columns follow ui-pages.md §"/": agent, model, start,
// duration, status, turns, ops, tokens in/out, cost, failures. Duration is
// derived from end_ts - start_ts when the session has ended.

export interface SessionRowProps {
  session: SessionListItem;
}

/** durationUs returns end-start when ended, else null (still running). */
function durationUs(s: SessionListItem): number | null {
  return s.end_ts === null ? null : s.end_ts - s.start_ts;
}

export function SessionRow({ session }: SessionRowProps) {
  return (
    <tr className={styles.row}>
      <td className={styles.agent}>
        <Link to={`/sessions/${encodeURIComponent(session.id)}`}>
          {session.agent_name || session.native_id}
        </Link>
      </td>
      <td className={styles.mono}>{session.model || '—'}</td>
      <td className={styles.mono}>{formatTimestamp(session.start_ts)}</td>
      <td>{formatDuration(durationUs(session))}</td>
      <td>
        <StatusBadge status={session.status} />
      </td>
      <td className={styles.num}>{formatNumber(session.turn_count)}</td>
      <td className={styles.num}>{formatNumber(session.op_count)}</td>
      <td className={styles.num}>{formatNumber(session.tokens_in)}</td>
      <td className={styles.num}>{formatNumber(session.tokens_out)}</td>
      <td className={styles.num}>{formatCost(session.cost_usd)}</td>
      <td className={styles.num}>{formatNumber(session.failure_count)}</td>
    </tr>
  );
}
