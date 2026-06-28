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
  progress_updated_at: 1_700_000_000,
  lifecycle_state: 'tailing',
  lifecycle_state_at: 1_700_000_000,
  tail_started_at: 1_700_000_000,
  tail_heartbeat_at: 1_700_000_000,
  tail_restart_count: 0,
  read_model_state: 'ready',
  read_model_state_at: 1_700_000_000,
  read_model_repair_attempts: 0,
};

const SRC_LOTS_OF_ERRORS: SourceItem = {
  ...SRC_OK,
  id: 'codex-bad',
  parse_errors: 2042,
  lifecycle_state: 'tail_stale',
};

const SRC_AI_AGENT: SourceItem = {
  ...SRC_OK,
  id: 'ai-agent-v2',
  parse_errors: 544,
  read_model_state: 'repair_failed',
  read_model_error: 'repair failed',
};

const HEALTH_OK: HealthResponse = {
  status: 'ok',
  version: '0.1.0',
  schema_version: 1,
  uptime_s: 1000,
  db_path: '/var/lib/ai-viewer/db.sqlite',
  db_size_bytes: 1024 * 1024,
  sources: [],
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

  it('renders the summary strip (Total parse errors / Sources with errors / Sources degraded / Health)', () => {
    sourcesSpy.mockReturnValue({ data: { items: [SRC_OK, SRC_LOTS_OF_ERRORS, SRC_AI_AGENT] } as SourcesResponse, isPending: false, isError: false });
    healthSpy.mockReturnValue({ data: HEALTH_OK, isPending: false, isError: false });
    renderPage();
    expect(screen.getByText('Total parse errors')).toBeInTheDocument();
    expect(screen.getByText('Sources with errors')).toBeInTheDocument();
    expect(screen.getByText('Sources degraded')).toBeInTheDocument();
    expect(screen.getByText('Health')).toBeInTheDocument();
    // 2042 + 544 = 2586 total errors; 2 sources with errors; 2 sources degraded
    expect(screen.getByText('2,586')).toBeInTheDocument();
    expect(screen.getAllByText('2')).toHaveLength(2);
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

  it('highlights parse_errors and degraded lifecycle state in red', () => {
    sourcesSpy.mockReturnValue({ data: { items: [SRC_LOTS_OF_ERRORS] } as SourcesResponse, isPending: false, isError: false });
    healthSpy.mockReturnValue({ data: HEALTH_OK, isPending: false, isError: false });
    renderPage();
    const table = screen.getByRole('table');
    const errorCell = within(table).getByText('2,042');
    expect(errorCell.className).toContain('text-status-failed');
    const staleCell = within(table).getByText('Tail stale');
    expect(staleCell.className).toContain('text-status-failed');
  });
});
