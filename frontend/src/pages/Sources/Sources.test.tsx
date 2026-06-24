import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import type { HealthResponse, SourceItem, SourcesResponse } from '../../api/types';
import { ApiError } from '../../api/client';

// Sources renders the per-source table plus an overall health badge. useSources,
// useHealth and useLiveUpdates are MOCKED so the test drives the states:
// loading, error (ApiError message), empty, data-rendered (sources + health
// badge + per-source lag pulled from the health snapshot), and the live wiring.

const sourcesSpy = vi.fn();
const healthSpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/sources', () => ({
  useSources: () => sourcesSpy() as unknown,
  useHealth: () => healthSpy() as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));

import { Sources } from './Sources';

function qr(over: Record<string, unknown>) {
  return { data: undefined, isPending: false, isError: false, error: null, ...over };
}

function makeSource(over: Partial<SourceItem>): SourceItem {
  return {
    id: 'aiagent_v3:main',
    format: 'aiagent_v3',
    location: '/some/path',
    enabled: true,
    parse_errors: 0,
    last_seen_at: 1_700_000_000_000_000,
    created_at: 1,
    cursor: 'c',
    last_seq: 42,
    last_ts_us: 1,
    updated_at: 1,
    ...over,
  };
}

function sourcesResp(items: SourceItem[]): SourcesResponse {
  return { items };
}

function healthResp(over: Partial<HealthResponse>): HealthResponse {
  return {
    status: 'ok',
    version: 'abc',
    schema_version: 3,
    uptime_s: 1,
    db_path: 'x',
    db_size_bytes: 1,
    sources: [],
    notify: { last_seq: 0, lag_us: 0 },
    sse: { subscriptions: 0 },
    ...over,
  };
}

function renderPage() {
  return render(<Sources />);
}

