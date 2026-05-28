import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import type { SubscriptionFilterRequest } from '../api/types';

// useLiveUpdates owns the per-view SSE connection lifecycle
// (frontend-architecture.md §useLiveUpdates). connectSse is MOCKED here so the
// tests assert lifecycle only: one subscription on mount, abort + close on
// unmount, and a re-subscription when the filter content changes. The
// invalidation wiring itself is covered by sse.test.ts.

// vi.mock is hoisted above top-level declarations, so the spies and the
// sentinel class it references must be created via vi.hoisted (which runs
// before the mock factory). SseCanceledError is re-exported by the mock so the
// hook's instanceof check (swallow cancellation) resolves against the same
// class the mock throws.
const { connectSpy, closeSpy, FakeCanceled } = vi.hoisted(() => {
  class FakeCanceled extends Error {}
  return { connectSpy: vi.fn(), closeSpy: vi.fn(), FakeCanceled };
});

vi.mock('../api/sse', () => ({
  connectSse: (...args: unknown[]) => connectSpy(...args) as unknown,
  SseCanceledError: FakeCanceled,
}));

// Imported AFTER vi.mock so the hook binds to the mocked module.
import { useLiveUpdates } from './useLiveUpdates';

// A STABLE QueryClient (created once per render tree, not per render) — the hook
// keys its effect on queryClient identity, so a fresh client each render would
// spuriously re-subscribe and mask the content-stability assertion.
function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

beforeEach(() => {
  connectSpy.mockReset();
  closeSpy.mockReset();
  // Default: resolve to a connection whose close() is the spy.
  connectSpy.mockResolvedValue({ close: closeSpy, subscriptionId: 'sub-x' });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('useLiveUpdates', () => {
  it('opens exactly one subscription on mount with the filter + an AbortSignal', async () => {
    const filter: SubscriptionFilterRequest = { status: ['failed'] };
    renderHook(() => useLiveUpdates(filter), { wrapper: makeWrapper() });

    await waitFor(() => expect(connectSpy).toHaveBeenCalledTimes(1));
    const args = connectSpy.mock.calls[0] as unknown[];
    // (queryClient, filter, handlers, signal)
    expect(args[1]).toEqual(filter);
    expect(args[3]).toBeInstanceOf(AbortSignal);
  });

  it('aborts and closes the connection on unmount', async () => {
    const filter: SubscriptionFilterRequest = { session_id: 's1' };
    const { unmount } = renderHook(() => useLiveUpdates(filter), { wrapper: makeWrapper() });

    await waitFor(() => expect(connectSpy).toHaveBeenCalledTimes(1));
    const signal = (connectSpy.mock.calls[0] as unknown[])[3] as AbortSignal;
    expect(signal.aborted).toBe(false);

    unmount();
    // The effect cleanup aborts the controller AND closes the resolved conn.
    expect(signal.aborted).toBe(true);
    await waitFor(() => expect(closeSpy).toHaveBeenCalled());
  });

  it('re-subscribes when the filter content changes', async () => {
    const { rerender } = renderHook(
      ({ f }: { f: SubscriptionFilterRequest }) => useLiveUpdates(f),
      { wrapper: makeWrapper(), initialProps: { f: { session_id: 'a' } } },
    );
    await waitFor(() => expect(connectSpy).toHaveBeenCalledTimes(1));

    rerender({ f: { session_id: 'b' } });
    // New content → old effect torn down (close) + a fresh connectSse call.
    await waitFor(() => expect(connectSpy).toHaveBeenCalledTimes(2));
    expect(closeSpy).toHaveBeenCalled();
    expect((connectSpy.mock.calls[1] as unknown[])[1]).toEqual({ session_id: 'b' });
  });

  it('does NOT re-subscribe when a new filter object has identical content', async () => {
    const { rerender } = renderHook(
      ({ f }: { f: SubscriptionFilterRequest }) => useLiveUpdates(f),
      { wrapper: makeWrapper(), initialProps: { f: { status: ['failed'] } } },
    );
    await waitFor(() => expect(connectSpy).toHaveBeenCalledTimes(1));

    // Fresh object literal, same content → stable filterKey → no churn.
    rerender({ f: { status: ['failed'] } });
    await Promise.resolve();
    expect(connectSpy).toHaveBeenCalledTimes(1);
  });

  it('swallows SseCanceledError (cancellation is not a failure)', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    connectSpy.mockRejectedValueOnce(new FakeCanceled('canceled'));
    renderHook(() => useLiveUpdates({ session_id: 's1' }), { wrapper: makeWrapper() });

    await waitFor(() => expect(connectSpy).toHaveBeenCalled());
    await Promise.resolve();
    expect(warn).not.toHaveBeenCalled();
  });

  it('surfaces a real connect failure via console.warn (no silent failure)', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    connectSpy.mockRejectedValueOnce(new Error('boom'));
    renderHook(() => useLiveUpdates({ session_id: 's1' }), { wrapper: makeWrapper() });

    await waitFor(() => expect(warn).toHaveBeenCalled());
    expect(String(warn.mock.calls[0]?.[0])).toContain('SSE connect failed');
  });
});
