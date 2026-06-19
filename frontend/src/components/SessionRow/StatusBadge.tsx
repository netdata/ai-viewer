import styles from './SessionRow.module.css';

// Status badge: color + text label (never color alone — frontend-architecture.md
// §Accessibility "Color is never the only signal"). Maps the canonical session
// status to a token class; unknown statuses use the neutral style. A *missing*
// CSS-module class (a typo/rename in the module) is surfaced in dev/test rather
// than silently rendering an unstyled badge (a plain `?? ''` would hide it).
//
// SOW-0073: the SessionRow's text-only badge is kept for backward compatibility
// with this folder's tests + visual contract; the design-system StatusBadge
// (with icon + pulse) lives at components/StatusBadge.tsx and is used in new
// code (toolbars, overview tiles, etc.). The two render in the same semantic
// colors so the UI stays visually consistent.

/** StatusStyles is the subset of CSS-module keys the badge maps onto. The CSS
 *  module satisfies this; tests inject a plain object to exercise the mapper. */
export interface StatusStyles {
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

/** knownStatuses is the closed canonical set; the mapper tests pin it. */
export const knownStatuses: readonly string[] = Array.from(STATUS_CLASS_BY_STATUS.keys());

/** resolveStatusClass returns the CSS-module key for a session status.
 *
 *  Behavior matrix:
 *  - known status + styles has the mapped class  → that class
 *  - known status + styles missing the mapped class → dev error, return ''
 *    (no styling at all so the gap is visible)
 *  - unknown status + styles.unknown defined  → styles.unknown (neutral)
 *  - unknown status + styles.unknown undefined → dev error, return ''
 *  The badge base class is added by the caller (StatusBadge below), not here,
 *  so the returned token is only the status-specific piece.
 */
export function resolveStatusClass(
  status: string,
  styles: StatusStyles,
): string {
  const mapped = STATUS_CLASS_BY_STATUS.get(status);
  if (mapped !== undefined) {
    const cls = styles[mapped];
    if (cls !== undefined) return cls;
    if (import.meta.env.DEV === true) {
      console.error(`StatusBadge: missing CSS-module class for status=${status} key=${mapped}`);
    }
    return '';
  }
  if (styles.unknown !== undefined) return styles.unknown;
  if (import.meta.env.DEV === true) {
    console.error(`StatusBadge: missing CSS-module class for unknown status=${status}`);
  }
  return '';
}

export function StatusBadge({
  status,
  styles: styleOverride,
}: {
  status: string;
  styles?: StatusStyles;
}) {
  const resolvedStyles = styleOverride ?? styles;
  return (
    <span
      className={`${resolvedStyles.badge ?? ''} ${resolveStatusClass(status, resolvedStyles)}`}
      data-status={status}
    >
      {status}
    </span>
  );
}