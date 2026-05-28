import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { EmptyState, ErrorState, LoadingState, errorMessage } from './StatusViews';
import { ApiError } from '../../api/client';

// The shared status primitives. The load-bearing behavior is ErrorState
// surfacing the real error message (no silent failure — AGENTS.md): an ApiError
// shows its decoded server message; a plain Error shows its message; anything
// else shows a stable fallback.

describe('LoadingState', () => {
  it('renders a status region with the default label', () => {
    render(<LoadingState />);
    expect(screen.getByRole('status')).toHaveTextContent('Loading…');
  });

  it('renders a custom label', () => {
    render(<LoadingState label="Fetching sessions" />);
    expect(screen.getByRole('status')).toHaveTextContent('Fetching sessions');
  });
});

describe('errorMessage', () => {
  it('uses the ApiError message', () => {
    expect(errorMessage(new ApiError(400, 'BAD_REQUEST', 'bad cursor'))).toBe('bad cursor');
  });
  it('uses a plain Error message', () => {
    expect(errorMessage(new Error('network down'))).toBe('network down');
  });
  it('falls back for a non-error value', () => {
    expect(errorMessage('weird')).toBe('Something went wrong.');
  });
});

describe('ErrorState', () => {
  it('surfaces the ApiError message in an alert', () => {
    render(<ErrorState error={new ApiError(500, 'INTERNAL_ERROR', 'db exploded')} />);
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Failed to load');
    expect(alert).toHaveTextContent('db exploded');
  });

  it('uses a custom title', () => {
    render(<ErrorState error={new Error('x')} title="Logs unavailable" />);
    expect(screen.getByRole('alert')).toHaveTextContent('Logs unavailable');
  });
});

describe('EmptyState', () => {
  it('renders the default empty message', () => {
    render(<EmptyState />);
    expect(screen.getByRole('status')).toHaveTextContent('Nothing to show.');
  });
  it('renders custom children', () => {
    render(<EmptyState>No sessions match.</EmptyState>);
    expect(screen.getByRole('status')).toHaveTextContent('No sessions match.');
  });
});
