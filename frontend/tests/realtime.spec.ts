import { test, expect, type Request } from '@playwright/test';

// Realtime / SSE liveness coverage (Chunk 18 D4). The ingester is read-only over
// static fixtures, so this does NOT assert a new session appears mid-test (a true
// append→fade-in test needs a writer the product does not expose — noted as a
// Phase-2 follow-up). Scope here is connection LIVENESS:
//   - the page opens its SSE subscription (POST /api/subscriptions) and the
//     EventSource stream (GET /api/events?sub=<id>) — the documented handshake
//     (sse-protocol.md; api/sse.ts);
//   - navigating between live views re-establishes the subscription and throws
//     no uncaught page error.
// NB: the shipped Phase-1 Layout/pages render NO visible "connected" indicator
// (state/useLiveUpdates does not surface ConnectionStatus to the DOM), so the
// established connection is asserted at the network level, which is the ground
// truth for "the SSE subscription connected".

test.describe('realtime', () => {
  // SSE flow tuning (SOW-0012 AC#4 / R3): 30 s per-test timeout (the
  // subscription POST + EventSource open against the built binary is the
  // slowest checkpoint) and a single retry for CI-runner timing slack. Scoped
  // here, NOT global — every non-SSE spec stays deterministic with no retries.
  test.describe.configure({ retries: 1, timeout: 30_000 });

  test('opening / establishes the SSE subscription and event stream', async ({ page }) => {
    const pageErrors: Error[] = [];
    page.on('pageerror', (e) => pageErrors.push(e));

    // The EventSource GET is long-lived; match the request (don't await a
    // response body that never completes while the stream is open).
    const eventsReq = page.waitForRequest(
      (req: Request) => req.url().includes('/api/events?sub='),
      { timeout: 30_000 },
    );
    // ...but ALSO confirm the stream actually OPENED: EventSource resolves the
    // response headers as soon as the server commits 200 + the SSE content type
    // (the body then stays open). Matching the response — not just the request —
    // proves the handshake succeeded, not merely that the browser tried. Wire the
    // waiter up BEFORE navigation so the early response is never missed.
    const eventsResp = page.waitForResponse(
      (resp) => resp.url().includes('/api/events?sub='),
      { timeout: 30_000 },
    );
    // The subscription is created via POST /api/subscriptions before the stream.
    const subResp = page.waitForResponse(
      (resp) =>
        resp.url().endsWith('/api/subscriptions') && resp.request().method() === 'POST',
      { timeout: 30_000 },
    );

    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible();

    const created = await subResp;
    expect(created.ok()).toBeTruthy();
    const sub = (await created.json()) as { id: string };
    expect(sub.id).toBeTruthy();

    const stream = await eventsReq;
    expect(stream.url()).toContain(`sub=${encodeURIComponent(sub.id)}`);

    // The stream opened: 200 with the SSE media type (per sse-protocol.md).
    const streamResp = await eventsResp;
    expect(streamResp.status()).toBe(200);
    expect(streamResp.headers()['content-type']).toContain('text/event-stream');

    expect(pageErrors).toEqual([]);
  });

  test('navigating between live views re-establishes a subscription without errors', async ({
    page,
  }) => {
    const pageErrors: Error[] = [];
    page.on('pageerror', (e) => pageErrors.push(e));

    // First DRAIN the initial '/' subscription. Without this, a late-resolving
    // initial POST could satisfy the post-click waiter below and produce a false
    // pass — so we await it here, then arm a FRESH waiter right before the nav.
    const initialSub = page.waitForResponse(
      (resp) =>
        resp.url().endsWith('/api/subscriptions') && resp.request().method() === 'POST',
      { timeout: 30_000 },
    );
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible();
    expect((await initialSub).ok()).toBeTruthy();

    // Now arm a fresh waiter and navigate to /sources, which mounts a new view
    // that opens its OWN subscription. Because the initial one is already
    // resolved, this waiter can only be satisfied by the /sources navigation.
    const subOnSources = page.waitForResponse(
      (resp) =>
        resp.url().endsWith('/api/subscriptions') && resp.request().method() === 'POST',
      { timeout: 30_000 },
    );
    await page
      .getByRole('navigation', { name: 'Primary' })
      .getByRole('link', { name: 'Sources', exact: true })
      .click();
    await expect(page.getByRole('heading', { name: 'Sources', level: 1 })).toBeVisible();
    expect((await subOnSources).ok()).toBeTruthy();

    expect(pageErrors).toEqual([]);
  });
});
