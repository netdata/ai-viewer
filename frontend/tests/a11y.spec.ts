import { test, expect, type Page } from '@playwright/test';
import { AxeBuilder } from '@axe-core/playwright';
import { THEME_PREFERENCE_STORAGE_NAME as THEME_STORAGE_SLOT } from '../src/state/theme';

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
    ([storageSlot, value]) => {
      window.localStorage.setItem(storageSlot, value);
    },
    [THEME_STORAGE_SLOT, theme] as const,
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
          await expect(p.getByRole('group', { name: 'Session overview' })).toBeVisible();
          await expect(p.getByRole('region', { name: /waterfall visualization/i })).toBeVisible();
          await expect(p.getByRole('region', { name: 'Turn view' })).toBeVisible();
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

    // The NotFound catch-all is still a route the operator can reach, so the
    // "axe on every route" gate must cover it — a stub page can still ship a
    // contrast or landmark violation. It renders an <h1> as its accessible
    // landmark; we key the ready-gate on that heading by name.
    // (/tools, /models, /agents were removed in commit 861b45b — the routes no
    // longer exist, so only the NotFound `*` route remains in this block.)
    const CATCH_ALL_ROUTES: ReadonlyArray<{ path: string; heading: string; label: string }> = [
      { path: '/no-such-route', heading: 'Not found', label: '* (NotFound)' },
    ];
    for (const { path, heading, label } of CATCH_ALL_ROUTES) {
      test(`${label} has no serious/critical axe violations (${theme})`, async ({ page }) => {
        const blocking = await axeRoute(page, theme, path, async (p) => {
          await expect(p.getByRole('heading', { name: heading, level: 1 })).toBeVisible();
        });
        expect(blocking, `serious/critical violations on ${label} (${theme} theme)`).toEqual([]);
      });
    }
  }
});
