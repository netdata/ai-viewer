import type { OpKind, SessionStatus } from '../api/types';

// Theme-aware color reader for the D3/Canvas Trace renderings
// (frontend-architecture.md §Theming; ui-pages.md §Span detail drawer:
// "Op-kind/status colors come from the theme tokens, consistent with the
// Overview status badges"). It resolves the CSS custom properties on
// <html> ONCE and caches them, re-reading on demand (refreshThemeColors)
// and on a data-theme MutationObserver (startThemeColorWatch). Keeping the
// read in viz/ honors the D3-boundary rule: renderers ask for a concrete
// color, never touch the DOM token layer themselves.

/** Status colors map to the same tokens the StatusBadge uses. */
const STATUS_TOKEN: Record<string, string> = {
  completed: '--success',
  running: '--warning',
  failed: '--error',
  interrupted: '--error',
  abandoned: '--error',
};

// Op-kind palette. Each known kind reads a base theme token so the palette
// tracks light/dark automatically; kinds without a dedicated semantic token
// borrow a related one (reasoning↔info, internal/system↔text-secondary). An
// unknown future kind falls back to the neutral token (never crashes — the
// client treats enums as open, api/types.ts).
const KIND_TOKEN: Record<string, string> = {
  llm: '--accent',
  tool: '--success',
  reasoning: '--info',
  session: '--warning',
  agent: '--warning',
  compaction: '--error',
  retry: '--warning',
  internal: '--text-secondary',
  system: '--text-secondary',
};

const NEUTRAL_TOKEN = '--text-secondary';

// Hard-coded fallbacks used only if a token resolves empty (e.g. a misnamed
// property or a non-browser context). They mirror the DARK token values in
// theme/tokens.css so a renderer always gets a usable color.
const FALLBACK: Record<string, string> = {
  '--success': '#3fb950',
  '--warning': '#d29922',
  '--error': '#f85149',
  '--accent': '#58a6ff',
  '--info': '#a5a5ff',
  '--text-secondary': '#8b949e',
};

// Resolved-token cache. Populated lazily on first read and refreshed by
// refreshThemeColors / the MutationObserver.
let cache: Map<string, string> | null = null;

function readToken(name: string): string {
  if (typeof window === 'undefined' || typeof window.getComputedStyle !== 'function') {
    return FALLBACK[name] ?? '#888888';
  }
  const value = window
    .getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim();
  return value !== '' ? value : (FALLBACK[name] ?? '#888888');
}

function ensureCache(): Map<string, string> {
  if (cache === null) {
    refreshThemeColors();
  }
  return cache as Map<string, string>;
}

/**
 * refreshThemeColors re-reads every token this module exposes from the current
 * computed style. Called on init, on a theme flip (the MutationObserver), or
 * manually by a renderer that knows the theme just changed.
 */
export function refreshThemeColors(): void {
  const next = new Map<string, string>();
  const names = new Set<string>([NEUTRAL_TOKEN, ...Object.values(STATUS_TOKEN), ...Object.values(KIND_TOKEN)]);
  for (const name of names) {
    next.set(name, readToken(name));
  }
  cache = next;
}

function tokenValue(name: string): string {
  const c = ensureCache();
  return c.get(name) ?? readToken(name);
}

/** colorForOpKind returns the palette color for an op kind (open enum). */
export function colorForOpKind(kind: OpKind): string {
  const token = KIND_TOKEN[kind] ?? NEUTRAL_TOKEN;
  return tokenValue(token);
}

/** colorForStatus returns the theme status color (completed/running/failed/…).
 *  SessionStatus is an open union (api/types.ts), so any string is accepted. */
export function colorForStatus(status: SessionStatus): string {
  const token = STATUS_TOKEN[status] ?? NEUTRAL_TOKEN;
  return tokenValue(token);
}

/**
 * startThemeColorWatch observes <html data-theme> and refreshes the cached
 * colors whenever it changes, so a renderer that re-reads on the next paint
 * picks up the new palette. Returns an idempotent disposer; a non-DOM context
 * is a no-op. The caller (a viz React component) disposes on unmount.
 */
export function startThemeColorWatch(): () => void {
  if (typeof MutationObserver === 'undefined' || typeof document === 'undefined') {
    return () => {};
  }
  const observer = new MutationObserver(() => {
    refreshThemeColors();
  });
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ['data-theme'],
  });
  let disposed = false;
  return () => {
    if (disposed) {
      return;
    }
    disposed = true;
    observer.disconnect();
  };
}
