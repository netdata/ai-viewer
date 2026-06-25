import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within, type RenderOptions } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
import { MemoryRouter } from 'react-router-dom';
import type { OpDetail, PayloadRef, TurnDetail } from '../../api/types';
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
    tokens_cache_read: 0,
    tokens_cache_write: 0,
    cost_usd: 0.42,
    bytes_in: 0,
    bytes_out: 0,
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
    tokens_cache_read: 0,
    tokens_cache_write: 0,
    cost_usd: 0.42,
    op_count: 1,
    ops: [],
    ...over,
  };
}

function payload(over: Partial<PayloadRef> & { id: number; kind: string; artifact_class: string }): PayloadRef {
  return {
    op_id: 'op-default',
    format: 'text',
    compression: null,
    original_bytes: null,
    stored_bytes: null,
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
      payload_refs: [payload({ id: 100, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 30, stored_bytes: 30 })],
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
      payload_refs: [payload({ id: 101, kind: 'llm_reasoning', artifact_class: 'reasoning_text', format: 'text', original_bytes: 35, stored_bytes: 35 })],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-reason');
    expect(step.dataset['kind']).toBe('reasoning');
    const reasoning = await within(step).findByRole('region', { name: /reasoning/i });
    expect(reasoning.textContent).toContain('The CSS bug is in the');
  });

  it('renders an llm message op as the assistant reply', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response('Fixed. See `app.css` line 300.', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }),
    );
    const op = makeOp({
      id: 'op-llm',
      kind: 'llm',
      name: 'message',
      payload_refs: [payload({ id: 102, kind: 'llm_response', artifact_class: 'llm_response', format: 'text', original_bytes: 35, stored_bytes: 35 })],
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
        payload({ id: 103, kind: 'tool_request', artifact_class: 'tool_request', format: 'json', original_bytes: 18, stored_bytes: 18 }),
        payload({ id: 104, kind: 'tool_response', artifact_class: 'tool_response', format: 'text', original_bytes: 14, stored_bytes: 14 }),
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

  it('selects tool params and response by artifact class, not array position', async () => {
    fetchMock
      .mockResolvedValueOnce(new Response('{"cmd":"npm test"}', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }))
      .mockResolvedValueOnce(new Response('PASS 42 tests', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }));

    const op = makeOp({
      id: 'op-tool-semantic',
      kind: 'tool',
      name: 'exec_command',
      payload_refs: [
        payload({ id: 204, kind: 'tool_response', artifact_class: 'tool_response', format: 'text', original_bytes: 14, stored_bytes: 14 }),
        payload({ id: 203, kind: 'tool_request', artifact_class: 'tool_request', format: 'json', original_bytes: 18, stored_bytes: 18 }),
      ],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-tool-semantic');
    await waitFor(() => {
      expect(wasFetchedWithUrl('/api/payloads/203')).toBe(true);
      expect(wasFetchedWithUrl('/api/payloads/204')).toBe(true);
    });
    const params = await within(step).findByRole('region', { name: /params/i });
    const response = await within(step).findByRole('region', { name: /response/i });
    await waitFor(() => {
      expect(params.textContent).toContain('npm test');
      expect(response.textContent).toContain('PASS 42 tests');
    });
  });

  it('renders SDK response payloads through the assistant path with an SDK badge', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response('SDK response text', { status: 200, headers: { 'X-Payload-Truncated': 'false' } }),
    );
    const op = makeOp({
      id: 'op-sdk',
      kind: 'llm',
      name: 'message',
      payload_refs: [payload({ id: 205, kind: 'sdk_response', artifact_class: 'llm_sdk_response', format: 'text', original_bytes: 17, stored_bytes: 17 })],
    });

    renderInRouter(<TurnView turn={makeTurn({ ops: [op] })} />);

    const step = await screen.findByTestId('turn-step-op-sdk');
    expect(within(step).getByText('SDK')).toBeInTheDocument();
    await waitFor(() => {
      expect(step.textContent).toContain('SDK response text');
    });
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
      payload_refs: [payload({ id: 105, kind: 'log', artifact_class: 'log', format: 'json', original_bytes: 14, stored_bytes: 14 })],
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
      payload_refs: [payload({ id: 200, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 5, stored_bytes: 5 })],
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
      payload_refs: [payload({ id: 300, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 5, stored_bytes: 5 })],
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
      payload_refs: [payload({ id: 400, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 5, stored_bytes: 5 })],
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
      payload_refs: [payload({ id: 500, kind: 'llm_response', artifact_class: 'llm_response', format: 'text', original_bytes: 8192, stored_bytes: 4096 })],
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
      payload_refs: [payload({ id: 600, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 2, stored_bytes: 2 })],
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
      payload_refs: [payload({ id: 700, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 2, stored_bytes: 2 })],
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
      payload_refs: [payload({ id: 800, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 11, stored_bytes: 11 })],
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
      payload_refs: [payload({ id: 900, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 2, stored_bytes: 2 })],
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
      makeOp({ id: 'op-1', kind: 'internal', name: 'user_input', payload_refs: [payload({ id: 1000, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 15, stored_bytes: 15 })] }),
      makeOp({ id: 'op-2', kind: 'llm', name: 'message', payload_refs: [payload({ id: 1001, kind: 'llm_response', artifact_class: 'llm_response', format: 'text', original_bytes: 19, stored_bytes: 19 })] }),
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
      payload_refs: [payload({ id: 1002, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 5, stored_bytes: 5 })],
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
      payload_refs: [payload({ id: 1003, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 4, stored_bytes: 4 })],
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
      payload_refs: [payload({ id: 1005, kind: 'llm_request', artifact_class: 'llm_request', format: 'text', original_bytes: 4, stored_bytes: 4 })],
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

// ---------------------------------------------------------------------------
// Per-step metadata row (SOW-0090 chunk 8)
// ---------------------------------------------------------------------------

describe('TurnView — step metadata row', () => {
  it('renders step index, elapsed since turn start, and wall-clock time', () => {
    const turnStartTs = 1_700_000_000_000_000;
    const ops = [
      makeOp({ id: 'op-meta-a', kind: 'tool', name: 'read_file', start_ts: turnStartTs + 1_200_000, payload_refs: [] }),
      makeOp({ id: 'op-meta-b', kind: 'tool', name: 'read_file', start_ts: turnStartTs + 5_500_000, payload_refs: [] }),
      makeOp({ id: 'op-meta-c', kind: 'llm', name: 'message', start_ts: turnStartTs + 1_234_567, payload_refs: [] }),
    ];
    const turn = makeTurn({ start_ts: turnStartTs, ops });
    renderInRouter(<TurnView turn={turn} />);

    // Each step shows its 1-based index and total.
    expect(within(screen.getByTestId('turn-step-op-meta-a')).getByText('1/3')).toBeInTheDocument();
    expect(within(screen.getByTestId('turn-step-op-meta-b')).getByText('2/3')).toBeInTheDocument();
    expect(within(screen.getByTestId('turn-step-op-meta-c')).getByText('3/3')).toBeInTheDocument();

    // Elapsed since turn start: +1.2s, +5.5s, +1.2s (1.234s rounds down per Math.floor semantics).
    expect(within(screen.getByTestId('turn-step-op-meta-a')).getByText('+1.2s')).toBeInTheDocument();
    expect(within(screen.getByTestId('turn-step-op-meta-b')).getByText('+5.5s')).toBeInTheDocument();
  });

  it('renders the op-id badge with the first 8 chars + a copy button', () => {
    const op = makeOp({ id: 'abcdef0123456789extra', kind: 'tool', name: 'read_file', payload_refs: [] });
    const turn = makeTurn({ ops: [op] });
    renderInRouter(<TurnView turn={turn} />);

    const step = screen.getByTestId('turn-step-abcdef0123456789extra');
    const badge = within(step).getByRole('button', { name: /copy op id/i });
    expect(badge).toBeInTheDocument();
    expect(within(badge).getByText('abcdef01')).toBeInTheDocument();
    expect(badge.getAttribute('title')).toBe('abcdef0123456789extra');
  });

  it('copies the full op id (not the short form) when the badge is clicked', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    });

    const op = makeOp({ id: '21cd490e83b974aee895cbd88d5bfbfd', kind: 'tool', name: 'read_file', payload_refs: [] });
    const turn = makeTurn({ ops: [op] });
    renderInRouter(<TurnView turn={turn} />);

    const badge = within(screen.getByTestId('turn-step-21cd490e83b974aee895cbd88d5bfbfd')).getByRole('button', { name: /copy op id/i });
    await user.click(badge);

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith('21cd490e83b974aee895cbd88d5bfbfd');
    });
  });

  it('renders wall-clock time in UTC with Z suffix', () => {
    // 2026-06-21T11:42:18Z = Date.UTC(2026, 5, 21, 11, 42, 18) * 1000 µs
    const tsUs = Date.UTC(2026, 5, 21, 11, 42, 18) * 1000;
    const op = makeOp({ id: 'op-clock', kind: 'llm', name: 'message', start_ts: tsUs, payload_refs: [] });
    const turn = makeTurn({ start_ts: tsUs, ops: [op] });
    renderInRouter(<TurnView turn={turn} />);

    expect(within(screen.getByTestId('turn-step-op-clock')).getByText('11:42:18Z')).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Step filter (SOW-0090 chunk 9)
// ---------------------------------------------------------------------------

describe('TurnView — step filter', () => {
  const mixedTurn = (): TurnDetail =>
    makeTurn({
      ops: [
        makeOp({ id: 'op-u1', kind: 'internal', name: 'user_input', payload_refs: [] }),
        makeOp({ id: 'op-r1', kind: 'reasoning', name: '', payload_refs: [] }),
        makeOp({ id: 'op-a1', kind: 'llm', name: 'message', payload_refs: [] }),
        makeOp({ id: 'op-t1', kind: 'tool', name: 'read_file', payload_refs: [] }),
        makeOp({ id: 'op-t2', kind: 'tool', name: 'write_file', payload_refs: [] }),
        makeOp({ id: 'op-r2', kind: 'reasoning', name: '', payload_refs: [] }),
      ],
    });

  it("renders every step when filter is 'all' (default)", () => {
    renderInRouter(<TurnView turn={mixedTurn()} />);
    for (const id of ['op-u1', 'op-r1', 'op-a1', 'op-t1', 'op-t2', 'op-r2']) {
      expect(screen.getByTestId(`turn-step-${id}`)).toBeInTheDocument();
    }
  });

  it("filters to tool calls only when 'tool' is active", async () => {
    const user = userEvent.setup();
    renderInRouter(<TurnView turn={mixedTurn()} />);
    await user.click(screen.getByRole('tab', { name: /^tool/i }));
    expect(screen.getByTestId('turn-step-op-t1')).toBeInTheDocument();
    expect(screen.getByTestId('turn-step-op-t2')).toBeInTheDocument();
    expect(screen.queryByTestId('turn-step-op-r1')).toBeNull();
    expect(screen.queryByTestId('turn-step-op-u1')).toBeNull();
    expect(screen.queryByTestId('turn-step-op-a1')).toBeNull();
  });

  it("filters to reasoning only when 'reasoning' is active", async () => {
    const user = userEvent.setup();
    renderInRouter(<TurnView turn={mixedTurn()} />);
    await user.click(screen.getByRole('tab', { name: /reasoning/i }));
    expect(screen.getByTestId('turn-step-op-r1')).toBeInTheDocument();
    expect(screen.getByTestId('turn-step-op-r2')).toBeInTheDocument();
    expect(screen.queryByTestId('turn-step-op-t1')).toBeNull();
  });

  it("filters to user prompts only when 'user' is active", async () => {
    const user = userEvent.setup();
    renderInRouter(<TurnView turn={mixedTurn()} />);
    await user.click(screen.getByRole('tab', { name: /user/i }));
    expect(screen.getByTestId('turn-step-op-u1')).toBeInTheDocument();
    expect(screen.queryByTestId('turn-step-op-r1')).toBeNull();
  });

  it("preserves the original step index when filtering", async () => {
    const user = userEvent.setup();
    renderInRouter(<TurnView turn={mixedTurn()} />);
    await user.click(screen.getByRole('tab', { name: /^tool/i }));
    // Array order is [op-u1, op-r1, op-a1, op-t1, op-t2, op-r2] — op-t1
    // is at 0-based index 3 (so 4/6), op-t2 at index 4 (so 5/6). The
    // step index stays tied to the ORIGINAL position so 'step 4' is a
    // stable reference after toggling the filter.
    expect(within(screen.getByTestId('turn-step-op-t1')).getByText('4/6')).toBeInTheDocument();
    expect(within(screen.getByTestId('turn-step-op-t2')).getByText('5/6')).toBeInTheDocument();
  });

  it('honors the initialStepKindFilter prop', () => {
    renderInRouter(<TurnView turn={mixedTurn()} initialStepKindFilter="reasoning" />);
    expect(screen.getByTestId('turn-step-op-r1')).toBeInTheDocument();
    expect(screen.getByTestId('turn-step-op-r2')).toBeInTheDocument();
    expect(screen.queryByTestId('turn-step-op-t1')).toBeNull();
  });

  it('invokes onStepKindFilterChange when a pill is clicked', async () => {
    const _user = userEvent.setup();
    const onChange = vi.fn();
    renderInRouter(<TurnView turn={mixedTurn()} onStepKindFilterChange={onChange} />);
    await _user.click(screen.getByRole('tab', { name: /tool/i }));
    expect(onChange).toHaveBeenCalledWith('tool');
  });

  it('renders the empty-state when no steps match the filter', () => {
    // A turn with no session forks → filter 'session' should show empty.
    renderInRouter(<TurnView turn={mixedTurn()} initialStepKindFilter="session" />);
    expect(screen.getByRole('status')).toHaveTextContent(/no steps of this kind/i);
  });

  it('renders the count badges on every pill', () => {
    renderInRouter(<TurnView turn={mixedTurn()} />);
    const allPill = screen.getByRole('tab', { name: /all/i });
    expect(within(allPill).getByText('6')).toBeInTheDocument();
    const toolPill = screen.getByRole('tab', { name: /^tool/i });
    expect(within(toolPill).getByText('2')).toBeInTheDocument();
  });
});
