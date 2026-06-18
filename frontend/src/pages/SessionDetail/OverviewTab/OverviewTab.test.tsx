import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { OverviewTab, toolsUsed } from './OverviewTab';
import type {
  ChildSummary,
  OpDetail,
  SessionDetailResponse,
  TurnDetail,
} from '../../../api/types';

// OverviewTab renders the session header + per-session aggregate StatCards from
// the DETAIL response row (not /api/stats), plus a tools-used summary derived
// from the response's tool ops. toolsUsed is the pure aggregator under test.

function op(over: Partial<OpDetail>): OpDetail {
  return {
    id: 'op',
    kind: 'tool',
    name: 'mcp__slack__send',
    model: '',
    provider: '',
    start_ts: 1,
    end_ts: 2,
    duration_us: 1,
    status: 'completed',
    error_class: null,
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    ctx_used: null,
    ctx_max: null,
    child_session_id: null,
    payload_refs: [],
    ...over,
  };
}

function turn(ops: OpDetail[]): TurnDetail {
  return {
    id: 't',
    seq: 1,
    start_ts: 1,
    end_ts: 2,
    status: 'completed',
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    op_count: ops.length,
    ops,
  };
}

function child(over: Partial<ChildSummary>): ChildSummary {
  return {
    id: 'child-1',
    native_id: 'cn1',
    kind: 'sub_agent',
    agent_name: 'nedi-sub',
    model: 'claude-haiku-4-5',
    status: 'running',
    start_ts: 1,
    end_ts: null,
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    op_count: 0,
    failure_count: 0,
    ...over,
  };
}

function detail(turns: TurnDetail[], children: ChildSummary[] = []): SessionDetailResponse {
  return {
    session: {
      id: 's1',
      native_id: 'n1',
      root_session_id: 's1',
      parent_session_id: null,
      source_id: 'src',
      kind: 'root',
      agent_name: 'nedi',
      model: 'claude-opus-4-7',
      provider: 'anthropic',
      status: 'completed',
      error_class: null,
      start_ts: 1,
      end_ts: 2,
      tokens_in: 1234,
      tokens_out: 5678,
      tokens_cache_read: 3000,
      tokens_cache_write: 500,
      cost_usd: 0.42,
      turn_count: 2,
      op_count: 9,
      failure_count: 1,
      child_session_count: children.length,
    },
    turns,
    child_sessions: children,
  };
}

/** detailNoCache builds a session whose token + cache counts are all zero. */
function detailNoCache(): SessionDetailResponse {
  const d = detail([]);
  d.session = {
    ...d.session,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    tokens_cache_write: 0,
  };
  return d;
}

function renderTab(d: SessionDetailResponse) {
  return render(
    <MemoryRouter>
      <OverviewTab detail={d} />
    </MemoryRouter>,
  );
}

describe('toolsUsed', () => {
  it('aggregates tool ops by name with call + failure counts', () => {
    const result = toolsUsed([
      turn([
        op({ name: 'tool.a' }),
        op({ name: 'tool.a', error_class: 'Timeout' }),
        op({ name: 'tool.b' }),
        op({ kind: 'llm', name: 'should-be-ignored' }),
      ]),
    ]);
    expect(result).toEqual([
      { name: 'tool.a', calls: 2, failures: 1 },
      { name: 'tool.b', calls: 1, failures: 0 },
    ]);
  });

  it('sorts by call count descending', () => {
    const result = toolsUsed([
      turn([op({ name: 'rare' }), op({ name: 'common' }), op({ name: 'common' })]),
    ]);
    expect(result[0]?.name).toBe('common');
  });

  it('returns empty when there are no tool ops', () => {
    expect(toolsUsed([turn([op({ kind: 'llm' })])])).toEqual([]);
  });
});

describe('OverviewTab', () => {
  it('renders the header with agent, model and status', () => {
    renderTab(detail([]));
    expect(screen.getByText('nedi')).toBeInTheDocument();
    expect(screen.getByText('claude-opus-4-7')).toBeInTheDocument();
    expect(screen.getByText('completed')).toBeInTheDocument();
  });

  it('renders per-session aggregate stat cards from the detail row', () => {
    renderTab(detail([]));
    // Thousands-separated tokens, formatted cost, counts.
    expect(screen.getByText('1,234')).toBeInTheDocument();
    expect(screen.getByText('5,678')).toBeInTheDocument();
    expect(screen.getByText('$0.42')).toBeInTheDocument();
  });

  it('labels tokens_in as FRESH/uncached input (not total)', () => {
    renderTab(detail([]));
    // The input number must be labeled so it is not confused with total input.
    expect(screen.getByText(/fresh/i)).toBeInTheDocument();
  });

  it('renders the cache breakdown (read + write) and the cache-hit-rate', () => {
    renderTab(detail([]));
    // cache_read = 3000, cache_write = 500.
    expect(screen.getByText('3,000')).toBeInTheDocument();
    expect(screen.getByText('500')).toBeInTheDocument();
    // hit-rate = 3000 / (1234 + 3000 + 500) = 3000/4734 = 63.4%.
    expect(screen.getByText('63.4%')).toBeInTheDocument();
    expect(screen.getByText(/cache hit/i)).toBeInTheDocument();
  });

  it('shows an em dash for cache-hit-rate when all token counts are zero', () => {
    renderTab(detailNoCache());
    const hit = screen.getByTestId('cache-hit-rate');
    expect(hit).toHaveTextContent('—');
  });

  it('renders a tools-used table derived from the ops', () => {
    renderTab(detail([turn([op({ name: 'tool.x' }), op({ name: 'tool.x' })])]));
    const table = screen.getByRole('table');
    expect(within(table).getByText('tool.x')).toBeInTheDocument();
    // 2 calls.
    expect(within(table).getByText('2')).toBeInTheDocument();
  });

  it('shows an empty message when there are no tool calls', () => {
    renderTab(detail([]));
    expect(screen.getByText('No tool calls in this session.')).toBeInTheDocument();
  });

  it('lists child sessions, each linking to the child detail page', () => {
    renderTab(
      detail(
        [],
        [
          child({
            id: 'child-1',
            agent_name: 'nedi-sub',
            model: 'claude-haiku-4-5',
            status: 'running',
            op_count: 7,
            failure_count: 2,
          }),
        ],
      ),
    );
    const section = screen.getByRole('region', { name: /^Child sessions/ });
    expect(within(section).getByText('nedi-sub')).toBeInTheDocument();
    expect(within(section).getByText('claude-haiku-4-5')).toBeInTheDocument();
    expect(within(section).getByText('running')).toBeInTheDocument();
    const link = within(section).getByRole('link', { name: 'nedi-sub' });
    expect(link).toHaveAttribute('href', '/sessions/child-1');
  });

  it('renders no child-sessions section when the session has no children', () => {
    renderTab(detail([]));
    expect(screen.queryByRole('region', { name: /^Child sessions/ })).toBeNull();
    expect(screen.queryByText(/^Child sessions/)).toBeNull();
  });
});
