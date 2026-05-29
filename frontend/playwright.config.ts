import { defineConfig, devices } from '@playwright/test';

// E2E config (Chunk 18). The specs under ./tests exercise the EMBEDDED SPA
// served by the built Go `ai-viewer-serve` binary against a deterministically
// seeded temp DB (the real end-to-end target), NOT a bare `vite preview`. The
// `webServer` block boots that binary via scripts/e2e-serve.sh, which ingests a
// fixed set of committed fixtures and then `exec`s the server on
// 127.0.0.1:7710 — Playwright owns the process lifecycle and tears it down. The
// `e2e` npm script ("playwright test") gates whether CI runs this step.
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
  // Boot the PRE-BUILT single binary (scripts/build.sh must have run first) with
  // seeded data. cwd is frontend/, so the script path is repo-relative. The
  // server is ready once /api/health answers.
  //
  // reuseExistingServer is ALWAYS false (locally and in CI): the E2E target is
  // the freshly seeded binary, never a stray dev server. Reusing whatever already
  // listens on 7710 would run the specs against an arbitrary server/DB and break
  // the deterministic seeded-binary contract (D2/D3) — tests would assert against
  // unknown data. With reuse off, an occupied 7710 makes Playwright error loudly
  // (correct) rather than silently testing the wrong process.
  webServer: {
    command: 'bash ../scripts/e2e-serve.sh',
    url: 'http://127.0.0.1:7710/api/health',
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
