import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { connectSse, SseCanceledError, type SseConnection, type ConnectionStatus } from '../api/sse';
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
 * relevant TanStack Query caches live. Returns the current SSE connection
 * status ('connecting' | 'open' | 'reconnecting' | 'closed') so the UI can
 * render a live indicator. The invalidation wiring lives in connectSse.
 */
export function useLiveUpdates(filter: SubscriptionFilterRequest): ConnectionStatus {
  const queryClient = useQueryClient();
  // Stable dependency: re-subscribe only when the filter CONTENT changes.
  const filterKey = JSON.stringify(filter);
  const [status, setStatus] = useState<ConnectionStatus>('connecting');

  useEffect(() => {
    const controller = new AbortController();
    let connection: SseConnection | null = null;
    let disposed = false;
    setStatus('connecting');

    void connectSse(queryClient, filter, { onStatus: setStatus }, controller.signal)
      .then((conn) => {
        connection = conn;
        if (disposed) {
          conn.close();
        }
      })
      .catch((err: unknown) => {
        if (err instanceof SseCanceledError) {
          return;
        }
        console.warn('useLiveUpdates: SSE connect failed', err);
      });

    return () => {
      disposed = true;
      controller.abort();
      connection?.close();
      setStatus('closed');
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterKey, queryClient]);

  return status;
}
