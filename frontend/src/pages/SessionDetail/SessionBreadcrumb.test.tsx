import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { SessionBreadcrumb } from './SessionBreadcrumb';

function renderCrumb(props: { parentSessionId: string | null; currentLabel?: string; backHref?: string }) {
  return render(
    <MemoryRouter initialEntries={['/sessions/abc']}>
      <Routes>
        <Route
          path="/sessions/abc"
          element={<SessionBreadcrumb {...props} />}
        />
        <Route path="/" element={<div data-testid="sessions-home" />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('SessionBreadcrumb', () => {
  it('renders "Back to sessions" link', () => {
    renderCrumb({ parentSessionId: null });
    const back = screen.getByRole('link', { name: /Back to sessions/i });
    expect(back).toBeInTheDocument();
    expect(back).toHaveAttribute('href', '/');
  });

  it('renders the home crumb link', () => {
    renderCrumb({ parentSessionId: null });
    expect(screen.getByRole('link', { name: /Sessions home/i })).toHaveAttribute('href', '/');
  });

  it('renders the current session id when no currentLabel is provided', () => {
    renderCrumb({ parentSessionId: null });
    // The current label falls back to 'this session' when omitted.
    expect(screen.getByText(/this session/i)).toBeInTheDocument();
  });

  it('renders the current label when provided', () => {
    renderCrumb({ parentSessionId: null, currentLabel: 'agent-alpha (abc12345…)' });
    expect(screen.getByText('agent-alpha (abc12345…)')).toBeInTheDocument();
  });

  it('renders the parent crumb link when parentSessionId is set', () => {
    renderCrumb({ parentSessionId: 'parent-xyz' });
    const parentLink = screen.getByRole('link', { name: /Sub-session/i });
    expect(parentLink).toHaveAttribute('href', '/sessions/parent-xyz');
  });

  it('does NOT render the parent crumb when parentSessionId is null', () => {
    renderCrumb({ parentSessionId: null });
    expect(screen.queryByRole('link', { name: /Sub-session/i })).not.toBeInTheDocument();
  });

  it('honors a custom backHref', () => {
    renderCrumb({ parentSessionId: null, backHref: '/failures' });
    expect(screen.getByRole('link', { name: /Back to sessions/i })).toHaveAttribute('href', '/failures');
  });

  it('marks the current label with aria-current=page', () => {
    renderCrumb({ parentSessionId: null, currentLabel: 'current-session' });
    const current = screen.getByText('current-session');
    expect(current).toHaveAttribute('aria-current', 'page');
  });
});
