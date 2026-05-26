# SOW-0001 - Phase 1 Foundation: ai-agent v3 + v2 ingest, minimal UI

## Status

Status: in-progress

Sub-state: moved to current/ on 2026-05-26 after operator approval. SOW-0002 (cross-format data model analysis) is a prerequisite and lands in done/ in the same commit. Implementation begins at Chunk 1 (spec deltas for UI/pricing/adapter behavior); Chunk 2 (CI scaffolding) lands second so the spec deltas are not gated on workflow setup.

## Requirements

### Purpose

Deliver the first working slice of ai-viewer: read the operator's existing ai-agent v3 and v2 session snapshots, ingest them into a canonical SQLite store, and serve a minimal browser UI that shows a filterable session list and per-session detail. This proves the architecture end-to-end on real data (294,000 v2 files + any v3 files present), unblocks all subsequent phases, and gives the operator something usable to start providing real feedback against.

### User Request

From the bootstrap conversation: build a real-time, multi-format AI-agent session explorer with strong separation of concerns, modularity, full tests, and modern dark/light UX. Phase 1 starts with ai-agent v3 + v2 (the formats we have richest evidence for) so the architecture is validated before claude-code / codex / opencode adapters are written.

### Assistant Understanding

Facts:

- The operator's workstation has ~294,000 v2 `.json.gz` files (~30 GB) under `~/.ai-agent/sessions/`.
- ai-agent v3 split format is fully specified in `ai-agent.git/.agents/sow/specs/snapshots.md`; v2 format is specified in the same file and in `optree.md`.
- Tech stack chosen: Go backend (`ai-viewer-ingest` + `ai-viewer-serve`), SQLite (modernc.org/sqlite), SSE + REST, React + Vite + TypeScript frontend, D3 only inside `viz/`.
- Two-binary architecture is approved; bind 127.0.0.1; no auth in v1.
- Repo: separate from ai-agent.git; lives at `~/src/ai-viewer.git/`; MIT license; public on GitHub when first released.

Inferences:

- A full backfill of 294K files on the operator's workstation should complete under 60 minutes with parallel workers; this is the perf target for Phase 1 validation.
- Phase 1's UI scope (list + per-session overview + logs + sources panel) is small enough to deliver alongside the ingester without dragging the timeline.
- The notify-channel design (Unix socket) is correct but if it complicates Phase 1, a WAL-mtime poll fallback is acceptable.

Unknowns:

- Exact frequency at which ai-agent rewrites a v2 `.json.gz` for an active session — to be measured against real files during implementation. Drives the v2 adapter's debounce strategy.
- Exact JSON field names of v2 (the spec sketch in `adapter-aiagent-v2.md` is preliminary; will be confirmed from real samples).
- Whether port 7710 conflicts with anything on the operator's workstation — to be verified at first run.

### Acceptance Criteria

1. `ai-viewer-ingest` binary builds, lints clean, and runs against `~/.ai-agent/sessions/`. **Verification**: build script exits 0, `golangci-lint` exits 0, ingester runs for 5 minutes without errors and reports its sources via `/api/health` (after server is also running).
2. `ai-viewer-serve` binary builds, lints clean, embeds the frontend, and serves it at `http://127.0.0.1:7710` (or alternate port if conflict). **Verification**: `curl http://127.0.0.1:7710/api/health` returns 200 with valid JSON; opening the URL in a browser renders the React app.
3. Full backfill of the operator's existing v2 snapshots completes in under 60 minutes wall-clock on the operator's workstation. **Verification**: timed run with the ingester logging "backfill complete" within 60 min.
4. The Sessions list page shows the ingested sessions correctly filtered by the global time range, agent, model, and status filters. **Verification**: manual UI walkthrough; backend unit tests for filter query SQL.
5. The Session Detail page shows: Overview tab with summary stats (tokens, cost, turns, ops, failures) and Logs tab with severity-filtered log entries. **Verification**: manual walkthrough; component tests; one E2E test asserting the page renders against a fixture session.
6. The Sources panel shows ingest status, lag, and parse error counts per source. **Verification**: manual; unit test on the `/api/sources` handler.
7. Real-time updates: writing a new v3 ledger record into the test fixture directory causes the UI to fade-in the new session within 2 seconds. **Verification**: Playwright E2E test that does exactly this.
8. CI: GitHub Actions workflow runs lint + tests on every push; zero warnings, zero failures. **Verification**: workflow green on the bootstrap commit and on every Phase 1 PR.
9. Specs under `.agents/sow/specs/` are updated to reflect every divergence discovered during implementation (especially the v2 field names). **Verification**: diff of specs alongside code in each commit.

### User Decisions (recorded 2026-05-26)

These were the open product decisions surfaced to the operator after milestone discussion. The operator's calls are now the binding scope for Phase 1.

1. **Repository visibility — public from day one.**
   Rationale: external eyes from day one; matches the operator's preference for open development of tools like this. The repo `netdata/ai-viewer` is created public; all subsequent visibility/permissions decisions live in their own SOW if changed.
   Implication: every commit, comment, and artifact must assume a public audience. Sensitive-data discipline (`AGENTS.md` "Sensitive Data In Durable Artifacts") is non-negotiable. Fixture sanitization is mandatory before any `testdata/` commit.

2. **UI theme — match the operating system.**
   Rationale: the operator wants the viewer to feel native to whichever environment runs it (dark workstation, light workstation, auto-switching).
   Implementation directive: the frontend uses `prefers-color-scheme` media query for the default; an in-app manual override is exposed in the UI (persisted in `localStorage`) so the operator can pin a theme when desired. Dark and light are first-class — equally polished, not "dark is the real one + a light afterthought".
   Spec impact: update `.agents/sow/specs/frontend-architecture.md` and `.agents/sow/specs/ui-pages.md` in Chunk 1 to capture this.

