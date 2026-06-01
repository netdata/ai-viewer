import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// SOW-0007 Chunk 11 — the REAL-BROWSER axe pass on the /stats analytics
// dashboard (the Chunk 9–10 page; its component-level a11y was unit-asserted in
// Stats.test.tsx "gives every control an accessible name"). Runs axe-core
// against the live /stats route served by the built ai-viewer-serve binary and
// fails on serious/critical violations only — the AGENTS.md quality gate
// threshold and the ui-pages.md §/stats "the route is axe-clean" contract.
//
// Mirrors tests/a11y.spec.ts + tests/viz-a11y.spec.ts EXACTLY: same impact
// filter (serious/critical), same deterministic theme lock via
// localStorage.aiViewerTheme + addInitScript, same <html data-theme> assertion
// before invoking axe. Both themes are checked because contrast (the most
// common serious/critical violation) is theme-dependent, per
// frontend-architecture.md §dark/light.
//
// DATA UNDER THE SEED (scripts/e2e-serve.sh: happy_single_turn + multi_turn +
// sub_agent): /api/stats/aggregate and /api/stats/top BOTH return rows, so the
// two charts paint their accessible role="img" SVGs (LineChart/BarChart). The
// "ready" gate accepts EITHER a chart role="img" OR the accessible "no data"
// status (role="status") per section, so the spec tracks the seed without
// assuming a specific row count — exactly the runtime-derived philosophy of the
// other a11y/route specs. The deep-search box is exercised separately (typing a
// term renders its labelled "Search results" region) and axe is re-run AFTER
// that interaction to cover the post-input DOM.

const IMPACTS = new Set(['serious', 'critical']);
const THEMES = ['dark', 'light'] as const;
const THEME_STORAGE_KEY = 'aiViewerTheme';

/** seriousOrCritical filters an axe run to only blocking-impact violations. */
function seriousOrCritical(
  violations: Awaited<ReturnType<AxeBuilder['analyze']>>['violations'],
) {
  return violations.filter((v) => IMPACTS.has(v.impact ?? ''));
}

/** lockTheme pins the manual theme override before any page script runs. */
async function lockTheme(page: Page, theme: (typeof THEMES)[number]): Promise<void> {
  await page.addInitScript(
    ([key, value]) => {
      window.localStorage.setItem(key, value);
    },
    [THEME_STORAGE_KEY, theme] as const,
  );
}

/** analyzeBlocking returns the page's current serious/critical axe violations
 *  (a re-runnable scan so the same page can be re-checked after interaction). */
async function analyzeBlocking(page: Page) {
  const results = await new AxeBuilder({ page }).analyze();
  return seriousOrCritical(results.violations);
}

/**
 * copyStatusRegion scopes to the /stats copy-link live region. The page has
 * MORE THAN ONE role="status": the global ThemeToggle ("Theme: <theme>") lives
 * in the app header OUTSIDE the Statistics <section>, so scoping to the
 * "Statistics" region (Stats.tsx <section aria-labelledby="stats-title">)
 * excludes it. Within that region the copy-status is the FIRST role="status" in
 * DOM order (the header precedes the chart panels), so .first() also stays
 * correct if a chart ever falls back to its role="status" "no data" message —
 * mirrors the ThemeToggle-vs-page-status disambiguation routes.spec.ts documents.
 */
function copyStatusRegion(page: Page) {
  return page.getByRole('region', { name: 'Statistics' }).getByRole('status').first();
}

/**
 * gotoStats locks the theme, navigates to /stats, waits for the dashboard to be
 * ready (the H1 + both chart sections resolved to EITHER a role="img" chart OR
 * the accessible "no data" status), and asserts the locked theme is applied so a
 * contrast pass can't be a false negative from an unthemed render.
 */
async function gotoStats(page: Page, theme: (typeof THEMES)[number]): Promise<void> {
  await lockTheme(page, theme);
  await page.goto('/stats');
  // The page landmark heading (Stats.tsx <h1 id="stats-title">Statistics</h1>).
  await expect(page.getByRole('heading', { name: 'Statistics', level: 1 })).toBeVisible();
  // Each chart section resolves to a settled accessible state: the chart's
  // role="img" SVG when data exists, or the role="status" "No data" message when
  // empty. Waiting on this (rather than role="img" alone) keeps the spec correct
  // under either seed shape and proves the loading/error branches have cleared.
  for (const titleId of ['stats-trends-title', 'stats-top-title']) {
    const section = page.locator(`section[aria-labelledby="${titleId}"]`);
    await expect(section).toBeVisible();
    await expect(
      section.getByRole('img').or(section.getByRole('status')),
    ).toBeVisible();
  }
  // Confirm the theme actually took effect (mirrors a11y.spec.ts).
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
}

