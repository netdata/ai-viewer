import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ComingSoon } from './ComingSoon';

// ComingSoon is the Phase-2/3 route stub. It renders the title as a heading, a
// custom note (or a default), and styles the note via a CSS-module class — never
// an inline color (AGENTS.md frontend style rule: no inline colors).

describe('ComingSoon', () => {
  it('renders the title as a heading', () => {
    render(<ComingSoon title="Trace (APM)" />);
    expect(screen.getByRole('heading', { name: 'Trace (APM)' })).toBeInTheDocument();
  });

  it('renders the provided note', () => {
    render(<ComingSoon title="Topology" note="Force-directed actor graph — Phase 2." />);
    expect(screen.getByText('Force-directed actor graph — Phase 2.')).toBeInTheDocument();
  });

  it('falls back to a default note when none is given', () => {
    render(<ComingSoon title="Timeline" />);
    expect(screen.getByText('This view is planned for a later phase.')).toBeInTheDocument();
  });

  it('styles the note via a class, not an inline color', () => {
    render(<ComingSoon title="Trace" note="soon" />);
    const note = screen.getByText('soon');
    expect(note).toHaveClass(/note/);
    expect(note.getAttribute('style')).toBeNull();
  });
});
