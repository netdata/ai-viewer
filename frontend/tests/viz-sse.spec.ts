import { test, expect, type Request, type APIRequestContext } from '@playwright/test';

// SOW-0006 AC#6 — SSE-driven live updates on the viz tabs. Mirrors
// tests/realtime.spec.ts: the ingester is READ-ONLY over static fixtures and the
// product exposes NO writer, so a true append→fade-in (write a new op → observe
// the SSE session_changed → assert the new span fades in) is not achievable in
// this harness. Per realtime.spec.ts, scope here is the SSE handshake LIVENESS,
// asserted at the network/protocol level while a VIZ tab is mounted:
//   - the session detail view opens its subscription (POST /api/subscriptions)
//     and the EventSource stream (GET /api/events?sub=<id>) — the documented
//     handshake (sse-protocol.md; api/sse.ts; useLiveUpdates) — when the Trace
//     tab is the active view;
//   - the stream actually OPENS (200 + text/event-stream), proving the live
//     seam the SSE-fix wired is reachable from the viz tabs;
//   - no uncaught page error on the viz route.
// The append→fade-in behavior itself (viz/spanFade + useNewlyAppeared) is covered
// by unit tests (spanFade.test.ts, useNewlyAppeared.test.ts) and the
// session_changed → ['session-timeline'/'session-topology'/'topology']
// invalidation wiring is covered in api/sse unit tests. The live-DOM append is
// LOGGED below as a known harness limitation (a Phase-2 writer is the unblock).

/** firstSessionId pulls a real seeded session id from the REST API. */
async function firstSessionId(request: APIRequestContext): Promise<string> {
  const resp = await request.get('/api/sessions');
  expect(resp.ok()).toBeTruthy();
  const body = (await resp.json()) as { items: Array<{ id: string }> };
  expect(body.items.length).toBeGreaterThanOrEqual(1);
  return body.items[0]!.id;
}

test.describe('realtime — viz tabs (AC#6)', () => {
  // SSE flow tuning (SOW-0012 AC#4 / R3): 30 s per-test timeout (subscription
  // POST + EventSource open is the slowest checkpoint) + a single retry for
  // CI-runner timing slack. Scoped to this SSE spec, NOT global.
  test.describe.configure({ retries: 1, timeout: 30_000 });

  test('opening a session Trace tab establishes the SSE subscription + event stream', async ({
    page,
    request,
  }, testInfo) => {
    const id = await firstSessionId(request);

    const pageErrors: Error[] = [];
    page.on('pageerror', (e) => pageErrors.push(e));

    // Arm the network waiters BEFORE navigation so an early handshake is caught.
    const eventsReq = page.waitForRequest(
      (req: Request) => req.url().includes('/api/events?sub='),
      { timeout: 30_000 },
    );
    const eventsResp = page.waitForResponse(
      (resp) => resp.url().includes('/api/events?sub='),
      { timeout: 30_000 },
    );
    const subResp = page.waitForResponse(
      (resp) =>
        resp.url().endsWith('/api/subscriptions') && resp.request().method() === 'POST',
      { timeout: 30_000 },
    );

    // Hard-navigate to the Trace tab (a viz view). SessionDetail mounts
    // useLiveUpdates({ session_id }) regardless of the active tab, so the
    // subscription opens while the Trace tab renders.
    await page.goto(`/sessions/${encodeURIComponent(id)}?tab=trace`);
    await expect(page.getByText(/\d+ ops/)).toBeVisible();
    await expect(page.getByRole('group', { name: /waterfall/i })).toBeVisible();

    // The subscription was created (POST /api/subscriptions → { id }).
    const created = await subResp;
    expect(created.ok()).toBeTruthy();
    const sub = (await created.json()) as { id: string };
    expect(sub.id).toBeTruthy();

    // The EventSource stream request carries the minted sub id …
    const stream = await eventsReq;
    expect(stream.url()).toContain(`sub=${encodeURIComponent(sub.id)}`);

    // … and the stream actually opened (200 + the SSE media type).
    const streamResp = await eventsResp;
    expect(streamResp.status()).toBe(200);
    expect(streamResp.headers()['content-type']).toContain('text/event-stream');

    expect(pageErrors).toEqual([]);

    testInfo.annotations.push({
      type: 'harness-limitation',
      description:
        'AC#6 append→fade-in is asserted at the SSE protocol level (subscription + ' +
        'stream open on a viz tab), mirroring realtime.spec.ts: the ingester is ' +
        'read-only over static fixtures and no writer is exposed, so a new op ' +
        'cannot be injected into the running seed. The fade-in itself is unit-tested ' +
        '(spanFade.test.ts / useNewlyAppeared.test.ts) and the session_changed → ' +
        "['session-timeline'/'session-topology'/'topology'] invalidation in api/sse unit tests.",
    });
  });
});
