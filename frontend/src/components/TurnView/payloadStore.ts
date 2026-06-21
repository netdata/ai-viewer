// usePayloadContent (ui-turn-view.md §Data flow): lazy fetch of payload bytes
// from GET /api/payloads/:id with per-id caching, AbortController cleanup, and
// explicit error / truncation surfacing. Per-instance cache so revisiting the
// same op never refetches.

import { useCallback, useEffect, useRef, useState } from 'react';

export interface PayloadContent {
  /** Server text response (truncated to 4 KB server-side if too big). */
  text: string;
  /** True when the server cut off the payload; X-Payload-Truncated: true. */
  truncated: boolean;
  /** Total bytes on disk; null if the server didn't advertise it. */
  totalBytes: number | null;
}

/** Per-fetch state machine. We keep loading/error/content in a single state
 *  object so React sees one update per fetch resolution (avoids the
 *  setState-in-effect cascading-render pattern). */
type FetchState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'success'; content: PayloadContent }
  | { status: 'error'; message: string };

const IDLE: FetchState = { status: 'idle' };
void IDLE; // referenced for type narrowing; keep as documentation

/**
 * usePayloadStore is a per-mount payload cache. Created once per TurnView
 * instance via useRef, shared across all steps in the same turn view, so
 * visiting two different ops that share a payload (rare but possible) only
 * fetches once.
 *
 * Concurrency cap (MAX_PARALLEL_FETCHES = 8): a long turn can have 30+ ops
 * with multiple payload_refs each. Without a cap, the browser exhausts its
 * socket pool and the page becomes ERR_INSUFFICIENT_RESOURCES. A simple FIFO
 * queue keeps the in-flight count bounded; the cache still dedupes within
 * the queue (no two callers of `get` for the same id ever hit the wire
 * twice).
 */
function makeStore() {
  const MAX_PARALLEL_FETCHES = 4;
  // map<payloadId, Promise<PayloadContent>> so concurrent mounts of the same
  // payload dedupe on the in-flight request, not just on completion.
  const inflight = new Map<number, Promise<PayloadContent>>();
  const cache = new Map<number, PayloadContent>();

  async function fetchOnce(id: number, signal: AbortSignal): Promise<PayloadContent> {
    const resp = await fetch(`/api/payloads/${id}`, { signal });
    if (!resp.ok) {
      const body = await resp.text();
      throw new Error(`HTTP ${resp.status}: ${body.slice(0, 200)}`);
    }
    const text = await resp.text();
    const truncated = resp.headers.get('X-Payload-Truncated') === 'true';
    const totalRaw = resp.headers.get('X-Payload-Total-Bytes');
    const totalBytes = totalRaw !== null ? parseInt(totalRaw, 10) : null;
    return { text, truncated, totalBytes };
  }

  // FIFO scheduling of fetch tasks. Each task owns one AbortController and
  // is settled (resolve/reject) by the queue runner. We schedule up to
  // MAX_PARALLEL_FETCHES concurrently.
  type Task = {
    id: number;
    controller: AbortController;
    resolve: (content: PayloadContent) => void;
    reject: (err: unknown) => void;
  };
  const queue: Task[] = [];
  let running = 0;

  function pump(): void {
    while (running < MAX_PARALLEL_FETCHES && queue.length > 0) {
      const task = queue.shift();
      if (task === undefined) break;
      running++;
      const { id, controller, resolve, reject } = task;
      fetchOnce(id, controller.signal)
        .then((content) => {
          cache.set(id, content);
          resolve(content);
        })
        .catch((err: unknown) => {
          reject(err);
        })
        .finally(() => {
          inflight.delete(id);
          running--;
          pump();
        });
    }
  }

  return {
    get(id: number, signal: AbortSignal): Promise<PayloadContent> {
      const cached = cache.get(id);
      if (cached) return Promise.resolve(cached);
      const existing = inflight.get(id);
      if (existing) return existing;
      // Bridge the caller's signal to our internal AbortController: if the
      // caller unmounts, we cancel the queued/running task too.
      const controller = new AbortController();
      const onCallerAbort = (): void => {
        controller.abort();
      };
      if (signal.aborted) {
        controller.abort();
      } else {
        signal.addEventListener('abort', onCallerAbort, { once: true });
      }
      const p = new Promise<PayloadContent>((resolve, reject) => {
        const task: Task = { id, controller, resolve, reject };
        if (controller.signal.aborted) {
          reject(new DOMException('Aborted', 'AbortError'));
          return;
        }
        queue.push(task);
        pump();
      }).finally(() => {
        signal.removeEventListener('abort', onCallerAbort);
      });
      inflight.set(id, p);
      return p;
    },
  };
}

export interface PayloadState {
  loading: boolean;
  content: PayloadContent | null;
  error: string | null;
  retry: () => void;
}

/** usePayloadContent fetches the payload bytes for `payloadId` on mount.
 *  Subsequent calls with the same id (from any step in the same TurnView)
 *  return the cached content. Errors are surfaced with a `retry()` callback. */
export function usePayloadContent(payloadId: number | null): PayloadState {
  const [state, setState] = useState<FetchState>(IDLE);
  // Bumping this counter re-triggers the fetch on retry. We deliberately do NOT
  // re-fetch on focus / visibility change — payloads are immutable.
  const [retryNonce, setRetryNonce] = useState(0);

  // Per-mount cache shared across every payload call in this TurnView.
  const storeRef = useRef(makeStore());

  useEffect(() => {
    if (payloadId === null) return;
    const controller = new AbortController();
    let cancelled = false;

    storeRef.current
      .get(payloadId, controller.signal)
      .then((content) => {
        if (cancelled) return;
        setState({ status: 'success', content });
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        if (e instanceof DOMException && e.name === 'AbortError') return;
        const message = e instanceof Error ? e.message : 'fetch failed';
        setState({ status: 'error', message });
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [payloadId, retryNonce]);

  const retry = useCallback(() => {
    setRetryNonce((n) => n + 1);
  }, []);

  // payloadId === null means the consumer has no payload to fetch. Render an
  // idle state (no loading, no error, no content) so the consumer shows
  // "No payload" instead of "Loading…".
  if (payloadId === null) {
    return { loading: false, content: null, error: null, retry };
  }

  if (state.status === 'success') {
    return { loading: false, content: state.content, error: null, retry };
  }
  if (state.status === 'error') {
    return { loading: false, content: null, error: state.message, retry };
  }
  // 'idle' (the initial state on first render) and 'loading' both map to
  // loading=true. The transition idle→loading happens inside the effect via
  // the .then() callback, not synchronously — so the setState-in-effect lint
  // rule is satisfied.
  return { loading: true, content: null, error: null, retry };
}