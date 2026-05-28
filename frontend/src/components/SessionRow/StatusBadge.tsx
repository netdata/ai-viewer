import type { SessionStatus } from '../../api/types';
import styles from './SessionRow.module.css';

// Status badge: color + text label (never color alone — frontend-architecture.md
// §Accessibility "Color is never the only signal"). Maps the canonical session
// status to a token class; unknown statuses use the neutral style. A *missing*
// CSS-module class (a typo/rename in the module) is surfaced in dev/test rather
// than silently rendering an unstyled badge (a plain `?? ''` would hide it).

/** StatusStyles is the subset of CSS-module keys the badge maps onto. The CSS
 *  module satisfies this; tests inject a plain object to exercise the mapper. */
type StatusStyles = Partial<Record<string, string>>;

// Known statuses → CSS-module key. Exhaustive over the canonical closed set
// (canonical-events.go SessionStatus); interrupted/abandoned share the failed
// style. A new canonical status added without an entry here renders neutral —
// caught by the StatusBadge test that enumerates the known set.
const STATUS_TO_KEY: Record<string, string> = {
  completed: 'completed',
  running: 'running',
  failed: 'failed',
  interrupted: 'failed',
  abandoned: 'failed',
};

/**
 * resolveStatusClass maps a status to its CSS-module class. Known statuses use
 * their mapped key; any other value uses the neutral `unknown` style. If a
 * *known* status maps to a key that is absent from the module (rename/typo),
 * dev/test builds console.error so the gap is visible; production stays
 * resilient — the badge still renders (just unstyled) and never throws.
 */
export function resolveStatusClass(
  status: SessionStatus,
  classes: StatusStyles,
): string {
  const key = STATUS_TO_KEY[status];
  if (key === undefined) {
    // Unknown/future status: neutral style, not an error.
    return classes['unknown'] ?? '';
  }
  const cls = classes[key];
  if (cls === undefined) {
    if (import.meta.env.DEV) {
      console.error(
        `StatusBadge: CSS-module class "${key}" is missing for status "${status}" — badge will render unstyled`,
      );
    }
    return '';
  }
  return cls;
}

export function StatusBadge({ status }: { status: SessionStatus }) {
  const cls = resolveStatusClass(status, styles);
  return <span className={`${styles.badge ?? ''} ${cls}`}>{status}</span>;
}
