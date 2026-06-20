import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { SourcesResponse, HealthResponse, SourceItem } from '../../api/types';

const sourcesSpy = vi.fn();
const healthSpy = vi.fn();
vi.mock('../../api/sources', () => ({
  useSources: () => sourcesSpy() as unknown,
  useHealth: () => healthSpy() as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: () => {},
}));

import { IngestErrors } from './IngestErrors';

const SRC_OK: SourceItem = {
  id: 'codex',
  format: 'codex',
  location: '/home/user/.codex',
  enabled: true,
  parse_errors: 0,
  last_seen_at: 1_700_000_000,
  created_at: 1_600_000_000,
  cursor: '',
  last_seq: 100,
  last_ts_us: 1_700_000_000,
  updated_at: 1_700_000_000,
};

const SRC_LOTS_OF_ERRORS: SourceItem = {
  ...SRC_OK,
  id: 'codex-bad',
  parse_errors: 2042,
};

const SRC_AI_AGENT: SourceItem = {
  ...SRC_OK,
  id: 'ai-agent-v2',
  parse_errors: 544,
};

const HEALTH_OK: HealthResponse = {
  status: 'ok',
  version: '0.1.0',
  schema_version: 1,
  uptime_s: 1000,
  db_path: '/var/lib/ai-viewer/db.sqlite',
  db_size_bytes: 1024 * 1024,
  sources: [
    { id: 'codex', format: 'codex', location: '/home/user/.codex', enabled: true, last_seen_at: 1_700_000_000, lag_us: 30_000_000, parse_errors: 0, last_seq: 100 },
    { id: 'codex-bad', format: 'codex', location: '/home/user/.codex-bad', enabled: true, last_seen_at: 1_700_000_000, lag_us: 600_000_000, parse_errors: 2042, last_seq: 100 },
    { id: 'ai-agent-v2', format: 'ai-agent', location: '/var/log/ai-agent', enabled: true, last_seen_at: 1_700_000_000, lag_us: 90_000_000, parse_errors: 544, last_seq: 100 },
  ],
  notify: { last_seq: 100, lag_us: 30_000_000 },
  sse: { subscriptions: 1 },
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/ingest-errors']}>
      <TooltipProvider>
        <Routes>
          <Route path="/ingest-errors" element={<IngestErrors />} />
          <Route path="/sessions" element={<div data-testid="sessions" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('IngestErrors', () => {
  beforeEach(() => {
    sourcesSpy.mockReset();
    healthSpy.mockReset();
  });

  it('renders the page header', () => {
    sourcesSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    healthSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: 'Ingest errors' })).toBeInTheDocument();
  });

  it('renders the summary strip (Total parse errors / Sources with errors / Sources lagging / Health)', () => {
    sourcesSpy.mockReturnValue({ data: { items: [SRC_OK, SRC_LOTS_OF_ERRORS, SRC_AI_AGENT] } as SourcesResponse, isPending: false, isError: false });
    healthSpy.mockReturnValue({ data: HEALTH_OK, isPending: false, isError: false });
    renderPage();
    expect(screen.getByText('Total parse errors')).toBeInTheDocument();
    expect(screen.getByText('Sources with errors')).toBeInTheDocument();
    expect(screen.getByText('Sources lagging ≥5m')).toBeInTheDocument();
    expect(screen.getByText('Health')).toBeInTheDocument();
    // 2042 + 544 = 2586 total errors; 2 sources with errors; 1 source >= 5min lag
    expect(screen.getByText('2,586')).toBeInTheDocument();
    expect(screen.getByText('1')).toBeInTheDocument(); // sources with errors
    // Health: OK
    expect(screen.getByText('OK')).toBeInTheDocument();
  });

  it('sorts sources by parse_errors desc (codex-bad first)', () => {
    sourcesSpy.mockReturnValue({ data: { items: [SRC_OK, SRC_LOTS_OF_ERRORS, SRC_AI_AGENT] } as SourcesResponse, isPending: false, isError: false });
    healthSpy.mockReturnValue({ data: HEALTH_OK, isPending: false, isError: false });
    renderPage();
    const table = screen.getByRole('table');
    const rows = within(table).getAllByRole('row');
    // Header row is index 0; first data row is index 1
    expect(rows[1]).toHaveTextContent('codex-bad');
    expect(rows[2]).toHaveTextContent('ai-agent-v2');
    expect(rows[3]).toHaveTextContent('codex');
  });

  it('shows the empty state when no sources are configured', () => {
    sourcesSpy.mockReturnValue({ data: { items: [] } as SourcesResponse, isPending: false, isError: false });
    healthSpy.mockReturnValue({ data: { ...HEALTH_OK, sources: [] }, isPending: false, isError: false });
    renderPage();
    expect(screen.getByText(/No sources in this window/i)).toBeInTheDocument();
  });

  it('shows the error state when the sources query fails', () => {
    sourcesSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new Error('boom'),
    });
    healthSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByText(/Failed to load sources/i)).toBeInTheDocument();
    expect(screen.getByText(/boom/)).toBeInTheDocument();
  });

  it('renders a link from each source row to /sessions?sources=<id>', () => {
    sourcesSpy.mockReturnValue({ data: { items: [SRC_LOTS_OF_ERRORS] } as SourcesResponse, isPending: false, isError: false });
    healthSpy.mockReturnValue({ data: HEALTH_OK, isPending: false, isError: false });
    renderPage();
    const link = screen.getByRole('link', { name: /Open sessions for codex-bad/i });
    expect(link).toHaveAttribute('href', '/sessions?sources=codex-bad');
  });

  it('highlights parse_errors > 0 in red and lag >= 5min in red', () => {
    sourcesSpy.mockReturnValue({ data: { items: [SRC_LOTS_OF_ERRORS] } as SourcesResponse, isPending: false, isError: false });
    healthSpy.mockReturnValue({ data: HEALTH_OK, isPending: false, isError: false });
    renderPage();
    const table = screen.getByRole('table');
    const errorCell = within(table).getByText('2,042');
    expect(errorCell.className).toContain('text-status-failed');
    const lagCell = within(table).getByText('10m');
    expect(lagCell.className).toContain('text-status-failed');
  });
});
