import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type {
  ChildSummary,
  OpDetail,
  SessionDetailResponse,
  TurnDetail,
} from '../../../api/types';

// useSessionRelated (SOW-0071) is mocked as a spy defaulting to no data; the
// "Possibly related" test overrides the return to exercise the section.
type RelatedResult = { data: { related: Array<Record<string, unknown>> } | undefined; isPending: boolean; isError: boolean; error: unknown };
const relatedSpy = vi.fn((): RelatedResult => ({ data: { related: [] }, isPending: false, isError: false, error: null }));
vi.mock('../../../api/sessions', () => ({
  useSessionRelated: () => relatedSpy(),
}));

import { OverviewTab, toolsUsed } from './OverviewTab';

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
    tokens_cache_read: 0,
    tokens_cache_write: 0,
    cost_usd: 0,
    bytes_in: 0,
    bytes_out: 0,
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
    tokens_cache_read: 0,
    tokens_cache_write: 0,
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
    provider: 'anthropic',
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
      provider_alias: 'claude',
      cwd: '/workspace/root-a',
      call_path: 'rootA>childA',
      status: 'completed',
      error_class: null,
      error_message: null,
      start_ts: 1,
      end_ts: 2,
      duration_us: 1,
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

  it('renders session-level diagnostics from the detail contract', () => {
    const d = detail([]);
    d.session.error_message = 'root warning';
    renderTab(d);
    expect(screen.getByText('Provider')).toBeInTheDocument();
    expect(screen.getByText('anthropic')).toBeInTheDocument();
    expect(screen.getByText('Provider alias')).toBeInTheDocument();
    expect(screen.getByText('claude')).toBeInTheDocument();
    expect(screen.getByText('Working dir')).toBeInTheDocument();
    expect(screen.getByText('/.../root-a')).not.toHaveAttribute('title');
    expect(screen.queryByText('/workspace/root-a')).not.toBeInTheDocument();
    expect(screen.getByText('Call path')).toBeInTheDocument();
    expect(screen.getByText('rootA>childA')).toBeInTheDocument();
    expect(screen.getByText('Duration')).toBeInTheDocument();
    expect(screen.getByText('1µs')).toBeInTheDocument();
    expect(screen.getByText('Error message')).toBeInTheDocument();
    expect(screen.getByText('root warning')).toBeInTheDocument();
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

  it('renders the nested grandchild under its parent (SOW-0069 recursive tree)', () => {
    // A child that itself has a child_sessions entry (the grandchild). The tree
    // renders both, the grandchild indented one level under the parent.
    renderTab(
      detail(
        [],
        [
          child({
            id: 'child-1',
            agent_name: 'worker',
            child_sessions: [
              child({
                id: 'grand-1',
                agent_name: 'sub-worker',
                kind: 'sub_agent',
              }),
            ],
          }),
        ],
      ),
    );
    const section = screen.getByRole('region', { name: /^Child sessions/ });
    // Both the child and the nested grandchild appear, each linking to its detail.
    expect(within(section).getByRole('link', { name: 'worker' })).toHaveAttribute(
      'href',
      '/sessions/child-1',
    );
    expect(within(section).getByRole('link', { name: 'sub-worker' })).toHaveAttribute(
      'href',
      '/sessions/grand-1',
    );
    // The grandchild row is indented (its Agent cell carries the tree marker).
    const grandRow = within(section)
      .getByRole('link', { name: 'sub-worker' })
      .closest('tr');
    expect(grandRow).not.toBeNull();
    // The grandchild cell carries the indent span with a depth>0 marker ("└ ").
    const grandCell = within(section).getByText('sub-worker').parentElement;
    expect(grandCell?.textContent).toContain('└');
  });

  // ── SOW-0071: heuristic cross-harness "Possibly related" section ───────────

  it('renders the "Possibly related" section with a dashed border when heuristic links exist', () => {
    relatedSpy.mockReturnValue({
      data: {
        related: [
          {
            id: 'codex-session',
            source_format: 'codex',
            agent_name: 'codex',
            status: 'completed' as const,
            start_ts: 1,
            end_ts: null,
            reason: 'same cwd, started during this session (different harness)',
          },
        ],
      },
      isPending: false,
      isError: false,
      error: null,
    });
    renderTab(detail([]));
    const heading = screen.getByRole('heading', { name: /possibly related/i });
    expect(heading).toBeInTheDocument();
    // The section carries the dashed-border class (AC2: visually distinct from
    // the solid-border child-sessions tree).
    const section = heading.closest('section');
    expect(section?.className).toMatch(/related/);
    // The related session links to its detail page.
    expect(screen.getByRole('link', { name: 'codex' })).toHaveAttribute(
      'href',
      '/sessions/codex-session',
    );
  });

  it('renders NO "Possibly related" section when no heuristic links exist', () => {
    relatedSpy.mockReturnValue({
      data: { related: [] },
      isPending: false,
      isError: false,
      error: null,
    });
    renderTab(detail([]));
    expect(screen.queryByRole('heading', { name: /possibly related/i })).not.toBeInTheDocument();
  });
});
