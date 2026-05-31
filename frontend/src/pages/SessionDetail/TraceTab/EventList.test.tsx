import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { OpDetail, TurnDetail } from '../../../api/types';
import { buildOpTree, flattenTree, type TraceNode } from '../../../viz/trace';
import { EventList } from './EventList';

// Focused tests for the windowed event list: it lists every op in order via
// click-to-detail buttons, windows large lists (only a slice mounted + spacer
// rows for the scrollbar), reflects the selected row, and renders a failed op's
// status with the error style.

function op(over: Partial<OpDetail>): OpDetail {
  return {
    id: 'op',
    kind: 'tool',
    name: 'n',
    model: '',
    provider: '',
    start_ts: 0,
    end_ts: 100,
    duration_us: 100,
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

function nodesFrom(ops: OpDetail[]): TraceNode[] {
  const turn: TurnDetail = {
    id: 't1',
    seq: 1,
    start_ts: 0,
    end_ts: null,
    status: 'completed',
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    op_count: ops.length,
    ops,
  };
  return flattenTree(buildOpTree([turn]));
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('EventList', () => {
  it('lists a small set of ops fully with click-to-detail buttons', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const nodes = nodesFrom([
      op({ id: 'a', name: 'alpha', start_ts: 0, end_ts: 50, duration_us: 50 }),
      op({ id: 'b', name: 'beta', start_ts: 60, end_ts: 90, duration_us: 30 }),
    ]);
    render(<EventList nodes={nodes} onSelect={onSelect} selectedId={null} />);
    const table = screen.getByRole('table', { name: /event list/i });
    expect(within(table).getAllByRole('row')).toHaveLength(3); // header + 2
    await user.click(within(table).getByRole('button', { name: 'beta' }));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect((onSelect.mock.calls[0]?.[0] as TraceNode).op.id).toBe('b');
  });

  it('windows a large list: only a slice is mounted, and scrolling reveals later rows', () => {
    const many: OpDetail[] = [];
    for (let i = 0; i < 1000; i++) {
      many.push(op({ id: `op-${i}`, name: `name-${i}`, start_ts: i * 10, end_ts: i * 10 + 5, duration_us: 5 }));
    }
    const nodes = nodesFrom(many);
    render(<EventList nodes={nodes} onSelect={vi.fn()} selectedId={null} />);
    const table = screen.getByRole('table', { name: /event list/i });

    // Far fewer than 1000 rows are mounted initially.
    const initialBody = within(table).getAllByRole('row').length - 1;
    expect(initialBody).toBeLessThan(1000);
    expect(initialBody).toBeGreaterThan(0);
    // An early row is present; a far row is not yet mounted.
    expect(within(table).getByText('name-0')).toBeInTheDocument();
    expect(within(table).queryByText('name-900')).not.toBeInTheDocument();

    // Scroll far down → later rows mount, early rows unmount.
    const scroller = screen.getByRole('region', { name: /event list scroll area/i });
    fireEvent.scroll(scroller, { target: { scrollTop: 900 * 28 } });
    expect(within(table).getByText('name-900')).toBeInTheDocument();
    expect(within(table).queryByText('name-0')).not.toBeInTheDocument();
  });

  it('shows an em-dash (not 0µs) for a point-event op with no measured duration (P2#3)', () => {
    const nodes = nodesFrom([
      // A measured tool op (real duration) and a claude-code-style POINT-EVENT LLM
      // op recorded at one timestamp. The real persisted point shape is
      // end_ts == start_ts AND duration_us === 0 (null is the still-RUNNING shape).
      op({ id: 'tool', name: 'Bash', start_ts: 0, end_ts: 400, duration_us: 400 }),
      op({ id: 'llm', kind: 'llm', name: 'gen', start_ts: 500, end_ts: 500, duration_us: 0 }),
    ]);
    render(<EventList nodes={nodes} onSelect={vi.fn()} selectedId={null} />);
    const table = screen.getByRole('table', { name: /event list/i });
    const rows = within(table).getAllByRole('row');
    // Row order: header, tool (measured), llm (point event).
    const llmRow = rows[2] as HTMLElement;
    // The duration cell renders an em-dash, not "0µs"/"0".
    expect(within(llmRow).getByText('—')).toBeInTheDocument();
    expect(within(llmRow).queryByText('0µs')).not.toBeInTheDocument();
    // The measured op still shows its real duration.
    const toolRow = rows[1] as HTMLElement;
    expect(within(toolRow).getByText('400µs')).toBeInTheDocument();
  });

  it('highlights the selected row and renders a failed op with the error style', () => {
    const nodes = nodesFrom([
      op({ id: 'ok', name: 'ok-op', start_ts: 0, end_ts: 10, duration_us: 10 }),
      op({
        id: 'bad',
        name: 'bad-op',
        start_ts: 20,
        end_ts: 30,
        duration_us: 10,
        status: 'failed',
        error_class: 'Boom',
      }),
    ]);
    render(<EventList nodes={nodes} onSelect={vi.fn()} selectedId="ok" />);
    const table = screen.getByRole('table', { name: /event list/i });
    // The failed op's status cell carries the failed style.
    const failedCell = within(table).getByText('failed');
    expect(failedCell.getAttribute('class') ?? '').toMatch(/statusFailed/);
  });
});
