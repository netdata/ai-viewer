import { test, expect } from '@playwright/test';

// Route coverage against the EMBEDDED SPA served by the built ai-viewer-serve
// binary with the deterministic seed (happy_single_turn + multi_turn +
// sub_agent — scripts/e2e-serve.sh). Each live route must render REAL data, not
// the empty state; a Phase-2 stub route must render its ComingSoon placeholder.
// Selectors mirror the actual DOM (frontend/src/pages/* + components/ComingSoon).

test.describe('routes', () => {
  test('/ renders the sessions table with seeded rows', async ({ page }) => {
    await page.goto('/');
    // Page heading is the <h1 id="sessions-title">Sessions</h1> landmark.
    await expect(page.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible();
    // The seed guarantees >= 1 root session, so the table (not the empty state)
    // is shown. Assert at least one body row is present.
    const rows = page.locator('table tbody tr');
    await expect(rows.first()).toBeVisible();
    expect(await rows.count()).toBeGreaterThanOrEqual(1);
    // The empty-state copy must NOT be on the page when data exists.
    await expect(page.getByText('No sessions match the current filters.')).toHaveCount(0);
  });

  // Every still-scaffolded Phase-2/3 route renders the shared ComingSoon
  // placeholder (components/ComingSoon.tsx: <h1 id="coming-soon-title">{title}</h1>).
  // The path→title map mirrors the route table in src/App.tsx and the title each
  // page passes (src/pages/<Name>/<Name>.tsx). One test per route.
  //
  // NOTE: /topology is NO LONGER a placeholder — SOW-0006 (chunk 6b) shipped the
  // real cross-session Topology page (App.tsx → <Topology/>), so it is covered by
  // tests/viz-topology.spec.ts instead and is intentionally absent here.
  const comingSoonRoutes: Array<{ path: string; title: string }> = [
    { path: '/tools', title: 'Tools' },
    { path: '/models', title: 'Models' },
    { path: '/agents', title: 'Agents' },
  ];
  for (const { path, title } of comingSoonRoutes) {
    test(`${path} renders the ComingSoon placeholder ("${title}")`, async ({ page }) => {
      await page.goto(path);
      // The ComingSoon landmark heading carries id="coming-soon-title" and the
      // route's own title; assert both so a mis-wired route can't pass.
      const heading = page.locator('h1#coming-soon-title');
      await expect(heading).toBeVisible();
      await expect(heading).toHaveText(title);
      await expect(page.getByRole('heading', { name: title, level: 1 })).toBeVisible();
    });
  }

  test('an unknown client path renders the SPA-fallback NotFound', async ({ page }) => {
    // Hard navigation to a path the client router does not match. The Go SPA
    // fallback (SOW-0016) serves the built shell for the unknown path, and the
    // client router then renders its OWN NotFound (pages/NotFound.tsx:
    // <h1 id="notfound-title">Not found</h1>) — NOT a server JSON 404.
    //
    // First PROVE the server served the shell, not a 404: capture the navigation
    // response and assert 200 + an HTML content-type. A server that returned 404
    // but still served HTML would otherwise satisfy the NotFound assertions below;
    // only the status+type check distinguishes the SOW-0016 SPA fallback (200 +
    // text/html shell) from a real server 404.
    const resp = await page.goto('/no-such-route-xyz');
    expect(resp, 'navigation produced a response').not.toBeNull();
    expect(resp!.status()).toBe(200);
    expect(resp!.headers()['content-type'] ?? '').toContain('text/html');
    const heading = page.locator('h1#notfound-title');
    await expect(heading).toBeVisible();
    await expect(heading).toHaveText('Not found');
    await expect(page.getByRole('heading', { name: 'Not found', level: 1 })).toBeVisible();
    // The client NotFound offers a "Back to sessions" link to "/"; its presence
    // confirms the React shell rendered (a raw server 404 would have none).
    await expect(page.getByRole('link', { name: 'Back to sessions' })).toBeVisible();
  });

  test('/sources renders the sources table and a health badge', async ({ page }) => {
    await page.goto('/sources');
    await expect(page.getByRole('heading', { name: 'Sources', level: 1 })).toBeVisible();
    // The seed ingested three aiagent_v3 sources, so the table renders (not the
    // "No sources configured." empty state). Assert >= 1 source row.
    const rows = page.locator('table tbody tr');
    await expect(rows.first()).toBeVisible();
    expect(await rows.count()).toBeGreaterThanOrEqual(1);
    // The Sources health badge (pages/Sources/Sources.tsx:49) is a role=status
    // span whose text is the literal status word (ok|degraded|down). Scope to the
    // Sources region (the <section aria-labelledby="sources-title">, named
    // "Sources") AND match the exact status text so this can ONLY hit the health
    // badge — never the header ThemeToggle's role=status live region, whose text
    // is "Theme: <theme>" (components/ThemeToggle/ThemeToggle.tsx:40) and which
    // lives outside this region.
    const sourcesRegion = page.getByRole('region', { name: /sources/i });
    await expect(sourcesRegion.getByText(/^(ok|degraded|down)$/)).toBeVisible();
  });
});
