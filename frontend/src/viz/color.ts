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
const STATUS_COMPLETED_TOKEN = '--success';
const STATUS_RUNNING_TOKEN = '--warning';
const STATUS_FAILED_TOKEN = '--error';

const STATUS_TOKEN_BY_STATUS = new Map<string, string>([
  ['completed', STATUS_COMPLETED_TOKEN],
  ['running', STATUS_RUNNING_TOKEN],
  ['failed', STATUS_FAILED_TOKEN],
  ['interrupted', STATUS_FAILED_TOKEN],
  ['abandoned', STATUS_FAILED_TOKEN],
]);

// Actor-kind tokens, named as concrete (provably non-undefined) strings so
// colorForActorKind needs no dead `?? NEUTRAL_TOKEN` fallback.
// They double as the agent/tool entries in KIND_TOKEN_BY_KIND — single source of
// truth so the topology palette and the op-kind palette never diverge.
const AGENT_TOKEN = '--warning';
const TOOL_TOKEN = '--success';

// Op-kind palette. Each known kind reads a base theme token so the palette
// tracks light/dark automatically; kinds without a dedicated semantic token
// borrow a related one (reasoning↔info, internal/system↔text-secondary). An
// unknown future kind falls back to the neutral token (never crashes — the
// client treats enums as open, api/types.ts).
const KIND_TOKEN_BY_KIND = new Map<string, string>([
  ['llm', '--accent'],
  ['tool', TOOL_TOKEN],
  ['reasoning', '--info'],
  ['session', '--warning'],
  ['agent', AGENT_TOKEN],
  ['compaction', '--error'],
  ['retry', '--warning'],
  ['internal', '--text-secondary'],
  ['system', '--text-secondary'],
]);

const NEUTRAL_TOKEN = '--text-secondary';

// Hard-coded fallbacks used only if a token resolves empty (e.g. a misnamed
// property or a non-browser context). They mirror the DARK token values in
// theme/tokens.css so a renderer always gets a usable color.
const DEFAULT_FALLBACK_COLOR = '#888888';

const FALLBACK_COLOR_BY_TOKEN = new Map<string, string>([
  ['--success', '#3fb950'],
  ['--warning', '#d29922'],
  ['--error', '#f85149'],
  ['--accent', '#58a6ff'],
  ['--info', '#a5a5ff'],
  ['--text-secondary', '#8b949e'],
]);

// Resolved-token cache. Populated lazily on first read and refreshed by
// refreshThemeColors / the MutationObserver.
let cache: Map<string, string> | null = null;

function readToken(name: string): string {
  if (typeof window === 'undefined' || typeof window.getComputedStyle !== 'function') {
    return FALLBACK_COLOR_BY_TOKEN.get(name) ?? DEFAULT_FALLBACK_COLOR;
  }
  const value = window
    .getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim();
  return value !== '' ? value : (FALLBACK_COLOR_BY_TOKEN.get(name) ?? DEFAULT_FALLBACK_COLOR);
}

function ensureCache(): Map<string, string> {
  if (cache === null) {
    refreshThemeColors();
  }
  return cache!;
}

/**
 * refreshThemeColors re-reads every token this module exposes from the current
 * computed style. Called on init, on a theme flip (the MutationObserver), or
 * manually by a renderer that knows the theme just changed.
 */
export function refreshThemeColors(): void {
  const next = new Map<string, string>();
  const names = new Set<string>([
    NEUTRAL_TOKEN,
    ...STATUS_TOKEN_BY_STATUS.values(),
    ...KIND_TOKEN_BY_KIND.values(),
    ...AGENT_PALETTE_TOKENS,
  ]);
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
  const token = KIND_TOKEN_BY_KIND.get(kind) ?? NEUTRAL_TOKEN;
  return tokenValue(token);
}

/** colorForStatus returns the theme status color (completed/running/failed/…).
 *  SessionStatus is an open union (api/types.ts), so any string is accepted. */
