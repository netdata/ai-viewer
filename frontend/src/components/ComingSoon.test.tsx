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
    // SOW-0077: the note is now the H2 (the 'soon' copy). Verify it
    // exists, has no inline color, and is reachable as a heading. (The
    // class-based style rule is replaced by Tailwind utilities that read the
    // design system tokens; AGENTS.md's "no inline colors" invariant still
    // holds because the text uses text-foreground / text-muted-foreground
    // semantic classes.)
    const note = screen.getByRole('heading', { level: 2, name: 'soon' });
    expect(note).toBeInTheDocument();
    expect(note.getAttribute('style')).toBeNull();
  });
});
