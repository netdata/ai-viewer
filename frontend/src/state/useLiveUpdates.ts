import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { connectSse, SseCanceledError, type SseConnection } from '../api/sse';
import type { SubscriptionFilterRequest } from '../api/types';

// useLiveUpdates owns the SSE connection lifecycle for one mounted view
// (frontend-architecture.md §useLiveUpdates). Pages never call connectSse
// directly — they call this hook with the subscription filter for the view, and
// the SSE client (api/sse.ts) maps incoming frames → queryClient.invalidateQueries,
// which refetches the affected queries. The hook's sole job is lifecycle:
//
//   - exactly ONE active subscription per mounted view at any time;
//   - re-subscribe on filter change (the effect re-runs when the serialized
//     filter dependency changes);
//   - abort-safe teardown on unmount / filter change / StrictMode double-invoke.
//
// The effect depends on a STABLE string serialization of the filter rather than
// the object identity, so a caller passing a fresh object literal with the same
// contents does not churn the subscription every render.

/**
 * useLiveUpdates connects an SSE subscription for `filter` and keeps the
 * relevant TanStack Query caches live. It returns nothing observable — the
 * invalidation wiring lives in connectSse.
 */
export function useLiveUpdates(filter: SubscriptionFilterRequest): void {
  const queryClient = useQueryClient();
  // Stable dependency: re-subscribe only when the filter CONTENT changes.
  const filterKey = JSON.stringify(filter);

  useEffect(() => {
    const controller = new AbortController();
    let connection: SseConnection | null = null;
    let disposed = false;

    void connectSse(queryClient, filter, {}, controller.signal)
      .then((conn) => {
        connection = conn;
        // If teardown already ran before the POST resolved, close immediately.
        // (connectSse also tears down on an aborted signal, but close() is
        // idempotent so this is a harmless belt-and-suspenders guard.)
        if (disposed) {
          conn.close();
        }
      })
      .catch((err: unknown) => {
        // An abort during connect is a cancellation, not a failure — swallow it.
        if (err instanceof SseCanceledError) {
          return;
        }
        // Any real failure is surfaced, never silently dropped (AGENTS.md).
        console.warn('useLiveUpdates: SSE connect failed', err);
      });

    return () => {
      disposed = true;
      // Abort drives the cancellation contract for an in-flight/just-opened
      // connection; close() tears down one that already resolved. Idempotent.
      controller.abort();
      connection?.close();
    };
    // filterKey is the content fingerprint of `filter`; queryClient is stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterKey, queryClient]);
}
