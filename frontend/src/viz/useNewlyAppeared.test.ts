import { describe, expect, it } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useNewlyAppeared } from './useNewlyAppeared';

// useNewlyAppeared tracks the previous render's id set in a ref and returns the
// ids that appeared since the last render (SOW-0006 AC#6). First render returns
// an empty set (a load, not an append); a re-render with extra ids returns just
// the new ones; the ref then advances so the NEXT render no longer flags them.

describe('useNewlyAppeared', () => {
  it('returns empty on the first render (initial load does not animate)', () => {
    const { result } = renderHook(({ ids }) => useNewlyAppeared(ids), {
      initialProps: { ids: ['a', 'b'] },
    });
    expect(result.current.size).toBe(0);
  });

  it('flags the ids added when the list grows, stays stable across same-list re-renders, and clears on the next change', () => {
    const { result, rerender } = renderHook(({ ids }) => useNewlyAppeared(ids), {
      initialProps: { ids: ['a', 'b'] as string[] },
    });
    // First render: nothing new.
    expect(result.current.size).toBe(0);

    // Append 'c' and 'd' (a live session_changed refetch added two spans).
    rerender({ ids: ['a', 'b', 'c', 'd'] });
    expect(result.current).toEqual(new Set(['c', 'd']));

    // A re-render with the SAME ids keeps the appeared set (the class persists on
    // the same DOM nodes; CSS animation does not replay, so this is still a
    // one-shot fade — no re-animation of c/d).
    rerender({ ids: ['a', 'b', 'c', 'd'] });
    expect(result.current).toEqual(new Set(['c', 'd']));

    // When the id list changes again (here: nothing new — a span was removed), the
    // appeared set clears; the previously-faded ids no longer carry the class.
    rerender({ ids: ['a', 'b', 'c'] });
    expect(result.current.size).toBe(0);

    // Another append reports just the newest id.
    rerender({ ids: ['a', 'b', 'c', 'e'] });
    expect(result.current).toEqual(new Set(['e']));
  });
});
