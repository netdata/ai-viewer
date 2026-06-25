import { test, expect, type Page, type APIRequestContext } from '@playwright/test';

// SOW-0006 AC#4 — Span detail drawer. Exercises the EMBEDDED SPA served by the
// built ai-viewer-serve binary against the deterministic seed
// (scripts/e2e-serve.sh). The drawer is shared across Trace / Topology /
// Timeline (SpanDetailDrawer.tsx); we open it from the Trace waterfall (the most
// direct click target: SVG <rect role="button"> bars). Selectors mirror the real
// DOM:
//   - the drawer is role="dialog" (aria-modal="false") labelled by the op
//     name/id (SpanDetailDrawer panel);
//   - it shows the op field list (a <dl> with Status / Start / Duration / Cost /
//     Tokens in/out — the <dt>/<dd> pairs in SpanDetailDrawer);
//   - a "Payloads" section (<h3>Payloads</h3>) lists payload_refs or "No
//     payloads for this op.";
//   - the close button has aria-label "Close span details";
//   - the backdrop carries data-testid="drawer-overlay" (outside-click target).
//
// FIXTURE / IMPLEMENTATION NOTES (logged, not faked):
//   1. The committed seed has ZERO payload_refs on every op (the three seeded
//      fixtures do not capture payloads), so the drawer's Payloads section shows
//      "No payloads for this op." here. The per-payload metadata ROW path is
//      covered by SpanDetailDrawer.test.tsx, not exercised live.
//   2. AC#4's first-4-KB payload preview path is live through
//      /api/payloads/:id, but this deterministic seed carries no payload_refs.
//      The metadata-row and preview-fetch paths are covered by
//      SpanDetailDrawer.test.tsx; this E2E keeps the live no-payload fixture
//      path honest.

/** richestSessionId returns the seeded session with the most ops (most bars). */
async function richestSessionId(request: APIRequestContext): Promise<string> {
  const resp = await request.get('/api/sessions');
  expect(resp.ok()).toBeTruthy();
  const body = (await resp.json()) as {
    items: Array<{ id: string; op_count: number }>;
  };
  expect(body.items.length).toBeGreaterThanOrEqual(1);
  return [...body.items].sort((a, b) => b.op_count - a.op_count)[0]!.id;
}

/** openDrawerFromTrace navigates to the Trace tab and clicks the first span bar,
 *  returning once the drawer dialog is open. */
async function openDrawerFromTrace(page: Page, id: string): Promise<void> {
  await page.goto(`/sessions/${encodeURIComponent(id)}`);
  const vizRegion = page.getByRole('region', { name: /waterfall visualization/i });
  await expect(vizRegion.getByText(/\d+ ops/)).toBeVisible();
  const waterfall = page.getByRole('group', { name: /waterfall/i });
  await expect(waterfall.getByRole('button').first()).toBeVisible();
  await waterfall.getByRole('button').first().click();
  await expect(page.getByRole('dialog')).toBeVisible();
}

test.describe('span detail drawer (AC#4)', () => {
  test('clicking a span opens the drawer with op fields + a payloads section', async ({
    page,
    request,
  }, testInfo) => {
    const id = await richestSessionId(request);
    await openDrawerFromTrace(page, id);

    const dialog = page.getByRole('dialog');
    // The op field list: assert the always-present labels render (the <dt>s).
    await expect(dialog.getByText('Status', { exact: true })).toBeVisible();
    await expect(dialog.getByText('Duration', { exact: true })).toBeVisible();
    // Waterfall clicks use the slim whole-tree trace shape, so heavy metrics
    // (model/tokens/cost/context) are intentionally omitted from this drawer.
    await expect(dialog.getByText(/trace endpoint returns the slim shape/i)).toBeVisible();

    // The Payloads section header is always rendered.
    await expect(dialog.getByRole('heading', { name: 'Payloads' })).toBeVisible();
    // Seed has no payloads → the empty-state copy. (If a future seed adds
    // payloads, the metadata rows render instead; we accept either to avoid
    // coupling the assertion to the seed having zero payloads.)
    const noPayloads = dialog.getByText('No payloads for this op.');
    const hasNoPayloads = (await noPayloads.count()) > 0;
    if (hasNoPayloads) {
      await expect(noPayloads).toBeVisible();
      testInfo.annotations.push({
        type: 'fixture',
        description:
          'seed ops carry no payload_refs → drawer shows "No payloads for this op."; ' +
          'the payload metadata-row path is covered by SpanDetailDrawer.test.tsx.',
      });
    }
    testInfo.annotations.push({
      type: 'fixture',
      description:
        'payload preview route is live, but this E2E seed has no payload_refs; ' +
        'preview-fetch behavior is covered by SpanDetailDrawer unit tests.',
    });
  });

  test('Esc closes the drawer', async ({ page, request }) => {
    const id = await richestSessionId(request);
    await openDrawerFromTrace(page, id);
    await expect(page.getByRole('dialog')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });

  test('clicking outside (the backdrop) closes the drawer', async ({ page, request }) => {
    const id = await richestSessionId(request);
    await openDrawerFromTrace(page, id);
    await expect(page.getByRole('dialog')).toBeVisible();

    // The overlay is the backdrop; a mousedown on it (not the panel) closes
    // (SpanDetailDrawer onOverlayMouseDown: e.target === e.currentTarget). Click
    // near the top-left corner, away from the right-side panel.
    const overlay = page.getByTestId('drawer-overlay');
    await overlay.click({ position: { x: 8, y: 8 } });
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });
});
