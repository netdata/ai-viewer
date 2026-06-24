import { defineConfig, devices } from '@playwright/test';

const e2ePortValue = process.env.AI_VIEWER_E2E_PORT ?? '7710';
const e2ePort = Number(e2ePortValue);
if (!Number.isInteger(e2ePort) || e2ePort < 1 || e2ePort > 65_535) {
  throw new Error('AI_VIEWER_E2E_PORT must be an integer TCP port from 1 to 65535');
}

const e2eBaseURL = `http://127.0.0.1:${e2ePort}`;

// E2E config (Chunk 18). The specs under ./tests exercise the EMBEDDED SPA
// served by the built Go `ai-viewer-serve` binary against a deterministically
// seeded temp DB (the real end-to-end target), NOT a bare `vite preview`. The
// `webServer` block boots that binary via scripts/e2e-serve.sh, which ingests a
// fixed set of committed fixtures and then `exec`s the server on
// the selected localhost port — Playwright owns the process lifecycle and tears
// it down. The `e2e` npm script ("playwright test") gates whether CI runs this
// step.
export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // No GLOBAL retries (SOW-0012 AC#4 + Chunk-B handoff). A blanket `retries: 2`
  // masks real flakiness across the whole suite. Retries are opt-in PER SCENARIO
  // via `test.describe.configure({ retries: 1 })` and are scoped to the SSE flows
  // only (tests/sse-update.spec.ts, tests/realtime.spec.ts, tests/viz-sse.spec.ts)
  // — the connection handshake / EventSource timing is the one place CI-runner
  // slowness can legitimately cause a transient miss. Everything else must be
  // deterministic, so a failure there is a real defect, not retried away.
  retries: 0,
  // Per-test timeout budget (AC#4 / R3). 15 s is generous for the deterministic
  // DOM/network assertions on slower CI runners; the SSE flows raise this to 30 s
  // locally via `test.describe.configure({ timeout: 30_000 })` because the
  // subscription POST + EventSource open is the slowest checkpoint.
  timeout: 15_000,
  reporter: 'list',
  use: {
    baseURL: e2eBaseURL,
    trace: 'on-first-retry',
  },
  // Two projects (SOW-0012 AC#4):
  //   - `chromium` is the GATING suite. Its `testIgnore` excludes
  //     tests/quarantine/, so a genuinely flaky spec MOVED there (never
  //     `test.skip`-ed, always with a linked SOW in its header) stops blocking
  //     merge automatically. `npm run e2e` / `npm run e2e:a11y` run this project.
  //   - `quarantine` runs ONLY tests/quarantine/ (its own `testDir`), so
  //     quarantined specs still RUN (`npm run e2e:quarantine` →
  //     `--project=quarantine`) for diagnosis without gating. It is empty on
  //     delivery, so this is a forward-looking guard, not an active exclusion.
  // `npm run e2e` names the gating project explicitly so a bare `playwright
  // test` does not silently fold the non-gating quarantine project into the gate.
  projects: [
    {
      name: 'chromium',
      testIgnore: '**/quarantine/**',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'quarantine',
      testDir: './tests/quarantine',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  // Boot the PRE-BUILT single binary (scripts/build.sh must have run first) with
  // seeded data. cwd is frontend/, so the script path is repo-relative. The
  // server is ready once /api/health answers.
  //
  // reuseExistingServer is ALWAYS false (locally and in CI): the E2E target is
  // the freshly seeded binary, never a stray dev server. Reusing whatever already
  // listens on the selected port would run the specs against an arbitrary
  // server/DB and break the deterministic seeded-binary contract (D2/D3) —
  // tests would assert against unknown data. With reuse off, an occupied port
  // makes Playwright error loudly (correct) rather than silently testing the
  // wrong process.
  webServer: {
    command: `bash ../scripts/e2e-serve.sh ${e2ePort}`,
    url: `${e2eBaseURL}/api/health`,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