3. **Cost / pricing data — static + shell refresh script.**
   Rationale: AGENTS.md mandates zero outbound network calls from the ingester or server. Live pricing APIs are therefore out. A static pricing table baked into the binary keeps runtime offline; a maintenance script keeps the table current.
   Implementation directive:
   - Pricing data lives in a versioned JSON file, e.g. `internal/pricing/pricing.json`, embedded via `go:embed`.
   - Schema: per-provider, per-model, per-unit price (input tokens, output tokens, cached-input where applicable), currency, effective date, citation URL.
   - `scripts/refresh-pricing.sh` invokes an external CLI AI tool (`claude`, `codex`, `gemini`, `opencode`, or any compatible CLI selectable by env var or argument) to fetch current public pricing, validates the response is well-formed JSON matching the schema, and writes back to the file.
   - The script never runs at build, install, or runtime — only when an operator explicitly runs it. Updates land via PR (so the diff is visible).
   - The script must not silently overwrite — produces a diff for human review (`git diff`) and exits without commit.
   Spec impact: new spec `.agents/sow/specs/pricing.md` created in Chunk 1, plus update to `.agents/sow/specs/canonical-events.md` to reference how cost is computed from the pricing file at ingest time.

4. **Sub-agent linkage — ingestion-side, via parent's listing of children.**
   Rationale: the parent session already records its children's IDs in the opTree (`childSessionRef`, `childSessionSummary` per `ai-agent.git/.agents/sow/specs/snapshots.md`). The ingester resolves parent → child by walking parents. No new ai-agent feature is needed for Phase 1 to work.
   Implementation directive: the v3 and v2 adapters emit `SessionStartedEvent` events with `ParentNativeID` populated **when the parent session has already been parsed**. When the child is parsed before the parent (file mtimes are independent), the child is emitted with `ParentNativeID = ""` and the ingester's resolver pass (every 5 s) backfills the link once the parent appears. This was already in the SOW's R6 mitigation and remains the approach.
   Cross-repo follow-up: an explicit `parent_session_id` field on the child's v3 `session_start` record (defense in depth) is opened as a separate SOW in `ai-agent.git/.agents/sow/pending/SOW-0029-20260526-evidence-explicit-parent-id-on-child.md`. Once that lands, the ai-viewer v3 adapter will prefer the explicit child-side field when present and fall back to parent-side discovery when absent. **Phase 1 ships without requiring the explicit field** — it is an enhancement, not a blocker.
   Spec impact: update `.agents/sow/specs/adapter-aiagent-v3.md` and `.agents/sow/specs/adapter-aiagent-v2.md` in Chunk 1 to describe the resolver pass and the future fast-path.

These four decisions close the original "Open decisions" list below. The Open decisions section is preserved as historical context but no longer gates implementation start.

## Analysis

Sources checked (at SOW drafting):

- `ai-agent.git/.agents/sow/specs/snapshots.md` — v3 record contract, payloads layout.
- `ai-agent.git/.agents/sow/specs/optree.md` — opTree shape (referenced; will be re-read in detail when implementing v2 adapter).
- `ai-agent.git/src/persistence.ts` — v2/v3 write path entry points.
- `ai-agent.git/src/evidence/writer.ts` — v3 producer.
- Real sample at `~/.ai-agent/sessions/0000174a-5d17-480f-bd27-1b97ddc47410.json.gz` (14 KB gz → 62 KB JSON).
- `~/.claude/projects/` layout (for Phase 2 awareness only).
- `~/.codex/sessions/` layout (Phase 2).
- `~/.local/share/opencode/opencode.db` (Phase 2).

Current state of ai-viewer repo:

- Bootstrap complete: `AGENTS.md`, all specs under `.agents/sow/specs/`, all skills under `.agents/skills/project-*`, SOW infra (template + audit.sh), README, LICENSE, gitignore.
- No Go code, no frontend code, no CI workflows yet. Phase 1 will create all of these.

Risks:

- **R1 — v2 backfill performance**: 294K files × decompress + parse may exceed 60 min if not parallelized correctly. Mitigation: bounded worker pool, measure early; if over target, the SOW pauses and we discuss whether to raise the target or change strategy.
- **R2 — v2 schema sketch is preliminary**: real field names may differ from the spec sketch. Mitigation: confirm against 10 random real samples in the first hour of implementation; update `adapter-aiagent-v2.md` before writing the parser.
- **R3 — fsnotify watch limit**: Linux default 8192 watches. Mitigation: do not recurse into per-session subdirectories for v2 (one watch on the root suffices since files live flat); for v3, one watch on `session/` and one on `payloads/<sessionId>/` only when actively read.
- **R4 — active v2 session rewriting fast**: an active session can rewrite its `.json.gz` dozens of times per minute. Mitigation: debounce: if mtime advances during a read, finish current read and re-read once (not in a loop).
- **R5 — go:embed across the binary split**: only `ai-viewer-serve` embeds the frontend; `ai-viewer-ingest` does not. Build script must produce both binaries from a single repo. Mitigation: standard `cmd/<name>/` Go layout handles this cleanly.
- **R6 — sub-agent linkage timing**: child session may be parsed before parent in v2 (file mtimes are independent). Mitigation: ingester resolver pass every 5s; child sessions are written with `parent_session_id=NULL` first, linked when parent appears.

## Pre-Implementation Gate

Status: ready (pending user sign-off to move SOW to current/)

Problem / root-cause model:

- ai-viewer does not exist yet. The bootstrap created the contract and specs; this SOW creates the first working slice. Without this slice, every subsequent phase is blocked.

Evidence reviewed:

- All specs under `.agents/sow/specs/` (just-written; consistent with each other).
- ai-agent.git snapshot specs (paths cited above).
- One real sample v2 file inspected for size profile.
- Bootstrap conversation: design decisions, transport choice, repo split, ownership model.

Affected contracts and surfaces:

- New: every contract in this repo (canonical events, SQLite schema, REST API, SSE protocol, UI routes). All defined by specs; this SOW implements them.
- External: read-only consumer of ai-agent's snapshot directory. No change to ai-agent.

Existing patterns to reuse:

- `netdata-cloud-nedi-tools/` (Go HTTP service in ai-agent.git) for: cmd/ layout, structured logging, graceful shutdown patterns.
- `ai-agent.git/src/evidence/reader.ts` as a behavioral reference for v3 parsing (not for code; for understanding what the reader is supposed to do).

Risk and blast radius:

- Local-only impact (workstation tool, no network exposure, no writes to source).
- Worst case: ingester bug corrupts its own SQLite — fix is `rm index.db` and rerun. No data loss.

Sensitive data handling plan:

- SOWs/specs/skills/docs already audited for sensitive data at bootstrap.
- Fixture files committed under `testdata/` MUST be sanitized via `scripts/sanitize-fixture.sh` (which will be written in this SOW). CI grep-scans for common secret patterns.
- Logs MUST NOT include raw user message content from snapshots.

