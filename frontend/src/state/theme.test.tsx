import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { act, render, renderHook, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {
  readStoredPreference,
  resolveTheme,
  ThemeProvider,
  THEME_PREFERENCE_STORAGE_NAME as THEME_STORAGE_SLOT,
  useTheme,
} from './theme';
import { installMatchMedia, type MatchMediaController } from '../test/matchMedia';

// Theme tests cover both the pure resolution algorithm (every override × OS
// combination) and the provider's side effects on <html data-theme> per
// frontend-architecture.md §Theming.

describe('resolveTheme', () => {
  it.each([
    ['dark', true, 'dark'],
    ['dark', false, 'dark'],
    ['light', true, 'light'],
    ['light', false, 'light'],
    [null, true, 'dark'],
    [null, false, 'light'],
    ['auto', true, 'dark'],
    ['auto', false, 'light'],
    ['garbage', true, 'dark'],
    ['garbage', false, 'light'],
  ] as const)(
    'override=%s osDark=%s -> %s',
    (override, osDark, expected) => {
      expect(resolveTheme(override, osDark)).toBe(expected);
    },
  );
});

describe('readStoredPreference', () => {
  it('maps explicit values through and everything else to auto', () => {
    expect(readStoredPreference('dark')).toBe('dark');
    expect(readStoredPreference('light')).toBe('light');
    expect(readStoredPreference(null)).toBe('auto');
    expect(readStoredPreference('')).toBe('auto');
    expect(readStoredPreference('nonsense')).toBe('auto');
  });
});

describe('ThemeProvider', () => {
  let mm: MatchMediaController;

  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
  });

  afterEach(() => {
    mm.cleanup();
  });

  function wrapper({ children }: { children: React.ReactNode }) {
    return <ThemeProvider>{children}</ThemeProvider>;
  }

  it('defaults to OS dark when no override is stored', () => {
    mm = installMatchMedia(true);
    const { result } = renderHook(() => useTheme(), { wrapper });
    expect(result.current.preference).toBe('auto');
    expect(result.current.resolved).toBe('dark');
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
  });

  it('defaults to OS light when no override is stored', () => {
    mm = installMatchMedia(false);
    const { result } = renderHook(() => useTheme(), { wrapper });
    expect(result.current.resolved).toBe('light');
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
  });

  it('manual override to light persists and sets data-theme', () => {
    mm = installMatchMedia(true); // OS dark, override should win
    const { result } = renderHook(() => useTheme(), { wrapper });
    act(() => {
      result.current.setPreference('light');
    });
    expect(result.current.preference).toBe('light');
    expect(result.current.resolved).toBe('light');
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
    expect(window.localStorage.getItem(THEME_STORAGE_SLOT)).toBe('light');
  });

  it('reload reads the persisted override (light) ignoring OS dark', () => {
    window.localStorage.setItem(THEME_STORAGE_SLOT, 'light');
    mm = installMatchMedia(true); // OS dark
    const { result } = renderHook(() => useTheme(), { wrapper });
    expect(result.current.preference).toBe('light');
    expect(result.current.resolved).toBe('light');
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
  });

  it('auto preference clears the stored override', () => {
    window.localStorage.setItem(THEME_STORAGE_SLOT, 'dark');
    mm = installMatchMedia(false);
    const { result } = renderHook(() => useTheme(), { wrapper });
    expect(result.current.preference).toBe('dark');
    act(() => {
      result.current.setPreference('auto');
    });
    expect(result.current.preference).toBe('auto');
    expect(window.localStorage.getItem(THEME_STORAGE_SLOT)).toBeNull();
    // Falls back to OS (light).
    expect(result.current.resolved).toBe('light');
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
  });

  it('respects an OS preference change while in auto mode', () => {
    mm = installMatchMedia(false); // OS light initially
    const { result } = renderHook(() => useTheme(), { wrapper });
    expect(result.current.resolved).toBe('light');
    act(() => {
      mm.setDark(true);
      mm.fireChange();
    });
    expect(result.current.resolved).toBe('dark');
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
  });

  it('ignores OS preference change when an explicit override is set', () => {
    window.localStorage.setItem(THEME_STORAGE_SLOT, 'light');
    mm = installMatchMedia(false);
    const { result } = renderHook(() => useTheme(), { wrapper });
    expect(result.current.resolved).toBe('light');
    act(() => {
      mm.setDark(true);
      mm.fireChange();
    });
    // Explicit light stays put regardless of OS flipping to dark.
    expect(result.current.resolved).toBe('light');
    expect(document.documentElement.getAttribute('data-theme')).toBe('light');
  });

  it('cyclePreference walks auto -> dark -> light -> auto', () => {
    mm = installMatchMedia(true);
    const { result } = renderHook(() => useTheme(), { wrapper });
    expect(result.current.preference).toBe('auto');
    act(() => result.current.cyclePreference());
    expect(result.current.preference).toBe('dark');
    act(() => result.current.cyclePreference());
    expect(result.current.preference).toBe('light');
    act(() => result.current.cyclePreference());
    expect(result.current.preference).toBe('auto');
  });

  it('useTheme throws outside a provider', () => {
    mm = installMatchMedia(true);
    expect(() => renderHook(() => useTheme())).toThrow(
      /must be used within a ThemeProvider/,
    );
  });

  it('provider renders its children', () => {
    mm = installMatchMedia(true);
    render(
      <ThemeProvider>
        <span>child-content</span>
      </ThemeProvider>,
    );
    expect(screen.getByText('child-content')).toBeInTheDocument();
  });

  it('setPreference via a button updates resolved theme (integration)', async () => {
    mm = installMatchMedia(true);
    const user = userEvent.setup();
    function Probe() {
      const { resolved, setPreference } = useTheme();
      return (
        <div>
          <span data-testid="resolved">{resolved}</span>
          <button type="button" onClick={() => setPreference('light')}>
            go-light
          </button>
        </div>
      );
    }
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    expect(screen.getByTestId('resolved')).toHaveTextContent('dark');
    await user.click(screen.getByText('go-light'));
    expect(screen.getByTestId('resolved')).toHaveTextContent('light');
  });
});
