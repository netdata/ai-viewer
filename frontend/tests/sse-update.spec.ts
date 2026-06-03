import { test, expect } from '@playwright/test';

// AC#4 — real-time SSE update via a DETERMINISTIC fixture (NOT timing-luck).
//
// The ingester is read-only over static fixtures and the product exposes no
// writer, so a server-driven append cannot be injected (realtime.spec.ts /
// viz-sse.spec.ts assert only the handshake liveness at the network level).
// To make the live-update path itself deterministic we drive it from the
// CLIENT seam: a fake EventSource installed via addInitScript BEFORE any app
// script runs replaces the browser's, captures the instance the SSE client
// opens for /api/events, and lets the test DISPATCH a controlled
// `session_changed` frame at an exact moment. The real SseConnection listener
// (api/sse.ts `listen('session_changed', …)`) parses it and runs the documented
// TanStack Query invalidation (`['sessions']`), so the SessionsList refetches
// `GET /api/sessions`. We assert that refetch fires — proving the end-to-end
// live-update wiring (EventSource frame → typed handler → query invalidation →
// network refetch) without depending on wall-clock timing or a server writer.
//
// This complements (does not replace) realtime.spec.ts: that proves the REAL
// stream opens against the binary; this proves a frame on that stream updates
// the UI, deterministically.

// The minimal fake-EventSource shim. It is serialized into the page by
// addInitScript, so it must be a standalone function with no outer references.
// It records every constructed instance on window.__sse so the test can target
// the one opened for /api/events and dispatch named frames into it. Connection
// semantics mirror the browser: readyState transitions CONNECTING→OPEN on the
// next microtask and `onopen` fires, so api/sse.ts sees a normal open.
function installFakeEventSource(): void {
  interface FakeFrameInit {
    data: string;
  }
  class FakeEventSource extends EventTarget {
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSED = 2;
    readonly CONNECTING = 0;
    readonly OPEN = 1;
    readonly CLOSED = 2;
    readonly url: string;
    readyState = 0;
    onopen: ((this: EventSource, ev: Event) => unknown) | null = null;
    onerror: ((this: EventSource, ev: Event) => unknown) | null = null;
    onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null;

    constructor(url: string | URL) {
      super();
      this.url = String(url);
      const store = (window as unknown as { __sse: FakeEventSource[] });
      store.__sse = store.__sse ?? [];
      store.__sse.push(this);
      // Open on the next microtask so the caller has attached its listeners.
      queueMicrotask(() => {
        this.readyState = this.OPEN;
        const ev = new Event('open');
        this.onopen?.call(this as unknown as EventSource, ev);
        this.dispatchEvent(ev);
      });
    }

    /** dispatchFrame delivers a named SSE frame exactly as the browser would —
     *  a MessageEvent whose `data` is the raw string the server wrote. */
    dispatchFrame(name: string, init: FakeFrameInit): void {
      const ev = new MessageEvent(name, { data: init.data });
      this.dispatchEvent(ev);
    }

    close(): void {
      this.readyState = this.CLOSED;
    }
  }
  (window as unknown as { EventSource: unknown }).EventSource = FakeEventSource;
}

test.describe('realtime — deterministic SSE update (AC#4)', () => {
  // SSE flow: opt into the per-scenario tuning the global config deliberately
  // omits — 30 s timeout (the EventSource open is the slowest checkpoint) and a
  // single retry for CI-runner timing slack (SOW-0012 AC#4 / R3). The injected
  // frame itself is deterministic; the retry only covers transport startup.
  test.describe.configure({ retries: 1, timeout: 30_000 });

  test('a session_changed frame triggers a sessions refetch', async ({ page }) => {
    const pageErrors: Error[] = [];
    page.on('pageerror', (e) => pageErrors.push(e));

    // Install the fake EventSource before the app boots so SseConnection.open()
    // constructs ours, not the browser's.
    await page.addInitScript(installFakeEventSource);

    // Count GET /api/sessions calls so we can prove a SECOND one fires after the
    // injected frame (the first is the initial list load). The single glob
    // matches the list path with OR without a query string (the SessionsList
    // always sends `?group=root&…`), so there is no overlapping-route ambiguity.
    let sessionsGets = 0;
    await page.route('**/api/sessions**', async (route) => {
      if (route.request().method() === 'GET') {
        sessionsGets += 1;
      }
      await route.continue();
    });

    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible();
    // The initial list resolved (≥ 1 seeded row), so the first GET happened.
    await expect(page.locator('table tbody tr').first()).toBeVisible();
    await expect.poll(() => sessionsGets).toBeGreaterThanOrEqual(1);
    const before = sessionsGets;

    // Wait until the SSE client has opened the events stream (our fake captured
    // an instance whose url is /api/events). Polling on window.__sse is
    // deterministic — it resolves the instant the subscription opens, no sleeps.
    await expect
      .poll(async () =>
        page.evaluate(
          () =>
            (window as unknown as { __sse?: { url: string }[] }).__sse?.some((s) =>
              s.url.includes('/api/events'),
            ) ?? false,
        ),
      )
      .toBe(true);

    // Dispatch a deterministic session_changed frame into the events stream.
    // The id need not exist in the seed: session_changed invalidates the whole
    // ['sessions'] list query (api/sse.ts), which is what we assert.
    await page.evaluate(() => {
      const sources = (
        window as unknown as {
          __sse: {
            url: string;
            dispatchFrame: (n: string, i: { data: string }) => void;
          }[];
        }
      ).__sse;
      const stream = sources.find((s) => s.url.includes('/api/events'));
      if (!stream) {
        throw new Error('events stream not opened');
      }
      stream.dispatchFrame('session_changed', {
        data: JSON.stringify({
          session_id: 'sse-test-deterministic',
          root_session_id: 'sse-test-deterministic',
          ts: 1,
        }),
      });
    });

    // The invalidation refetches the list: a second GET /api/sessions fires.
    await expect.poll(() => sessionsGets).toBeGreaterThan(before);

    expect(pageErrors).toEqual([]);
  });
});
