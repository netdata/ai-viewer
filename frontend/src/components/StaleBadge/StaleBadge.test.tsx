import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StaleBadge, shouldMarkStale } from './StaleBadge';

const NOW_US = 1_700_000_000_000_000; // fixed point in time
const TEN_MIN = 10 * 60 * 1_000_000;
const SIXTY_S = 60 * 1_000_000;

describe('shouldMarkStale', () => {
  it('returns false for non-running status', () => {
    expect(shouldMarkStale('completed', NOW_US - 60 * 60 * 1_000_000, NOW_US)).toBe(false);
    expect(shouldMarkStale('failed', NOW_US - 60 * 60 * 1_000_000, NOW_US)).toBe(false);
    expect(shouldMarkStale('abandoned', NOW_US - 60 * 60 * 1_000_000, NOW_US)).toBe(false);
  });

  it('returns false when last_activity_ts is missing', () => {
    expect(shouldMarkStale('running', null, NOW_US)).toBe(false);
    expect(shouldMarkStale('running', undefined, NOW_US)).toBe(false);
  });

  it('returns false when last_activity_ts is within threshold', () => {
    expect(shouldMarkStale('running', NOW_US - 5 * 60 * 1_000_000, NOW_US)).toBe(false);
    expect(shouldMarkStale('running', NOW_US - (TEN_MIN - SIXTY_S - 1), NOW_US)).toBe(false);
  });

  it('returns true when last_activity_ts is beyond threshold (with grace)', () => {
    expect(shouldMarkStale('running', NOW_US - 11 * 60 * 1_000_000, NOW_US)).toBe(true);
    expect(shouldMarkStale('running', NOW_US - 60 * 60 * 1_000_000, NOW_US)).toBe(true);
  });
});

describe('StaleBadge', () => {
  it('renders nothing for non-running sessions', () => {
    const { container } = render(
      <StaleBadge status="completed" lastActivityTs={NOW_US - 60 * 60 * 1_000_000} nowUs={NOW_US} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('renders nothing when last_activity_ts is missing', () => {
    const { container } = render(
      <StaleBadge status="running" lastActivityTs={null} nowUs={NOW_US} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('renders the badge for stale running sessions', () => {
    render(
      <StaleBadge
        status="running"
        lastActivityTs={NOW_US - 15 * 60 * 1_000_000}
        nowUs={NOW_US}
      />,
    );
    const badge = screen.getByRole('status');
    expect(badge).toBeInTheDocument();
    expect(badge.textContent).toMatch(/stale/);
  });

  it('does not render for fresh running sessions (within threshold)', () => {
    const { container } = render(
      <StaleBadge
        status="running"
        lastActivityTs={NOW_US - 2 * 60 * 1_000_000}
        nowUs={NOW_US}
      />,
    );
    expect(container.firstChild).toBeNull();
  });
});
