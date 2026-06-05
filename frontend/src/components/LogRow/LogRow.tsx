import type { LogItem, LogSeverity } from '../../api/types';
import { formatTimestamp } from '../../lib/format';
import styles from './LogRow.module.css';

// One log-entry row for the Logs tab (ui-pages.md §/sessions/:id Logs): ts,
// severity (color + text — never color alone, per a11y), source, op id, message.
// Presentational; the LogsTab owns fetching/pagination.

const SEVERITY_CLASSES = new Map<LogSeverity, string | undefined>([
  ['ERR', styles.sevErr],
  ['WRN', styles.sevWrn],
  ['INF', styles.sevInf],
  ['DBG', styles.sevDbg],
]);

/** severityClass maps a severity to its CSS-module class (neutral if unknown). */
function severityClass(severity: LogSeverity): string {
  return SEVERITY_CLASSES.get(severity) ?? '';
}

export function LogRow({ entry }: { entry: LogItem }) {
  return (
    <tr className={styles.row}>
      <td className={styles.ts}>{formatTimestamp(entry.ts)}</td>
      <td>
        <span className={`${styles.sev} ${severityClass(entry.severity)}`}>
          {entry.severity}
        </span>
      </td>
      <td className={styles.mono}>{entry.source || '—'}</td>
      <td className={styles.mono}>{entry.op_id ?? '—'}</td>
      <td className={styles.message}>{entry.message}</td>
    </tr>
  );
}
