import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
import type { OpDetail, TopologyNode } from '../../api/types';
import { SpanDetailDrawer, type SpanDetail } from './SpanDetailDrawer';

// SpanDetailDrawer is the shared right-side drawer (ui-pages.md §Span detail
// drawer): NOT a modal — the visualization stays visible behind it. It is
// SOURCE-AWARE via a discriminated `SpanDetail` prop and NEVER fabricates
// unavailable fields as zero (SOW-0006 decision, from real-data review):
//   - 'op'   (Trace tab)    → full op row + payload_refs (the only source with
//     op metrics: model/tokens/cost/context/payloads); the byte-preview is
//     deferred (SOW-0033) so each ref shows a disabled "preview coming soon".
//   - 'span' (Timeline tab) → only lane/span fields (kind/name/status/start/end/
//     derived duration); NO $0.00 / 0 tokens / "No payloads" — it points to the
//     Trace tab for token/cost/payload detail instead.
//   - 'node' (per-session Topology) → an aggregate actor: failure % + the value
//     of the selected size metric, labeled honestly; op-only fields omitted.
// Closes on Esc / outside-click, is focus-trapped with ARIA dialog semantics, and
// returns focus to the trigger on close.

function op(over: Partial<OpDetail>): OpDetail {
  return {
    id: 'op-1',
    kind: 'llm',
    name: 'claude-opus',
    model: 'claude-opus-4-7',
    provider: 'anthropic',
    start_ts: 1_700_000_000_000_000,
    end_ts: 1_700_000_000_500_000,
    duration_us: 500_000,
    status: 'completed',
    error_class: null,
    tokens_in: 1234,
    tokens_out: 5678,
    cost_usd: 0.42,
    ctx_used: 1000,
    ctx_max: 200_000,
    child_session_id: null,
    payload_refs: [],
    ...over,
  };
}

/** opDetail wraps an OpDetail in the 'op' SpanDetail variant (Trace-tab source). */
function opDetail(over: Partial<OpDetail> = {}): SpanDetail {
  return { kind: 'op', op: op(over) };
}

/** The inner shape of the 'span' SpanDetail variant (Timeline-tab source). */
type SpanShape = Extract<SpanDetail, { kind: 'span' }>['span'];

/** spanDetail builds the 'span' SpanDetail variant (Timeline-tab source). */
function spanDetail(over: Partial<SpanShape> = {}): SpanDetail {
  return {
    kind: 'span',
    span: {
      id: 'span-1',
      kind: 'tool',
      name: 'Bash',
      start_ts: 1_700_000_000_000_000,
      end_ts: 1_700_000_000_250_000,
      duration_us: 250_000,
      status: 'completed',
      ...over,
    },
  };
}

