import { test, expect, type Page } from '@playwright/test';

// Theme coverage (frontend-architecture.md §Theming; state/theme.ts +
// AppTopbar ThemeMenu). The dropdown sets a ThemePreference:
//   - explicit Dark/Light => persisted to localStorage 'aiViewerTheme' and
//     written to <html data-theme>; wins over the OS preference;
//   - Auto => clears the override and follows prefers-color-scheme.
// Menu item selectors use stable data-testid values from AppTopbar.tsx.

const html = (theme: 'dark' | 'light') => `html[data-theme="${theme}"]`;

async function chooseTheme(
  page: Page,
  testId: 'theme-auto' | 'theme-dark' | 'theme-light',
): Promise<void> {
  await page.getByRole('button', { name: /^Theme:/ }).click();
  await page.getByTestId(testId).click();
}

test.describe('theme', () => {
  test('explicit Dark / Light update <html data-theme> and persist across reload', async ({
    page,
  }) => {
    await page.goto('/');

    // Click Dark -> data-theme="dark".
    await chooseTheme(page, 'theme-dark');
    await expect(page.locator(html('dark'))).toHaveCount(1);
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');

    // Click Light -> data-theme="light".
    await chooseTheme(page, 'theme-light');
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
    await chooseTheme(page, 'theme-auto');
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
