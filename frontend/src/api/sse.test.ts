import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { QueryClient } from '@tanstack/react-query';
import {
  connectSse,
  openSubscription,
  SseCanceledError,
  type SseHandlers,
} from './sse';

// sse.ts is the load-bearing, race-prone SSE client (sse-protocol.md). These
// tests drive it with a FAKE EventSource (a minimal mock assigned to
// globalThis.EventSource that captures the url + per-event listeners) and a
// mocked fetch for the POST /api/subscriptions + DELETE /api/subscriptions/:id
// calls. They verify: the subscribe→open handshake, that each of the five
// frames invalidates the right TanStack Query keys, that close() DELETEs +
// closes the stream, that a malformed frame is surfaced (not silently dropped),
// and the abort/unmount race (abort during/after the POST must leave no open
// EventSource and no undeleted subscription).

// ── Fake EventSource ─────────────────────────────────────────────────────────

interface CapturedListener {
  type: string;
  fn: (ev: MessageEvent<string>) => void;
}

/** FakeEventSource records its url + listeners and lets tests dispatch frames. */
class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];

  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;

  url: string;
  readyState = FakeEventSource.CONNECTING;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  listeners: CapturedListener[] = [];

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, fn: (ev: MessageEvent<string>) => void): void {
    this.listeners.push({ type, fn });
  }

  close(): void {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }

  /** dispatch delivers a frame to every listener registered for `type`. */
  dispatch(type: string, data: string): void {
    const ev = { data } as MessageEvent<string>;
    for (const l of this.listeners) {
      if (l.type === type) {
        l.fn(ev);
      }
    }
  }

  /** triggerOpen simulates the transport connecting. */
  triggerOpen(): void {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }

  /** triggerError simulates a transport error at the given readyState. */
  triggerError(readyState: number): void {
    this.readyState = readyState;
    this.onerror?.();
  }
}

function lastEs(): FakeEventSource {
  const es = FakeEventSource.instances.at(-1);
  if (!es) {
    throw new Error('no EventSource was constructed');
  }
  return es;
}

// ── Fetch mock ───────────────────────────────────────────────────────────────

interface FetchCall {
  url: string;
  method: string;
}

let fetchCalls: FetchCall[] = [];

/** okJson builds a minimal 200 Response whose json() resolves to body. */
function okJson(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    statusText: 'mock',
    headers: { get: () => null },
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

/** emptyOk builds a minimal 204 Response (DELETE). */
function emptyOk(): Response {
  return {
    ok: true,
    status: 204,
    statusText: 'no content',
    headers: { get: (h: string) => (h.toLowerCase() === 'content-length' ? '0' : null) },
    json: () => Promise.reject(new Error('no body')),
  } as unknown as Response;
}

/**
 * installFetch installs a fetch mock. The POST /subscriptions resolves with
 * subId; everything else (DELETE) resolves 204. An optional postGate lets a
 * test resolve the POST manually to drive the abort-during-POST race.
 */
function installFetch(opts: {
  subId: string;
  postGate?: Promise<void>;
  /** When set, the POST rejects with this error (e.g. a real AbortError). */
  postRejectsWith?: Error | DOMException;
}): void {
  fetchCalls = [];
  const mock = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET';
    fetchCalls.push({ url, method });
    if (method === 'POST') {
      if (opts.postGate) {
        await opts.postGate;
      }
      if (opts.postRejectsWith !== undefined) {
        throw opts.postRejectsWith;
      }
      return okJson({ id: opts.subId, filter_normalized: {} });
    }
    return emptyOk();
  });
  vi.stubGlobal('fetch', mock);
}

// ── Query client spy ─────────────────────────────────────────────────────────

interface InvalidateSpy {
  client: QueryClient;
  calls: Array<ReadonlyArray<unknown> | undefined>;
}

/** fakeQueryClient records invalidateQueries calls (the only method used). */
function fakeQueryClient(): InvalidateSpy {
  const calls: Array<ReadonlyArray<unknown> | undefined> = [];
  const client = {
    invalidateQueries: (arg?: { queryKey?: ReadonlyArray<unknown> }) => {
      calls.push(arg?.queryKey);
      return Promise.resolve();
    },
  } as unknown as QueryClient;
  return { client, calls };
}

