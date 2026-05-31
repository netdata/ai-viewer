import { afterEach, describe, expect, it, vi } from 'vitest';
import { installMatchMedia } from '../test/matchMedia';
import { newlyAppeared, prefersReducedMotion, fadeClassFor } from './spanFade';

// SSE span-append fade logic (SOW-0006 AC#6; ui-pages.md §Realtime UX Rules:
// "Items entering view fade in over 200ms"). The animation itself is a CSS
// keyframe applied via a class; the DECISION of which ids get the class is pure
// and unit-tested here. The rule: an id present in the current render but absent
// from the previous render is "newly appeared" and gets the fade class — UNLESS
// the OS requests reduced motion, in which case no id animates (the fade is
// purely decorative and must respect prefers-reduced-motion).

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('newlyAppeared', () => {
  it('returns the ids present now but absent in the previous set', () => {
    const prev = new Set(['a', 'b']);
    const next = ['a', 'b', 'c', 'd'];
    expect(newlyAppeared(next, prev)).toEqual(new Set(['c', 'd']));
  });

  it('returns an empty set when nothing is new (re-render with same ids)', () => {
    const prev = new Set(['a', 'b', 'c']);
    expect(newlyAppeared(['a', 'b', 'c'], prev).size).toBe(0);
  });

  it('treats the very first render (no previous set) as nothing-new, so the whole initial trace does not fade', () => {
    // null previous = first mount; a first paint of a fully-loaded trace must NOT
    // animate every span (that is a load, not an append).
    expect(newlyAppeared(['a', 'b'], null).size).toBe(0);
  });

  it('ignores ids that disappeared (only additions matter for the append fade)', () => {
    const prev = new Set(['a', 'b', 'c']);
    expect(newlyAppeared(['a'], prev).size).toBe(0);
  });
});

describe('prefersReducedMotion', () => {
  it('is true when the OS reduced-motion media query matches', () => {
    const fn = vi.fn().mockReturnValue({ matches: true });
    vi.stubGlobal('matchMedia', fn);
    expect(prefersReducedMotion()).toBe(true);
    expect(fn).toHaveBeenCalledWith('(prefers-reduced-motion: reduce)');
  });

  it('is false when the OS does not request reduced motion', () => {
    vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false }));
    expect(prefersReducedMotion()).toBe(false);
  });

  it('is false (safe default) in a non-DOM context where matchMedia is absent', () => {
    vi.stubGlobal('matchMedia', undefined);
    expect(prefersReducedMotion()).toBe(false);
  });
});

describe('fadeClassFor', () => {
  const FADE = 'fade-enter';

  it('returns the fade class for a newly-appeared id when motion is allowed', () => {
    installMatchMedia(false); // matchMedia exists; reduced-motion NOT requested
    const isNew = new Set(['c']);
    expect(fadeClassFor('c', isNew, FADE)).toBe(FADE);
  });

  it('returns undefined for an id that is not new', () => {
    installMatchMedia(false);
    const isNew = new Set(['c']);
    expect(fadeClassFor('a', isNew, FADE)).toBeUndefined();
  });

  it('returns undefined for EVERY id when reduced motion is requested (no animation)', () => {
    const fn = vi.fn().mockReturnValue({ matches: true });
    vi.stubGlobal('matchMedia', fn);
    const isNew = new Set(['c', 'd']);
    expect(fadeClassFor('c', isNew, FADE)).toBeUndefined();
    expect(fadeClassFor('d', isNew, FADE)).toBeUndefined();
  });

  it('returns undefined when the fade class itself is undefined (CSS-module miss under noUncheckedIndexedAccess)', () => {
    installMatchMedia(false); // motion allowed; the undefined class is the reason
    const isNew = new Set(['c']);
    expect(fadeClassFor('c', isNew, undefined)).toBeUndefined();
  });
});
