# Quarantined E2E tests

This directory holds Playwright E2E specs that are **genuinely flaky** and have
been moved out of the gating suite while a linked SOW fixes the root cause.

It is **empty on delivery** (SOW-0012 AC#4). A spec lives here only while a SOW
to de-flake it is open.

## Policy (SOW-0012 AC#4)

- **`test.skip` is forbidden as a way to land.** A flaky test is not silenced —
  it is quarantined. A quarantined spec is **runnable on demand** via
  `npm run e2e:quarantine` (a **manual/diagnostic, non-gating** run); it **does
  not gate merge** until its linked SOW resolves.
- **CI does not run the quarantine suite today.** The CI `frontend` job runs only
  the gating `npm run e2e` (the `chromium` project, which excludes this dir). The
  quarantine project is therefore a local diagnostic until the dir is populated.
  When a spec is first quarantined, add a dedicated CI step that runs
  `npm run e2e:quarantine` with **`continue-on-error: true`** (so it is visible in
  CI but never blocks merge) — do **not** fold it into the gating `e2e` step.
- The **gating** run (`npm run e2e`, and `npm run e2e:a11y`) **excludes** this
  directory via `playwright.config.ts` (`testIgnore: '**/quarantine/**'`), so a
  spec moved here automatically stops blocking CI without any `test.skip`.
- Every quarantined spec **must** carry, in its file header, the **SOW filename**
  tracking the de-flake work, plus a one-line note of the observed flake. Example:

  ```ts
  // QUARANTINED — flaky on CI runners (~1/20): the SSE stream occasionally opens
  // after the 30 s budget under load. Tracked by
  // .agents/sow/pending/SOW-00NN-deflake-sse-stream.md. Do NOT add test.skip;
  // this file runs via `npm run e2e:quarantine` and is excluded from the gate.
  ```

## Workflow when a test flakes 3+ times

1. File a SOW in `.agents/sow/pending/` describing the flake and the planned
   deterministic fix (fake clock, seeded fixture, controlled event, …).
2. Move the spec here and add the header note above (with the SOW filename).
   Add a `continue-on-error: true` CI step running `npm run e2e:quarantine` so it
   is visible in CI without gating (the dir is empty today, so no such step exists
   yet).
3. Verify the gating suite (`npm run e2e`) is green without it.
4. When the SOW lands the fix, move the spec back under `frontend/tests/` and
   delete its quarantine header. This directory returns to empty.

## Scripts

- `npm run e2e` — the **gating** suite. Excludes `quarantine/`.
- `npm run e2e:a11y` — the **gating** axe suite (the three `*-a11y.spec.ts`).
- `npm run e2e:quarantine` — runs **only** this directory (non-gating;
  diagnostic). A no-op while the directory is empty.