/** keyInvalidated asserts an invalidateQueries call matched the given key. */
function keyInvalidated(
  spy: InvalidateSpy,
  key: ReadonlyArray<unknown>,
): boolean {
  return spy.calls.some(
    (k) => k !== undefined && JSON.stringify(k) === JSON.stringify(key),
  );
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal('EventSource', FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('connectSse handshake', () => {
  it('POSTs the filter then opens /api/events?sub=<id>', async () => {
    installFetch({ subId: 'sub-abc' });
    const spy = fakeQueryClient();
    const conn = await connectSse(spy.client, { status: ['failed'] });

    // POST hit /api/subscriptions with the filter in the body.
    const post = fetchCalls.find((c) => c.method === 'POST');
    expect(post?.url).toBe('/api/subscriptions');

    // EventSource opened with the returned id.
    expect(lastEs().url).toBe('/api/events?sub=sub-abc');
    expect(conn.subscriptionId).toBe('sub-abc');
  });

  it('encodes the subscription id in the events URL', async () => {
    installFetch({ subId: 'sub-a/b c' });
    const spy = fakeQueryClient();
    await connectSse(spy.client, {});
    expect(lastEs().url).toBe('/api/events?sub=sub-a%2Fb%20c');
  });
});

describe('frame → invalidation', () => {
  it('session_changed invalidates [session,id], [sessions] and the [logs,id] family', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    await connectSse(spy.client, {});
    lastEs().dispatch(
      'session_changed',
      JSON.stringify({ session_id: 's7', root_session_id: 'r1', ts: 1 }),
    );
    expect(keyInvalidated(spy, ['session', 's7'])).toBe(true);
    expect(keyInvalidated(spy, ['sessions'])).toBe(true);
    // Logs belong to the session: the open Logs tab (cached under
    // ['logs', id, severities]) must refresh on a session_changed frame. The
    // invalidation uses the ['logs', id] prefix so any severities sub-key matches.
    expect(keyInvalidated(spy, ['logs', 's7'])).toBe(true);
  });

  it('stats_invalidated invalidates [stats]', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    await connectSse(spy.client, {});
    lastEs().dispatch('stats_invalidated', JSON.stringify({ ts: 2 }));
    expect(keyInvalidated(spy, ['stats'])).toBe(true);
  });

  it('source_status_changed invalidates [sources] and [health]', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    await connectSse(spy.client, {});
    lastEs().dispatch(
      'source_status_changed',
      JSON.stringify({ source_id: 'src1', ts: 3 }),
    );
    expect(keyInvalidated(spy, ['sources'])).toBe(true);
    expect(keyInvalidated(spy, ['health'])).toBe(true);
  });

  it('keepalive (no listener) invalidates nothing', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    await connectSse(spy.client, {});
    // keepalive is a comment line, never a real frame; nothing is registered.
    expect(lastEs().listeners.some((l) => l.type === 'keepalive')).toBe(false);
    expect(spy.calls.length).toBe(0);
  });

  it('disconnect frame fires the onDisconnect handler', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    const onDisconnect = vi.fn();
    await connectSse(spy.client, {}, { onDisconnect });
    lastEs().dispatch(
      'disconnect',
      JSON.stringify({ reason: 'server_shutdown', retry_after_ms: 2000 }),
    );
    expect(onDisconnect).toHaveBeenCalledWith({
      reason: 'server_shutdown',
      retry_after_ms: 2000,
    });
  });

  it('resync invalidates everything (no key)', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    await connectSse(spy.client, {});
    lastEs().dispatch('resync', JSON.stringify({ reason: 'buffer_overflow' }));
    // resync calls invalidateQueries() with no arg → key undefined.
    expect(spy.calls.some((k) => k === undefined)).toBe(true);
  });
});

