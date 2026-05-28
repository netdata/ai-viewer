import type { LogItem, LogSeverity } from '../../api/types';
import { formatTimestamp } from '../../lib/format';
import styles from './LogRow.module.css';

// One log-entry row for the Logs tab (ui-pages.md §/sessions/:id Logs): ts,
// severity (color + text — never color alone, per a11y), source, op id, message.
// Presentational; the LogsTab owns fetching/pagination.

const SEVERITY_TO_CLASS: Record<string, string> = {
  ERR: 'sevErr',
  WRN: 'sevWrn',
  INF: 'sevInf',
  DBG: 'sevDbg',
};

/** severityClass maps a severity to its CSS-module class (neutral if unknown). */
function severityClass(severity: LogSeverity, classes: Partial<Record<string, string>>): string {
  const key = SEVERITY_TO_CLASS[severity];
  return (key !== undefined ? classes[key] : undefined) ?? '';
}

export function LogRow({ entry }: { entry: LogItem }) {
  return (
    <tr className={styles.row}>
      <td className={styles.ts}>{formatTimestamp(entry.ts)}</td>
      <td>
        <span className={`${styles.sev} ${severityClass(entry.severity, styles)}`}>
          {entry.severity}
        </span>
      </td>
      <td className={styles.mono}>{entry.source || '—'}</td>
      <td className={styles.mono}>{entry.op_id ?? '—'}</td>
      <td className={styles.message}>{entry.message}</td>
    </tr>
  );
}