export function colorForStatus(status: SessionStatus): string {
  const token = STATUS_TOKEN_BY_STATUS.get(status) ?? NEUTRAL_TOKEN;
  return tokenValue(token);
}

/**
 * colorForFailureRatio maps a node's failed/total ratio (0..1, the topology
 * `failure_ratio`) to a theme status color so a topology node's fill encodes how
 * much it fails (ui-pages.md §Topology: "node color encodes failures"). Three
 * bands keep the signal categorical (not a misleading continuous gradient):
 * 0 → success (green), up to a third failing → warning (amber), more → error
 * (red). The thresholds mirror the StatusBadge families so the Topology view and
 * the Overview badges read consistently. Color is never the only signal — the
 * renderer also labels the node and shows the ratio in the drawer.
 */
export function colorForFailureRatio(ratio: number): string {
  if (!Number.isFinite(ratio) || ratio <= 0) {
    return tokenValue(STATUS_COMPLETED_TOKEN);
  }
  if (ratio < 1 / 3) {
    return tokenValue(STATUS_RUNNING_TOKEN);
  }
  return tokenValue(STATUS_FAILED_TOKEN);
}

/**
 * colorForActorKind returns the base stroke/accent color distinguishing a
 * topology agent node from a tool node (ui-pages.md §Topology: "node icon
 * distinguishes agent vs tool"). Agents reuse the session/agent kind hue, tools
 * the tool hue, so the topology palette tracks the rest of the viz. An unknown
 * future kind falls back to neutral.
 */
export function colorForActorKind(kind: string): string {
  const token = kind === 'agent' ? AGENT_TOKEN : kind === 'tool' ? TOOL_TOKEN : NEUTRAL_TOKEN;
  return tokenValue(token);
}

// ── Sub-agent palette (SOW-0070 whole-tree trace) ───────────────────────────
//
// The trace spans multiple sub-agent sessions; each op row carries a colored
// indicator keyed by its session_agent_name so the operator sees which
// sub-agent executed it. The palette is a small set of theme tokens cycled by a
// DETERMINISTIC string hash of the agent name, so (a) a given agent is the same
// color across renders/refreshes, and (b) the palette tracks light/dark via
// tokens (no hardcoded hex). Color is never the only signal — the agent name is
// also rendered + filterable.

const AGENT_PALETTE_TOKENS = [
  '--accent',
  '--success',
  '--warning',
  '--error',
  '--info',
  NEUTRAL_TOKEN,
] as const;

/** hashAgentName is a small deterministic string hash (djb2) → bucket index. */
function hashAgentName(name: string): number {
  let h = 5381;
  for (let i = 0; i < name.length; i++) {
    h = ((h << 5) + h + name.charCodeAt(i)) | 0;
  }
  return Math.abs(h);
}

/**
 * colorForAgent returns a stable theme-token color for a session agent name
 * (SOW-0070). The same agent is always the same color; the palette cycles
 * through theme tokens so it tracks light/dark. An empty/unknown name falls
 * back to the neutral token.
 */
export function colorForAgent(agentName: string): string {
  if (agentName.length === 0) {
    return tokenValue(NEUTRAL_TOKEN);
  }
  const token = AGENT_PALETTE_TOKENS[hashAgentName(agentName) % AGENT_PALETTE_TOKENS.length];
  return tokenValue(token ?? NEUTRAL_TOKEN);
}

/**
 * startThemeColorWatch observes <html data-theme> and refreshes the cached
 * colors whenever it changes, so a renderer that re-reads on the next paint
 * picks up the new palette. Returns an idempotent disposer; a non-DOM context
 * is a no-op. The caller (a viz React component) disposes on unmount.
 */
export function startThemeColorWatch(): () => void {
  if (typeof MutationObserver === 'undefined' || typeof document === 'undefined') {
    return () => { /* no-op disposer: there is no observer to disconnect outside a DOM context */ };
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