describe('connection status transitions', () => {
  it('emits connecting → open and reconnecting/closed on transport events', async () => {
    installFetch({ subId: 'sub-s' });
    const spy = fakeQueryClient();
    const statuses: string[] = [];
    await connectSse(spy.client, {}, { onStatus: (s) => statuses.push(s) });
    const es = lastEs();

    expect(statuses).toContain('connecting');
    es.triggerOpen();
    expect(statuses).toContain('open');
    es.triggerError(FakeEventSource.CONNECTING); // transient → reconnecting
    expect(statuses).toContain('reconnecting');
    es.triggerError(FakeEventSource.CLOSED); // terminal → closed
    expect(statuses).toContain('closed');
  });

  it('suppresses status events after intentional close()', async () => {
    installFetch({ subId: 'sub-s2' });
    const spy = fakeQueryClient();
    const statuses: string[] = [];
    const conn = await connectSse(spy.client, {}, { onStatus: (s) => statuses.push(s) });
    const es = lastEs();
    conn.close();
    statuses.length = 0; // ignore the 'closed' from close()
    // Late transport callbacks after close are ignored.
    es.triggerOpen();
    es.triggerError(FakeEventSource.CLOSED);
    expect(statuses).toEqual([]);
  });
});

describe('close()', () => {
  it('DELETEs the subscription and closes the EventSource', async () => {
    installFetch({ subId: 'sub-x' });
    const spy = fakeQueryClient();
    const conn = await connectSse(spy.client, {});
    const es = lastEs();
    conn.close();
    expect(es.closed).toBe(true);
    // DELETE /api/subscriptions/sub-x issued (best-effort).
    await vi.waitFor(() => {
      expect(
        fetchCalls.some(
          (c) => c.method === 'DELETE' && c.url === '/api/subscriptions/sub-x',
        ),
      ).toBe(true);
    });
  });

  it('is idempotent (second close() does not DELETE twice)', async () => {
    installFetch({ subId: 'sub-x' });
    const spy = fakeQueryClient();
    const conn = await connectSse(spy.client, {});
    conn.close();
    await vi.waitFor(() => {
      expect(fetchCalls.filter((c) => c.method === 'DELETE').length).toBe(1);
    });
    conn.close();
    // Still exactly one DELETE.
    expect(fetchCalls.filter((c) => c.method === 'DELETE').length).toBe(1);
  });
});

describe('malformed frame (no silent failures)', () => {
  it('routes a bad frame to onMalformedEvent and keeps the stream alive', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    const onMalformedEvent = vi.fn();
    const handlers: SseHandlers = { onMalformedEvent };
    await connectSse(spy.client, {}, handlers);
    const es = lastEs();

    // Malformed JSON for session_changed.
    expect(() => es.dispatch('session_changed', '{not json')).not.toThrow();
    expect(onMalformedEvent).toHaveBeenCalledWith('session_changed', '{not json');
    // No invalidation happened for the bad frame.
    expect(keyInvalidated(spy, ['sessions'])).toBe(false);

    // A subsequent valid frame is still processed → stream survived.
    es.dispatch(
      'session_changed',
      JSON.stringify({ session_id: 's1', root_session_id: 'r', ts: 9 }),
    );
    expect(keyInvalidated(spy, ['sessions'])).toBe(true);
  });

  it('console.warns when no onMalformedEvent handler is supplied', async () => {
    installFetch({ subId: 'sub-1' });
    const spy = fakeQueryClient();
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    await connectSse(spy.client, {});
    expect(() => lastEs().dispatch('stats_invalidated', 'oops')).not.toThrow();
    expect(warn).toHaveBeenCalled();
    expect(warn.mock.calls[0]?.join(' ')).toContain('stats_invalidated');
  });
});

