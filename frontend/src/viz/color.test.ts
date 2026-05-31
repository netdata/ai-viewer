import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  colorForActorKind,
  colorForFailureRatio,
  colorForOpKind,
  colorForStatus,
  refreshThemeColors,
  startThemeColorWatch,
} from './color';

// viz/color.ts is the theme-aware color reader for the D3 renderings
// (frontend-architecture.md §Theming, ui-pages.md §Span detail drawer:
// "Op-kind/status colors come from the theme tokens"). It reads CSS custom
// properties once and re-reads them on a data-theme MutationObserver. These
// tests stub getComputedStyle so the resolution is deterministic in jsdom
// (which does not resolve real CSS variables).

// Token values keyed by custom-property name. The shim returns these so a test
// can assert that colorForStatus maps a status to the resolved token, and that
// a theme flip re-reads them.
let tokens: Record<string, string>;

function installComputedStyle(): void {
  // Only the documentElement is queried by the module; return our token map.
  vi.spyOn(window, 'getComputedStyle').mockImplementation(
    () =>
      ({
        getPropertyValue: (prop: string) => tokens[prop.trim()] ?? '',
      }) as unknown as CSSStyleDeclaration,
  );
}

beforeEach(() => {
  tokens = {
    '--success': '#3fb950',
    '--warning': '#d29922',
    '--error': '#f85149',
    '--text-secondary': '#8b949e',
    '--accent': '#58a6ff',
    '--info': '#a5a5ff',
  };
  installComputedStyle();
  refreshThemeColors();
});

afterEach(() => {
  vi.restoreAllMocks();
  document.documentElement.removeAttribute('data-theme');
});

describe('colorForOpKind', () => {
  it('returns a distinct, stable color per known op kind', () => {
    const llm = colorForOpKind('llm');
    const tool = colorForOpKind('tool');
    const reasoning = colorForOpKind('reasoning');
    const session = colorForOpKind('session');
    const compaction = colorForOpKind('compaction');

    // Each known kind has its own color.
    const set = new Set([llm, tool, reasoning, session, compaction]);
    expect(set.size).toBe(5);

    // Values are non-empty hex/color strings and stable across calls.
    expect(llm).toMatch(/^#|rgb|hsl/);
    expect(colorForOpKind('llm')).toBe(llm);
  });

  it('maps system and internal kinds to their own colors', () => {
    expect(colorForOpKind('system')).toMatch(/^#|rgb|hsl/);
    expect(colorForOpKind('internal')).toMatch(/^#|rgb|hsl/);
  });

  it('falls back to a neutral color for an unknown future kind', () => {
    const unknown = colorForOpKind('quantum_flux');
    expect(unknown).toMatch(/^#|rgb|hsl/);
    // Unknown shares the neutral fallback, not any known-kind hue.
    expect(unknown).toBe(colorForOpKind('another_unknown'));
  });
});

describe('colorForStatus', () => {
  it('maps completed → success token, failed → error token, running → warning token', () => {
    expect(colorForStatus('completed')).toBe('#3fb950');
    expect(colorForStatus('failed')).toBe('#f85149');
    expect(colorForStatus('running')).toBe('#d29922');
  });

  it('maps interrupted and abandoned to the error token (failure family)', () => {
    expect(colorForStatus('interrupted')).toBe('#f85149');
    expect(colorForStatus('abandoned')).toBe('#f85149');
  });

  it('maps an unknown status to the neutral text-secondary token', () => {
    expect(colorForStatus('weird')).toBe('#8b949e');
  });

  it('re-reads token values after a theme flip (refreshThemeColors)', () => {
    expect(colorForStatus('completed')).toBe('#3fb950');
    // Simulate the light theme resolving --success to a different value.
    tokens['--success'] = '#1a7f37';
    refreshThemeColors();
    expect(colorForStatus('completed')).toBe('#1a7f37');
  });
});

describe('colorForFailureRatio', () => {
  it('maps a zero failure ratio to the success token (green)', () => {
    expect(colorForFailureRatio(0)).toBe('#3fb950');
  });

  it('maps a low (<1/3) failure ratio to the warning token (amber)', () => {
    expect(colorForFailureRatio(0.2)).toBe('#d29922');
  });

  it('maps a high (≥1/3) failure ratio to the error token (red)', () => {
    expect(colorForFailureRatio(0.5)).toBe('#f85149');
    expect(colorForFailureRatio(1)).toBe('#f85149');
  });

  it('treats a non-finite or negative ratio as no failures (success)', () => {
    expect(colorForFailureRatio(Number.NaN)).toBe('#3fb950');
    expect(colorForFailureRatio(-1)).toBe('#3fb950');
  });
});

describe('colorForActorKind', () => {
  it('gives agent and tool nodes distinct base colors', () => {
    const agent = colorForActorKind('agent');
    const tool = colorForActorKind('tool');
    expect(agent).toMatch(/^#|rgb|hsl/);
    expect(tool).toMatch(/^#|rgb|hsl/);
    expect(agent).not.toBe(tool);
  });

  it('falls back to the neutral token for an unknown actor kind', () => {
    expect(colorForActorKind('mystery')).toBe('#8b949e');
  });
});

describe('startThemeColorWatch', () => {
  it('re-reads tokens when the data-theme attribute mutates, and the disposer stops it', async () => {
    const dispose = startThemeColorWatch();
    try {
      expect(colorForStatus('completed')).toBe('#3fb950');

      // A theme flip changes the resolved token and the data-theme attribute.
      tokens['--success'] = '#1a7f37';
      document.documentElement.setAttribute('data-theme', 'light');
      // MutationObserver callbacks fire on a microtask after the mutation.
      await Promise.resolve();
      expect(colorForStatus('completed')).toBe('#1a7f37');

      // After disposal, a further mutation no longer triggers a re-read.
      dispose();
      tokens['--success'] = '#000000';
      document.documentElement.setAttribute('data-theme', 'dark');
      await Promise.resolve();
      expect(colorForStatus('completed')).toBe('#1a7f37');
    } finally {
      dispose();
    }
  });

  it('returns an idempotent disposer (double-dispose is safe)', () => {
    const dispose = startThemeColorWatch();
    dispose();
    expect(() => {
      dispose();
    }).not.toThrow();
  });
});
