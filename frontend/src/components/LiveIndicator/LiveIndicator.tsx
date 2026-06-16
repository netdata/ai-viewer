import type { ConnectionStatus } from '../../api/sse';
import styles from './LiveIndicator.module.css';

// Live indicator: a small pulsing dot in the header reflecting the SSE
// connection state (SOW-0018). Connected = steady green pulse; connecting/
// reconnecting = amber; closed = red. The dot is never the only signal — an
// accessible text label reads the state (ui-pages.md §Accessibility: "color
// is never the only signal").

const STATUS_LABEL: Record<ConnectionStatus, string> = {
  open: 'Live',
  connecting: 'Connecting…',
  reconnecting: 'Reconnecting…',
  closed: 'Disconnected',
};

export function LiveIndicator({ status }: { status: ConnectionStatus }) {
  return (
    <div className={styles.indicator} role="status" aria-label={`SSE status: ${STATUS_LABEL[status]}`}>
      <span className={`${styles.dot} ${styles[status]}`} aria-hidden="true" />
      <span className={styles.label}>{STATUS_LABEL[status]}</span>
    </div>
  );
}
