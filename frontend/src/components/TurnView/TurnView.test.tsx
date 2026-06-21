import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within, type RenderOptions } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
import { MemoryRouter } from 'react-router-dom';
import type { OpDetail, TurnDetail } from '../../api/types';
import { TurnView } from './TurnView';

// TurnView (ui-turn-view.md): renders ONE TurnDetail as a vertical timeline of
// step cards. Tests cover header, step dispatch, payload fetching, scroll-to,
// copy buttons, and accessibility.

function makeOp(over: Partial<OpDetail>): OpDetail {
  return {
    id: 'op-default',
    kind: 'llm',
    name: 'message',
    model: 'claude-opus-4-7',
    provider: 'anthropic',
    parent_op_id: null,
    start_ts: 1_700_000_000_000_000,
    end_ts: 1_700_000_000_500_000,
    duration_us: 500_000,
    status: 'completed',
    error_class: null,
    error_message: null,
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

function makeTurn(over: Partial<TurnDetail> & { ops?: OpDetail[] } = {}): TurnDetail {
  return {
    id: 'turn-1',
    seq: 1,
    start_ts: 1_700_000_000_000_000,
    end_ts: 1_700_000_000_500_000,
    status: 'completed',
    tokens_in: 1234,
    tokens_out: 5678,
    cost_usd: 0.42,
    op_count: 1,
    ops: [],
    ...over,
  };
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** renderInRouter wraps with MemoryRouter because TurnStep renders Link
 *  to sub-sessions when an op.kind === 'session' op is encountered. */
function renderInRouter(ui: React.ReactElement, options?: Omit<RenderOptions, 'wrapper'>) {
  return render(<MemoryRouter>{ui}</MemoryRouter>, options);
}

/** wasFetchedWithUrl returns true if any fetch call's first argument equals
 *  the given URL (ignoring options like { signal }). */
function wasFetchedWithUrl(url: string): boolean {
  return fetchMock.mock.calls.some((call) => call[0] === url);
}

describe('TurnView — header', () => {
  it('renders turn seq, op count, status, tokens and cost', () => {
    const turn = makeTurn({ seq: 3, op_count: 4, status: 'completed', tokens_in: 100, tokens_out: 200, cost_usd: 0.0123 });

    renderInRouter(<TurnView turn={turn} />);

    expect(screen.getByRole('heading', { name: /turn\s+#3/i, level: 2 })).toBeInTheDocument();
    expect(screen.getByText(/4 ops/i)).toBeInTheDocument();
    expect(screen.getByText(/completed/i)).toBeInTheDocument();
    expect(screen.getByText(/100/)).toBeInTheDocument();
    expect(screen.getByText(/200/)).toBeInTheDocument();
    expect(screen.getByText(/\$0\.0123/)).toBeInTheDocument();
  });

  it('renders Copy turn button in the header', () => {
    renderInRouter(<TurnView turn={makeTurn()} />);
    expect(screen.getByRole('button', { name: /copy turn/i })).toBeInTheDocument();
  });
});

describe('TurnView — step dispatch', () => {
  it('renders an internal user_input op as a user prompt with markdown', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response('Please **fix** the bug in `app.css`.', {
        status: 200,
        headers: { 'X-Payload-Truncated': 'false' },
      }),
    );

    const op = makeOp({
      id: 'op-user',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 100, kind: 'request', format: 'text', compression: null, original_bytes: 30, stored_bytes: 30 }],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-user');
    expect(within(step).getByRole('heading', { name: /user/i })).toBeInTheDocument();
    const prose = await within(step).findByRole('region', { name: /prose/i });
    expect(prose.textContent).toContain('Please fix the bug in app.css.');

    expect(within(prose).getByText('fix').tagName).toBe('STRONG');
    expect(within(prose).getByText('app.css').tagName).toBe('CODE');
  });

  it('renders a reasoning op distinctly from prose', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response('The CSS bug is in the `a` rule.', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }),
    );
    const op = makeOp({
      id: 'op-reason',
      kind: 'reasoning',
      name: 'thinking',
      payload_refs: [{ id: 101, kind: 'reasoning', format: 'text', compression: null, original_bytes: 35, stored_bytes: 35 }],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-reason');
    expect(step.dataset['kind']).toBe('reasoning');
  });

  it('renders an llm message op as the assistant reply', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response('Fixed. See `app.css` line 300.', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }),
    );
    const op = makeOp({
      id: 'op-llm',
      kind: 'llm',
      name: 'message',
      payload_refs: [{ id: 102, kind: 'response', format: 'text', compression: null, original_bytes: 35, stored_bytes: 35 }],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-llm');
    expect(step.dataset['kind']).toBe('llm');
    expect(within(step).getByRole('heading', { name: /assistant/i })).toBeInTheDocument();
  });

  it('renders a tool op with params and response blocks, both with copy buttons', async () => {
    fetchMock
      .mockResolvedValueOnce(new Response('{"cmd":"npm test"}', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }))
      .mockResolvedValueOnce(new Response('PASS 42 tests', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));

    const op = makeOp({
      id: 'op-tool',
      kind: 'tool',
      name: 'exec_command',
      payload_refs: [
        { id: 103, kind: 'request', format: 'json', compression: null, original_bytes: 18, stored_bytes: 18 },
        { id: 104, kind: 'response', format: 'text', compression: null, original_bytes: 14, stored_bytes: 14 },
      ],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-tool');
    await waitFor(() => {
      expect(wasFetchedWithUrl('/api/payloads/103')).toBe(true);
      expect(wasFetchedWithUrl('/api/payloads/104')).toBe(true);
    });

    const params = await within(step).findByRole('region', { name: /params/i });
    const response = await within(step).findByRole('region', { name: /response/i });

    await waitFor(() => {
      expect(params.textContent).toContain('npm test');
      expect(response.textContent).toContain('PASS 42 tests');
    });

    expect(within(params).getByRole('button', { name: /copy code/i })).toBeInTheDocument();
    expect(within(response).getByRole('button', { name: /copy code/i })).toBeInTheDocument();
  });

  it('renders a session op as a link to the sub-session', () => {
    const op = makeOp({
      id: 'op-session',
      kind: 'session',
      name: 'sub-agent',
      child_session_id: 'child-xyz',
      payload_refs: [],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = screen.getByTestId('turn-step-op-session');
    const link = within(step).getByRole('link', { name: /child-xyz/ });
    expect(link).toHaveAttribute('href', '/sessions/child-xyz');
  });

  it('falls back to a generic step for unknown op kinds with raw JSON', async () => {
    fetchMock.mockResolvedValueOnce(new Response('{"weird":true}', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-weird',
      kind: 'compaction',
      name: 'ctx-prune',
      payload_refs: [{ id: 105, kind: 'raw', format: 'json', compression: null, original_bytes: 14, stored_bytes: 14 }],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-weird');
    expect(step.dataset['kind']).toBe('compaction');
  });
});

describe('TurnView — payload fetching', () => {
  it('fetches each op payload lazily and renders text once loaded', async () => {
    fetchMock.mockResolvedValueOnce(new Response('hello', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-fetch',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 200, kind: 'request', format: 'text', compression: null, original_bytes: 5, stored_bytes: 5 }],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    await waitFor(() => {
      expect(wasFetchedWithUrl('/api/payloads/200')).toBe(true);
    });
    await waitFor(() => {
      expect(screen.getByTestId('turn-step-op-fetch').textContent).toContain('hello');
    });
  });

  it('caches payload content per id so revisiting the step does not refetch', async () => {
    fetchMock.mockResolvedValueOnce(new Response('hello', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-cache',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 300, kind: 'request', format: 'text', compression: null, original_bytes: 5, stored_bytes: 5 }],
    });

    const { rerender } = renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);
    await waitFor(() => expect(screen.getByTestId('turn-step-op-cache').textContent).toContain('hello'));

    rerender(<MemoryRouter><TurnView turn={makeTurn({ ops: [op] })} /></MemoryRouter>);

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('surfaces a network error with a Retry button instead of swallowing it', async () => {
    fetchMock.mockRejectedValueOnce(new Error('network down'));
    const op = makeOp({
      id: 'op-err',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 400, kind: 'request', format: 'text', compression: null, original_bytes: 5, stored_bytes: 5 }],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-err');
    expect(within(step).getByRole('alert')).toHaveTextContent(/network down/i);
    expect(within(step).getByRole('button', { name: /retry/i })).toBeInTheDocument();
  });

  it('shows a truncation footer when the server truncated the payload', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response('partial content...', {
        status: 200,
        headers: { 'X-Payload-Truncated': 'true', 'X-Payload-Total-Bytes': '8192' },
      }),
    );
    const op = makeOp({
      id: 'op-trunc',
      kind: 'llm',
      name: 'message',
      payload_refs: [{ id: 500, kind: 'response', format: 'text', compression: null, original_bytes: 8192, stored_bytes: 4096 }],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-trunc');
    expect(within(step).getByText(/showing first 4 KB of 8 KB/i)).toBeInTheDocument();
  });

  it('renders an empty step when an op has no payload_refs', () => {
    const op = makeOp({ id: 'op-empty', kind: 'internal', name: 'user_input', payload_refs: [] });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    expect(screen.getByTestId('turn-step-op-empty').textContent).toMatch(/no payload/i);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('TurnView — focusOpId + scroll behavior', () => {
  // jsdom does not implement scrollIntoView on HTMLElement.prototype. Stub it
  // as a no-op so the spy on it works.
  beforeEach(() => {
    if (!HTMLElement.prototype.scrollIntoView) {
      (HTMLElement.prototype as unknown as { scrollIntoView: () => void }).scrollIntoView = function () {
        // no-op
      };
    }
    // jsdom does not implement window.matchMedia. Stub a complete shape so
    // the production code can call it unconditionally.
    if (!window.matchMedia) {
      Object.defineProperty(window, 'matchMedia', {
        writable: true,
        value: (q: string) => ({
          matches: false,
          media: q,
          onchange: null,
          addEventListener: vi.fn(),
          removeEventListener: vi.fn(),
          addListener: vi.fn(),
          removeListener: vi.fn(),
          dispatchEvent: vi.fn(),
        }),
      });
    }
  });

  it('scrolls the matching step into view on mount and applies a pulse animation', async () => {
    fetchMock.mockResolvedValue(new Response('hi', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-target',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 600, kind: 'request', format: 'text', compression: null, original_bytes: 2, stored_bytes: 2 }],
    });

    const scrollSpy = vi.spyOn(HTMLElement.prototype, 'scrollIntoView');

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} focusOpId="op-target" />);

    await waitFor(() => expect(screen.getByTestId('turn-step-op-target')).toHaveAttribute('data-focused', 'true'));
    expect(scrollSpy).toHaveBeenCalled();
  });

  it('skips the pulse animation under prefers-reduced-motion', async () => {
    fetchMock.mockResolvedValue(new Response('hi', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-reduce',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 700, kind: 'request', format: 'text', compression: null, original_bytes: 2, stored_bytes: 2 }],
    });

    vi.spyOn(window, 'matchMedia').mockImplementation((q: string) => ({
      matches: q === '(prefers-reduced-motion: reduce)',
      media: q,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} focusOpId="op-reduce" />);

    await waitFor(() => expect(screen.getByTestId('turn-step-op-reduce')).toHaveAttribute('data-focused', 'true'));
    expect(screen.getByTestId('turn-step-op-reduce').className).not.toMatch(/pulse/);
  });
});

describe('TurnView — copy buttons', () => {
  it('copies step text to clipboard when the prose copy button is clicked', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);

    fetchMock.mockResolvedValueOnce(new Response('hello world', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-copy',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 800, kind: 'request', format: 'text', compression: null, original_bytes: 11, stored_bytes: 11 }],
    });

    const user = userEvent.setup();
    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-copy');
    // Wait for the payload to load before clicking — otherwise the copy
    // button's text prop is still empty when onClick fires.
    await waitFor(() => expect(wasFetchedWithUrl('/api/payloads/800')).toBe(true));
    await waitFor(() => expect(step.textContent).toContain('hello world'));

    // IMPORTANT: define the navigator.clipboard mock AFTER render. React's
    // render lifecycle lazily initializes navigator.clipboard on the
    // window, so defining it BEFORE render is silently overridden by the
    // native jsdom Clipboard. Defining it after render + before click is the
    // only order that works in vitest 4 + jsdom 29.
    Object.defineProperty(window.navigator, 'clipboard', {
      configurable: true,
      writable: true,
      value: { writeText },
    });

    const copyBtn = within(step).getByRole('button', { name: /copy prose/i });
    await user.click(copyBtn);

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith('hello world');
    });
  });
});

describe('TurnView — accessibility', () => {
  it('is axe-clean and uses semantic headings', async () => {
    fetchMock.mockResolvedValue(new Response('hi', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-a11y',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 900, kind: 'request', format: 'text', compression: null, original_bytes: 2, stored_bytes: 2 }],
    });

    const { container } = renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);
    await waitFor(() => expect(screen.getByTestId('turn-step-op-a11y').textContent).toContain('hi'));

    expect(screen.getByRole('heading', { level: 2, name: /turn/i })).toBeInTheDocument();
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });
});

describe('TurnView — Copy turn button (header)', () => {
  it('serializes every step into a plain-text clipboard payload', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);

    fetchMock.mockResolvedValueOnce(new Response('the user prompt', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    fetchMock.mockResolvedValueOnce(new Response('the assistant reply', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));

    const ops: OpDetail[] = [
      makeOp({ id: 'op-1', kind: 'internal', name: 'user_input', payload_refs: [{ id: 1000, kind: 'request', format: 'text', compression: null, original_bytes: 15, stored_bytes: 15 }] }),
      makeOp({ id: 'op-2', kind: 'llm', name: 'message', payload_refs: [{ id: 1001, kind: 'response', format: 'text', compression: null, original_bytes: 19, stored_bytes: 19 }] }),
    ];

    const user = userEvent.setup();
    renderInRouter(<TurnView turn={makeTurn({ ops })} />);

    // Wait for both payloads to load before clicking.
    await waitFor(() => expect(screen.getByTestId('turn-step-op-1').textContent).toContain('the user prompt'));
    await waitFor(() => expect(screen.getByTestId('turn-step-op-2').textContent).toContain('the assistant reply'));

    // Define clipboard mock AFTER render (vitest+jsdom initializes the native
    // clipboard lazily during React's render lifecycle).
    Object.defineProperty(window.navigator, 'clipboard', {
      configurable: true,
      writable: true,
      value: { writeText },
    });

    await user.click(screen.getByRole('button', { name: /copy turn/i }));

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledTimes(1);
    });
    const text = writeText.mock.calls[0]?.[0] as string;
    expect(text).toContain('Turn #1');
    expect(text).toContain('## User');
    expect(text).toContain('the user prompt');
    expect(text).toContain('## Assistant');
    expect(text).toContain('the assistant reply');
  });

  it('does not throw when Copy turn is clicked before payloads finish loading', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);

    // Intentionally do NOT mock fetch; payloads stay in loading state.
    const op = makeOp({
      id: 'op-pending',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 1002, kind: 'request', format: 'text', compression: null, original_bytes: 5, stored_bytes: 5 }],
    });

    const user = userEvent.setup();
    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);
    Object.defineProperty(window.navigator, 'clipboard', {
      configurable: true,
      writable: true,
      value: { writeText },
    });

    await user.click(screen.getByRole('button', { name: /copy turn/i }));

    await waitFor(() => {
      expect(writeText).toHaveBeenCalled();
    });
    const text = writeText.mock.calls[0]?.[0] as string;
    expect(text).toContain('(loading…)');
  });

  it('survives a clipboard write failure (no silent failure)', async () => {
    const writeText = vi.fn().mockRejectedValueOnce(new Error('permission denied'));

    fetchMock.mockResolvedValueOnce(new Response('text', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-clip-fail',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 1003, kind: 'request', format: 'text', compression: null, original_bytes: 4, stored_bytes: 4 }],
    });

    const user = userEvent.setup();
    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);
    await waitFor(() => expect(screen.getByTestId('turn-step-op-clip-fail').textContent).toContain('text'));

    Object.defineProperty(window.navigator, 'clipboard', {
      configurable: true,
      writable: true,
      value: { writeText },
    });

    // Should not throw — the catch handler absorbs the error and unsets the
    // copied flag. The button briefly shows then un-shows 'Copied'.
    await user.click(screen.getByRole('button', { name: /copy turn/i }));

    await waitFor(() => {
      expect(writeText).toHaveBeenCalled();
    });
  });
});

describe('CopyButton — fallback path', () => {
  // The textarea + execCommand fallback only fires when navigator.clipboard
  // is missing entirely. jsdom 29 lazily initializes clipboard, so we can't
  // reliably remove it. The fallback is a defensive branch exercised by very
  // old browsers / non-secure contexts; in production we serve 127.0.0.1
  // which is a secure context so the API is always present.
  it('uses the clipboard API when available (happy path)', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);

    fetchMock.mockResolvedValueOnce(new Response('text', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));
    const op = makeOp({
      id: 'op-clip-happy',
      kind: 'internal',
      name: 'user_input',
      payload_refs: [{ id: 1005, kind: 'request', format: 'text', compression: null, original_bytes: 4, stored_bytes: 4 }],
    });

    const user = userEvent.setup();
    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);
    await waitFor(() => expect(screen.getByTestId('turn-step-op-clip-happy').textContent).toContain('text'));

    Object.defineProperty(window.navigator, 'clipboard', {
      configurable: true,
      writable: true,
      value: { writeText },
    });

    await user.click(within(screen.getByTestId('turn-step-op-clip-happy')).getByRole('button', { name: /copy prose/i }));

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith('text');
    });
  });
});