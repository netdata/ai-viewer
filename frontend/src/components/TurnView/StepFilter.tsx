// StepFilter (SOW-0090 chunk 9): a compact pill-button filter strip that
// limits which step kinds the turn viewer renders. Operators want to focus
// on tool calls only (or reasoning only) when scanning a long turn. The
// filter state lives in the URL (`?stepKindFilter=tool`) so a focused view
// is shareable / bookmarkable.

import type { OpKind } from '../../api/types';
import styles from './TurnView.module.css';

/** FILTER_KINDS is the ordered set of filter pills. Order matters — left
 *  to right matches the natural reading order of a turn: user prompt,
 *  reasoning, assistant reply, tool call, session fork, compaction. */
export const FILTER_KINDS: readonly { value: StepKindFilter; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'user', label: 'User' },
  { value: 'reasoning', label: 'Reasoning' },
  { value: 'assistant', label: 'Assistant' },
  { value: 'tool', label: 'Tool' },
  { value: 'session', label: 'Sub-session' },
  { value: 'compaction', label: 'Compaction' },
];

export type StepKindFilter = 'all' | OpKind | 'user';

/** matchStepFilter returns true if `kind` matches the active filter. The
 *  'user' and 'assistant' filters are aliases that depend on `name` too:
 *  user → internal + name=user_input; assistant → llm + name=message. The
 *  other filters (tool / reasoning / session / compaction) match by kind
 *  alone. For the 'all' filter, everything matches. */
export function matchStepFilter(
  filter: StepKindFilter,
  kind: string,
  name: string,
): boolean {
  if (filter === 'all') return true;
  if (filter === 'user') return kind === 'internal' && name === 'user_input';
  if (filter === 'assistant') return kind === 'llm' && name === 'message';
  return kind === filter;
}

/** StepFilter renders the pill strip. The parent owns the active value
 *  and the change handler; the component is purely presentational. */
export function StepFilter({
  active,
  counts,
  onChange,
}: {
  active: StepKindFilter;
  counts: Record<StepKindFilter, number>;
  onChange: (next: StepKindFilter) => void;
}) {
  return (
    <div className={styles.stepFilter} role="tablist" aria-label="Step kind filter">
      {FILTER_KINDS.map((entry) => {
        const count = counts[entry.value];
        const isActive = active === entry.value;
        return (
          <button
            key={entry.value}
            type="button"
            role="tab"
            aria-selected={isActive ? 'true' : 'false'}
            className={styles.stepFilterPill}
            data-active={isActive ? 'true' : 'false'}
            onClick={() => {
              onChange(entry.value);
            }}
          >
            <span>{entry.label}</span>
            <span className={styles.stepFilterCount}>{count}</span>
          </button>
        );
      })}
    </div>
  );
}
