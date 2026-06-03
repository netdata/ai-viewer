import { test, expect, type Page } from '@playwright/test';
import { AxeBuilder } from '@axe-core/playwright';

// Accessibility coverage (AGENTS.md quality gate: axe, zero serious/critical on
// every live route). We run axe-core against each live route served by the built
// binary and fail only on serious/critical violations (the gate's threshold;
// minor/moderate are reported but non-blocking here).
//
// Each route is checked under BOTH themes: contrast (the most common
// serious/critical violation) is theme-dependent, and frontend-architecture.md
// §dark/light requires both to be accessible. We lock the theme deterministically
// per run via localStorage.aiViewerTheme (a manual override wins over the OS in
// both index.html's no-flash script and state/theme.ts), set through
// addInitScript so it runs before the page's own scripts, and we assert
// <html data-theme> matches before invoking axe.

const IMPACTS = new Set(['serious', 'critical']);
const THEMES = ['dark', 'light'] as const;
const THEME_STORAGE_KEY = 'aiViewerTheme';

/** seriousOrCritical filters an axe run to only the blocking-impact violations.
 *  axe's `impact` is optional (`ImpactValue | undefined | null`), so we coerce a
 *  missing impact to '' before the Set lookup — no non-null assertion needed. */
function seriousOrCritical(
  violations: Awaited<ReturnType<AxeBuilder['analyze']>>['violations'],
) {
  return violations.filter((v) => IMPACTS.has(v.impact ?? ''));
}

/** lockTheme pins the manual theme override for every document this page loads,
 *  BEFORE any page script runs (so the no-flash IIFE reads it on first paint). */
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
  // Confirm the theme actually took effect so a contrast pass can't be a
  // false negative from an unthemed (default) render.
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
  const results = await new AxeBuilder({ page }).analyze();
  return seriousOrCritical(results.violations);
}

test.describe('a11y', () => {
  for (const theme of THEMES) {
    test(`/ has no serious/critical axe violations (${theme})`, async ({ page }) => {
      const blocking = await axeRoute(page, theme, '/', async (p) => {
        await expect(p.getByRole('heading', { name: 'Sessions', level: 1 })).toBeVisible();
      });
      expect(blocking, `serious/critical violations on / (${theme} theme)`).toEqual([]);
    });

    test(`a seeded /sessions/<id> has no serious/critical axe violations (${theme})`, async ({
      page,
      request,
    }) => {
      // Derive a real session id from the running binary's seeded DB.
      const resp = await request.get('/api/sessions');
      expect(resp.ok()).toBeTruthy();
      const body = (await resp.json()) as { items: Array<{ id: string }> };
      expect(body.items.length).toBeGreaterThanOrEqual(1);
      const id = body.items[0]!.id;

      const blocking = await axeRoute(
        page,
        theme,
        `/sessions/${encodeURIComponent(id)}`,
        async (p) => {
          await expect(p.getByRole('heading', { name: 'Tools used' })).toBeVisible();
        },
      );
      expect(
        blocking,
        `serious/critical violations on /sessions/<id> (${theme} theme)`,
      ).toEqual([]);
    });

    test(`/sources has no serious/critical axe violations (${theme})`, async ({ page }) => {
      const blocking = await axeRoute(page, theme, '/sources', async (p) => {
        await expect(p.getByRole('heading', { name: 'Sources', level: 1 })).toBeVisible();
      });
      expect(blocking, `serious/critical violations on /sources (${theme} theme)`).toEqual([]);
    });
  }
});
