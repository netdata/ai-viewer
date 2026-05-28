import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeToggle } from './ThemeToggle';
import { ThemeProvider } from '../../state/theme';
import { installMatchMedia, type MatchMediaController } from '../../test/matchMedia';

// ThemeToggle is the three-state Auto/Dark/Light control. Contract
// (frontend-architecture.md §User control / §Accessibility): each control has a
// STATIC aria-label describing what it does; the resolved theme is announced
// through a dedicated aria-live="polite" region, NOT by mutating a control's
// aria-label on every OS-preference flip (which re-announces noisily for screen
// readers).

function renderToggle() {
  return render(
    <ThemeProvider>
      <ThemeToggle />
    </ThemeProvider>,
  );
}

describe('ThemeToggle', () => {
  let mm: MatchMediaController;

  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.removeAttribute('data-theme');
  });

  afterEach(() => {
    mm.cleanup();
  });

  it('renders three controls with static aria-labels', () => {
    mm = installMatchMedia(true);
    renderToggle();
    expect(
      screen.getByRole('button', { name: 'Auto (follow system)' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dark' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Light' })).toBeInTheDocument();
  });

  it('marks the active preference with aria-pressed', async () => {
    mm = installMatchMedia(true);
    const user = userEvent.setup();
    renderToggle();
    const dark = screen.getByRole('button', { name: 'Dark' });
    expect(dark).toHaveAttribute('aria-pressed', 'false');
    await user.click(dark);
    expect(dark).toHaveAttribute('aria-pressed', 'true');
  });

  it('announces the resolved theme via a polite live region', () => {
    mm = installMatchMedia(true); // OS dark → resolved dark
    renderToggle();
    const live = screen.getByRole('status');
    expect(live).toHaveAttribute('aria-live', 'polite');
    expect(live).toHaveTextContent(/dark/i);
  });

  it('updates the live region when an OS-preference flip changes the resolved theme', () => {
    mm = installMatchMedia(false); // OS light → resolved light, auto preference
    renderToggle();
    const live = screen.getByRole('status');
    expect(live).toHaveTextContent(/light/i);
    act(() => {
      mm.setDark(true);
      mm.fireChange();
    });
    expect(live).toHaveTextContent(/dark/i);
  });

  it('does NOT mutate a control aria-label on a resolved-theme flip', () => {
    mm = installMatchMedia(false);
    renderToggle();
    const auto = screen.getByRole('button', { name: 'Auto (follow system)' });
    const before = auto.getAttribute('aria-label');
    act(() => {
      mm.setDark(true);
      mm.fireChange();
    });
    // The control's label is stable; only the live region changed.
    expect(auto.getAttribute('aria-label')).toBe(before);
  });

  it('does not put the resolved theme on the group container', () => {
    mm = installMatchMedia(true);
    renderToggle();
    // The group is still a labelled radiogroup-like control, but its label must
    // not carry the volatile "showing <resolved>" text.
    const group = screen.getByRole('group');
    expect(group.getAttribute('aria-label') ?? '').not.toMatch(/showing/i);
  });
});
