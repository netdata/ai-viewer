import { defineConfig, devices } from '@playwright/test';

// Config only for Chunk 14 — actual E2E specs (theme a11y under dark/light,
// OS-preference switching, route smoke) land in Chunk 18. Tests live under
// frontend/tests/. The webServer block boots the Vite preview of the built
// app so E2E runs against production output; CI will build first.
export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    command: 'npm run preview',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
