import type { QueryClient } from '@tanstack/react-query';
import { API_BASE, post, del } from './client';
import type {
  CreateSubscriptionResponse,
  DisconnectEvent,
  ResyncEvent,
  SessionChangedEvent,
  SourceStatusChangedEvent,
  StatsInvalidatedEvent,
  SubscriptionFilterRequest,
} from './types';

// SSE client. Lifecycle per sse-protocol.md:
//   1. POST /api/subscriptions { filter } -> { id, filter_normalized }
//   2. open EventSource(/api/events?sub=<id>) — the browser reconnects
//      automatically and replays Last-Event-ID; the server fills gaps or sends
//      `resync` when the buffer can't prove coverage.
//   3. DELETE /api/subscriptions/<id> on close (best-effort; the server also
//      expires the subscription 60 s after the last client disconnect).
//
// One subscription per active page (frontend-architecture.md §SSE Integration).
// SseConnection exposes typed listeners; connectSse wires the standard
// TanStack Query invalidations the SessionsList (and other pages) rely on.

/** ConnectionStatus surfaces the live indicator state (ui-pages.md §Realtime). */
export type ConnectionStatus = 'connecting' | 'open' | 'reconnecting' | 'closed';

/** Typed handlers for each server event plus connection-status transitions. */
export interface SseHandlers {
  onSessionChanged?: (e: SessionChangedEvent) => void;
  onStatsInvalidated?: (e: StatsInvalidatedEvent) => void;
  onSourceStatusChanged?: (e: SourceStatusChangedEvent) => void;
  onDisconnect?: (e: DisconnectEvent) => void;
  onResync?: (e: ResyncEvent) => void;
  onStatus?: (status: ConnectionStatus) => void;
  /**
   * onMalformedEvent receives a frame whose `data` is not valid JSON. Without
   * it, a malformed frame is console.warn'd. Either way the frame is surfaced
   * (AGENTS.md "no silent failures") and never tears down the stream.
   */
  onMalformedEvent?: (eventName: string, raw: string) => void;
}

/**
 * SseCanceledError is the sentinel openSubscription / connectSse reject with
 * when the caller's AbortSignal fires before a live connection can be handed
 * back. Callers (and TanStack Query) can detect it to treat cancellation
 * distinctly from a real failure. By the time it is thrown, any subscription
 * that was created has been best-effort DELETEd and no EventSource is left open.
 */
export class SseCanceledError extends Error {
  constructor() {
    super('SSE connection canceled before open');
    this.name = 'SseCanceledError';
  }
}

/** safeParse decodes an SSE data line, returning null on malformed JSON. */
function safeParse<T>(data: string): T | null {
  try {
    return JSON.parse(data) as T;
  } catch {
    return null;
  }
}

/**
 * SseConnection owns one subscription + its EventSource. Construct it with the
 * subscription id and the desired handlers, then call open(); call close() to
 * tear down (closes the stream and deletes the subscription). The class does
 * not create the subscription itself — use openSubscription() / connectSse()
 * which POST first, so a caller can inspect the normalized filter.
 */
export class SseConnection {
  readonly subscriptionId: string;
  private es: EventSource | null = null;
  private handlers: SseHandlers;
  private closed = false;

  constructor(subscriptionId: string, handlers: SseHandlers) {
    this.subscriptionId = subscriptionId;
    this.handlers = handlers;
  }

  /** open attaches the EventSource and registers the frame listeners. */
  open(): void {
    if (this.es !== null || this.closed) {
      return;
    }
    this.handlers.onStatus?.('connecting');
    const url = `${API_BASE}/events?sub=${encodeURIComponent(this.subscriptionId)}`;
    const es = new EventSource(url);
    this.es = es;

    es.onopen = () => {
      if (!this.closed) {
        this.handlers.onStatus?.('open');
      }
    };
    // EventSource auto-reconnects on transport error; surface that as
    // 'reconnecting' unless we have intentionally closed.
    es.onerror = () => {
      if (this.closed) {
        return;
      }
      this.handlers.onStatus?.(
        es.readyState === EventSource.CLOSED ? 'closed' : 'reconnecting',
      );
    };

    this.listen<SessionChangedEvent>('session_changed', (e) =>
      this.handlers.onSessionChanged?.(e),
    );
    this.listen<StatsInvalidatedEvent>('stats_invalidated', (e) =>
      this.handlers.onStatsInvalidated?.(e),
    );
    this.listen<SourceStatusChangedEvent>('source_status_changed', (e) =>
      this.handlers.onSourceStatusChanged?.(e),
    );
    this.listen<DisconnectEvent>('disconnect', (e) =>
      this.handlers.onDisconnect?.(e),
    );
    this.listen<ResyncEvent>('resync', (e) => this.handlers.onResync?.(e));
  }

