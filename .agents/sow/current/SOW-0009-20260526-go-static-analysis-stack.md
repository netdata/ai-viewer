# SOW-0009 - Go Static Analysis Stack

## Status

Status: in progress

Sub-state: drafted 2026-05-26 as the first of the quality/CI cluster (with SOW-0010/0011). Activated 2026-06-02 under the operator's standing blanket mandate ("deliver the whole backlog autonomously, pick order yourself"). Re-scoped on activation: the original draft assumed it would run against a near-empty Phase-1 codebase ("No Go source yet … strict rules from the first lines"); that premise is dead — M1-M4 shipped ~36.7k LOC of production Go before this SOW ran, so this is a **retrofit** of the strict stack onto a mature, heavily-reviewed codebase, measured below.

## Requirements

### Purpose

Land a complete, locked-down Go static analysis chain that enforces every Go format/lint/static/security gate listed in `.agents/sow/specs/quality-gates.md` ("Go — Format/Vet/Lint/Security"). The chain enforces the same gate set in CI and locally, fails fast on any non-zero finding, and is version-pinned so reviewers see the same warnings the author saw (the precise CI-vs-local mechanics are in the paragraph below). This SOW closes the gap between the spec (which describes the gates) and the running system.

The chain enforces the **same gate set** in CI (the version-pinned `golangci-lint-action` reading `.golangci.yml`, plus standalone `gosec` and `govulncheck`) and locally (`./scripts/lint.sh`, the local mirror reading the same `.golangci.yml` + `.golangci-lint-version`). CI intentionally drives golangci through the cached action rather than invoking `scripts/lint.sh` directly (to keep golangci's analysis cache warm); consolidating CI onto a single aggregate script is SOW-0013 scope.

### User Request

Standing instruction: "every quality gate that can be automated, is automated" (`AGENTS.md` ownership model). The Quality Gates table in `AGENTS.md` and the catalog in `.agents/sow/specs/quality-gates.md` enumerate the Go static-analysis gates. The basic five (errcheck, govet, ineffassign, staticcheck, unused) + gofmt/goimports already run (landed incrementally with SOW-0001/M1-M4); gosec + govulncheck already run as standalone CI steps. This SOW lands the **remaining strict linter set**, the local `scripts/lint.sh`, the version pin, and the nightly govulncheck — and resolves every finding the strict set surfaces on the existing tree.

### Assistant Understanding

**Facts (live-repo audit, 2026-06-02):**

- golangci-lint on the workstation and in CI is **v2** (`/usr/bin/golangci-lint` 2.11.4). The config format is v2 (`version: "2"`, `linters.default` + `enable`, `formatters:` section, `linters.exclusions`).
- Current `.golangci.yml` enables only errcheck, govet, ineffassign, staticcheck, unused, with gofmt/goimports as formatters. A header comment reserves the strict set for this SOW.
- `.github/workflows/ci.yml` already has `lint` (gofmt/vet/golangci + standalone `gosec @v2.26.1 -severity medium -confidence medium ./...` + `govulncheck ./...`), `test`, `frontend`, `embed-smoke`, `gates` jobs. **No nightly schedule.** No `.golangci-lint-version` pin. No `scripts/lint.sh`.
- Production Go to lint: ~36.7k non-test LOC under `internal/` + `cmd/`.
- `grep -RnE '//\s*nolint([^:]|$)'` over `*.go` already returns **zero** un-linked nolint directives (AC#7 baseline clean).
- The sibling configs the original draft named (`~/src/ai-agent.git/.golangci.yml`, `~/src/netdata-ktsaou.git/.golangci.yml`) **do not exist** — no usable precedent to mirror. Tuning is grounded in golangci-lint v2 conventions + this codebase's measured nature instead.

**Measured strict-set delta (the real scope of this SOW):**

Enabling the full strict set (errorlint, gocritic, revive, gocyclo, misspell, nilerr, prealloc, unconvert, unparam, whitespace, bodyclose, noctx + gofumpt formatter) on the live tree, with `max-issues-per-linter: 0` / `max-same-issues: 0` (uncapped — the default 50/3 caps truncate counts):

| Linter | Raw (untuned) | After tuning | Disposition |
|---|---|---|---|
| gocyclo | ~50+ (capped) | **9** | min-complexity 25 + exclude `_test.go`; refactor the 1 genuine outlier |
| unparam | 32 | **13** | exclude `_test.go` + genfixtures; fix removable, suppress interface-required |
| revive | 25 | **9** | exclude `_test.go` from style rules; fix the 9 production |
| noctx | 18 | **1** | exclude `_test.go`; fix the 1 real production hit |
| prealloc | 15 | **1** | exclude `_test.go`; fix the 1 production |
| nilerr | 8 | **8** | all verified intentional+documented; suppress at-site with reasons |
| unconvert | 8 | **8** | mechanical: drop redundant conversions |
| gofumpt | 13 | **13** | auto-fix (`golangci-lint fmt`) — pure formatting, all in test files |
| errorlint | 2 | **2** | fix: `errors.Is` instead of `==`/`!=` (test fragility) |
| gocritic | 2 | **0** | both in `_test.go` → excluded |
| misspell / whitespace / bodyclose | 0 | **0** | clean |
| **TOTAL** | 158 (capped) | **64** | resolved to 0 via fixes + reasoned suppression + tuned config |

**Inferences:**

- The strict linters are stricter than golangci defaults; the config must enable each explicitly.
- `frontend/node_modules/flatted/golang/.../flatted.go` (a transitive npm dependency that ships a Go reference impl) is in the module tree and was being linted; it is not our code and must be path-excluded.
- Test files legitimately trip the *style/complexity* linters (table-driven tests are branchy; test setup uses context-less `DB.Exec`; test helpers have call-site-specific params). Excluding `_test.go` from gocyclo/noctx/unparam/prealloc/revive(style)/gocritic is standard golangci practice and does **not** weaken the bug-finders (errcheck/staticcheck/govet/ineffassign/unused/nilerr/errorlint/bodyclose stay active on tests).

**Unknowns (resolved during implementation, not blocking):**

- Exact gocyclo refactor shape for `validateDoc` (pricing/loader.go, complexity 31) — pure JSON validator, splits into per-section helpers, fully test-covered.
- Whether each of the 8 nilerr sites is truly intentional — spot-checks confirm the pattern (strict-decode-wins; file-rotated-away-is-not-fatal); the implementer verifies all 8 before suppressing any.

### Acceptance Criteria

1. `.golangci.yml` (v2) explicitly enables the full strict lint set: errcheck, govet, ineffassign, staticcheck, unused (existing) **plus** errorlint, gocritic, revive, gocyclo, misspell, nilerr, prealloc, unconvert, unparam, whitespace, bodyclose, noctx; formatters gofmt, goimports, gofumpt. `gosimple` is **not** listed (golangci v2 merged it into staticcheck). gosec stays a **standalone** CI step (not duplicated as a golangci linter). Project-specific tuning is recorded in inline comments. **Verification**: `golangci-lint config verify` passes; `golangci-lint linters` shows every listed linter Enabled.
2. `scripts/lint.sh` exists, is executable, uses the `run()` transparency helper, and runs (fail-fast): `golangci-lint run` (the umbrella — subsumes gofmt/goimports/gofumpt format-check, `go vet`, and every enabled linter), then standalone `gosec -severity medium -confidence medium ./...`, then `govulncheck ./...`. **Verification**: `bash -n scripts/lint.sh` parses clean; a run on the final tree exits 0 with no findings.
3. gosec runs with `-severity medium -confidence medium` and reports zero findings on the current tree (already wired in CI; `scripts/lint.sh` mirrors it). **Verification**: run output in Validation.
4. govulncheck runs **per push AND on a nightly schedule** (so a freshly-disclosed transitive CVE does not block an unrelated same-day PR — the nightly job is the standing gate). **Verification**: `ci.yml` keeps the per-push govulncheck; a scheduled workflow (`on.schedule`) runs it nightly; both visible via `gh workflow view`.
5. The CI `lint` job enforces the **same gate set** as `scripts/lint.sh` — `golangci-lint` at the pinned version (via `golangci/golangci-lint-action`, which reads `.golangci.yml` and caches the analysis cache) plus standalone `gosec` and `govulncheck` — so a warm run completes the Go static stack in under ~2 min. CI drives golangci through the cached action rather than invoking `scripts/lint.sh` directly (to keep the analysis cache); `scripts/lint.sh` is the local mirror reading the same `.golangci.yml` + `.golangci-lint-version`. Consolidating CI onto a single aggregate script is SOW-0013 scope. **Verification**: timed CI run logged in Validation; `.golangci-lint-version` is the single pin source for both.
6. `.golangci-lint-version` (or equivalent) pins the exact version; the CI workflow installs that exact version so an upgrade is an intentional, reviewable diff. **Verification**: file committed; CI reads it.
7. Zero **un-linked** `//nolint` directives. nilerr's 8 verified-intentional sites are suppressed at-site with a one-line reason explaining the deliberate nil-return (these are permanent architectural patterns — strict-decode-wins / poll-loop rotation-tolerance — not deferred debt, so the reason stands in for a tracking link; the nolint-policy refinement lands in `project-quality-gates`). **Verification**: `grep -RnE '//\s*nolint([^:]|$)' --include='*.go' .` returns no bare nolints; every `//nolint:rule // reason` is reviewed.
8. Specs/skills updated in lockstep: `.agents/sow/specs/quality-gates.md` (drop gosimple; record gocyclo min-complexity 25 + the `_test.go` style-linter exclusions + the node_modules path exclusion + gosec-standalone) and `.agents/skills/project-quality-gates/SKILL.md` (commands, linter set, nolint-policy refinement). **Verification**: spec-drift check clean; specs match what landed.

## Analysis

Sources checked:

- `.agents/sow/specs/quality-gates.md`, `.agents/skills/project-quality-gates/SKILL.md`, `AGENTS.md` Quality Gates table.
- Live `.golangci.yml`, `.github/workflows/ci.yml`.
- Measured golangci-lint v2.11.4 output (uncapped) across the live tree — counts in the table above.

Current state (2026-06-02):

- Basic five linters + gofmt/goimports enforced; gosec + govulncheck standalone in CI per-push. Missing: the 12 strict linters + gofumpt, `scripts/lint.sh`, version pin, govulncheck nightly.
- 64 residual findings under the tuned config (table above), resolved to zero by this SOW.

Risks:

- **R1 — linter false positives on idiomatic Go** (gocritic/revive/gocyclo): mitigated by config-level tuning with inline rationale (gocyclo threshold; `_test.go` exclusions; node_modules path exclusion) and, only where genuinely needed, at-site `//nolint:rule // reason`. No blanket disabling of a whole linter.
- **R2 — govulncheck flapping on fresh CVEs**: per-push job stays advisory for unrelated transitive CVEs; the nightly job is the standing gate (AC#4). Two jobs for clarity (not `continue-on-error`).
- **R3 — golangci-lint upgrade silently changing behavior**: version pin (AC#6); upgrades land as their own PR.
- **R4 — refactoring stable code for a metric**: the gocyclo hits include the SOW-0007-hardened ingest event loop (`worker.run` 24, `applySessionStarted` 21) and heavily-reviewed stream processors (tailers/scanners/parsers 16-25). Refactoring those purely to satisfy complexity-15 is unjustified churn/regression-risk on freshly-stabilized hot paths. Mitigation: gocyclo min-complexity **25** (flags only the one genuine outlier, `validateDoc` 31, a pure validator that splits cleanly and is fully test-covered); the reviewed 21-24 functions are accepted under threshold with documented rationale. This is the fit-for-purpose call (quality + don't-add-risk), not the literal "max 15" the draft inherited.
- **R5 — CI runtime budget**: `actions/setup-go` module+build cache + golangci analysis cache; measure and tune to < 2 min warm.

## Pre-Implementation Gate

Status: ready (activated under blanket mandate 2026-06-02)

Problem / root-cause model:

- `AGENTS.md` commits to a strict Go static stack; the basic gates run but the strict set does not. The original "install before the codebase grows" framing is moot — the codebase already grew to ~36.7k LOC. So the task is a clean retrofit: enable the strict set, tune it to the codebase's measured nature (test exclusions, node_modules exclusion, a defensible gocyclo threshold), and resolve the 64 residual findings (fixes for real issues; reasoned suppression for the verified-intentional nilerr sites; config tuning for the test-noise classes).

Evidence reviewed:

- Measured finding table above (uncapped golangci-lint v2.11.4 run, candidate config schema-verified).
- nilerr spot-checks: `pricing/loader_null_check.go` (`return nil` is deliberate — "lets the strict error message win") and `aiagent_v2/tailer.go:206` (`return nil` is deliberate — "file may have been atomically renamed away mid-read; not fatal"). Both already carry explanatory code comments.
- noctx production hit: `presenter.go:342` (`DB.QueryRow` → `QueryRowContext`) — a real fix.
- The 9 gocyclo production functions and their complexities (table in execution log).

Affected contracts and surfaces:

- New: `scripts/lint.sh`, `.golangci-lint-version`, `.github/workflows/govulncheck-nightly.yml` (or a scheduled job).
- Modified config: `.golangci.yml` (strict set + tuning), `.github/workflows/ci.yml` (golangci-action reads the pin from `.golangci-lint-version` via a step output; standalone gosec/govulncheck retained), `.github/workflows/govulncheck-nightly.yml` (new).
- Modified production source (subagent-only): the 64-finding fixes — noctx×1, unconvert×8, errorlint×2, prealloc×1, revive×9, unparam×13, gofumpt×13 (auto), nilerr×8 (at-site suppression), `validateDoc` refactor×1. **Behavioral-change candidates** (test-first): `validateDoc` refactor (behavior must be byte-identical — pin with a test), any unparam signature change (compile-checked; add/adjust a test if a return value was being relied on), the noctx `QueryRowContext` change (pass the request/background context — pin behavior).
- Unaffected: frontend, fixtures, migrations, REST/SSE contracts, data model.

Spec deltas to land before any test/code:

1. `.agents/sow/specs/quality-gates.md` "Go — Lint": replace the linter list to drop `gosimple` (v2-merged into staticcheck); add the gocyclo `min-complexity: 25` value with the R4 rationale; document the `_test.go` style/complexity exclusions and the `frontend/node_modules` path exclusion; note gosec runs standalone (not as a golangci linter).
2. `.agents/skills/project-quality-gates/SKILL.md`: update the command list (golangci-lint as the umbrella that subsumes gofmt/goimports/vet; `scripts/lint.sh` order); update the enabled-linter set; **refine the nolint policy** — permanent, architectural, reasoned suppressions (e.g. nilerr on a deliberate-nil-return) are allowed with a `// reason` comment and need no tracking link; only *deferred-fix* suppressions need an issue/SOW link.

Existing patterns to reuse:

- `AGENTS.md` "Transparency in scripts" `run()` helper for `scripts/lint.sh`.
- The existing `ci.yml` `lint` job shape (extend, do not rewrite) and its gosec/govulncheck steps.
- golangci-lint v2 upstream config schema (verified locally with `golangci-lint config verify`).

Risk and blast radius:

- Config/scripts/CI: local + CI ergonomics only; recoverable by editing config.
- Source fixes: 51 are mechanical/auto/suppression (no behavior change); the behavioral-change candidates (validateDoc refactor, noctx context threading, unparam signature trims) are pinned by tests-first and are compile-checked. Blast radius is contained to the touched functions; the full `go test -race ./...` + the SOW-0007 rollup parity/lifecycle gates guard the ingest hot paths.

Sensitive data handling plan:

- All artifacts (config, scripts, CI, specs) are public. No user data in gosec output (source-only). The implementer confirms no inline comment references a customer name/URL. `scripts/scan-secrets.sh` runs as the last gate before every commit.

Implementation plan:

1. **Spec deltas first** (master): land the two spec/skill edits above.
2. **Config** (master, owns top-level config): finalize `.golangci.yml` (strict set + gocyclo 25 + `_test.go` style exclusions + `frontend/node_modules` path exclusion + genfixtures unparam exclusion + uncapped issues). `golangci-lint config verify`.
3. **Tooling** (master): `scripts/lint.sh` (run() helper; golangci umbrella + gosec + govulncheck), `.golangci-lint-version` = 2.11.4, `chmod +x`.
4. **CI** (master): `ci.yml` lint job drives golangci via the action at the pinned version (read from `.golangci-lint-version`), keeps standalone gosec/govulncheck; add `govulncheck-nightly.yml` (`on.schedule`). CI keeps the cached action; `scripts/lint.sh` is the local mirror — CI-runs-aggregate-script is SOW-0013.
5. **Source fixes** (delegated subagent, after all master edits — tree-clobber discipline): write/adjust tests first for the behavioral-change candidates, then apply all 64 fixes; subagent is **forbidden** from running external reviewers (orchestrator owns review).
6. **Gates** (master): `golangci-lint run` = 0; `go test -race ./...` green; `go vet`; gosec; govulncheck; frontend untouched; `scripts/scan-secrets.sh` + AI-attribution scan clean.
7. **Commit** spec + config + scripts + CI + fixes together (no attribution trailer), branch, push, PR.
8. **External review** (codex + glm + minimax in parallel, repo-root, background, timeout 1800) on the full diff; adjudicate on ground truth (codex decisive); iterate same-scope until codex is clean.
9. **Merge** (`gh pr merge --merge --delete-branch`); move SOW to `done/` with `Status: completed` in the same commit context; record Validation/Reviews/Outcome/Lessons.

Validation plan:

- AC#1-8 each have a named verification (above). Evidence captured in Validation at close.
- `golangci-lint run` on the final tree: zero findings (the gate).
- `go test -race ./...`: all pass (guards the validateDoc refactor + noctx + unparam changes).
- CI: warm-run timing logged; nightly workflow visible.
- Reviewers confirm no gate silently weakened, no bare nolint, no behavior regression from the refactor.

Artifact impact plan:

- `AGENTS.md`: no change (already lists these gates).
- Specs: `quality-gates.md` updated (deltas above). Skills: `project-quality-gates` updated (deltas above).
- End-user docs: none.
- SOW lifecycle: on success, move to `.agents/sow/done/` with `Status: completed`.

Open-source reference evidence:

- `golangci/golangci-lint @ v2.11.4` config schema (verified locally via `golangci-lint config verify`). No workstation absolute paths recorded for external OSS.

Open decisions:

- None blocking. gocyclo threshold (25), the `_test.go` style exclusions, node_modules exclusion, govulncheck two-jobs-not-continue-on-error, and nilerr at-site-reasoned-suppression are CTO calls inside scope, documented above.

## Implications And Decisions

No operator decisions required. All choices are technical and within the assistant's autonomous scope per the ownership model. The notable deviation from the inherited draft — gocyclo `min-complexity: 25` instead of a literal `15`, with `_test.go` excluded from the style/complexity linters — is documented (R4) as the fit-for-purpose call that avoids churning ~36.7k LOC of heavily-reviewed, hot-path code for a metric while still enforcing a real forward complexity gate.

## Plan

1. Spec/skill deltas (master).
2. `.golangci.yml` strict set + tuning + `golangci-lint config verify` (master).
3. `scripts/lint.sh` + `.golangci-lint-version` (master).
4. CI lint job (golangci-action at the pinned version + standalone gosec/govulncheck) + govulncheck nightly (master).
5. Delegated source fixes (64 findings; tests-first for behavioral ones).
6. Gates + scan + commit + PR.
7. External review (codex/glm/minimax) → converge on codex-clean.
8. Merge + close to done/.

## Execution Log

### 2026-06-02 — Activation + measurement (master)

- Audited live repo: golangci-lint v2.11.4 (v2 config); current `.golangci.yml` = basic 5 + gofmt/goimports; `ci.yml` already has standalone gosec + govulncheck per-push (AC#3 satisfied); zero bare nolints (AC#7 baseline clean); no `scripts/lint.sh`, no version pin, no nightly.
- Measured the full strict set uncapped: 64 residual under the tuned candidate config (table in Assistant Understanding). Schema-verified the candidate with `golangci-lint config verify`.
- gocyclo production refactor candidates (complexity): `validateDoc` 31 (pricing/loader.go), `readRollout` 25 (codex/scanner.go), `worker.run` 24 (ingest/worker.go), `mapEventMsg` 22 (codex/ops_event.go), `onOpStarted` 22 (ingest/catalog.go), `discoverRollouts` 21 (codex/discovery.go), `tailLoop` 21 (claude_code/tailer.go), `applySessionStarted` 21 (ingest/writer.go), `stripSchemaPrefix` 21 (store/store.go). Decision (R4): min-complexity 25 → flags only `validateDoc` (refactor it); accept the reviewed 21-24 functions.
- Decisions recorded in ACs + Pre-Implementation Gate. SOW moved pending/ → current/.

### 2026-06-02 — Implementation + gate verification

- Master-owned (config/tooling/specs): `.golangci.yml` strict set + tuning (schema-verified); `.golangci-lint-version` = v2.11.4; `scripts/lint.sh` (local mirror); `ci.yml` reads the pin via a step output; `.github/workflows/govulncheck-nightly.yml`; spec + skill deltas.
- Delegated (subagent) the 57 source fixes; master verified independently (did NOT trust the subagent summary):
  - gofumpt 13 (reformatted via `golangci-lint fmt`); unconvert 8 (removed redundant `json.RawMessage` casts); revive 9 (3 unused-param→`_`, 3 `max`-shadow renames, 1 exported-doc, 1 ctx-as-arg reorder + 22 callers, 1 var-declaration suppressed-with-reason); prealloc 1; noctx 1 (`CheckSchema` threaded the real request ctx → `QueryRowContext`); errorlint 2 (`errors.Is`); nilerr 8 (all suppressed at-site with site-specific reasons — verified intentional); unparam 14 (6 removed + 35 callers updated, 8 interface/dispatch-required suppressed with verified reasons); gocyclo 1 (`pricing.validateDoc` 31 → refactored into `validateDocHeader`/`validateProvider`/`validateModel`, byte-identical, existing `TestLoaderValidationCases` covers it).
- Highest-risk changes spot-checked by master: `writeJSON` was genuinely always-`StatusOK` (errors use a separate `writeJSONError` path); `validateDoc` helpers thread the dedup maps correctly.
- 60 `.go` files changed; scope clean (no edits outside `internal/`+`cmd/` beyond the master-owned config/specs/SOW).
- Pre-existing finding filed as a follow-up (not in scope): a `bufio.Scanner` loop in `claude_code/scanner.go:~1058` ignores `sc.Err()` (silent-truncation class; gopls `scannererr`, not a golangci finding). Audit task spawned.

## Validation

All gates re-run by the master orchestrator (not trusting the subagent), 2026-06-02:

- `/usr/bin/golangci-lint run ./...` → `0 issues` (exit 0). AC#1.
- `golangci-lint config verify` → valid. AC#1.
- `go vet ./...` → clean.
- `go test -race -count=1 ./...` → all 15 packages pass, race-clean (fresh, cache-defeated). Proves the 60-file change set (param removals, ctx threading, validateDoc refactor) preserved behavior.
- `gosec -severity medium -confidence medium ./...` → `Issues: 0` (exit 0). AC#3.
- `scripts/scan-secrets.sh` → PASS (768 tracked files); self-test 21/21. `scripts/scan-ai-attribution.sh` → PASS.
- `bash -n scripts/lint.sh` → parses clean; AC#2 order = golangci umbrella → gosec → govulncheck.
- AC#6: `.golangci-lint-version` committed; `ci.yml` installs that exact version via a read-file step output. AC#4: per-push govulncheck retained + `govulncheck-nightly.yml` added (`on.schedule`). AC#7: `grep -RnE '//\s*nolint([^:]|$)'` → no bare nolints; every `//nolint:rule` carries a reason.
- CI warm-run timing (AC#5) + nightly-trigger visibility (`gh workflow view`) to be captured from the PR run.

## Reviews

### Round 1 — 2026-06-02 (codex + glm + minimax on PR #35, commit 45c5107)

- **glm** (`glm-5.1`): **0 blocking; would merge.** Verified every risk area — param removals drop no errors, `validateDoc`/`CheckSchema`/errorlint/`max`-rename behavior-preserving, all 8 nilerr + 8 unparam + 1 revive suppressions justified, config sound, no injection. Two non-blocking notes: (a) `metaHashes` caller now runs `repairChangedMetas` unconditionally — **master verified byte-identical** (old `metaHashes` had no `return out, <non-nil>` path, so the old `if herr == nil` guard was always true); (b) pre-existing `claude_code/scanner.go:~1058` scanner ignores `sc.Err()` — already filed as a follow-up.
- **codex** (`gpt-5.5`, decisive): **runtime code mergeable** (all requested checks pass — validateDoc same order/messages/dedup, unparam removals safe, CheckSchema ctx long-lived/correct, errorlint preserves intent, max renames pure, suppressions justified, gocyclo 25 reasonable, exclusions acceptable, no gosec/injection issue). **BLOCKED merge on one real finding: quality-gate contract drift** — SOW/specs/skills did not match the implementation. Adjudicated on ground truth: **REAL**. Fixed all sub-points:
  - SOW said "CI runs `scripts/lint.sh`" (lines 13/66/110/139/178) → CI actually uses the cached `golangci-lint-action`. Resolution = codex's Option B: documented that CI intentionally uses the cached action enforcing the same `.golangci.yml`+pin; `scripts/lint.sh` is the local mirror; CI-runs-aggregate-script is SOW-0013 scope.
  - `quality-gates.md` "Aggregate Scripts" said `scripts/lint.sh` "not yet present" → updated (it exists; test.sh/gates.sh remain SOW-0013).
  - `project-go-backend` skill listed the stale linter set (`gosimple`/`gosec`) → updated to the strict set + points to `project-quality-gates` as authoritative.
  - `project-quality-gates` skill said CI installs `gosec@latest` → reconciled to the actual `@v2.26.1` pin.
  - `scripts/lint.sh` reused any existing gosec → now always (re)installs the pinned `gosec@v2.26.1` (a `go install`d gosec self-reports "dev", so install-time pinning is the only reliable guarantee); golangci stays a documented warn (dev's own tool; CI enforces the pin authoritatively).
- **minimax** (`MiniMax-M3`): **would merge.** Independently re-ran golangci at `min-complexity 30` (only excluded-region hits → confirms 25 is well-calibrated), gosec (0), govulncheck (0 in code). All suppressions comply with the refined nolint policy. Four non-blocking notes: (1) pre-existing slow test `TestRefreshRollups_OtherStaleRowRemoval` ~240s under `-race` makes the 15-min CI `test` job tight (this PR only gofumpt-reformatted those files) → filed in Followup; (2) unparam "removed" count is conservative (it counts 9 simplifications; SOW enumerates 6 removed + 8 suppressed) — cosmetic; (3) validateDoc relies on the existing 33-case `TestLoaderValidationCases` rather than a new direct-helper test — adequate (codex+glm+minimax all verified byte-identical), declined adding a redundant test; (4) scanner `sc.Err()` correctly out of scope (filed).
- **Adjudication**: glm + minimax would merge; codex (decisive) blocked only on doc drift, now fixed. Resolution commit: doc-alignment only (no `.go` changes); gates unaffected (`golangci-lint run` still 0, tests untouched). Round 2 re-runs all three on the fixed state.

### Round 2 — 2026-06-02 (re-review on commit 4c17fe6 after the round-1 doc-alignment)

- **glm** (`glm-5.1`): **MERGE.** Confirmed the gate contract is now consistent across all six surfaces (CI, lint.sh, quality-gates.md, project-quality-gates, project-go-backend, SOW); no new issues from the doc/script changes; all round-1 findings resolved.
- **codex** (`gpt-5.5`, decisive): runtime still mergeable (re-verified validateDoc, removed params, CheckSchema ctx, errorlint, max-rename, suppressions, config, security — all pass). **BLOCKED on two findings**, both adjudicated REAL:
  1. **Residual doc drift** — round-1 fixed only the lines codex cited, not the whole class (a fix-all-instances failure). codex found SOW-0009:13 still self-contradicted the paragraph below it, and `project-quality-gates`:226/234 + `workflow.md`:74 still claimed "CI runs the same gates from the same scripts" / listed `test.sh`/`gates.sh` as present. **Round-3 fix = comprehensive honesty sweep across ALL durable runtime docs**: SOW:13, the project-quality-gates Aggregate Scripts block, workflow.md:74, project-coding:50, project-testing:164, AGENTS.md (secret-scanner ref + Build/Test/Run caveat). Every doc now states current state honestly (lint.sh exists; test.sh = SOW-0010, gates.sh/spec-drift.sh = SOW-0013; CI uses per-gate jobs). Pending/done SOWs left untouched (they correctly describe future/past work).
  2. **lint.sh GOBIN bug** (non-blocking but real) — hardcoded `GOBIN=$(go env GOPATH)/bin` but `go install` honors `$GOBIN` when set → could run a stale/missing binary. **Round-3 fix** = resolve GOBIN the way Go does (`go env GOBIN` if set, else GOPATH/bin). Verified: `./scripts/lint.sh` runs end-to-end clean (exit 0).
- **minimax** (`MiniMax-M3`): in flight at this writing; folded into the round-3 re-review.
- Round-3 resolution commit: docs + lint.sh only; no `.go` changed (`golangci-lint run` still 0, `./scripts/lint.sh` exit 0).

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- **CI test-timing (minimax R1 note, non-blocking, pre-existing)**: `internal/ingest/rollup_refresh_test.go`'s `TestRefreshRollups_OtherStaleRowRemoval` runs ~240s under `-race -count=1`; the CI `test` job's 15-min budget is comfortable today but tightens as rollup tests grow. NOT introduced by SOW-0009 (this PR only gofumpt-reformatted those files). Fold into **SOW-0010** (test infra/coverage): parallelize or shrink the fixture before more rollup tests land.
- **Scanner `sc.Err()` audit**: pre-existing `bufio.Scanner` loop in `claude_code/scanner.go:~1058` ignores `sc.Err()` (silent-truncation class; gopls `scannererr`, not a golangci finding). Out of SOW-0009 scope; spawned as a separate task.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
