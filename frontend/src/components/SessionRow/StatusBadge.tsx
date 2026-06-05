import type { SessionStatus } from '../../api/types';
import styles from './SessionRow.module.css';

// Status badge: color + text label (never color alone — frontend-architecture.md
// §Accessibility "Color is never the only signal"). Maps the canonical session
// status to a token class; unknown statuses use the neutral style. A *missing*
// CSS-module class (a typo/rename in the module) is surfaced in dev/test rather
// than silently rendering an unstyled badge (a plain `?? ''` would hide it).

/** StatusStyles is the subset of CSS-module keys the badge maps onto. The CSS
 *  module satisfies this; tests inject a plain object to exercise the mapper. */
interface StatusStyles {
  badge?: string;
  completed?: string;
  running?: string;
  failed?: string;
  unknown?: string;
}

// Known statuses → CSS-module key. Exhaustive over the canonical closed set
// (canonical-events.go SessionStatus); interrupted/abandoned share the failed
// style. A new canonical status added without an entry here renders neutral —
// caught by the StatusBadge test that enumerates the known set.
type StatusClassName = 'completed' | 'running' | 'failed';

const STATUS_CLASS_BY_STATUS = new Map<string, StatusClassName>([
  ['completed', 'completed'],
  ['running', 'running'],
  ['failed', 'failed'],
  ['interrupted', 'failed'],
  ['abandoned', 'failed'],
]);

function classFromStyles(key: StatusClassName, classes: StatusStyles): string | undefined {
  switch (key) {
    case 'completed':
      return classes.completed;
    case 'running':
      return classes.running;
    case 'failed':
      return classes.failed;
  }
  const exhaustive: never = key;
  return exhaustive;
}

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
  const key = STATUS_CLASS_BY_STATUS.get(status);
  if (key === undefined) {
    // Unknown/future status: neutral style, not an error.
    return classes.unknown ?? '';
  }
  const cls = classFromStyles(key, classes);
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
  const statusStyles: StatusStyles = styles;
  const cls = resolveStatusClass(status, statusStyles);
  return <span className={`${statusStyles.badge ?? ''} ${cls}`}>{status}</span>;
}
