import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// SOW-0006 AC#5 — the REAL-BROWSER axe pass on the new viz surfaces (the
// component-level jest-axe work in chunk "a11y" prepared for this). Runs axe-core
// against the live routes served by the built binary and fails on
// serious/critical violations only (the AGENTS.md gate threshold). Mirrors
// tests/a11y.spec.ts exactly (same impact filter, same deterministic theme lock
// via localStorage.aiViewerTheme + addInitScript, same data-theme assertion),
// extended to the four AC#5 routes:
//   - /sessions/:id ?tab=trace
//   - /sessions/:id ?tab=topology
//   - /sessions/:id ?tab=timeline
//   - /topology (cross-session)
// Both themes are checked because contrast (the most common serious/critical
// violation) is theme-dependent, per frontend-architecture.md §dark/light.
// Session ids are derived at runtime from /api/sessions (never hardcoded).

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

/** axeRoute navigates, waits for the route's ready signal, asserts the locked
 *  theme is applied, then returns the blocking violations for that route+theme. */
async function axeRoute(
  page: Page,
  theme: (typeof THEMES)[number],
  path: string,
  ready: (p: Page) => Promise<void>,
) {
  await lockTheme(page, theme);
  await page.goto(path);
  await ready(page);
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
  const results = await new AxeBuilder({ page }).analyze();
  return seriousOrCritical(results.violations);
}

/** firstSessionId pulls a real seeded session id from the REST API. */
async function firstSessionId(page: Page): Promise<string> {
  const resp = await page.request.get('/api/sessions');
  expect(resp.ok()).toBeTruthy();
  const body = (await resp.json()) as { items: Array<{ id: string }> };
  expect(body.items.length).toBeGreaterThanOrEqual(1);
  return body.items[0]!.id;
}

test.describe('a11y — viz surfaces (AC#5)', () => {
  for (const theme of THEMES) {
    test(`Trace tab has no serious/critical axe violations (${theme})`, async ({ page }) => {
      const id = await firstSessionId(page);
      const blocking = await axeRoute(
        page,
        theme,
        `/sessions/${encodeURIComponent(id)}?tab=trace`,
        async (p) => {
          await expect(p.getByText(/\d+ ops/)).toBeVisible();
          await expect(p.getByRole('group', { name: /waterfall/i })).toBeVisible();
        },
      );
      expect(
        blocking,
        `serious/critical violations on Trace tab (${theme} theme)`,
      ).toEqual([]);
    });

    test(`Topology tab has no serious/critical axe violations (${theme})`, async ({ page }) => {
      const id = await firstSessionId(page);
      const blocking = await axeRoute(
        page,
        theme,
        `/sessions/${encodeURIComponent(id)}?tab=topology`,
        async (p) => {
          await expect(p.getByText(/\d+ nodes?/)).toBeVisible();
          await expect(p.getByRole('group', { name: /topology/i })).toBeVisible();
        },
      );
      expect(
        blocking,
        `serious/critical violations on Topology tab (${theme} theme)`,
      ).toEqual([]);
    });

    test(`Timeline tab has no serious/critical axe violations (${theme})`, async ({ page }) => {
      const id = await firstSessionId(page);
      const blocking = await axeRoute(
        page,
        theme,
        `/sessions/${encodeURIComponent(id)}?tab=timeline`,
        async (p) => {
          await expect(p.getByText(/\d+ spans?\s+·\s+\d+ lanes?/)).toBeVisible();
          await expect(p.getByRole('group', { name: /timeline/i })).toBeVisible();
        },
      );
      expect(
        blocking,
        `serious/critical violations on Timeline tab (${theme} theme)`,
      ).toEqual([]);
    });

    test(`/topology (cross-session) has no serious/critical axe violations (${theme})`, async ({
      page,
    }) => {
      const blocking = await axeRoute(page, theme, '/topology', async (p) => {
        await expect(p.getByRole('heading', { name: 'Topology', level: 1 })).toBeVisible();
        await expect(p.getByRole('group', { name: /topology/i })).toBeVisible();
      });
      expect(
        blocking,
        `serious/critical violations on /topology (${theme} theme)`,
      ).toEqual([]);
    });
  }
});