Implementation plan (ordered chunks):

1. **Spec deltas (lands FIRST, no code)**: new `.agents/sow/specs/pricing.md` (pricing JSON schema + refresh-script contract); update `.agents/sow/specs/frontend-architecture.md` and `.agents/sow/specs/ui-pages.md` for OS-theme matching + manual override; refine `adapter-aiagent-v3.md` / `adapter-aiagent-v2.md` for any small gaps not covered by SOW-0002. Per the discipline contract, specs change first.
2. **CI scaffolding**: `.github/workflows/ci.yml` running Go lint+test, frontend lint+test, build. First green on the bootstrap-only repo. Foundation for every gate that lands in SOW-0009 — SOW-0013.
3. **Go module setup**: `go.mod`, `internal/canonical/` package with Event types from `canonical-events.md`, `internal/store/` with migration `0001_initial.sql` creating the v1 schema from `data-model.md`. **Co-land `.golangci.yml`** in the same commit so the lint job activates the full Go linter chain the moment `go.mod` appears (per glm reviewer recommendation on Chunk 2). The starting `.golangci.yml` is minimal — `govet`, `errcheck`, `staticcheck`, `unused`, `gosimple`, `ineffassign`, `gofmt`, `goimports`. SOW-0009 extends it to the full linter set with version pinning.
4. **Adapter scaffolding**: `internal/adapters/registry.go`, `internal/adapters/aiagent_v3/adapter.go` skeleton, `internal/adapters/aiagent_v2/adapter.go` skeleton; both with TODO bodies and the `canonical.Adapter` interface compile-checked.
5. **Sanitization tooling**: `scripts/sanitize-fixture.sh` for stripping sensitive content from real samples before committing. Mandatory before any `testdata/` commit.
6. **v3 adapter implementation**: complete the Scan + Tail + cursor; fixtures + golden tests for all mandatory scenarios from `adapter-aiagent-v3.md`. ISO-8601 → microsecond conversion at the boundary. Fast-path parent linkage via child-side `parentSessionId` (96.8% observed).
7. **v3 → ingest → store path**: complete `internal/ingest/`, wire v3 adapter, test end-to-end on a small fixture. 5-second resolver pass for cross-session linkage.
8. **v2 adapter implementation**: parser, debounce on active rewrites, content-hash cursor (since v2 rewrites the whole file). Streaming gzip + streaming JSON for files > 50 MB. Skip zero-byte and `.tmp-*` files. Embedded sub-agent walk.
9. **v2 backfill perf measurement**: timed full scan of operator's `~/.ai-agent/sessions/` (294,316 files, 25.4 GB). Per SOW-0002 analysis the target is < 60 min; bench expects 5-10 min with 8 workers. If > 60 min, pause and discuss.
10. **Pricing data + refresh script**: `internal/pricing/pricing.json` initial seed; `scripts/refresh-pricing.sh` invoking an external CLI AI tool; pricing computed-by-default on ops where source doesn't record cost (claude-code, codex; see `pricing.md`).
11. **Server scaffolding**: `cmd/ai-viewer-serve/`, `internal/presenter/`, basic `/api/health`, `/api/sources`.
12. **REST endpoints**: `/api/sessions`, `/api/sessions/:id`, `/api/sessions/:id/logs`, `/api/stats` (Phase 1 subset).
13. **SSE hub**: subscriptions, event push, keepalive, reconnect support.
14. **Frontend scaffolding**: Vite + React app, theme tokens, OS-prefers-color-scheme detection + manual override, layout, FilterBar.
15. **Frontend pages**: SessionsList, SessionDetail (Overview + Logs tabs), Sources.
16. **SSE integration in frontend**: real-time list updates.
17. **Build pipeline**: `scripts/build.sh` builds frontend, embeds, builds Go binaries.
18. **E2E test**: ingest → server → browser asserting realtime update via Playwright.
19. **systemd user units + install script**.
20. **Operator runbook stub**: `docs/runbook.md` for the Phase 1 surfaces.
21. **External review round**: codex + gemini + glm + qwen, full repo + diff.
22. **Address review findings**, re-review, mark SOW completed, move to done/.

Validation plan:

- Per-chunk unit tests (mandatory before next chunk).
- CI green on every commit.
- Step 8 perf measurement is a hard gate — if it fails, the SOW pauses for redesign.
- Step 16 E2E test is the cross-system smoke test.
- Step 19 external reviews must converge (no actionable findings remain) before SOW close.

Artifact impact plan:

- `AGENTS.md`: no expected change in Phase 1 (the contract is right).
- Specs under `.agents/sow/specs/`: every spec touched by implementation gets refined with evidence-based detail (especially `adapter-aiagent-v2.md` field-name confirmation).
- Project skills under `.agents/skills/`: may grow with patterns discovered during implementation. Update during retrospection at SOW close.
- README: update Status section from "Pre-alpha" to "v0.1 — Phase 1 complete".
- New: `docs/runbook.md`, `docs/architecture-overview.md` (operator-facing summary of the architecture spec), `SECURITY.md`.

Open decisions (closed by `### User Decisions (recorded 2026-05-26)` in the Requirements section above; preserved here as history):

