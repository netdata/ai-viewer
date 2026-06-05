import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from 'react';

// Theme model per frontend-architecture.md §Theming.
//
// - ThemePreference is what the operator chooses: 'auto' (follow OS, no
//   localStorage entry), or an explicit 'dark' | 'light' that is persisted and
//   overrides the OS.
// - ResolvedTheme is the concrete theme applied to <html data-theme>.
//
// Resolution precedence (resolveTheme):
//   1. manual override: localStorage.aiViewerTheme === 'dark' | 'light' wins.
//   2. OS preference: matchMedia('(prefers-color-scheme: dark)').
export type ThemePreference = 'auto' | 'dark' | 'light';
export type ResolvedTheme = 'dark' | 'light';

export const THEME_PREFERENCE_STORAGE_NAME = 'aiViewerTheme';
const OS_DARK_QUERY = '(prefers-color-scheme: dark)';

/**
 * resolveTheme computes the active theme from a stored override and the OS
 * preference. Pure and side-effect free so it can be unit-tested across every
 * (override × OS) combination. An override of 'dark'/'light' wins; anything
 * else (null, 'auto', garbage) falls through to the OS preference.
 */
export function resolveTheme(
  override: string | null,
  osPrefersDark: boolean,
): ResolvedTheme {
  if (override === 'dark' || override === 'light') {
    return override;
  }
  return osPrefersDark ? 'dark' : 'light';
}

/** readStoredPreference maps the raw localStorage value to a ThemePreference. */
export function readStoredPreference(raw: string | null): ThemePreference {
  return raw === 'dark' || raw === 'light' ? raw : 'auto';
}

/** safeGetStored reads the override, tolerating a disabled/throwing storage. */
function safeGetStored(): string | null {
  try {
    return window.localStorage.getItem(THEME_PREFERENCE_STORAGE_NAME);
  } catch {
    return null;
  }
}

/** osPrefersDark reads the OS dark-mode preference, defaulting to dark. */
function osPrefersDark(): boolean {
  try {
    return window.matchMedia(OS_DARK_QUERY).matches;
  } catch {
    return true;
  }
}

/** applyTheme writes the resolved theme onto the <html> element. */
function applyTheme(theme: ResolvedTheme): void {
  document.documentElement.setAttribute('data-theme', theme);
}

interface ThemeContextValue {
  /** The operator's choice: auto (follow OS) or an explicit lock. */
  preference: ThemePreference;
  /** The concrete theme currently applied. */
  resolved: ResolvedTheme;
  /** Set an explicit preference; 'auto' clears the persisted override. */
  setPreference: (next: ThemePreference) => void;
  /** Cycle Auto -> Dark -> Light -> Auto (keyboard shortcut `t`). */
  cyclePreference: () => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

const CYCLE: readonly ThemePreference[] = ['auto', 'dark', 'light'];

/** persistPreference writes (or clears) the override in localStorage. */
function persistPreference(next: ThemePreference): void {
  try {
    if (next === 'auto') {
      window.localStorage.removeItem(THEME_PREFERENCE_STORAGE_NAME);
    } else {
      window.localStorage.setItem(THEME_PREFERENCE_STORAGE_NAME, next);
    }
  } catch {
    /* storage disabled — the in-memory preference still drives the UI */
  }
}

/**
 * ThemeProvider owns theme resolution and keeps <html data-theme> in sync.
 *
 * `resolved` is DERIVED during render (resolveTheme of the preference + the
 * current OS preference) — it is not stored in state, so there is no
 * setState-in-effect cascade. The only React state is the operator's
 * `preference` and the live `osDark` reading; the OS media-query listener
 * updates `osDark`, and because `resolved` is derived, an OS flip re-resolves
 * automatically (and is ignored when an explicit lock is set, since resolveTheme
 * gives the lock precedence). The single effect performs the genuine external
 * sync: writing the resolved theme onto the document element.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPreferenceState] = useState<ThemePreference>(() =>
    readStoredPreference(safeGetStored()),
  );
  const [osDark, setOsDark] = useState<boolean>(() => osPrefersDark());

  const resolved = resolveTheme(
    preference === 'auto' ? null : preference,
    osDark,
  );

  // Keep osDark in sync with the OS preference. resolved re-derives from it, so
  // an OS change auto-applies in auto mode and is overridden by an explicit
  // lock (resolveTheme precedence) — no branching needed here.
  useEffect(() => {
    let mql: MediaQueryList;
    try {
      mql = window.matchMedia(OS_DARK_QUERY);
    } catch {
      return;
    }
    const onChange = (e: MediaQueryListEvent): void => {
      setOsDark(e.matches);
    };
    mql.addEventListener('change', onChange);
    return () => {
      mql.removeEventListener('change', onChange);
    };
  }, []);

  // External sync: reflect the resolved theme onto <html data-theme>.
  useEffect(() => {
    applyTheme(resolved);
  }, [resolved]);

  const setPreference = useCallback((next: ThemePreference): void => {
    setPreferenceState(next);
    persistPreference(next);
  }, []);

  const cyclePreference = useCallback((): void => {
    setPreferenceState((cur) => {
      const idx = CYCLE.indexOf(cur);
      const next = CYCLE[(idx + 1) % CYCLE.length] ?? 'auto';
      persistPreference(next);
      return next;
    });
  }, []);

  const value: ThemeContextValue = {
    preference,
    resolved,
    setPreference,
    cyclePreference,
  };
  return createElement(ThemeContext.Provider, { value }, children);
}

/** useTheme returns the theme context; throws if used outside the provider. */
export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (ctx === null) {
    throw new Error('useTheme must be used within a ThemeProvider');
  }
  return ctx;
}