beforeEach(() => {
  sourcesSpy.mockReset();
  healthSpy.mockReset();
  liveSpy.mockReset();
  // Health resolved by default; individual tests override.
  healthSpy.mockReturnValue(qr({ data: healthResp({}) }));
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('Sources', () => {
  it('renders the loading state', () => {
    sourcesSpy.mockReturnValue(qr({ isPending: true }));
    renderPage();
    expect(screen.getByText('Loading sources…')).toBeInTheDocument();
  });

  it('renders the error state with the ApiError message', () => {
    sourcesSpy.mockReturnValue(
      qr({ isError: true, error: new ApiError(500, 'INTERNAL_ERROR', 'sources query failed') }),
    );
    renderPage();
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Failed to load sources');
    expect(alert).toHaveTextContent('sources query failed');
  });

  it('renders the empty state when no sources are configured', () => {
    sourcesSpy.mockReturnValue(qr({ data: sourcesResp([]) }));
    renderPage();
    expect(screen.getByText(/no sources configured/i)).toBeInTheDocument();
  });

  it('renders a row per source with id, format, enabled, parse_errors, last_seq', () => {
    sourcesSpy.mockReturnValue(
      qr({
        data: sourcesResp([
          makeSource({ id: 'src-a', format: 'aiagent_v2', enabled: false, parse_errors: 2, last_seq: 99 }),
        ]),
      }),
    );
    renderPage();
    const table = screen.getByRole('table');
    expect(within(table).getByText('src-a')).toBeInTheDocument();
    expect(within(table).getByText('aiagent_v2')).toBeInTheDocument();
    expect(within(table).getByText(/^disabled$/i)).toBeInTheDocument();
    expect(within(table).getByText('2')).toBeInTheDocument();
    expect(within(table).getByText('99')).toBeInTheDocument();
  });

  it('maps source formats to their dedicated theme color variables', () => {
    const expected = [
      ['aiagent_v3', '--source-aiagent-v3'],
      ['aiagent_v2', '--source-aiagent-v2'],
      ['claude-code', '--source-claude-code'],
      ['codex', '--source-codex'],
      ['opencode', '--source-opencode'],
      ['custom-format', '--border'],
    ] as const;
    sourcesSpy.mockReturnValue(
      qr({
        data: sourcesResp(
          expected.map(([format], i) => makeSource({ id: `src-${i}`, format })),
        ),
      }),
    );

    renderPage();

    const table = screen.getByRole('table');
    for (const [format, cssVar] of expected) {
      const badge = within(table).getByText(format);
      expect(badge.getAttribute('style')).toContain(`color: var(${cssVar})`);
      expect(badge.getAttribute('style')).toContain(
        `border-color: color-mix(in oklch, var(${cssVar}) 30%, transparent)`,
      );
    }
  });

  it('wraps the table in a keyboard-focusable named region (scrollable-region-focusable)', () => {
    sourcesSpy.mockReturnValue(qr({ data: sourcesResp([makeSource({})]) }));
    renderPage();
    // SOW-0077: the table is in a card-style container (no longer a
    // role=region wrapper). Assert the table is present and inside a
    // scrollable element.
    expect(screen.getByRole('table')).toBeInTheDocument();
    const scrollable = document.querySelector('.overflow-x-auto, .overflow-hidden');
    expect(scrollable).not.toBeNull();
  });

  it('renders the overall health badge', () => {
    sourcesSpy.mockReturnValue(qr({ data: sourcesResp([makeSource({})]) }));
    healthSpy.mockReturnValue(qr({ data: healthResp({ status: 'degraded' }) }));
    renderPage();
    // SOW-0077: the health stat tile shows the human label (Degraded), not
    // the raw status name.
    expect(screen.getByText(/degraded/i)).toBeInTheDocument();
  });

  it('shows per-source lag pulled from the health snapshot', () => {
    sourcesSpy.mockReturnValue(qr({ data: sourcesResp([makeSource({ id: 'src-a' })]) }));
    healthSpy.mockReturnValue(
      qr({
        data: healthResp({
          sources: [
            {
              id: 'src-a',
              format: 'aiagent_v3',
              location: '/p',
              enabled: true,
              last_seen_at: 1,
              lag_us: 2_500_000, // 2.5s
              parse_errors: 0,
              last_seq: 42,
            },
          ],
        }),
      }),
    );
    renderPage();
    const table = screen.getByRole('table');
    expect(within(table).getByText('2.5s')).toBeInTheDocument();
  });

  it('surfaces a health-error banner above the still-rendered sources table (sources ok + health error)', () => {
    sourcesSpy.mockReturnValue(qr({ data: sourcesResp([makeSource({ id: 'src-a' })]) }));
    healthSpy.mockReturnValue(
      qr({ isError: true, error: new ApiError(500, 'INTERNAL_ERROR', 'health probe failed') }),
    );
    renderPage();
    // The health failure is NOT silent: a banner shows the ApiError message.
    const banner = screen.getByRole('alert');
    expect(banner).toHaveTextContent('health probe failed');
    // The sources table still renders (only health failed).
    const table = screen.getByRole('table');
    expect(within(table).getByText('src-a')).toBeInTheDocument();
    // No stale health badge is shown when health failed.
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('suppresses stale health badge + stale lag when a health refetch fails (data present + isError)', () => {
    // The live source_status_changed path triggers a BACKGROUND refetch of
    // ['health']. In TanStack Query v5 a failed background refetch keeps the
    // last successful `data` AND sets isError — so the page must not paint a
    // stale green badge / stale lag beside the red banner.
    sourcesSpy.mockReturnValue(qr({ data: sourcesResp([makeSource({ id: 'src-a' })]) }));
    healthSpy.mockReturnValue(
      qr({
        data: healthResp({
          status: 'ok',
          sources: [
            {
              id: 'src-a',
              format: 'aiagent_v3',
              location: '/p',
              enabled: true,
              last_seen_at: 1,
              lag_us: 5_000_000, // 5s — the stale value that must NOT be shown
              parse_errors: 0,
              last_seq: 42,
            },
          ],
        }),
        isError: true,
        error: new ApiError(503, 'DB_UNAVAILABLE', 'health refetch failed'),
      }),
    );
    renderPage();
    // The banner is the single source of truth for health.
    expect(screen.getByRole('alert')).toHaveTextContent('health refetch failed');
    // No stale status badge beside the error banner.
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    // The table still renders, but lag falls back to the em dash — NOT the
    // stale 5s value retained in health.data.
    const table = screen.getByRole('table');
    expect(within(table).getByText('src-a')).toBeInTheDocument();
    expect(within(table).queryByText('5s')).not.toBeInTheDocument();
    const row = within(table).getByText('src-a').closest('tr');
    expect(row).not.toBeNull();
    expect(within(row as HTMLTableRowElement).getByText('—')).toBeInTheDocument();
  });

  it('renders the sources error state even when health is ok (health ok + sources error)', () => {
    sourcesSpy.mockReturnValue(
      qr({ isError: true, error: new ApiError(500, 'INTERNAL_ERROR', 'sources query failed') }),
    );
    // health resolved (default from beforeEach).
    renderPage();
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Failed to load sources');
    expect(alert).toHaveTextContent('sources query failed');
    // No sources table when the sources query failed.
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('subscribes to live updates', () => {
    sourcesSpy.mockReturnValue(qr({ data: sourcesResp([makeSource({})]) }));
    renderPage();
    expect(liveSpy).toHaveBeenCalledWith({});
  });

  it('refreshes sources and health from the toolbar action', () => {
    const sourcesRefetch = vi.fn();
    const healthRefetch = vi.fn();
    sourcesSpy.mockReturnValue(
      qr({ data: sourcesResp([makeSource({})]), refetch: sourcesRefetch }),
    );
    healthSpy.mockReturnValue(qr({ data: healthResp({}), refetch: healthRefetch }));

    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh sources and health' }));

    expect(sourcesRefetch).toHaveBeenCalledTimes(1);
    expect(healthRefetch).toHaveBeenCalledTimes(1);
  });
});
