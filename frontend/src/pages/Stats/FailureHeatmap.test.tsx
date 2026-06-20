import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { FailureHeatmap } from './FailureHeatmap';
import type { Filters } from '../../state/filters';

// We mock fetch so we don't need a real backend for the component test.
// The component calls /api/sessions repeatedly (paginated) until
// next_cursor is absent. Each mocked response returns one page.
type FetchResponse = {
  items: Array<{ id: string; start_ts: number; status: string }>;
  next_cursor?: string;
};

const FILTER: Filters = {
  agents: [],
  models: [],
  tools: [],
  status: [],
  sources: [],
};

function mkFetchMock(responses: FetchResponse[]): typeof globalThis.fetch {
  let call = 0;
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString();
    expect(url).toMatch(/^\/api\/sessions\?/);
    const r = responses[call] ?? { items: [] };
    call += 1;
    return new Response(JSON.stringify(r), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  }) as unknown as typeof globalThis.fetch;
}

describe('FailureHeatmap', () => {
  let originalFetch: typeof globalThis.fetch;
  beforeEach(() => {
    originalFetch = globalThis.fetch;
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it('renders skeletons while pending', () => {
    globalThis.fetch = mkFetchMock([]);
    render(<FailureHeatmap filters={FILTER} />);
    // The component renders an aria-busy skeleton container while
    // waiting for the data fetch. The 'aria-busy' attribute is the
    // most reliable signal across react testing-library + jsdom.
    expect(document.querySelector('[aria-busy="true"]')).not.toBeNull();
  });

  it('renders the heatmap with at least one cell when failures exist', async () => {
    // Pretend today is a fixed date so the day-of-week index is deterministic.
    // We use start_ts values that map to Wed 14:00.
    // New Date(1_700_000_000_000) = 2023-11-15 14:13:20 UTC = Wed 15:13 local in EET.
    // That maps to key "3:15" (Wed=3, hour=15). Use a range that covers several days.
    globalThis.fetch = mkFetchMock([
      {
        items: [
          { id: '1', start_ts: 1_700_000_000_000_000, status: 'failed' },
          { id: '2', start_ts: 1_700_000_000_000_000, status: 'failed' },
        ],
      },
    ]);
    render(<FailureHeatmap filters={FILTER} />);
    await waitFor(() => {
      // A cell with count > 0 exists (look for any cell with
      // bg-status-failed)
      const failedCells = document.querySelectorAll('.bg-status-failed');
      expect(failedCells.length).toBeGreaterThan(0);
    });
  });

  it('renders an error message when fetch fails', async () => {
    globalThis.fetch = vi.fn(async () => {
      return new Response('{"error":"boom"}', { status: 500 });
    }) as unknown as typeof globalThis.fetch;
    render(<FailureHeatmap filters={FILTER} />);
    await waitFor(() => {
      expect(screen.getByText(/Heatmap unavailable/i)).toBeInTheDocument();
    });
  });
});
