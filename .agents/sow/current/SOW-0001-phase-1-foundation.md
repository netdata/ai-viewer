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
3. **Go module setup**: `go.mod`, `internal/canonical/` package with Event types from `canonical-events.md`, `internal/store/` with migration `0001_initial.sql` creating the v1 schema from `data-model.md`.
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

(Filled as chunks complete. One sub-section per chunk, with: commit refs, evidence, deviations from plan.)

## Validation

(Filled at end. Test summary, perf numbers, review summary.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

(Filled at SOW close.)

## Lessons / Follow-Ups

(Filled at SOW close. Items that should become new SOWs in `pending/`.)
