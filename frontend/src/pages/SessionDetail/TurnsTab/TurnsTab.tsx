// TurnsTab — SOW-0088 chunk 2 interim wire-up. Renders every turn in the
// session as a vertical stack of TurnView cards. This is the "what did this
// session actually do, turn by turn?" view that the operator specifically
// called out as the biggest miss in the current Session Detail page: prior
// to TurnView, the only way to see a turn's content was to click the Span
// Detail Drawer's Preview button, one payload at a time.
//
// SCOPE: a large codex session can have 16 turns × ~170 ops × ~250
// payload_refs = thousands of fetches and DOM nodes. The page would either
// hang or 504 before the user sees anything. We paginate by turn count:
// render the most recent N turns by default and offer a "Show all" button
// to expand. This stays responsive even for the worst case (the largest
// real session in the production DB), and the operator can still validate
// the TurnView component end-to-end on a smaller session.
//
// TurnsTab exists as a standalone tab so the operator can validate the
// TurnView component end-to-end before the unified shell (SOW-0088 chunk 4)
// collapses every view into one resizable 3-zone layout with the turn view
// pinned to the right sidebar.

import { useMemo, useState } from 'react';
import type { SessionDetailResponse } from '../../../api/types';
import { TurnView } from '../../../components/TurnView';
import { EmptyState } from '../../../components/StatusViews';
import styles from './TurnsTab.module.css';

const DEFAULT_TURN_LIMIT = 5;

export function TurnsTab({ detail }: { detail: SessionDetailResponse }) {
  // Sort ascending by seq so turn #1 renders first and #N renders last. This
  // matches the natural reading order (chronological) — the operator reading
  // top-to-bottom sees the session play out.
  const turns = useMemo(
    () => [...detail.turns].sort((a, b) => a.seq - b.seq),
    [detail.turns],
  );

  const [showAll, setShowAll] = useState(false);
  const visible = showAll ? turns : turns.slice(0, DEFAULT_TURN_LIMIT);
  const hiddenCount = turns.length - visible.length;

  if (turns.length === 0) {
    return <EmptyState>This session has no recorded turns.</EmptyState>;
  }

  return (
    <div className={styles.wrap}>
      <header className={styles.header}>
        <h3 className={styles.title}>Turns</h3>
        <p className={styles.subtitle}>
          Every user prompt, reasoning step, tool call, and assistant reply in
          this session, in order. Click any step&apos;s copy button to copy that block.
          Click the header &ldquo;Copy turn&rdquo; to copy an entire turn&apos;s text.
          {hiddenCount > 0
            ? ` Showing the first ${visible.length} of ${turns.length} turns to keep the page responsive.`
            : null}
        </p>
        {hiddenCount > 0 ? (
          <button
            type="button"
            className={styles.showAllButton}
            onClick={() => {
              setShowAll(true);
            }}
          >
            Show all {turns.length} turns
          </button>
        ) : null}
      </header>
      <ol className={styles.list}>
        {visible.map((turn) => (
          <li key={turn.id} className={styles.item}>
            <TurnView turn={turn} />
          </li>
        ))}
      </ol>
    </div>
  );
}