/** nodeDetail builds the 'node' SpanDetail variant (per-session Topology source). */
function nodeDetail(node: Partial<TopologyNode>, metricLabel: string, metricValue: number): SpanDetail {
  return {
    kind: 'node',
    node: {
      id: 'tool:shell.Bash',
      kind: 'tool',
      label: 'shell.Bash',
      size_metric: metricValue,
      failure_ratio: 0,
      ...node,
    },
    metricLabel,
    metricValue,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('SpanDetailDrawer', () => {
  it('renders nothing when detail is null (closed)', () => {
    const { container } = render(<SpanDetailDrawer detail={null} onClose={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  // ── 'op' variant (Trace tab) — the full rendering is unchanged ──────────────

  it('renders a non-modal dialog with the op fields when open (op variant)', () => {
    render(<SpanDetailDrawer detail={opDetail()} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog');
    // Non-modal: aria-modal is false so the viz behind stays in the a11y tree.
    expect(dialog).toHaveAttribute('aria-modal', 'false');
    // Named by its title so screen readers announce it.
    expect(dialog).toHaveAccessibleName(/claude-opus/i);
    expect(within(dialog).getByText('anthropic')).toBeInTheDocument();
    expect(within(dialog).getByText('claude-opus-4-7')).toBeInTheDocument();
    expect(within(dialog).getByText(/llm/i)).toBeInTheDocument();
  });

  it('shows the op cost, tokens, and a payloads section (op variant has real metrics)', () => {
    render(<SpanDetailDrawer detail={opDetail()} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('$0.42')).toBeInTheDocument();
    expect(within(dialog).getByText('1,234')).toBeInTheDocument();
    expect(within(dialog).getByText('5,678')).toBeInTheDocument();
    // The Payloads section header is present for an op.
    expect(within(dialog).getByText(/^Payloads$/)).toBeInTheDocument();
  });

  it('shows the error class and message when the op failed', () => {
    render(
      <SpanDetailDrawer
        detail={opDetail({ status: 'failed', error_class: 'RateLimitError' })}
        onClose={vi.fn()}
      />,
    );
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('RateLimitError')).toBeInTheDocument();
  });

  it('lists payload_refs with kind + format, and a disabled preview affordance (op variant)', () => {
    render(
      <SpanDetailDrawer
        detail={opDetail({
          payload_refs: [
            { id: 1, kind: 'llm_request', format: 'http', compression: 'gzip', original_bytes: 2048, stored_bytes: 512 },
            { id: 2, kind: 'llm_response', format: 'json', compression: null, original_bytes: 4096, stored_bytes: 4096 },
          ],
        })}
        onClose={vi.fn()}
      />,
    );
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('llm_request')).toBeInTheDocument();
    expect(within(dialog).getByText('llm_response')).toBeInTheDocument();
    // Byte-preview is deferred: a disabled control communicates "coming soon".
    const previews = within(dialog).getAllByRole('button', { name: /preview/i });
    expect(previews.length).toBeGreaterThan(0);
    for (const btn of previews) {
      expect(btn).toBeDisabled();
    }
  });

  it('shows an empty-payloads note when the op has no payload_refs (op variant)', () => {
    render(<SpanDetailDrawer detail={opDetail({ payload_refs: [] })} onClose={vi.fn()} />);
    expect(screen.getByText(/no payloads/i)).toBeInTheDocument();
  });

  // ── 'span' variant (Timeline tab) — no fabricated op metrics ────────────────

  it('renders only the lane/span fields for a span and points to the Trace tab', () => {
    render(<SpanDetailDrawer detail={spanDetail()} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/Bash/i);
    // Status + Duration are shown (the span carries them).
    expect(within(dialog).getByText('completed')).toBeInTheDocument();
    // It directs the operator to the Trace tab for the missing detail.
    expect(within(dialog).getByText(/open this op in the Trace tab/i)).toBeInTheDocument();
  });

  it('does NOT fabricate $0.00 / 0 tokens / "No payloads" for a span', () => {
    render(<SpanDetailDrawer detail={spanDetail()} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByText('$0.00')).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/\b0 tokens\b/i)).not.toBeInTheDocument();
    // No "Tokens in"/"Tokens out"/"Cost" labels, no zeroed values for them.
    expect(within(dialog).queryByText(/tokens in/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/tokens out/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/^Cost$/)).not.toBeInTheDocument();
    // No payloads section at all (header or empty note).
    expect(within(dialog).queryByText(/no payloads/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/^Payloads$/)).not.toBeInTheDocument();
    // And no "$0.00" text and no standalone "0" token value: the literal 0 the old
    // synthesized OpDetail produced for tokens_in must never appear as a value.
    expect(within(dialog).queryByText('0')).not.toBeInTheDocument();
  });

  it('shows an em dash (never "0µs") for a point-event span (duration_us null) and a real duration for a closed one', () => {
    // A point event (end_ts === start_ts) carries a null derived duration: the
    // drawer must render "—", never a fabricated "0µs"/"0".
    const { rerender } = render(
      <SpanDetailDrawer
        detail={spanDetail({ id: 'pt', name: 'instant', start_ts: 500, end_ts: 500, duration_us: null })}
        onClose={vi.fn()}
      />,
    );
    let dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByText('0µs')).not.toBeInTheDocument();
    const durationField = within(dialog).getByText('Duration').closest('div');
    expect(durationField).not.toBeNull();
    expect(within(durationField as HTMLElement).getByText('—')).toBeInTheDocument();

    // A strictly-closed span still shows its real formatted duration.
    rerender(<SpanDetailDrawer detail={spanDetail()} onClose={vi.fn()} />);
    dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('250ms')).toBeInTheDocument();
  });

  it('shows an em dash (never "0µs") for a still-RUNNING op (end_ts null, duration_us null) in the op variant', () => {
    // GROUND TRUTH: a null end_ts with a null duration_us is a still-RUNNING op
    // (no recorded end yet) — NOT a point event (a point event is persisted with
    // end_ts == start_ts AND duration_us === 0; see the next test). isInstantOp is
    // true for a running op, so the op drawer must render Duration as "—", never a
    // fabricated "0µs".
    render(
      <SpanDetailDrawer detail={opDetail({ duration_us: null, end_ts: null })} onClose={vi.fn()} />,
    );
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByText('0µs')).not.toBeInTheDocument();
    const durationField = within(dialog).getByText('Duration').closest('div');
    expect(durationField).not.toBeNull();
    expect(within(durationField as HTMLElement).getByText('—')).toBeInTheDocument();
  });

  it('shows an em dash (never "0µs") for the REAL persisted point-event op shape (end_ts==start_ts, duration_us 0) in the op variant', () => {
    // GROUND TRUTH: a point-event op is persisted with end_ts === start_ts AND
    // duration_us === 0 (the ingest writer computes end_ts - start_ts = 0; it is
    // NOT null — null is the still-running shape). isInstantOp is true for it
    // (end_ts <= start_ts), so the op drawer must render Duration as "—", never a
    // fabricated "0µs". A measured op (end_ts > start_ts, duration_us > 0) still
    // shows its formatted duration.
    const { rerender } = render(
      <SpanDetailDrawer
        detail={opDetail({ start_ts: 1000, end_ts: 1000, duration_us: 0 })}
        onClose={vi.fn()}
      />,
    );
    let dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByText('0µs')).not.toBeInTheDocument();
    const durationField = within(dialog).getByText('Duration').closest('div');
    expect(durationField).not.toBeNull();
    expect(within(durationField as HTMLElement).getByText('—')).toBeInTheDocument();

    // A measured op still shows its real formatted duration (regression guard).
    rerender(
      <SpanDetailDrawer
        detail={opDetail({ start_ts: 1000, end_ts: 1400, duration_us: 400 })}
        onClose={vi.fn()}
      />,
    );
    dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('400µs')).toBeInTheDocument();
  });

  it('shows a derived duration for a closed span and an em dash for a running one', () => {
    const { rerender } = render(<SpanDetailDrawer detail={spanDetail()} onClose={vi.fn()} />);
    expect(within(screen.getByRole('dialog')).getByText('250ms')).toBeInTheDocument();
    // A running span (null end / null duration) shows an em dash, not "0".
    rerender(
      <SpanDetailDrawer
        detail={spanDetail({ id: 's2', name: 'run', start_ts: 1, end_ts: null, duration_us: null, status: 'running' })}
        onClose={vi.fn()}
      />,
    );
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/run/i);
    expect(within(dialog).queryByText('0µs')).not.toBeInTheDocument();
  });

  // ── 'node' variant (per-session Topology) — actor aggregate, no op fields ────

  it('renders the failure rate and the selected size-metric value for a node', () => {
    render(
      <SpanDetailDrawer detail={nodeDetail({ failure_ratio: 0.5 }, 'Cost', 0.42)} onClose={vi.fn()} />,
    );
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/shell\.Bash/i);
    // Failure rate is formatted as a percent.
    expect(within(dialog).getByText('50.0%')).toBeInTheDocument();
    // The metric is labeled honestly ("Cost") and formatted by its kind ($).
    expect(within(dialog).getByText('Cost')).toBeInTheDocument();
    expect(within(dialog).getByText('$0.42')).toBeInTheDocument();
  });

  it('formats the node size-metric value by its metric (duration / tokens / ctx %)', () => {
    const { rerender } = render(
      <SpanDetailDrawer detail={nodeDetail({}, 'Duration', 500_000)} onClose={vi.fn()} />,
    );
    expect(within(screen.getByRole('dialog')).getByText('500ms')).toBeInTheDocument();

    rerender(<SpanDetailDrawer detail={nodeDetail({}, 'Tokens', 12_345)} onClose={vi.fn()} />);
    expect(within(screen.getByRole('dialog')).getByText('12,345')).toBeInTheDocument();

    rerender(<SpanDetailDrawer detail={nodeDetail({}, 'Context %', 0.731)} onClose={vi.fn()} />);
    expect(within(screen.getByRole('dialog')).getByText('73.1%')).toBeInTheDocument();
  });

  it('does NOT fabricate $0.00 / 0 tokens / "No payloads" for a node', () => {
    render(<SpanDetailDrawer detail={nodeDetail({}, 'Cost', 0.42)} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByText('$0.00')).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/\b0 tokens\b/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/tokens in/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/tokens out/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/no payloads/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/^Payloads$/)).not.toBeInTheDocument();
    // No model/provider/context rows (op-only fields are omitted, not zeroed).
    expect(within(dialog).queryByText(/^Model$/)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/^Context$/)).not.toBeInTheDocument();
  });

  // ── close / focus behavior (unchanged, now keyed on detail !== null) ─────────

  it('calls onClose when the close button is clicked', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<SpanDetailDrawer detail={opDetail()} onClose={onClose} />);
    await user.click(screen.getByRole('button', { name: /close/i }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('calls onClose on Escape', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<SpanDetailDrawer detail={opDetail()} onClose={onClose} />);
    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('calls onClose on an outside (overlay) click but not on an inside click', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<SpanDetailDrawer detail={opDetail()} onClose={onClose} />);
    // Clicking the panel itself does not close.
    await user.click(screen.getByRole('dialog'));
    expect(onClose).not.toHaveBeenCalled();
    // Clicking the backdrop closes.
    await user.click(screen.getByTestId('drawer-overlay'));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('moves focus into the drawer on open and returns it to the trigger on close', async () => {
    const user = userEvent.setup();

    // A realistic harness: a trigger button opens the drawer; closing it must
    // return focus to that trigger (ui-pages.md §Span detail drawer).
    function Harness() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            open span
          </button>
          {open ? <SpanDetailDrawer detail={opDetail()} onClose={() => setOpen(false)} /> : null}
        </>
      );
    }

    render(<Harness />);
    const trigger = screen.getByRole('button', { name: 'open span' });
    await user.click(trigger);

    const dialog = screen.getByRole('dialog');
    // Focus moved into the dialog (the close button is the initial focus).
    expect(dialog.contains(document.activeElement)).toBe(true);

    // Tab cycles within the dialog (focus trap) — focus never escapes.
    await user.tab();
    expect(dialog.contains(document.activeElement)).toBe(true);
    await user.tab();
    expect(dialog.contains(document.activeElement)).toBe(true);

    // Close via Escape; focus returns to the trigger that opened it.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(document.activeElement).toBe(trigger);
  });

  it('focus-traps a payload-less span variant too (Tab stays within the panel)', async () => {
    // The span variant has no payload buttons, so the close button is the sole
    // focusable — Tab must still keep focus inside the panel.
    const user = userEvent.setup();
    render(<SpanDetailDrawer detail={spanDetail()} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog');
    expect(dialog.contains(document.activeElement)).toBe(true);
    await user.tab();
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it('has no axe violations (op variant with payloads)', async () => {
    const { container } = render(
      <SpanDetailDrawer
        detail={opDetail({
          payload_refs: [
            { id: 1, kind: 'llm_request', format: 'http', compression: 'gzip', original_bytes: 2048, stored_bytes: 512 },
          ],
        })}
        onClose={vi.fn()}
      />,
    );
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('has no axe violations (span and node variants)', async () => {
    const span = render(<SpanDetailDrawer detail={spanDetail()} onClose={vi.fn()} />);
    expect(await axe(span.container)).toHaveNoViolations();
    span.unmount();
    const node = render(
      <SpanDetailDrawer detail={nodeDetail({ failure_ratio: 0.25 }, 'Cost', 1.5)} onClose={vi.fn()} />,
    );
    expect(await axe(node.container)).toHaveNoViolations();
  });
});
