import { useState } from 'react';
import { newlyAppeared } from './spanFade';

// useNewlyAppeared is the render-time companion to spanFade.newlyAppeared
// (SOW-0006 AC#6). It reports which ids in the current render are new relative to
// the last DISTINCT id set — the spans a live session_changed refetch appended —
// so the renderer can fade them in.
//
// Why state, not a ref: react-hooks/refs forbids reading/writing a ref during
// render (react.dev/reference/eslint-plugin-react-hooks/lints/refs), and points
// to useState for previous-value needs. We use React's "storing information from
// previous renders" pattern (react.dev/reference/react/useState): a set-call
// during render, guarded by a key-change check, advances the baseline. Because
// the set-call re-runs the render and discards the in-progress return, the value
// the renderer ultimately reads must come FROM state — so the computed appeared
// set is stored, not returned from a local.
//
// Contract: the returned set is stable across re-renders of the SAME id list
// (it reflects the most recent append) and only changes when the id list itself
// changes. CSS `animation` plays once per element when the class is first applied
// and does not replay on a same-node re-render, so a persisted class is visually
// a one-shot fade; it falls off the element on the next id change.

const KEY_SEP = ' '; // a separator no id contains, so the join is unambiguous.

interface NewlyAppearedState {
  /** Ids of the baseline render (the previous DISTINCT id set). */
  ids: Set<string>;
  /** Stable join of `ids` — detects "changed" without per-render set compares. */
  key: string;
  /** Ids new at the transition INTO this baseline (the fade set for this id list). */
  appeared: Set<string>;
}

const EMPTY: ReadonlySet<string> = new Set<string>();

/**
 * useNewlyAppeared returns the set of ids in `currentIds` that are new relative
 * to the previous distinct id list. The first render returns an empty set (the
 * initial load is not a stream of appends). When the id list grows (a live
 * append), the added ids are returned and stay reported until the id list changes
 * again, so each appended span fades exactly once.
 */
export function useNewlyAppeared(currentIds: readonly string[]): ReadonlySet<string> {
  const key = currentIds.join(KEY_SEP);
  const [state, setState] = useState<NewlyAppearedState | null>(null);

  if (state === null) {
    // First render: nothing is new; seed the baseline with an empty appeared set.
    // The set-call re-runs this render; on the rerun state.key === key → returns
    // the stored (empty) appeared set.
    setState({ ids: new Set(currentIds), key, appeared: new Set<string>() });
    return EMPTY;
  }
  if (state.key !== key) {
    // Ids changed → diff against the prior baseline and STORE the result (the
    // local would be discarded by the re-render the set-call triggers). The rerun
    // sees state.key === key and returns the stored appeared set below.
    setState({ ids: new Set(currentIds), key, appeared: newlyAppeared(currentIds, state.ids) });
    return EMPTY;
  }
  // Stable render for the current id list → the appeared set captured for it.
  return state.appeared;
}
