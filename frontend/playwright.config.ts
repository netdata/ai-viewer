import { defineConfig, devices } from '@playwright/test';

// Config skeleton only (Chunk 14). The actual E2E specs (theme a11y under
// dark/light, OS-preference switching, route + realtime smoke) and the `e2e`
// npm script land in Chunk 18 — together with a `webServer` block that boots
// the Go `ai-viewer-serve` binary serving the embedded SPA against a seeded
// temp DB (the real end-to-end target), NOT a bare `vite preview`. There is
// intentionally NO `e2e` script in package.json yet, so CI skips the Playwright
// step until Chunk 18 lands specs. Tests will live under frontend/tests/.
export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:7710',
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