- Port 7710 — kept as default.
- Repo visibility — **public** (decided).
- GitHub Actions vs other CI — GitHub Actions (matches operator's other repos).
- UI theme strategy — **match the operating system** with manual override (decided).
- Pricing data approach — **static JSON embedded in binary, refreshed by `scripts/refresh-pricing.sh` using a CLI AI tool** (decided).
- Sub-agent linkage approach — **ingester resolver pass using parent-side child references**; cross-repo SOW-0029 in ai-agent.git adds explicit child-side `parent_session_id` as future fast-path (decided).

## Implementation

### Chunk 1 — Spec deltas (2026-05-26)

Landed in PR #2 (commit pending merge).

- **New**: `.agents/sow/specs/pricing.md` (~280 lines). Per-model JSON schema embedded via `go:embed`; refresh script contract; cost computation algorithm; source-recorded-cost-takes-precedence rule; Phase 1 acceptance "zero unknown-pricing warnings on backfill".
- **Updated**: `.agents/sow/specs/frontend-architecture.md` Theming section — full theme resolution algorithm with manual-override precedence, OS-preference fallback, no-flash inline script, three-state Auto/Dark/Light header control, tests enumerated.
- **Updated**: `.agents/sow/specs/ui-pages.md` Theme section — corrected the bootstrap-era "Dark mode default" claim to "OS preference is the default"; clarified the three-state control; keyboard shortcut `t` cycles Auto→Dark→Light.
- **Updated**: `.agents/sow/specs/index.md` — adds `pricing.md` to Cross-cutting section.

No code changes in this chunk. Deviations from plan: none. Adapter-v2/v3 spec refinements anticipated in the original plan are unnecessary — SOW-0002's evidence-based rewrites already cover the 5-second resolver-pass behavior and fast-path child-side `parentSessionId` lookup.

Next: Chunk 2 — CI scaffolding.

### Chunk 2 — CI scaffolding (2026-05-26)

Landed in PR #<N> (replace after master merge):

- New: `.github/workflows/ci.yml` (386 lines). Four jobs: `lint`,
  `test`, `frontend`, `gates`. Conditional bodies that skip when the
  relevant tree is absent — `go.mod` for the Go jobs,
  `frontend/package.json` for the frontend job, `scripts/gates.sh` /
  `scripts/scan-secrets.sh` / `scripts/spec-drift.sh` for the gates
  job. First green on the spec-only master. Stable job names ready
  to be registered as required status checks by SOW-0013.

Trigger model: push to `master`, `pull_request` against `master`,
`workflow_dispatch` for debugging. Concurrency: cancel-in-progress on
PRs only via `cancel-in-progress: ${{ github.event_name ==
'pull_request' }}`; master runs preserved for audit.

Action versions pinned to current latest stable majors (verified via
`gh release view` on 2026-05-26): `actions/checkout@v6`,
`actions/setup-go@v6`, `actions/setup-node@v6`, `actions/cache@v5`,
`actions/upload-artifact@v7`, `golangci/golangci-lint-action@v9`.
This supersedes the lower pins suggested in the chunk brief; the
SOW's library version policy is "latest stable at the time of work".

Validation: `python3 -c "yaml.safe_load(...)"` parses cleanly;
`actionlint v1.7.12` reports zero findings.

Deviations from plan: action versions raised to latest stable (see
above). No structural deviations.

Reviewer iteration 1 (2026-05-26): two external reviewers (codex,
qwen) converged on five actionable fixes to the initial workflow,
applied in-place to `.github/workflows/ci.yml` (425 lines after):

1. **Standalone `goimports` step in `lint`** (codex, qwen) — added
   after `gofmt`. `quality-gates.md` requires `goimports -l .` →
   zero output as a standalone gate; the bundled goimports inside
   `golangci-lint` was conditional on `.golangci.yml` existing
   (which it does not until SOW-0009 lands).
2. **Standalone `gosec` step in `lint`** (codex, qwen) — added
   after `govulncheck`. Same conditionality reasoning: `gosec
   -severity medium -confidence medium ./...` is mandated by
   `quality-gates.md` and was gated behind the absent
   `.golangci.yml` until this fix.
3. **`go mod tidy` verification step in `lint`** (codex, qwen) —
   added between `Set up Go` and `gofmt`. Runs `go mod tidy` then
   `git diff --exit-code go.mod go.sum`. Catches stale dependency
   tracking before it lands on master.
4. **Always install Playwright OS dependencies** (codex, qwen) —
   collapsed the previous cache-miss-gated install + bare E2E step
   into a single `Install Playwright (browsers + OS deps)` step
   that always runs `npx playwright install --with-deps`. Browser
   binaries are still cached via `actions/cache@v5`; the
   `--with-deps` flag is idempotent on binaries but always
   reapplies OS package state. Avoids mysterious E2E failures on a
   cache hit on a fresh runner image.
5. **Deferred-gates block in workflow header** (codex, qwen) —
   added a "Deferred to subsequent SOWs" subsection between
   "Extending" and "Out of scope here" that names every gate from
   `quality-gates.md` not enforced in this initial workflow and
   maps each to its target SOW (SOW-0009 through SOW-0013). Future
   readers see the whole gate roadmap from the workflow file
   itself.

Validation after fixes: `python3 -c 'import yaml;
yaml.safe_load(open(...))'` parses cleanly; all four job names
unchanged (`lint`, `test`, `frontend`, `gates`); all pinned action
versions unchanged.

Reviewer iteration 2 (2026-05-26): the third reviewer (glm, run as
substitute for gemini which returned empty output on this session)
completed independently after the five fixes above had landed. Its
report: 0 critical/high, 1 medium and 2 low — all overlapping with
the codex/qwen findings already addressed, plus three informational
notes. **One new process recommendation accepted**: "ensure
`.golangci.yml` co-lands with `go.mod` in Chunk 3". The
Implementation plan above is amended to require `.golangci.yml` in
the same commit as `go.mod`; this closes the lint/security gap that
would otherwise let unlinted Go code merge between Chunk 3 landing
and SOW-0009 landing. Convergence reached: no actionable findings
remain. gemini availability follow-up logged for the operator's
local LLM infrastructure (out of scope for this SOW).

Next: Chunk 3 — Go module + canonical + store.

### Chunk 3 — Go module + canonical + store (2026-05-26)

Foundation Go code landed on branch `sow-0001-chunk-3-go-foundation`.
Master had zero Go code before this chunk; every Go file in the repo
originates here.

Files created (line counts include doc comments):

- `go.mod` (20) + `go.sum` (populated by `go mod tidy`) — module
  `github.com/netdata/ai-viewer`, Go directive `1.26`, direct deps
  `modernc.org/sqlite v1.50.1` and `github.com/google/go-cmp v0.7.0`.
  `github.com/google/uuid v1.6.0` is reachable through `modernc.org/sqlite`
  transitive closure and is recorded as `// indirect`; it will become
  a direct dependency the moment ingest code first calls into it
  (Chunks 4+). No testify, logrus, viper, fmtxprint, or other framework
  pulled in.
- `.golangci.yml` (38) — golangci-lint v2 schema (`version: "2"`).
  Linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`.
  Formatters: `gofmt`, `goimports`. `gosimple` deliberately omitted —
  v2 merges it into `staticcheck`. SOW-0009 extends to the full set.
- `internal/canonical/doc.go` (25), `events.go` (346), `adapter.go` (71),
  `events_test.go` (314). Every event type, every enum
  (`EventKind`, `OpKind`, `SessionKind`, `SessionStatus`), every field
  named in `canonical-events.md`. Compile-time interface conformance
  asserted for all 11 concrete events. Coverage: 100.0%.
- `internal/store/doc.go` (22), `store.go` (112), `migrations.go` (169),
  `store_test.go` (291), `migrations_test.go` (130). PRAGMAs:
  `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`,
  `foreign_keys=ON`. Idempotent embedded-FS migration runner using a
  `_schema_migrations` bookkeeping table separate from `schema_meta`.
  Coverage after iteration 3: 85.6% (store.go alone 93.7%; the
  package-aggregate gap is unreachable defensive code in
  `migrations.go` — embed-FS `ReadDir`/`ReadFile` errors,
  `tx.Rollback` failure logging, `rows.Err()` on a cancellation that
  the test driver does not simulate. All new code added in this
  chunk is exercised; the new-code ≥90% threshold is met).
- `internal/store/migrations/0001_initial.sql` (273) — the complete v1
  schema from `data-model.md`: `sources`, `sessions`, `turns`, `ops`,
  `payload_refs`, `log_entries`, `catalog_providers`, `catalog_models`,
  `catalog_tools`, `catalog_agents`, `catalog_cwds`, `schema_meta`.
  Every index from the spec. Final `INSERT OR REPLACE INTO schema_meta`
  pins `version='1'` and `created_at` to the migration's wall-clock
  microseconds.
- `cmd/ai-viewer-ingest/main.go` (14), `cmd/ai-viewer-serve/main.go` (14)
  — print-and-exit stubs so `go build ./...` succeeds. Real
  implementations land in Chunks 4+.

Local gates (run from `/home/costa/src/ai-viewer.git`, all clean):

```
go mod tidy           # no changes
gofmt -l .            # zero output
goimports -l .        # zero output
go vet ./...          # zero warnings
go build ./...        # exit 0
go test -race -count=1 ./...   # all pass (canonical + store)
golangci-lint run --timeout=5m # 0 issues
```

Coverage (informational only this chunk, post iteration 3): repo
total 86.1%; canonical 100.0%; store package 85.6% (store.go alone
93.7%, exceeding the new-code 90% threshold).

Deviations from plan:

- The SOW brief described `Cursor` as `[]byte` shorthand. The
  authoritative spec (`adapter-contract.md`) defines `Cursor` as an
  interface with `String()` and `After(Cursor) bool`. The
  implementation follows the spec. No spec change required.
- `gosimple` was listed in the brief's linter set but is gone in
  golangci-lint v2 (merged into `staticcheck`). Config omits it.
- `github.com/google/uuid` is `// indirect` in `go.mod` because no
  ai-viewer code references it yet. This is exactly what `go mod tidy`
  produces and matches the brief's "deps available, not yet used"
  intent.

No spec drift discovered during implementation. `canonical-events.md`
and `data-model.md` were translated line-by-line; the only translation
choices (e.g. `_schema_migrations` as bookkeeping table name) are
internal and documented in the migrations source.

Reviewer iteration 2 (2026-05-26): three external reviewers (codex,
qwen, glm) ran in parallel on the initial chunk. Codex surfaced
three P1s; convergent across reviewers were the PRAGMA per-
connection issue and the coverage gap. Six accepted fixes were
applied atomically with code + spec deltas in the same commit:

1. EventKind hard-coded per concrete event type (codex P3) —
   removed the mutable Kind field from EventBase; each event type
   has its own EventKind() method returning the spec constant.
2. PRAGMAs via _pragma DSN params (codex P1, glm, qwen) —
   replaced post-Open ExecContext with modernc.org/sqlite's DSN
   _pragma syntax; pragmas now propagate to every pooled
   connection. Verified against the driver's own test
   suite (modernc/sqlite all_test.go:2419-2495).
3. OpenWriter / OpenReader split (codex P1) — Open is now an
   alias for OpenWriter; OpenReader applies mode=ro + query_only
   and skips migrations; doc.go documents the single-writer
   architecture invariant.
4. log_entries schema fix (codex P1) — session_id now nullable;
   source_id column added with CHECK ensuring at least one is set;
   idx_log_source_ts index added; data-model.md and
   canonical-events.md updated in the same commit so spec leads
   code.
5. TestOpen_BadDSN deterministic via t.TempDir() (glm) — relative
   path removed; subdirectory under TempDir guarantees the open
   fails for the right reason.
6. Schema-shape contract test (codex P2) — new
   TestSchema_ColumnContract asserts every table's columns + types
   + nullability + indexes + FK relationships via SQLite PRAGMAs.
   Catches silent schema drift on every future migration. Also
   closed the store coverage gap to ≥80%.

One codex P2 deferred with documentation: migration concurrency
across processes. The single-writer architecture (ingester only
writes; server uses OpenReader) makes this a non-issue today;
store/doc.go notes the invariant and the path forward if multi-
writer is ever introduced.

One qwen MEDIUM rejected: "Go 1.26 declared but doesn't exist";
the workstation runs go1.26.3 (system date 2026-05-26); qwen's
training cutoff predates the Go 1.26 release.

Reviewer iteration 3 (2026-05-26): codex re-reviewed after iteration
2's fixes. Resolved iteration-1 findings confirmed: EventKind
hard-coded, _pragma propagation, OpenWriter/OpenReader split,
log_entries schema, deterministic TestOpen_BadDSN, single-writer
documented. Six NEW findings (2xP1, 3xP2, 1xP3) addressed in
iteration 3:

A. OpenReader uses file: URI (codex P1). modernc.org/sqlite strips
   query params from non-file: DSNs at conn.go:53-55, so mode=ro
   was silently lost. New pathToFileURI() helper wraps path DSNs
   into file:URI form; OpenReader (and OpenWriter, for symmetry)
   passes them through. New test
   TestOpenReader_RejectsMissingFile asserts a non-existent path
   returns error AND does not create the file (no
   READWRITE|CREATE leak). TestOpenReader_ModeROAtOSLevel pins the
   OS-level enforcement even when an operator DSN tries to flip
   mode=rwc.
B. Mandatory pragmas non-overridable (codex P1). buildDSN now
   appends mandatory pragmas (foreign_keys, busy_timeout,
   journal_mode/synchronous for writer, query_only for reader)
   LAST so operator-supplied _pragma values cannot override.
   addPragma's "skip if present" dedup was removed; the simpler
   appendPragma always appends. New tests
   (TestBuildDSN_OperatorCannotOverrideForeignKeys,
   TestBuildDSN_OperatorCannotOverrideQueryOnly,
   TestBuildDSN_OperatorCannotOverrideBusyTimeout,
   TestBuildDSN_OperatorCannotOverrideModeRO,
   TestOpenWriter_OperatorCannotDisableForeignKeys) pin the
   contract end-to-end. TestBuildDSN_OperatorCustomPragmaPreserved
   ensures non-mandatory operator pragmas (e.g. cache_size) still
   round-trip intact.
C. Schema contract test strengthened (codex P2). Three new
   subtests added to schema_contract_test.go:
   - TestSchema_PartialIndexPredicates reads the raw sqlite_master
     `sql` column for every partial index and asserts the WHERE
     clause matches the spec, with a whitespace-normalised
     comparison so cosmetic formatting differences don't fail.
   - TestSchema_LogEntriesCheckConstraintShape pins the CHECK
     constraint text, complementing the behavioural test that
     verifies the constraint actually rejects NULL/NULL rows.
   - TestSchema_CompositeUniqueAutoindexes enumerates
     sqlite_autoindex_* with origin in {'pk', 'u'} to catch
     silent drops of composite PRIMARY KEY or UNIQUE constraints.
D. TEXT PRIMARY KEY NOT NULL (codex P2). SQLite default rowid
   tables allow NULL in TEXT PRIMARY KEY columns; only INTEGER
   PRIMARY KEY (the rowid alias) is implicitly NOT NULL. Spec
   (data-model.md) and migration (0001_initial.sql) both updated
   so every single TEXT PRIMARY KEY column is explicitly NOT NULL:
   sources.id, sessions.id, turns.id, ops.id, schema_meta.key.
   Composite-PK tables (catalog_*) already had NOT NULL on every
   PK column. Contract test expectations updated to assert
   NotNull: true on every PK column.
E. buildDSN returns error on malformed query (codex P3). The
   previous swallow-and-continue branch could turn a malformed
   DSN into a silently-valid one with unintended pragmas. buildDSN
   now returns (string, error); both OpenWriter and OpenReader
   propagate the error. TestBuildDSN_MalformedQueryReturnsError
   exercises invalid percent-encoding; TestOpenWriter_RejectsMalformedDSN
   and TestOpenReader_RejectsMalformedDSN verify it surfaces at
   Open time. Empty DSN is also rejected by pathToFileURI
   (TestOpenWriter_RejectsEmptyDSN, TestOpenReader_RejectsEmptyDSN).
F. New-code coverage boosted (codex P2, quality-gates.md
   new-code rule). store.go is now 93.7% covered, above the 90%
   new-code threshold. The store package as a whole is 85.6%
   (up from 78.9% pre-iteration-3, 83.9% mid iteration). The
   remaining package-aggregate gap is defensive code in
   migrations.go — embed-FS ReadDir/ReadFile errors,
   tx.Rollback failure logging during applyMigration's deferred
   cleanup, rows.Err() on a cancellation the test harness does
   not simulate, and tx.Commit error injection. These paths are
   testably-impossible without driver-level mocking; they are
   typed defensive returns that match the existing migration
   runner's design and were not introduced by iteration 3. The
   `// coverage-target` Gate Suppression in quality-gates.md
   covers this class of exception.

Other simplifications during iteration 3:
- Dead-branch removal in buildDSN: the empty-params and
  prefix-contains-'?' branches were unreachable (mandatory
  pragmas guarantee non-empty params; splitDSNQuery splits at
  the first '?'). Removed; coverage rose as a side-effect.
- pragmaName helper removed: the new appendPragma does not need
  to compare pragma identifiers, so the helper became dead
  code. Its test (TestPragmaName_VariousSeparators) was
  replaced by a private pragmaIdent test helper used by the new
  override-precedence tests.

The stale "78.9%" coverage references in earlier paragraphs of
this subsection have been updated to the iteration-3 numbers
(store package 85.6%, store.go 93.7%, repo total 86.1%).

Reviewer iteration 4 (2026-05-26): codex re-reviewed iteration 3's
fixes and found two P1 regressions plus one process gap. All three
addressed in iteration 4:

A4. **Mandatory pragmas — strip-then-append, not append-last**
    (codex P1). The iteration-3 "append last" strategy was
    fundamentally wrong: `modernc.org/sqlite@v1.50.1/sqlite.go:143-159`
    sorts the `_pragma` slice alphabetically (with `busy_timeout`
    pinned first) before executing it. So an operator-supplied
    `synchronous(off)` would sort AFTER `synchronous(normal)`
    (alphabetical: 'of' > 'no') and override the store's value at
    runtime, even though the store appended last. The fix in
    iteration 4 changes the strategy: `buildDSN()` now STRIPS every
    operator-supplied `_pragma` whose pragma name is in the
    mandatory set (foreign_keys, busy_timeout, journal_mode,
    synchronous for writers; foreign_keys, busy_timeout, query_only
    for readers; foreign_keys, busy_timeout for memory writers)
    BEFORE appending the store's value. The final DSN therefore
    carries exactly one entry per mandatory pragma name; the
    driver's sort order is now irrelevant. The strip helper is the
    new `pragmaNameInSet` + `pragmaName` pair; the obsolete
    `pragmaIdent` test helper was replaced by `pragmasForName` that
    asserts the encoded query carries ONLY the store's value for a
    given pragma name. New tests `TestBuildDSN_OperatorCannotOverrideJournalMode`
    and `TestBuildDSN_OperatorCannotOverrideSynchronous` pin the
    two cases the alphabetical sort would have broken.
    `TestOpenWriter_OperatorCannotWeakenSynchronous` and
    `TestOpenWriter_OperatorCannotOverrideJournalMode` verify the
    runtime effective value at the SQLite layer, not just the DSN
    string. The existing `TestOpenWriter_OperatorCannotDisableForeignKeys`
    also continues to validate end-to-end.

B4. **OpenReader/OpenWriter Ping the database** (codex P1/P2).
    `sql.Open` is lazy — it returns a `*sql.DB` without ever opening
    the database file. Iteration 3's `pathToFileURI` + `mode=ro`
    fix prevented file creation, but `OpenReader` against a missing
    file still returned `(non-nil *Store, nil error)`; the error
    only materialised on the first query. Iteration 4 adds
    `db.PingContext(ctx)` immediately after `sql.Open` in BOTH
    `OpenReader` and `OpenWriter` (the writer also pings before
    running migrations, so a broken open does not even attempt
    migrations). `TestOpenReader_RejectsMissingFile` was tightened
    to assert `OpenReader` itself returns the error and that no
    `*Store` value is returned — the fall-through-to-Ping
    permissive branch was removed from the test contract. The
    file-not-created assertion remains.

C4. **Gate Suppression — concrete file:line catalogue**
    (codex P2 / `quality-gates.md` §"Gate Suppression"). Iteration
    3's narrative pointed at "embed-FS read errors, tx.Rollback
    logging, rows.Err()" but did not list the specific suppressed
    branches, and referenced a `// coverage-target` marker that
    does not exist in source. Iteration 4 replaces that with an
    explicit table below. No inline `// coverage-skip:` comments
    are added in source — they would clutter the small, idiomatic
    defensive returns without adding signal; this SOW table is the
    documented suppression contract instead.

**Gate Suppression — Chunk 3** (per
`.agents/sow/specs/quality-gates.md` §"When a Gate Fails", item 6;
revisited every time these files change). Iteration 5 removed three
rows that are now exercised by real tests
(`pragmaName no-delimiter fall-through`,
`OpenWriter Up() failure branch`,
`loadAppliedMigrations db.QueryContext error`) and annotated every
remaining row with the specific reason the branch is unreachable
without invasive driver-level plumbing:

| file:line | uncovered branch | reason untestable (verified) | restoration trigger |
|---|---|---|---|
| internal/store/store.go:128 | OpenWriter — `sql.Open` returns non-nil error | verified untestable because modernc.org/sqlite's `sql.Open` only errors on driver-name lookup (the driver is registered at package init via the `_ "modernc.org/sqlite"` import); reproducing the failure requires registering a fake driver under the name `sqlite`, which would conflict with the real registration and break every other test in the package | when SOW-0009 introduces driver-level fault injection |
| internal/store/store.go:196 | OpenReader — `sql.Open` returns non-nil error | verified untestable for the same reason as store.go:128; same registration constraint | same |
| internal/store/store.go:383 | `pathToFileURI` — `filepath.Abs` error | verified untestable because `filepath.Abs` only errors when `os.Getwd` itself fails (POSIX `syscall.Getwd` returns ENOENT, etc.); a `go test` process always has a valid cwd, and overriding it would require either `unix.Chdir` to a deleted directory (racy and platform-specific) or replacing the `os.Getwd` syscall, which is not pluggable | accept as untestable |
| internal/store/store.go:393 | `pathToFileURI` — leading-slash insertion | verified untestable on Linux because POSIX `filepath.Abs` always returns absolute paths starting with `/`; the branch exists to support Windows drive-letter paths (`file:/C:/...`) where `filepath.ToSlash("C:\\foo")` returns `C:/foo` (no leading slash) | exercised when CI gains a Windows runner (out of scope for v1) |
| internal/store/store.go:433 | `Close` — `db.Close` returns non-`ErrConnDone` error | verified untestable because modernc.org/sqlite's `Close` only returns `ErrConnDone` (after a previous Close) or `nil`; producing a different error class would require a driver that fails on Close, which modernc.org/sqlite never does for in-memory or local file paths | when SOW-0009 introduces driver-level fault injection |
| internal/store/migrations.go:48-50 | `loadMigrations` — `fs.ReadDir(migrationFS, …)` error | verified untestable because `embed.FS` is compile-time immutable and never returns runtime read errors for embedded payloads; injecting a failure requires changing `loadMigrations`' signature to accept an `fs.FS` parameter, which would alter the public surface | revisit if the migration loader is generalised to a `fs.FS` parameter (deferred SOW) |
| internal/store/migrations.go:54-55,58-59,63-65,69 | `loadMigrations` — non-`.sql` entries and inner `fs.ReadFile` failure | verified untestable because the embedded `migrations/` directory contains only `.sql` files at build time (the `//go:embed migrations/*.sql` pattern guarantees this) and `embed.FS.ReadFile` does not surface I/O errors for embedded payloads | same as 48-50 |
| internal/store/migrations.go:86-88 | `loadAppliedMigrations` — `rows.Scan` error | verified untestable because the SELECT projects a single TEXT column scanned into `*string`; modernc.org/sqlite never returns a scan error for that conversion. Synthesising one would require a driver-level row hook that the standard `database/sql` API does not expose | accept as untestable |
| internal/store/migrations.go:91-93 | `loadAppliedMigrations` — `rows.Err()` from iteration | verified untestable because modernc.org/sqlite does not surface an iterator-level error after `rows.Next` returned false for a well-formed SELECT against a healthy connection; reproducing requires driver-level injection | accept as untestable |
| internal/store/migrations.go:101-103 | `applyMigration` — `BeginTx` failure | verified untestable because modernc.org/sqlite only fails `BeginTx` on a closed `*sql.DB`; the surrounding `Up` runs against an open handle that earlier statements have already succeeded against, so the branch is dead at this call site | when driver-level fault injection lands |
| internal/store/migrations.go:106-111 | `applyMigration` — `tx.Rollback` failure logging in deferred cleanup | verified untestable because Rollback failing after a previous Exec error requires a driver-level fault path that modernc.org/sqlite does not produce; only a fault-injection wrapper around `*sql.Tx` could exercise it | when we switch to a transaction wrapper that supports fault injection |
| internal/store/migrations.go:123-126 | `applyMigration` — bookkeeping INSERT failure | verified untestable because the bookkeeping INSERT runs against a freshly created `_schema_migrations` table inside the same transaction as the migration body; the only ways it could fail (constraint or schema mismatch) are excluded by the table definition and the fact that the migration body has already succeeded | accept as untestable |
| internal/store/migrations.go:128-131 | `applyMigration` — `tx.Commit` failure | verified untestable because modernc.org/sqlite does not surface commit-level faults for in-memory or local file paths; would require driver-level injection | when driver-level fault injection lands |
| internal/store/migrations.go:148-150 | `Up` — `loadMigrations` propagating error | verified untestable today because `loadMigrations` itself is unreachable-to-fail (see migrations.go:48-50); the propagation branch only becomes reachable once the loader accepts an injected FS | tied to the loader generalisation SOW |

Expiry / next review: revisit each row when the driver-level
fault-injection harness lands (SOW-0009 candidate) or when the
migration loader is generalised to accept an `fs.FS` parameter
(future SOW). Each row independently lifts as the matching enabler
ships.

Reviewer iteration 5 (2026-05-26): codex re-reviewed iteration 4
and surfaced two findings:

A5. **Schema-qualified `_pragma` bypass** (codex P1). `pragmaName`
    split on `(`, `=`, and whitespace but NOT on `.` — so
    `_pragma=main.foreign_keys(off)` parsed as identifier
    `main.foreign_keys`, missed the strip-list match, and survived
    into the final DSN. modernc.org/sqlite would then execute it
    alphabetically after the store's `foreign_keys(on)` and the
    operator's `off` would win. Verified externally:
    `sqlite3 :memory: 'PRAGMA foreign_keys=ON;
    PRAGMA main.foreign_keys(OFF); PRAGMA foreign_keys;'`
    returns `0`. Iteration 5 adds `stripSchemaPrefix(s)` to
    `pragmaName` so any leading `<schema>.` qualifier (bare,
    quoted `"x"`, bracketed `[x]`, or backticked `` `x` ``) is
    removed before the splitter runs. New tests:
    `TestPragmaName_StripsBareSchemaPrefix`,
    `TestPragmaName_StripsQuotedSchemaPrefix`,
    `TestPragmaName_NoSchemaUnchanged`,
    `TestPragmaName_NoValueFallthrough`,
    `TestStripSchemaPrefix_EdgeCases`,
    `TestBuildDSN_OperatorCannotOverrideForeignKeys_Qualified`,
    `TestBuildDSN_OperatorCannotOverrideQueryOnly_Qualified`,
    `TestBuildDSN_OperatorCannotOverrideBusyTimeout_Qualified`,
    `TestOpenWriter_OperatorCannotDisableForeignKeys_SchemaQualified`,
    `TestOpenReader_OperatorCannotDisableQueryOnly_SchemaQualified`.
    The runtime tests round-trip through SQLite and assert effective
    pragma values, not just the encoded DSN string.

B5. **Tighten Gate Suppression table** (codex P2). Iteration 4's
    table marked three testable paths as untestable. Iteration 5
    converts each into a real test and removes the row:
    `pragmaName` no-delimiter fall-through →
    `TestPragmaName_NoValueFallthrough`;
    `OpenWriter` `Up()` failure branch →
    `TestOpenWriter_FailsOnTaintedSchema` (pre-creates a `sessions`
    table on disk and asserts the error wraps `apply migrations`);
    `loadAppliedMigrations` `db.QueryContext` error →
    `TestUp_FailsOnMalformedBookkeeping` (pre-creates
    `_schema_migrations` with the wrong column shape so the
    SELECT fails with "no such column"). The remaining rows now
    each carry an explicit "verified untestable because …" note so
    the suppression is concrete rather than narrative.

Coverage after iteration 5 (informational): repo total 90.3%;
store package 90.4% (up from 86.0% in iteration 4). store.go
function-level coverage: `pragmaName` 100%, `stripSchemaPrefix`
100%, `buildDSN` 100%, `pragmaNameInSet` 100%, `OpenWriter`
95.2%, `OpenReader` 93.8%, `pathToFileURI` 88.2%, `Close` 83.3%.
canonical package remains at 100%. All package totals continue to
satisfy the package-level ≥80% bar.

Next: Chunk 4 — adapter scaffolding (`internal/adapters/registry.go`
plus v3/v2 skeletons), to land after this chunk's external review
converges.

### Chunk 4 — Adapter scaffolding (2026-05-26)

Landed on branch `sow-0001-chunk-4-adapter-scaffolding` (PR # pending):

- `internal/adapters/{doc.go,registry.go,export_test.go,
  registry_test.go,registry_init_test.go}` — thread-safe registry,
  init-time registration pattern, snapshot/reset/restore test
  helpers that keep package-level mutations isolated, and a
  blank-import integration test that proves the init wiring fires
  end to end. The registry uses `sync.RWMutex` even though writes
  only occur at init; this is documentation of the read/write
  contract for downstream callers and a guard against future
  runtime-registration use cases.
- `internal/adapters/aiagent_v3/{doc.go,adapter.go,adapter_test.go}`
  — v3 skeleton with compile-time `var _ canonical.Adapter =
  (*Adapter)(nil)` conformance and Scan/Tail/ParseCursor bodies
  that return `errNotImplemented`. The constructor rejects an
  empty root and substitutes a no-op `OnError` so adapter code
  can call it unconditionally. Real implementation lands in
  Chunk 6.
- `internal/adapters/aiagent_v2/{doc.go,adapter.go,adapter_test.go}`
  — v2 skeleton; same shape as v3. Real implementation lands in
  Chunk 8.

Factory signature follows the canonical contract from Chunk 3
(`func(location string, opts AdapterOptions) (Adapter, error)`).
The factories in both subpackages delegate to a typed `New`
constructor so direct Go callers and registry callers share a
single validation path. Duplicate registration, empty format
name, and nil factory all panic at init so misconfigured
processes refuse to start instead of silently shadowing
adapters.

No spec changes. No deviations from plan. No new dependencies.

Gates run locally on the branch tip:

- `go mod tidy` — no changes.
- `gofmt -l .` — clean.
- `goimports -l .` — clean.
- `go vet ./...` — clean.
- `go build ./...` — clean.
- `go test -race -count=1 ./...` — 5 packages pass; adapter
  packages all at 100.0% coverage; canonical 100%; store 90.4%
  (unchanged); repo total 92.9% (up from 90.3%).
- `golangci-lint run --timeout=5m` — 0 issues.
- `govulncheck ./...` — 0 vulnerabilities in called code.

Next: Chunk 5 — sanitization tooling.

## Validation

(Filled at end. Test summary, perf numbers, review summary.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

(Filled at SOW close.)

## Lessons / Follow-Ups

(Filled at SOW close. Items that should become new SOWs in `pending/`.)
