import { describe, expect, it } from 'vitest';
import { render, screen, within, cleanup } from '@testing-library/react';
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
    expect(within(row).getAllByText('—').length).toBeGreaterThanOrEqual(2);
    expect(within(row).getByText('running')).toBeInTheDocument();
  });

  it('renders the Source column with the source label', () => {
    renderRow(makeSession({ source_id: 'claude-code:/home/user/.claude' }));
    expect(screen.getByText('claude-code')).toBeInTheDocument();
  });

  it('renders all five source formats with their compact labels', () => {
    const cases: Array<[string, string]> = [
      ['aiagent_v3:/x', 'ai-agent v3'],
      ['aiagent_v2:/x', 'ai-agent v2'],
      ['claude-code:/x', 'claude-code'],
      ['codex:/x', 'codex'],
      ['opencode:/x', 'opencode'],
    ];
    for (const [sourceID, expected] of cases) {
      cleanup();
      renderRow(makeSession({ source_id: sourceID }));
      expect(screen.getByText(expected)).toBeInTheDocument();
    }
  });

  it('renders cost, tokens, turns, ops, and failures with formatted values', () => {
    renderRow(makeSession({
      cost_usd: 12.34,
      tokens_in: 12345,
      tokens_out: 6789,
      turn_count: 5,
      op_count: 42,
      failure_count: 3,
    }));
    const row = screen.getByRole('row');
    expect(within(row).getByText('$12.34')).toBeInTheDocument();
    expect(within(row).getByText('12,345')).toBeInTheDocument();
    expect(within(row).getByText('6,789')).toBeInTheDocument();
    expect(within(row).getByText('5')).toBeInTheDocument();
    expect(within(row).getByText('42')).toBeInTheDocument();
    expect(within(row).getByText('3')).toBeInTheDocument();
  });

  it('renders a completed duration from end_ts - start_ts', () => {
    renderRow(makeSession({ start_ts: 1_700_000_000_000_000, end_ts: 1_700_000_060_000_000 }));
    // 60s duration formatted as "1m 0s" or "60s" depending on the formatter
    const row = screen.getByRole('row');
    expect(within(row).getByText(/1m|60s/)).toBeInTheDocument();
  });
});
