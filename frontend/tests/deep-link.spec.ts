import { test, expect } from '@playwright/test';

// Deep-link / SPA-fallback coverage (SOW-0016). A HARD navigation to a
// client-side route (/sessions/<id>) must work: the Go server's SPA fallback
// serves the built shell for the unknown path, and the client router then
// resolves the route and renders the unified detail view. The session id is
// derived at runtime from /api/sessions — never hardcoded — so the test tracks
// the seed.

test.describe('deep link', () => {
  test('hard navigation to /sessions/<id> renders the unified detail view', async ({
    page,
    request,
  }) => {
    // Pull a real seeded session id from the REST API (items[0].id).
    const resp = await request.get('/api/sessions');
    expect(resp.ok()).toBeTruthy();
    const body = (await resp.json()) as { items: Array<{ id: string; agent_name: string }> };
    expect(body.items.length).toBeGreaterThanOrEqual(1);
    const session = body.items[0]!;
    const id = session.id;
    expect(id).toBeTruthy();
    expect(session.agent_name).toBeTruthy();

    // Hard navigation (full page load), not an in-app Link click — this is what
    // proves the server fallback + client hydration path, the SOW-0016 contract.
    await page.goto(`/sessions/${encodeURIComponent(id)}`);

    // The unified detail heading is the agent name; the id is still echoed in a
    // <code> below it (src/pages/SessionDetail).
    await expect(page.getByRole('heading', { name: session.agent_name, level: 1 })).toBeVisible();
    await expect(page.getByText(id, { exact: false }).first()).toBeVisible();

    // The unified page renders overview tiles, the left visualization/event
    // region, and the right turn-view region. These landmarks prove the detail
    // payload rendered, not a not-found / error state.
    const overview = page.getByRole('group', { name: 'Session overview' });
    await expect(overview).toBeVisible();
    await expect(overview.getByText('Status', { exact: true })).toBeVisible();
    await expect(overview.getByText('Tokens', { exact: true })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Visualization and event list' })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Turn view' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Waterfall' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Event list' })).toBeVisible();
    // Negative: the not-found state must not be shown for a valid seeded id.
    await expect(page.getByText('Session not found.')).toHaveCount(0);
  });

  test('parent session renders the SOW-0016 child-session boundary in the unified waterfall', async ({
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

    // Pull the first child id from the same detail contract the UI consumes so
    // the waterfall/drawer assertion below is exact, not a guess.
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

    // The unified view exposes child sessions as session-boundary spans in the
    // default waterfall. Click a session-boundary span and assert the shared
    // detail drawer reports the derived child id.
    const waterfall = page.getByRole('group', { name: /waterfall/i });
    await expect(waterfall).toBeVisible();
    const childBoundary = waterfall.getByRole('button', { name: /— session —/ }).first();
    await expect(childBoundary).toBeVisible();
    await childBoundary.click();

    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText('Child session', { exact: true })).toBeVisible();
    await expect(dialog.getByText(childId, { exact: true })).toBeVisible();
    await expect(dialog.getByText(parentId, { exact: true })).toHaveCount(0);
  });
});
