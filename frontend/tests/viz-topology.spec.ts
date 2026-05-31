import { test, expect, type Page, type APIRequestContext } from '@playwright/test';

// SOW-0006 AC#2 — Topology tab (per-session) + cross-session /topology page.
// Exercises the EMBEDDED SPA served by the built ai-viewer-serve binary against
// the deterministic seed (scripts/e2e-serve.sh). Selectors mirror the real DOM:
//   - the per-session graph container is role="group" name "Session topology
//     graph" (TopologyRenderer.tsx TopologySvg/TopologyCanvas);
//   - the cross-session graph container is the SAME renderer, so also role=
//     "group" name "Session topology graph", but rendered under the /topology
//     page whose <h1>Topology</h1> + global FilterBar (role="search" name
//     "Session filters", FilterBar.tsx) frame it;
//   - the metric selector is a <select> labelled "Size by" (TopologyTab.tsx);
//   - the layout modes are radios "Seeded force" / "Plain force" /
//     "Hierarchical" in the "Layout" fieldset.
// Session ids are derived at runtime from /api/sessions (deep-link.spec.ts
// convention).
//
// FIXTURE NOTE (logged): the seed renders small graphs (per-session: 2–3 nodes;
// cross-session: 3 root nodes), all far below the 100-node Web-Worker/Canvas
// threshold, so this asserts the SVG render path and layout/metric switching.
// The < 500 ms 100-session PERF target (AC#2) and the Canvas+worker path need a
// 100+ node fixture; that is timed best-effort and logged, never faked.

/** anySessionId returns the first seeded session id. */
async function anySessionId(request: APIRequestContext): Promise<string> {
  const resp = await request.get('/api/sessions');
  expect(resp.ok()).toBeTruthy();
  const body = (await resp.json()) as { items: Array<{ id: string }> };
  expect(body.items.length).toBeGreaterThanOrEqual(1);
  return body.items[0]!.id;
}

/** gotoTopologyTab hard-navigates to a session's Topology tab and waits for the
 *  graph (the toolbar's node count proves the topology query resolved). */
async function gotoTopologyTab(page: Page, id: string): Promise<void> {
  await page.goto(`/sessions/${encodeURIComponent(id)}?tab=topology`);
  await expect(
    page.getByRole('heading', { name: new RegExp('Session\\b'), level: 1 }),
  ).toBeVisible();
  // Toolbar shows "<n> node(s)"; wait for it so the fetch+layout completed.
  await expect(page.getByText(/\d+ nodes?/)).toBeVisible();
}

test.describe('topology tab — per session (AC#2)', () => {
  test('renders the graph and switches all 3 layouts + the metric selector', async ({
    page,
    request,
  }, testInfo) => {
    const id = await anySessionId(request);
    await gotoTopologyTab(page, id);

    const graph = page.getByRole('group', { name: /topology/i });
    await expect(graph).toBeVisible();
    // At least one node (role=button) is painted in the SVG path.
    await expect(graph.getByRole('button').first()).toBeVisible();
    expect(await graph.getByRole('button').count()).toBeGreaterThanOrEqual(1);

    // Switch each of the three layout modes; the graph re-renders each time.
    for (const mode of ['Plain force', 'Hierarchical', 'Seeded force']) {
      await page.getByRole('radio', { name: mode }).check();
      await expect(page.getByRole('radio', { name: mode })).toBeChecked();
      await expect(page.getByRole('group', { name: /topology/i })).toBeVisible();
    }

    // The "Freeze layout" button is enabled for a force layout and disabled for
    // the static hierarchical one (TopologyTab freezeButton). Verify both states.
    await page.getByRole('radio', { name: 'Seeded force' }).check();
    const freeze = page.getByRole('button', { name: /freeze layout/i });
    await expect(freeze).toBeEnabled();
    await page.getByRole('radio', { name: 'Hierarchical' }).check();
    await expect(freeze).toBeDisabled();

    // The size metric selector drives a refetch+relayout (?metric=). Switch it
    // across all options; the graph stays rendered.
    await page.getByRole('radio', { name: 'Seeded force' }).check();
    const sizeBy = page.getByRole('combobox', { name: 'Size by' });
    await expect(sizeBy).toBeVisible();
    for (const metric of ['Tokens', 'Duration', 'Calls', 'Context %', 'Cost']) {
      await sizeBy.selectOption({ label: metric });
      await expect(page.getByRole('group', { name: /topology/i })).toBeVisible();
    }

    testInfo.annotations.push({
      type: 'perf',
      description:
        'per-session topology graph is small in the seed (2–3 nodes, SVG path); ' +
        "AC#2's < 500 ms / 100-node Canvas+worker target needs a 100+ node fixture.",
    });
  });
});

test.describe('topology page — cross session (AC#2)', () => {
  test('/topology renders the graph with the global FilterBar', async ({ page }, testInfo) => {
    await page.goto('/topology');

    // The page landmark (Topology.tsx <h1 id="topology-title">Topology</h1>).
    await expect(page.getByRole('heading', { name: 'Topology', level: 1 })).toBeVisible();

    // The GLOBAL FilterBar is present (Layout renders it above every route;
    // role="search" name "Session filters" — FilterBar.tsx). This is the AC#2
    // "reuses the global FilterBar" requirement for the cross-session view.
    await expect(page.getByRole('search', { name: 'Session filters' })).toBeVisible();

    // The toolbar's "<n> session(s)" count proves /api/topology resolved.
    await expect(page.getByText(/\d+ sessions?/)).toBeVisible();

    // The graph renders via the same TopologyRenderer (role="group").
    const graph = page.getByRole('group', { name: /topology/i });
    await expect(graph).toBeVisible();
    await expect(graph.getByRole('button').first()).toBeVisible();

    // The cross-session page also exposes the 3-layout toggle (its OWN radio
    // group name "cross-topology-mode", but the visible labels match).
    for (const mode of ['Plain force', 'Hierarchical', 'Seeded force']) {
      await page.getByRole('radio', { name: mode }).check();
      await expect(page.getByRole('group', { name: /topology/i })).toBeVisible();
    }

    testInfo.annotations.push({
      type: 'perf',
      description:
        'cross-session /topology renders 3 root nodes in the seed (SVG path); ' +
        "AC#2's < 500 ms / 100-session target needs a 100+ session fixture.",
    });
  });
});