  /**
   * listen registers a typed JSON listener for one named SSE event. A frame
   * that fails to parse is surfaced via onMalformedEvent (or console.warn) and
   * dropped — it never throws, so the stream survives for the next valid frame.
   */
  private listen<T>(name: string, fn: (e: T) => void): void {
    this.es?.addEventListener(name, (ev: MessageEvent<string>) => {
      const parsed = safeParse<T>(ev.data);
      if (parsed !== null) {
        fn(parsed);
        return;
      }
      if (this.handlers.onMalformedEvent) {
        this.handlers.onMalformedEvent(name, ev.data);
      } else {
        console.warn(`SSE: dropped malformed "${name}" frame`, ev.data);
      }
    });
  }

  /**
   * close stops the stream and deletes the subscription (best-effort — the
   * server expires it after 60 s regardless). Idempotent.
   */
  close(): void {
    if (this.closed) {
      return;
    }
    this.closed = true;
    this.es?.close();
    this.es = null;
    this.handlers.onStatus?.('closed');
    void del(`/subscriptions/${encodeURIComponent(this.subscriptionId)}`).catch(
      () => {
        /* subscription already gone or server shutting down — ignore */
      },
    );
  }
}

/** isAbortError narrows the DOMException fetch throws when its signal aborts. */
function isAbortError(err: unknown): boolean {
  return (
    err instanceof DOMException &&
    (err.name === 'AbortError' || err.code === DOMException.ABORT_ERR)
  );
}

/**
 * openSubscription creates a subscription for the given filter and returns an
 * unopened SseConnection plus the server's normalized filter. The caller calls
 * .open() to start streaming. Throws ApiError if the filter is rejected (400)
 * or the server is shutting down (503).
 *
 * Cancellation-safe: if `signal` aborts while the POST is in flight, the fetch
 * rejects and that abort is rethrown as SseCanceledError. If the POST resolves
 * but `signal` aborted in the meantime (the unmount/StrictMode race), the
 * just-created subscription is best-effort DELETEd and SseCanceledError is
 * thrown — the caller never receives a connection it would not tear down, and
 * no server-side subscription is left to expire on its own 60 s timer.
 */
export async function openSubscription(
  filter: SubscriptionFilterRequest,
  handlers: SseHandlers,
  signal?: AbortSignal,
): Promise<{ connection: SseConnection; normalized: CreateSubscriptionResponse }> {
  let normalized: CreateSubscriptionResponse;
  try {
    normalized = await post<CreateSubscriptionResponse>(
      '/subscriptions',
      { filter },
      signal,
    );
  } catch (err) {
    // A fetch aborted by `signal` is a cancellation, not a failure.
    if (signal?.aborted && isAbortError(err)) {
      throw new SseCanceledError();
    }
    throw err;
  }

  const connection = new SseConnection(normalized.id, handlers);
  // The POST resolved; if the caller aborted in the meantime, the subscription
  // exists server-side but the caller is gone. Tear it down rather than leak.
  if (signal?.aborted) {
    connection.close();
    throw new SseCanceledError();
  }
  return { connection, normalized };
}

/**
 * connectSse is the high-level helper pages use: it creates a subscription,
 * opens the stream, and wires the standard TanStack Query invalidations
 * (frontend-architecture.md §SSE Integration). Returns the live connection so
 * the caller can close() it on unmount / filter change. Extra handlers can be
 * layered on for page-specific UX (status indicator, fade-in animations).
 */
export async function connectSse(
  queryClient: QueryClient,
  filter: SubscriptionFilterRequest,
  extra: SseHandlers = {},
  signal?: AbortSignal,
): Promise<SseConnection> {
  const handlers: SseHandlers = {
    ...extra,
    onSessionChanged: (e) => {
      void queryClient.invalidateQueries({ queryKey: ['session', e.session_id] });
      void queryClient.invalidateQueries({ queryKey: ['sessions'] });
      extra.onSessionChanged?.(e);
    },
    onStatsInvalidated: (e) => {
      void queryClient.invalidateQueries({ queryKey: ['stats'] });
      extra.onStatsInvalidated?.(e);
    },
    onSourceStatusChanged: (e) => {
      void queryClient.invalidateQueries({ queryKey: ['sources'] });
      void queryClient.invalidateQueries({ queryKey: ['health'] });
      extra.onSourceStatusChanged?.(e);
    },
    onResync: (e) => {
      void queryClient.invalidateQueries();
      extra.onResync?.(e);
    },
  };
  // openSubscription guarantees: if `signal` aborted before/while the POST
  // resolved, it already cleaned up and threw SseCanceledError — so reaching
  // here means we hold a live, un-aborted subscription.
  const { connection } = await openSubscription(filter, handlers, signal);
  connection.open();
  // An abort that arrives AFTER open() still tears the stream down. close() is
  // idempotent, so this is safe alongside any caller-driven close().
  if (signal) {
    signal.addEventListener('abort', () => connection.close(), { once: true });
    // addEventListener does not fire for an already-aborted signal, so cover
    // the sync window between openSubscription's check and this registration.
    if (signal.aborted) {
      connection.close();
      throw new SseCanceledError();
    }
  }
  return connection;
}
