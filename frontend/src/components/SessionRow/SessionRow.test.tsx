import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { SessionRow } from './SessionRow';
import type { SessionListItem } from '../../api/types';

// SessionRow is the standalone <tr> wrapper around SessionRowBody (the cells).
// SessionsList renders SessionRowBody directly with its own leading column; this
// test pins the standalone row's columns + the agent link target.

function makeSession(over: Partial<SessionListItem>): SessionListItem {
  return {
    id: 'sess/9',
    native_id: 'native-9',
    root_session_id: 'sess/9',
    parent_session_id: null,
    source_id: 'src',
    kind: 'root',
    agent_name: 'nedi',
    model: 'claude-opus-4-7',
    status: 'completed',
    start_ts: 1_700_000_000_000_000,
    end_ts: 1_700_000_060_000_000,
    tokens_in: 100,
    tokens_out: 200,
    cost_usd: 0.42,
    turn_count: 3,
    op_count: 7,
    failure_count: 0,
    child_session_count: 0,
    ...over,
  };
}

function renderRow(session: SessionListItem) {
  return render(
    <MemoryRouter>
      <table>
        <tbody>
          <SessionRow session={session} />
        </tbody>
      </table>
    </MemoryRouter>,
  );
}

describe('SessionRow', () => {
  it('renders one row with the canonical columns', () => {
    renderRow(makeSession({}));
    const row = screen.getByRole('row');
    expect(within(row).getByRole('link', { name: 'nedi' })).toBeInTheDocument();
    expect(within(row).getByText('claude-opus-4-7')).toBeInTheDocument();
    expect(within(row).getByText('completed')).toBeInTheDocument();
  });

  it('links the agent name to the encoded session detail route', () => {
    renderRow(makeSession({ id: 'sess/9' }));
    expect(screen.getByRole('link', { name: 'nedi' })).toHaveAttribute(
      'href',
      '/sessions/sess%2F9',
    );
  });

  it('falls back to native_id when agent_name is empty', () => {
    renderRow(makeSession({ agent_name: '' }));
    expect(screen.getByRole('link', { name: 'native-9' })).toBeInTheDocument();
  });

  it('renders an em dash for an empty model and a running (null end_ts) duration', () => {
    renderRow(makeSession({ model: '', end_ts: null, status: 'running' }));
    const row = screen.getByRole('row');
    // Both the empty model and the open-ended duration render the em dash.
    expect(within(row).getAllByText('—').length).toBeGreaterThanOrEqual(2);
    expect(within(row).getByText('running')).toBeInTheDocument();
  });
});
