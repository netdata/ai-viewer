import { test, expect } from '@playwright/test';

// Deep-link / SPA-fallback coverage (SOW-0016). A HARD navigation to a
// client-side route (/sessions/<id>) must work: the Go server's SPA fallback
// serves the built shell for the unknown path, and the client router then
// resolves the route and renders the detail Overview. The session id is derived
// at runtime from /api/sessions — never hardcoded — so the test tracks the seed.

test.describe('deep link', () => {
  test('hard navigation to /sessions/<id> renders the detail Overview', async ({
    page,
    request,
  }) => {
    // Pull a real seeded session id from the REST API (items[0].id).
    const resp = await request.get('/api/sessions');
    expect(resp.ok()).toBeTruthy();
    const body = (await resp.json()) as { items: Array<{ id: string }> };
    expect(body.items.length).toBeGreaterThanOrEqual(1);
    const id = body.items[0]!.id;
    expect(id).toBeTruthy();

    // Hard navigation (full page load), not an in-app Link click — this is what
    // proves the server fallback + client hydration path, the SOW-0016 contract.
    await page.goto(`/sessions/${encodeURIComponent(id)}`);

    // The detail heading echoes the id in a <code> (src/pages/SessionDetail).
    await expect(
      page.getByRole('heading', { name: new RegExp(`Session\\b`), level: 1 }),
    ).toBeVisible();
    await expect(page.getByText(id, { exact: false }).first()).toBeVisible();

    // The Overview tab is the default; assert a StatCard from OverviewTab is
    // present (label "Tokens in") — proving the detail payload rendered, not a
    // not-found / error state.
    await expect(page.getByText('Tokens in', { exact: true })).toBeVisible();
    // The "Tools used" section header is always rendered on a loaded Overview.
    await expect(page.getByRole('heading', { name: 'Tools used' })).toBeVisible();
    // Negative: the not-found state must not be shown for a valid seeded id.
    await expect(page.getByText('Session not found.')).toHaveCount(0);
  });

  test('parent session renders the SOW-0016 child-sessions drill-down', async ({
    page,
    request,
  }) => {
    // Find the seeded parent: the sub_agent fixture root has child_session_count
    // >= 1 (api/types SessionListItem.child_session_count). Derive it at runtime
    // — never hardcoded — so the test tracks the seed, and fail clearly if the
    // seed regressed (e2e-serve.sh guarantees exactly one such parent).
    const listResp = await request.get('/api/sessions');
    expect(listResp.ok()).toBeTruthy();
    const list = (await listResp.json()) as {
      items: Array<{ id: string; child_session_count: number }>;
    };
    const parent = list.items.find((s) => s.child_session_count > 0);
    expect(
      parent,
      'seed must contain a parent session with child_session_count > 0 (sub_agent fixture)',
    ).toBeDefined();
    const parentId = parent!.id;

    // The Overview child table renders from the DETAIL response's child_sessions
    // (OverviewTab.tsx). Pull the first child id from the same contract the UI
    // consumes so the link assertion below is exact, not a guess.
    const detailResp = await request.get(`/api/sessions/${encodeURIComponent(parentId)}`);
    expect(detailResp.ok()).toBeTruthy();
    const detail = (await detailResp.json()) as {
      child_sessions: Array<{ id: string }>;
    };
    expect(
      detail.child_sessions.length,
      'parent detail must expose >= 1 child session',
    ).toBeGreaterThanOrEqual(1);
    const childId = detail.child_sessions[0]!.id;
    expect(childId).toBeTruthy();
    expect(childId).not.toBe(parentId);

    // Hard navigation to the parent — same SPA-fallback path as the test above.
    await page.goto(`/sessions/${encodeURIComponent(parentId)}`);

    // The SOW-0016 child-sessions section must render: region + heading +
    // a populated table (selectors mirror OverviewTab.tsx: a
    // <section aria-labelledby="child-sessions-title"> with an
    // <h2 id="child-sessions-title">Child sessions</h2>).
    const childSection = page.getByRole('region', { name: 'Child sessions' });
    await expect(childSection).toBeVisible();
    await expect(
      childSection.getByRole('heading', { name: 'Child sessions', level: 2 }),
    ).toBeVisible();
    const childRows = childSection.locator('table tbody tr');
    await expect(childRows.first()).toBeVisible();
    expect(await childRows.count()).toBeGreaterThanOrEqual(1);

    // Each child row links to /sessions/<child id> — assert the drill-down link
    // for the derived child exists INSIDE the section and points elsewhere than
    // the parent itself (proves the parent→child navigation target end-to-end).
    const childLink = childSection.locator(
      `a[href="/sessions/${encodeURIComponent(childId)}"]`,
    );
    await expect(childLink.first()).toBeVisible();
    await expect(
      childSection.locator(`a[href="/sessions/${encodeURIComponent(parentId)}"]`),
    ).toHaveCount(0);
  });
});
