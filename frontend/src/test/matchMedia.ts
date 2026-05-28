import { vi } from 'vitest';

// jsdom does not implement matchMedia. installMatchMedia stubs it with a
// controllable dark-mode preference and a working addEventListener/
// removeEventListener so the theme provider's OS-change listener can be
// exercised. Returns a controller to flip the preference and fire the change
// event, plus the set of registered listeners for assertions.
export interface MatchMediaController {
  setDark: (dark: boolean) => void;
  /** Fire a 'change' event reflecting the current preference. */
  fireChange: () => void;
  cleanup: () => void;
}

export function installMatchMedia(initialDark: boolean): MatchMediaController {
  let dark = initialDark;
  const listeners = new Set<(e: MediaQueryListEvent) => void>();

  const mql = {
    get matches() {
      return dark;
    },
    media: '(prefers-color-scheme: dark)',
    onchange: null,
    addEventListener: (_type: string, cb: (e: MediaQueryListEvent) => void) => {
      listeners.add(cb);
    },
    removeEventListener: (_type: string, cb: (e: MediaQueryListEvent) => void) => {
      listeners.delete(cb);
    },
    addListener: (cb: (e: MediaQueryListEvent) => void) => listeners.add(cb),
    removeListener: (cb: (e: MediaQueryListEvent) => void) => listeners.delete(cb),
    dispatchEvent: () => true,
  };

  const fn = vi.fn().mockReturnValue(mql);
  vi.stubGlobal('matchMedia', fn);
  // Also attach to window for code paths that read window.matchMedia.
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: fn,
  });

  return {
    setDark: (next: boolean) => {
      dark = next;
    },
    fireChange: () => {
      const event = { matches: dark } as MediaQueryListEvent;
      for (const cb of listeners) {
        cb(event);
      }
    },
    cleanup: () => {
      listeners.clear();
      vi.unstubAllGlobals();
    },
  };
}
