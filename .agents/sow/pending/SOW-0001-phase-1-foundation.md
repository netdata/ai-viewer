# SOW-0001 - Phase 1 Foundation: ai-agent v3 + v2 ingest, minimal UI

## Status

Status: open

Sub-state: drafted at bootstrap; awaits user approval before moving to current/.

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

1. **CI scaffolding**: `.github/workflows/ci.yml` running Go lint+test, frontend lint+test, build. Initial commit makes CI green on the bootstrap-only repo.
2. **Go module setup**: `go.mod`, `internal/canonical/` package with Event types, `internal/store/` with migration 0001 creating the v1 schema.
3. **Adapter scaffolding**: `internal/adapters/registry.go`, `internal/adapters/aiagent_v3/adapter.go` skeleton, `internal/adapters/aiagent_v2/adapter.go` skeleton; both with TODO bodies.
4. **Sanitization tooling**: `scripts/sanitize-fixture.sh` for stripping sensitive content from real samples before committing.
5. **v3 adapter implementation**: complete the Scan + Tail + cursor; fixtures + golden tests for all mandatory scenarios.
6. **v3 → ingest → store path**: complete `internal/ingest/`, wire v3 adapter, test end-to-end on a small fixture.
7. **v2 adapter implementation**: confirm field names against real samples, write parser, fixtures, golden tests. Particular attention to debounce on active files.
8. **v2 backfill perf measurement**: timed full scan of operator's `~/.ai-agent/sessions/`. If > 60 min, pause and discuss.
9. **Server scaffolding**: `cmd/ai-viewer-serve/`, `internal/presenter/`, basic /api/health, /api/sources.
10. **REST endpoints**: /api/sessions, /api/sessions/:id, /api/sessions/:id/logs, /api/stats (Phase 1 subset).
11. **SSE hub**: subscriptions, event push, keepalive, reconnect support.
12. **Frontend scaffolding**: Vite + React app, theme tokens, layout, FilterBar.
13. **Frontend pages**: SessionsList, SessionDetail (Overview + Logs tabs), Sources.
14. **SSE integration in frontend**: real-time list updates.
15. **Build pipeline**: `scripts/build.sh` builds frontend, embeds, builds Go binaries.
16. **E2E test**: ingest → server → browser asserting realtime update via Playwright.
17. **systemd user units + install script**.
18. **Operator runbook stub**: `docs/runbook.md` for the Phase 1 surfaces.
19. **External review round**: codex + gemini + glm + qwen, full repo + diff.
20. **Address review findings**, re-review, mark SOW completed, move to done/.

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

Open decisions (none blocking implementation; user may override):

- Port 7710 — keep or change if conflict found. Default: 7710.
- Repo visibility — public from day 1 vs. private until Phase 1 complete. Default: public (per bootstrap discussion).
- GitHub Actions vs other CI — default GitHub Actions (matches operator's other repos).

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
