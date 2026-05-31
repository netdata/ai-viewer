// SSE span-append fade logic (SOW-0006 AC#6; ui-pages.md §Realtime UX Rules:
// "Items entering view fade in over 200ms"). When a live `session_changed` frame
// refetches the open session, the Trace and Timeline views re-render with one (or
// a few) extra spans. Those new spans fade in over ~200ms instead of popping in.
//
// This module is the PURE decision layer: given the current render's ids and the
// previous render's ids, which ids are "newly appeared" and should carry the fade
// class. The actual animation is a CSS keyframe in the tab's stylesheet; the
// renderer asks fadeClassFor(id, …) for the class to attach. Keeping the decision
// here (not in a renderer) makes it unit-testable and keeps the rule in one place.
//
// prefers-reduced-motion is honored: the fade is purely decorative, so when the
// OS requests reduced motion NO id animates (WCAG 2.3.3 / the project a11y rule
// "respect prefers-reduced-motion"). The CSS keyframe is ALSO disabled under the
// same media query (belt-and-suspenders): even if a stale class lingers, the
// reduced-motion user sees no movement.

/**
 * newlyAppeared returns the set of ids present in `current` but absent from
 * `previous`. `previous === null` means "first render" and yields an empty set —
 * a fully-loaded trace's first paint is a load, not a stream of appends, so it
 * must not animate every span. Disappearances are irrelevant to an append fade.
 */
export function newlyAppeared(current: readonly string[], previous: ReadonlySet<string> | null): Set<string> {
  const out = new Set<string>();
  if (previous === null) {
    return out;
  }
  for (const id of current) {
    if (!previous.has(id)) {
      out.add(id);
    }
  }
  return out;
}

/**
 * prefersReducedMotion reports whether the OS requests reduced motion. Safe in a
 * non-DOM/headless context (returns false when matchMedia is unavailable), so a
 * renderer can call it unconditionally.
 */
export function prefersReducedMotion(): boolean {
  if (typeof matchMedia !== 'function') {
    return false;
  }
  return matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/**
 * fadeClassFor returns `fadeClass` when `id` is in the `newIds` set AND the OS
 * does not request reduced motion; otherwise undefined (so the caller can spread
 * `className={fadeClassFor(...)}` and get no class). Centralizes the
 * "new + motion-allowed" gate so both renderers stay identical.
 *
 * `fadeClass` is `string | undefined` because callers pass a CSS-module member
 * (`styles.fadeIn`), which is `string | undefined` under noUncheckedIndexedAccess;
 * an absent class simply yields undefined (no fade), never a crash.
 */
export function fadeClassFor(
  id: string,
  newIds: ReadonlySet<string>,
  fadeClass: string | undefined,
): string | undefined {
  if (fadeClass === undefined || !newIds.has(id) || prefersReducedMotion()) {
    return undefined;
  }
  return fadeClass;
}
