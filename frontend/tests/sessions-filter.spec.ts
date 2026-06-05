import { test, expect, type APIRequestContext } from '@playwright/test';

// AC#4 — sessions-list FILTER flow. Exercises the global FilterBar (rendered in
// Layout, role="search" name "Session filters" — FilterBar.tsx) end-to-end
// against the EMBEDDED SPA served by the built binary with the deterministic
// seed (scripts/e2e-serve.sh). Every FilterBar control reads/writes the URL via
// useFilters(); SessionsList re-reads it and refetches GET /api/sessions. This
// spec proves: a matching agent filter narrows to ≥1 row, the URL carries the
// filter; a non-matching term collapses to the accessible empty state; Clear
// restores the full list. Selectors mirror the real DOM
// (FilterBar.tsx + SessionsList.tsx). The agent name is derived at RUNTIME from
// /api/sessions (never hardcoded), so the spec tracks the seed — the convention
// every other E2E spec here follows (deep-link.spec.ts).

/** firstAgentName reads a real agent_name from the seeded session list. */
async function firstAgentName(request: APIRequestContext): Promise<string> {
  const resp = await request.get('/api/sessions');
  expect(resp.ok()).toBeTruthy();
  const body = (await resp.json()) as { items: Array<{ agent_name: string }> };
  expect(body.items.length).toBeGreaterThanOrEqual(1);
  const name = body.items[0]!.agent_name;
  expect(name, 'seed sessions must carry an agent_name to filter on').toBeTruthy();
  return name;
}

test.describe('sessions list — filter (AC#4)', () => {
  test('the FilterBar agents filter narrows the list and Clear restores it', async ({
    page,
    request,
  }) => {
    const agent = await firstAgentName(request);

    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible();

    // Baseline: the seed renders ≥ 1 root session row (routes.spec.ts invariant).
    const rows = page.locator('table tbody tr');
    await expect(rows.first()).toBeVisible();
    const baseline = await rows.count();
    expect(baseline).toBeGreaterThanOrEqual(1);

    // The global FilterBar is present (role="search").
    const filters = page.getByRole('search', { name: 'Session filters' });
    await expect(filters).toBeVisible();

    // Filter to the derived agent (FilterBar "Agents filter" text input drives
    // ?agents=<name> via useFilters → SessionsList refetches). The list still
    // shows ≥ 1 row and EVERY visible Agent cell equals the filtered agent.
    await filters.getByLabel('Agents filter').fill(agent);
    await expect.poll(() => new URL(page.url()).searchParams.get('agents')).toBe(agent);
    await expect(rows.first()).toBeVisible();
    const agentCells = page.locator('table tbody tr td:nth-child(2)');
    const matchedCount = await agentCells.count();
    expect(matchedCount).toBeGreaterThanOrEqual(1);
    for (let i = 0; i < matchedCount; i++) {
      await expect(agentCells.nth(i)).toHaveText(agent);
    }

    // A clearly non-existent agent collapses the list to the accessible empty
    // state (proves the filter actually narrows server-side, not just visually).
    await filters.getByLabel('Agents filter').fill('no-such-agent-xyz-deterministic');
    await expect(page.getByText('No sessions match the current filters.')).toBeVisible();
    await expect(rows).toHaveCount(0);

    // Clear filters restores the full list (FilterBar "Clear filters" button).
    await filters.getByRole('button', { name: 'Clear filters' }).click();
    await expect.poll(() => new URL(page.url()).searchParams.has('agents')).toBe(false);
    await expect(rows.first()).toBeVisible();
    await expect(rows).toHaveCount(baseline);
  });
});
