import { test, expect } from '@playwright/test';

// Theme coverage (frontend-architecture.md §Theming; state/theme.ts +
// components/ThemeToggle). The 3-button segmented control sets a ThemePreference:
//   - explicit Dark/Light => persisted to localStorage 'aiViewerTheme' and
//     written to <html data-theme>; wins over the OS preference;
//   - Auto => clears the override and follows prefers-color-scheme.
// Button selectors use the STATIC aria-labels from ThemeToggle.tsx.

const html = (theme: 'dark' | 'light') => `html[data-theme="${theme}"]`;

test.describe('theme', () => {
  test('explicit Dark / Light update <html data-theme> and persist across reload', async ({
    page,
  }) => {
    await page.goto('/');

    // Click Dark -> data-theme="dark".
    await page.getByRole('button', { name: 'Dark' }).click();
    await expect(page.locator(html('dark'))).toHaveCount(1);
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');

    // Click Light -> data-theme="light".
    await page.getByRole('button', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');

    // The explicit choice is persisted under the documented key.
    const stored = await page.evaluate(() => window.localStorage.getItem('aiViewerTheme'));
    expect(stored).toBe('light');

    // After a reload the persisted explicit choice is reapplied.
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  });

  test('Auto follows the OS prefers-color-scheme', async ({ page }) => {
    await page.goto('/');

    // Select Auto: clears any override so the OS preference drives the theme.
    await page.getByRole('button', { name: 'Auto (follow system)' }).click();
    // Auto must NOT persist an override (state/theme.ts persistPreference).
    expect(
      await page.evaluate(() => window.localStorage.getItem('aiViewerTheme')),
    ).toBeNull();

    // Emulated OS dark -> resolved dark.
    await page.emulateMedia({ colorScheme: 'dark' });
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');

    // Flip the OS preference to light -> resolved follows to light (no reload).
    await page.emulateMedia({ colorScheme: 'light' });
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  });
});
