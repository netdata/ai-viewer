import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { resolveStatusClass, StatusBadge } from './StatusBadge';

// StatusBadge maps a SessionStatus to a CSS-module class. The contract
// (frontend-architecture.md §Accessibility): the status text is always shown
// (color is never the only signal), every known status has an explicit class,
// an unknown status renders with the neutral style, and a *missing* CSS-module
// class is surfaced in dev/test via console.error rather than silently
// rendering an unstyled badge. CSS-module class names are hashed in real
// builds; under Vitest `css: true` they resolve to identifiers, so we assert on
// the rendered text + className containing the status token where stable, and
// on the dev assertion behavior directly.

afterEach(() => {
  vi.restoreAllMocks();
});

describe('StatusBadge', () => {
  it('renders the status text as a visible label (not color alone)', () => {
    render(<StatusBadge status="completed" />);
    expect(screen.getByText('completed')).toBeInTheDocument();
  });

  it.each(['completed', 'running', 'failed', 'interrupted', 'abandoned'] as const)(
    'renders known status %s without a dev error',
    (status) => {
      const err = vi.spyOn(console, 'error').mockImplementation(() => {});
      render(<StatusBadge status={status} />);
      expect(screen.getByText(status)).toBeInTheDocument();
      // A known status must resolve to a class — no dev assertion fired.
      expect(err).not.toHaveBeenCalled();
    },
  );

  it('renders an unknown status with the neutral style, no dev error', () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {});
    // An open-union future value the server might emit (SessionStatus is open).
    render(<StatusBadge status="queued" />);
    expect(screen.getByText('queued')).toBeInTheDocument();
    // Unknown is a real, handled case → not an error.
    expect(err).not.toHaveBeenCalled();
  });

  it('always sets the base badge class', () => {
    const { container } = render(<StatusBadge status="running" />);
    const span = container.querySelector('span');
    expect(span).not.toBeNull();
    // Base badge token is always present (the visual chrome of the badge).
    expect(span?.className).toMatch(/badge/);
  });
});

// resolveStatusClass is the pure status→class mapper. Testing it directly lets
// us exercise the dev assertion that fires when a CSS-module class is missing
// (a rename/typo in the module) — a path the real (hashed) module never hits
// because every known key exists. The styles object is injected here.
describe('resolveStatusClass', () => {
  const styles = {
    completed: 'c',
    running: 'r',
    failed: 'f',
    unknown: 'u',
  } as const;

  it('maps known statuses to their class', () => {
    expect(resolveStatusClass('completed', styles)).toBe('c');
    expect(resolveStatusClass('running', styles)).toBe('r');
    // interrupted + abandoned share the failed style.
    expect(resolveStatusClass('interrupted', styles)).toBe('f');
    expect(resolveStatusClass('abandoned', styles)).toBe('f');
  });

  it('maps an unknown status to the neutral class with no dev error', () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(resolveStatusClass('weird', styles)).toBe('u');
    expect(err).not.toHaveBeenCalled();
  });

  it('console.errors in dev when a mapped CSS class is missing', () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {});
    // A module that lost its `.completed` rule (typo/rename).
    const broken = { running: 'r', failed: 'f', unknown: 'u' };
    const cls = resolveStatusClass('completed', broken);
    // Production resilience: still returns a (falsy) class, never throws.
    expect(cls).toBe('');
    // Dev visibility: the gap is surfaced (import.meta.env.DEV is true in vitest).
    expect(err).toHaveBeenCalled();
    expect(err.mock.calls[0]?.join(' ')).toContain('completed');
  });
});
