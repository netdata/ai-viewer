import { test, expect, type APIRequestContext } from '@playwright/test';

// AC#4 — sessions-list FILTER flow. Exercises the global FilterBar (reachable
// through Layout's topbar Filters sheet, role="search" name "Session filters")
// end-to-end
// against the EMBEDDED SPA served by the built binary with the deterministic
// seed (scripts/e2e-serve.sh). Every FilterBar control reads/writes the URL via
// useFilters(); SessionsList re-reads it and refetches GET /api/sessions. This
// spec proves: a matching agent filter narrows to ≥1 row, the URL carries the
// filter; a non-matching term collapses to the accessible empty state; Clear
// restores the full list. Selectors mirror the real DOM
// (FilterBar.tsx + SessionsList.tsx). The agent name is derived at RUNTIME from
// /api/sessions (never hardcoded), so the spec tracks the seed — the convention
// every other E2E spec here follows (deep-link.spec.ts).

interface SessionListEnvelope {
  items: Array<{ agent_name: string }>;
}

interface FilterableAgent {
  name: string;
  expectedRows: number;
}

/**
 * firstFilterableAgent reads a real root-session agent from the seeded list and
 * verifies the same API contract the UI will use after the browser filter
 * changes. This keeps the spec tied to the seed without assuming fixture order.
 */
async function firstFilterableAgent(request: APIRequestContext): Promise<FilterableAgent> {
  const resp = await request.get('/api/sessions?group=root');
  expect(resp.ok()).toBeTruthy();
  const body = (await resp.json()) as SessionListEnvelope;
  expect(body.items.length).toBeGreaterThanOrEqual(1);

  const candidates = [...new Set(body.items.map((item) => item.agent_name).filter(Boolean))];
  for (const name of candidates) {
    const filtered = await request.get(
      `/api/sessions?group=root&agents=${encodeURIComponent(name)}`,
    );
    expect(filtered.ok()).toBeTruthy();
    const filteredBody = (await filtered.json()) as SessionListEnvelope;
    if (filteredBody.items.length > 0) {
      return { name, expectedRows: filteredBody.items.length };
    }
  }

  throw new Error('seed sessions must include at least one filterable root-session agent');
}

test.describe('sessions list — filter (AC#4)', () => {
  test('the FilterBar agents filter narrows the list and Clear restores it', async ({
    page,
    request,
  }) => {
    const agent = await firstFilterableAgent(request);

    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible();

    // Baseline: the seed renders ≥ 1 root session row (routes.spec.ts invariant).
    const rows = page.locator('table tbody tr');
    await expect(rows.first()).toBeVisible();
    const baseline = await rows.count();
    expect(baseline).toBeGreaterThanOrEqual(1);

    // The global FilterBar is reachable from the topbar Filters sheet.
    await page.getByRole('button', { name: 'Filters' }).click();
    const filters = page.getByRole('search', { name: 'Session filters' });
    await expect(filters).toBeVisible();

    // Filter to the derived agent (FilterBar "Agents filter" text input drives
    // ?agents=<name> via useFilters → SessionsList refetches). The API keeps
    // related rows visible for context, so assert the list narrows/settles and
    // contains the requested agent without requiring every visible row to have
    // the same agent name.
    await filters.getByLabel('Agents filter').fill(agent.name);
    await expect.poll(() => new URL(page.url()).searchParams.get('agents')).toBe(agent.name);
    await page.keyboard.press('Escape');
    await expect
      .poll(async () => rows.count(), { message: 'browser table reached the API-filtered row count' })
      .toBe(agent.expectedRows);
    const filteredCount = await rows.count();
    expect(filteredCount).toBeGreaterThanOrEqual(1);
    expect(filteredCount).toBeLessThanOrEqual(baseline);
    await expect(page.getByRole('link', { name: agent.name, exact: true }).first()).toBeVisible();

    // A clearly non-existent agent collapses the list to the accessible empty
    // state (proves the filter actually narrows server-side, not just visually).
    await page.getByRole('button', { name: 'Filters' }).click();
    const missingAgent = 'no-such-agent-xyz-deterministic';
    await filters.getByLabel('Agents filter').fill(missingAgent);
    await expect.poll(() => new URL(page.url()).searchParams.get('agents')).toBe(missingAgent);
    await page.keyboard.press('Escape');
    await expect(
      page.getByRole('heading', { name: 'No sessions match these filters', level: 2 }),
    ).toBeVisible();
    await expect(rows).toHaveCount(0);

    // Clear filters restores the full list (FilterBar "Clear filters" button).
    await page.getByRole('button', { name: 'Filters' }).click();
    await filters.getByRole('button', { name: 'Clear filters' }).click();
    await expect.poll(() => new URL(page.url()).searchParams.has('agents')).toBe(false);
    await page.keyboard.press('Escape');
    await expect(rows.first()).toBeVisible();
    await expect(rows).toHaveCount(baseline);
  });
});