describe('abort / unmount race (FIX 1)', () => {
  it('already-aborted signal: no EventSource opens, subscription is DELETEd', async () => {
    installFetch({ subId: 'sub-race' });
    const spy = fakeQueryClient();
    const controller = new AbortController();
    controller.abort();

    await expect(
      connectSse(spy.client, {}, {}, controller.signal),
    ).rejects.toBeInstanceOf(SseCanceledError);

    // No live EventSource (either none constructed, or the one constructed is closed).
    const open = FakeEventSource.instances.filter((es) => !es.closed);
    expect(open.length).toBe(0);
    // Subscription cleaned up if the POST resolved.
    await vi.waitFor(() => {
      const deleted = fetchCalls.some((c) => c.method === 'DELETE');
      const posted = fetchCalls.some((c) => c.method === 'POST');
      // If the POST went out, the sub must have been DELETEd.
      expect(!posted || deleted).toBe(true);
    });
  });

  it('abort while the POST is in flight: connection torn down, no open stream', async () => {
    let releasePost: () => void = () => {};
    const postGate = new Promise<void>((resolve) => {
      releasePost = resolve;
    });
    installFetch({ subId: 'sub-inflight', postGate });
    const spy = fakeQueryClient();
    const controller = new AbortController();

    const p = connectSse(spy.client, {}, {}, controller.signal);
    // Abort before the POST resolves, then let it resolve.
    controller.abort();
    releasePost();

    await expect(p).rejects.toBeInstanceOf(SseCanceledError);
    const open = FakeEventSource.instances.filter((es) => !es.closed);
    expect(open.length).toBe(0);
  });

  it('POST rejected by an aborted signal surfaces as SseCanceledError', async () => {
    const abortErr = new DOMException('aborted', 'AbortError');
    installFetch({ subId: 'sub-rej', postRejectsWith: abortErr });
    const spy = fakeQueryClient();
    const controller = new AbortController();
    controller.abort();
    await expect(
      connectSse(spy.client, {}, {}, controller.signal),
    ).rejects.toBeInstanceOf(SseCanceledError);
    // No EventSource constructed at all (POST never resolved).
    expect(FakeEventSource.instances.length).toBe(0);
  });

  it('a non-abort POST failure propagates unchanged (not SseCanceledError)', async () => {
    const boom = new Error('network down');
    installFetch({ subId: 'sub-boom', postRejectsWith: boom });
    const spy = fakeQueryClient();
    const controller = new AbortController(); // never aborted
    await expect(
      connectSse(spy.client, {}, {}, controller.signal),
    ).rejects.toBe(boom);
  });

  it('covers the sync abort window between the POST check and listener registration', async () => {
    installFetch({ subId: 'sub-sync' });
    const spy = fakeQueryClient();
    // A signal that is NOT aborted when openSubscription checks it, but flips to
    // aborted by the time connectSse re-checks after registering the listener —
    // the exact race the post-registration guard defends against. (Real signals
    // do not fire addEventListener for an already-aborted signal.)
    let reads = 0;
    const flakySignal = {
      get aborted() {
        reads += 1;
        return reads > 1; // false on the openSubscription check, true after.
      },
      addEventListener: () => {},
      removeEventListener: () => {},
    } as unknown as AbortSignal;

    await expect(
      connectSse(spy.client, {}, {}, flakySignal),
    ).rejects.toBeInstanceOf(SseCanceledError);
    // The stream that was opened got torn down by the guard.
    const open = FakeEventSource.instances.filter((es) => !es.closed);
    expect(open.length).toBe(0);
  });

  it('abort AFTER open: the one-shot abort listener closes the stream', async () => {
    installFetch({ subId: 'sub-late' });
    const spy = fakeQueryClient();
    const controller = new AbortController();
    await connectSse(spy.client, {}, {}, controller.signal);
    const es = lastEs();
    expect(es.closed).toBe(false);

    controller.abort();
    expect(es.closed).toBe(true);
    await vi.waitFor(() => {
      expect(
        fetchCalls.some(
          (c) =>
            c.method === 'DELETE' && c.url === '/api/subscriptions/sub-late',
        ),
      ).toBe(true);
    });
  });
});

describe('openSubscription', () => {
  it('returns an unopened connection plus the normalized filter', async () => {
    installFetch({ subId: 'sub-open' });
    const { connection, normalized } = await openSubscription({}, {});
    // No EventSource yet — caller must call .open().
    expect(FakeEventSource.instances.length).toBe(0);
    expect(connection.subscriptionId).toBe('sub-open');
    expect(normalized.id).toBe('sub-open');
  });

  it('honors an already-aborted signal without opening a stream', async () => {
    installFetch({ subId: 'sub-open2' });
    const controller = new AbortController();
    controller.abort();
    await expect(
      openSubscription({}, {}, controller.signal),
    ).rejects.toBeInstanceOf(SseCanceledError);
    const open = FakeEventSource.instances.filter((es) => !es.closed);
    expect(open.length).toBe(0);
  });
});
