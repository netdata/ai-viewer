import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
import type { OpDetail } from '../../api/types';
import { SpanDetailDrawer } from './SpanDetailDrawer';

// SpanDetailDrawer is the shared right-side drawer (ui-pages.md §Span detail
// drawer): NOT a modal — the visualization stays visible behind it. It shows the
// clicked op's full fields + its payload_refs, closes on Esc / outside-click, is
// focus-trapped with ARIA dialog semantics, and returns focus to the trigger on
// close. The payload byte-preview is deferred until GET /api/payloads lands
// (rest-api.md §GET /api/payloads); the drawer renders the ref metadata + a
// disabled "preview coming soon" affordance today.

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

afterEach(() => {
  vi.restoreAllMocks();
});

describe('SpanDetailDrawer', () => {
  it('renders nothing when op is null (closed)', () => {
    const { container } = render(<SpanDetailDrawer op={null} onClose={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('renders a non-modal dialog with the op fields when open', () => {
    render(<SpanDetailDrawer op={op({})} onClose={vi.fn()} />);
    const dialog = screen.getByRole('dialog');
    // Non-modal: aria-modal is false so the viz behind stays in the a11y tree.
    expect(dialog).toHaveAttribute('aria-modal', 'false');
    // Named by its title so screen readers announce it.
    expect(dialog).toHaveAccessibleName(/claude-opus/i);
    expect(within(dialog).getByText('anthropic')).toBeInTheDocument();
    expect(within(dialog).getByText('claude-opus-4-7')).toBeInTheDocument();
    expect(within(dialog).getByText(/llm/i)).toBeInTheDocument();
  });

  it('shows the error class and message when the op failed', () => {
    render(
      <SpanDetailDrawer
        op={op({ status: 'failed', error_class: 'RateLimitError' })}
        onClose={vi.fn()}
      />,
    );
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('RateLimitError')).toBeInTheDocument();
  });

  it('lists payload_refs with kind + format, and a disabled preview affordance', () => {
    render(
      <SpanDetailDrawer
        op={op({
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

  it('shows an empty-payloads note when the op has no payload_refs', () => {
    render(<SpanDetailDrawer op={op({ payload_refs: [] })} onClose={vi.fn()} />);
    expect(screen.getByText(/no payloads/i)).toBeInTheDocument();
  });

  it('calls onClose when the close button is clicked', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<SpanDetailDrawer op={op({})} onClose={onClose} />);
    await user.click(screen.getByRole('button', { name: /close/i }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('calls onClose on Escape', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<SpanDetailDrawer op={op({})} onClose={onClose} />);
    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('calls onClose on an outside (overlay) click but not on an inside click', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<SpanDetailDrawer op={op({})} onClose={onClose} />);
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
          {open ? <SpanDetailDrawer op={op({})} onClose={() => setOpen(false)} /> : null}
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

  it('has no axe violations', async () => {
    const { container } = render(
      <SpanDetailDrawer
        op={op({
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
});