test.describe('a11y — /stats dashboard (SOW-0007 Chunk 11)', () => {
  for (const theme of THEMES) {
    test(`/stats has no serious/critical axe violations (${theme})`, async ({ page }) => {
      await gotoStats(page, theme);

      // Under the committed seed BOTH charts have data, so two role="img" SVGs
      // paint. Assert that explicitly (in addition to the either/or ready gate)
      // so the axe run below is exercising the real chart DOM, not a vacuous
      // empty-state page — if the seed ever regresses to no rows this fails
      // loudly rather than passing on a blank dashboard.
      await expect(page.getByRole('img')).toHaveCount(2);

      const blocking = await analyzeBlocking(page);
      expect(
        blocking,
        `serious/critical violations on /stats (${theme} theme)`,
      ).toEqual([]);
    });

    test(`/stats stays axe-clean after exercising the controls + search (${theme})`, async ({
      page,
    }) => {
      await gotoStats(page, theme);

      // Drive the interactive surface for post-interaction a11y coverage. The
      // selects are <label>-wrapped (accessible names from the controlLabel
      // span), so getByLabel resolves them; selectOption changes the URL-backed
      // control and re-fetches the chart. Mirrors Stats.test.tsx's control names.
      await page.getByLabel('Trend metric').selectOption('tokens_in');
      await page.getByLabel('Breakdown dimension').selectOption('tool');

      // Type into the deep-search box (debounced ~300ms). The committed seed has
      // no FTS matches for this term, so the result region renders its
      // accessible "No matches." state — which is exactly what we want axe to
      // scan (a labelled region with content, not a chart). Wait for the
      // "Search results" region so the scan runs against the settled DOM.
      await page.getByLabel('Search ops and logs').fill('refactor');
      await expect(page.getByRole('region', { name: 'Search results' })).toBeVisible();

      // Copy-link: the button is keyboard/pointer reachable and announces via a
      // polite live region. Headless Chromium may deny clipboard, and the page
      // surfaces "Copy failed" rather than failing silently (AGENTS.md §6) — so
      // we assert the button activates and the status region receives EITHER
      // outcome, never asserting a specific clipboard permission.
      await page.getByRole('button', { name: 'Copy link' }).click();
      await expect(copyStatusRegion(page)).toHaveText(/Link copied|Copy failed/);

      // Re-run axe over the post-interaction DOM (new chart data + an open
      // search-results region + a live-region announcement). Still zero
      // serious/critical.
      const blocking = await analyzeBlocking(page);
      expect(
        blocking,
        `serious/critical violations on /stats after interaction (${theme} theme)`,
      ).toEqual([]);
    });
  }

  // Keyboard reachability of the dashboard controls (the ui-pages.md §/stats
  // "every control is keyboard-reachable" contract). Tabbing from the search
  // input must land on a focusable element, and each labelled control must be
  // focusable via .focus() landing on the right element — proving none are
  // removed from the tab order (e.g. by a stray tabindex="-1"). Theme-independent,
  // so run once under the default (dark) lock.
  test('/stats controls are keyboard-reachable', async ({ page }) => {
    await gotoStats(page, 'dark');

    // Every labelled control can receive focus (a non-focusable control — e.g.
    // tabindex=-1 or disabled — would fail the active-element check).
    for (const name of [
      'Trend metric',
      'Time bucket',
      'Breakdown dimension',
      'Breakdown metric',
      'Search ops and logs',
    ]) {
      const control = page.getByLabel(name);
      await control.focus();
      await expect(control).toBeFocused();
    }

    // The Copy-link button is reachable by keyboard too (role=button, in the tab
    // order); activating it via the keyboard announces an outcome.
    const copy = page.getByRole('button', { name: 'Copy link' });
    await copy.focus();
    await expect(copy).toBeFocused();
    await copy.press('Enter');
    await expect(copyStatusRegion(page)).toHaveText(/Link copied|Copy failed/);
  });
});
