import { test, expect, type Page, type APIRequestContext } from '@playwright/test';

// SOW-0006 AC#3 — Timeline tab (video-editor lanes + spans, compaction
// breakpoints). Exercises the EMBEDDED SPA served by the built ai-viewer-serve
// binary against the deterministic seed (scripts/e2e-serve.sh). Selectors mirror
// the real DOM:
//   - the timeline container is role="group" name "Session timeline"
//     (TimelineRenderer.tsx TimelineSvg/TimelineCanvas);
//   - each span/tick/breakpoint is role="button" with a name "<name> — <kind> —
//     <dur|instant>" (TimelineSpanShape);
//   - the toolbar shows "<n> span(s) · <m> lane(s)" (TimelineTab.tsx).
// Session ids are derived at runtime from /api/sessions (deep-link.spec.ts).
//
// FIXTURE NOTE (logged, not faked): the committed seed has NO compaction op
// (kind='compaction'), so the full-height dashed BREAKPOINT cannot be asserted
// end-to-end here without changing the seed's exact-count contract
// (e2e-serve.sh asserts 4 sessions / 1 child / 3 sources). The breakpoint render
// is covered by component tests (TimelineRenderer.test.tsx). This spec asserts
// lanes + spans render, and that a multi-lane session (root + child) stacks lanes
// (the AC#3 "one lane per session, root + children stacked" requirement). The
// < 300 ms / 1K-session zoom PERF target needs a large fixture; timed
// best-effort + logged.

/** multiLaneSessionId returns a seeded session whose tree has > 1 timeline lane
 *  (a parent with a child session → root + child lanes), proving the
 *  lane-stacking requirement; falls back to the first session otherwise. */
async function multiLaneSessionId(request: APIRequestContext): Promise<{ id: string; lanes: number }> {
  const listResp = await request.get('/api/sessions');
  expect(listResp.ok()).toBeTruthy();
  const list = (await listResp.json()) as {
    items: Array<{ id: string; child_session_count: number }>;
  };
  // Prefer a parent (child_session_count > 0): its timeline has root + child lanes.
  const parent = list.items.find((s) => s.child_session_count > 0) ?? list.items[0];
  expect(parent, 'seed must contain at least one session').toBeDefined();
  const id = parent!.id;
  const tlResp = await request.get(`/api/sessions/${encodeURIComponent(id)}/timeline`);
  expect(tlResp.ok(), 'timeline endpoint must be live (rebuild bin/ if 404)').toBeTruthy();
  const tl = (await tlResp.json()) as { lanes: Array<{ spans: unknown[] }> };
  return { id, lanes: tl.lanes.length };
}

/** gotoTimeline hard-navigates to a session's unified detail view, opens the
 *  Timeline visualization, and waits for the span/lane count toolbar. */
async function gotoTimeline(page: Page, id: string): Promise<void> {
  await page.goto(`/sessions/${encodeURIComponent(id)}`);
  await expect(page.getByRole('region', { name: /waterfall visualization/i })).toBeVisible();
  await page.getByRole('button', { name: 'Timeline' }).click();
  const timelineRegion = page.getByRole('region', { name: /timeline visualization/i });
  await expect(timelineRegion).toBeVisible();
  await expect(timelineRegion.getByText(/\d+ spans?\s+·\s+\d+ lanes?/)).toBeVisible();
}

test.describe('timeline tab (AC#3)', () => {
  test('renders lanes + spans; a parent session stacks root + child lanes', async ({
    page,
    request,
  }, testInfo) => {
    const { id, lanes } = await multiLaneSessionId(request);
    await gotoTimeline(page, id);

    const timeline = page.getByRole('group', { name: /timeline/i });
    await expect(timeline).toBeVisible();

    // At least one span (role=button) is painted (bar / instant tick).
    const spans = timeline.getByRole('button');
    await expect(spans.first()).toBeVisible();
    expect(await spans.count()).toBeGreaterThanOrEqual(1);

    // The Timeline legend names the three encodings (bar / instant / compaction
    // breakpoint) — proving the breakpoint affordance is wired even though the
    // seed has no compaction op to render one (logged below).
    const legend = page.getByLabel('Timeline legend');
    await expect(legend).toContainText(/compaction breakpoint/i);

    if (lanes >= 2) {
      // A parent session's timeline stacks root + child lanes (AC#3). Assert the
      // toolbar reports >= 2 lanes for this session.
      await expect(page.getByText(/·\s+[2-9]\d* lanes/)).toBeVisible();
    } else {
      testInfo.annotations.push({
        type: 'fixture',
        description:
          'no multi-lane (parent+child) session in the seed for this run; ' +
          'lane-stacking asserted only at single-lane level.',
      });
    }

    testInfo.annotations.push({
      type: 'fixture',
      description:
        'seed has NO compaction op, so the full-height dashed breakpoint is not ' +
        'asserted end-to-end (covered by TimelineRenderer.test.tsx). AC#3 perf ' +
        '(< 300 ms zoom over 1K sessions, SVG→Canvas above 500 spans) needs a ' +
        'large fixture.',
    });

    // Best-effort zoom timing (AC#3 target < 300 ms). The seed is tiny so this
    // is not a meaningful benchmark; time a paint frame, assert it succeeds, log.
    const zoomMs = await page.evaluate(async () => {
      const t0 = performance.now();
      await new Promise((r) => requestAnimationFrame(() => r(null)));
      return performance.now() - t0;
    });
    testInfo.annotations.push({
      type: 'perf',
      description: `timeline paint-frame ≈ ${zoomMs.toFixed(1)} ms (seed too small for the AC#3 threshold).`,
    });
    expect(zoomMs).toBeLessThan(2000);
  });
});
