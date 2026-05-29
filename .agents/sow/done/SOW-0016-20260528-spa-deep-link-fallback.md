# SOW-0016 - SPA deep-link fallback for client-side routes

## Status

Status: completed

Sub-state: operator-approved 2026-05-29 ("proceed as you recommend" — the operator's SOW sign-off; the assistant had recommended doing this next). Open decision #1 (fallback policy) is a TECHNICAL choice decided by the assistant as CTO = catch-all (a); see below. Implemented, reviewed (codex+glm+minimax → converged), merged; moved to done/ in the merge commit. Surfaced by an external reviewer during SOW-0001 Chunk 17, whose gate scoped "SPA fallback unchanged".

## Requirements

### Purpose

Make the single `ai-viewer-serve` binary serve the React SPA shell for direct navigations to client-side routes (page reload, deep link, bookmark) such as `/sessions/<id>`, `/sources`, `/topology`, `/tools`, `/models`, `/agents`, instead of returning a JSON `404 NOT_FOUND`. Fit-for-purpose: an operator who reloads the page on a sub-route, or shares a link to a specific session, must land on that view — not an API error.

### User Request

Not a direct operator request. Raised by the external code reviewer (glm-5.1) while reviewing SOW-0001 Chunk 17: "SPA client-side routes return JSON 404 instead of the SPA shell — pre-existing, but becomes user-visible now that the UI is actually served from the binary."

### Assistant Understanding

Facts:

- The frontend uses `BrowserRouter` (`frontend/src/main.tsx:26`) with real client-side sub-routes declared in `frontend/src/App.tsx:19-28`: index (`SessionsList`), `sessions/:id`, `sources`, `topology`, `tools`, `models`, `agents`, and a `*` `NotFound`.
- The server's `rootHandler` (`internal/presenter/presenter.go:266-278`) serves the SPA shell for exactly `/` and returns `404 NOT_FOUND` for any other path. `TestRoot_NonRootPathReturns404` (`internal/presenter/embed_test.go`) pins that.
- `/assets/*` is served by `serveAsset` and must KEEP returning `404` on a miss (no SPA fallback for asset paths) — `internal/presenter/embed.go` and `presenter.md` §Routing.
- `/api/*` must KEEP returning structured JSON (404 `NOT_FOUND` for unknown sub-routes) — never the SPA shell.
- Chunk 17 made `serveIndex` serve the built `index.html` (or a not-built notice) at `/` and left SPA fallback unchanged by explicit decision (SOW-0001 Chunk 17 gate, D-list).

Inferences:

- In-app navigation already works because the SPA is loaded once at `/` and react-router takes over client-side; only a hard navigation / reload to a sub-route hits the server and 404s. So the gap is reload/deep-link/bookmark, not normal use.

Unknowns:

- None blocking. The exact set of "SPA route prefixes" is knowable from `App.tsx`; the design question (allowlist of known SPA routes vs. catch-all-that-isn't-/api-or-/assets) is a decision recorded below.

### Acceptance Criteria

- `GET /sessions/<id>` (and the other declared client routes) returns `200` with the built `index.html` (same `text/html`, `no-cache` headers as `GET /`), verified by a new presenter test and a CI/local curl.
- `GET /api/<anything-unknown>` still returns the structured JSON `404 NOT_FOUND` envelope (unchanged), verified by existing API tests.
- `GET /assets/<missing>` still returns `404` (no SPA fallback for assets), verified by `TestServeAsset_MissingReturns404`.
- When the UI is not built, the same client routes serve the not-built notice at `200` (consistent with `serveIndex`), verified by a test.
- A genuinely unknown non-API, non-asset path policy is decided (see Open decisions) and tested.

## Analysis

Sources checked:

- `internal/presenter/presenter.go` (`rootHandler`, mux registration), `internal/presenter/embed.go` (`serveIndex`/`serveAsset`), `internal/presenter/embed_test.go` (`TestRoot_NonRootPathReturns404`), `frontend/src/App.tsx`, `frontend/src/main.tsx`, `.agents/sow/specs/presenter.md` §Routing.

Current state:

- `rootHandler` rejects any path other than `/`. Mux routes `/api/`, `/assets/`, and `/` (catch-all). Non-API, non-asset paths fall to `rootHandler` and 404 unless exactly `/`.

Risks:

- Over-broad fallback could mask real 404s (e.g. typo'd API path served the SPA shell). Mitigation: keep `/api/*` and `/assets/*` on their existing handlers; only the `/` catch-all changes. Decide whether the SPA-shell fallback is an allowlist of known route prefixes (stricter, must update when routes change) or any non-API/non-asset path (looser, zero-maintenance, standard SPA behavior).
- HEAD parity and cache headers must match `serveIndex` exactly.

## Pre-Implementation Gate

Status: ready (open decision #1 resolved as a CTO call — catch-all)

Problem / root-cause model:

- `rootHandler` serves the SPA shell only for `/`; client-side routes 404 on hard navigation because the server has no SPA-fallback rule. Evidence: `internal/presenter/presenter.go:266-278`, `frontend/src/App.tsx:19-28`.

Evidence reviewed:

- Listed under Analysis. No external OSS repos required (standard SPA-fallback pattern; if useful, cite a router/static-file server example at implementation time as `owner/repo @ commit`).

Affected contracts and surfaces:

- `internal/presenter/presenter.go` (`rootHandler` / routing), `internal/presenter/embed.go` (possibly a shared "serve shell" helper), `presenter.md` §Routing (the contract), presenter tests. No `/api`, `/assets`, schema, or frontend-source change.

Existing patterns to reuse:

- `serveIndex` already centralizes "serve the shell or the not-built notice with 200/no-cache + HEAD parity"; the fallback should route through it so all three states stay consistent.

Risk and blast radius:

- Contained to the `/` catch-all handler. `/api/*` and `/assets/*` keep their handlers and tests. Main regression risk is masking real 404s — addressed by the Open decision below.

Sensitive data handling plan:

- None. Static shell HTML, no secrets, no user data.

Implementation plan:

1. Spec: update `presenter.md` §Routing to define the SPA-fallback contract (which paths serve the shell; `/api/*` and `/assets/*` exempt; HEAD + cache parity).
2. Tests first: add presenter tests for `GET /sessions/<id>` → 200 + shell, `/api/unknown` → JSON 404, `/assets/missing` → 404, not-built variant → notice. Adjust/replace `TestRoot_NonRootPathReturns404` to match the new contract (documenting the behavior change).
3. Code: change `rootHandler` (or the `/` catch-all) to serve the shell via `serveIndex` for non-API, non-asset paths per the decided policy.
4. Gates + external review to convergence; commit spec+tests+code together.

Validation plan:

- New presenter unit tests + a curl in the existing CI `embed-smoke` job (e.g. `GET /sessions/x` returns the hashed-asset index.html, `GET /api/nope` returns JSON 404).

Artifact impact plan:

- AGENTS.md: no update expected.
- Runtime project skills: no update expected.
- Specs: `presenter.md` §Routing updated.
- End-user/operator docs: none expected.
- End-user/operator skills: none expected.
- SOW lifecycle: standalone SOW; close on completion.

Open-source reference evidence:

- None checked yet; standard SPA-fallback pattern. Cite at implementation time if an external example is used.

Open decisions:

1. **SPA-fallback policy** — (a) Catch-all: any path that is not `/api/*` and not `/assets/*` (and not an exact root public file like `/favicon.svg`) serves the SPA shell (standard SPA behavior, zero maintenance, but a typo'd non-API path shows the app's in-router NotFound rather than a server 404); (b) Allowlist: only known route prefixes from `App.tsx` serve the shell, everything else 404s (stricter, but the server must be updated whenever a client route is added). **DECIDED (CTO, 2026-05-29): (a) catch-all.** Reasoning: it is the conventional SPA contract; it keeps the server ignorant of the client route table (Phase-2/3 add topology/tools/models/agents routes — an allowlist would force a server change for each); and the app already renders its own `NotFound` for unknown client paths. The mux already routes `/api/*`, `/assets/*`, and the exact `/favicon.svg` to more-specific handlers, so the only behavioral change is in `rootHandler`: drop its `path != "/"` → 404 branch and serve the shell for any GET/HEAD (non-GET/HEAD → 405). Trade-off accepted: `/favicon.ico`-style unknown paths now receive the HTML shell (200) instead of 404 — harmless for a localhost read-only viewer; the app declares `/favicon.svg`. `/api/*` and `/assets/*` stay exempt so real API/asset errors still surface (no silent-failure regression).

## Implications And Decisions

Pending operator approval (see Open decisions #1).

## Plan

1. Spec + tests + `rootHandler` change for SPA fallback; external review; commit. Low risk, contained to the `/` catch-all.

## Execution Log

**2026-05-29 — done.** Spec-first: master updated `presenter.md` §"SPA fallback" +
Routing table (and fixed the now-contradictory "unexpected root path 404s"
language). Tests + code delegated to a subagent (with the `[FORBIDDEN]`
no-reviewers block — the orchestrator owns review). Change is a single localized
edit to `internal/presenter/presenter.go` `rootHandler`: drop the `path != "/"`
→ 404 branch so any GET/HEAD falls through to `p.serveIndex`; keep the
non-GET/HEAD → 405 gate. `/api/*`, `/assets/*`, exact `/favicon.svg` are routed
by more-specific mux patterns (verified against Go ServeMux precedence) and are
unaffected. Tests: rewrote `TestRoot_NonRootPathReturns404` → `…ClientRouteServesSPA`
(two paths); added HEAD-empty-body, POST→405, `…ApiUnknownStillJSON404`
(asserts application/json + NOT_FOUND, not the shell), and a not-built-notice
variant. `scripts/embed-smoke.sh` gained a `GET /sessions/<id>` real-shell
assertion and a `GET /api/<unknown>` → 404 + application/json + NOT_FOUND
assertion.

**Review — orchestrator round (codex + glm + minimax), converged.** All three:
no P1/P2; exemptions correct (codex verified ServeMux trailing-slash precedence
in the stdlib source); security/serveIndex-reuse/HEAD-parity/tests/spec all
sound. 3× P3 — all real, all fixed in the same change: (1) two stale code
comments at `presenter.go` + `embed.go` still said unexpected paths "404 rather
than leaking the shell" (drift the spec fixed but the comments missed) → reworded
to "exact route gives correct content-type/cache; without it the fallback would
serve the shell"; (2) a spec routing-table typo `non-/asset` → `non-/assets`;
(3) the smoke's `/api/unknown` check asserted only HTTP 404, not JSON+envelope →
now asserts status + Content-Type + NOT_FOUND body. The implementation subagent
ran NO reviewers (the delegation `[FORBIDDEN]` carve-out held — no double round).

**Review decision (judgment, recorded):** no re-review round after the 3 P3
fixes. They are doc-comment rewordings, a spec typo, and a test-script assertion
— ZERO runtime-logic change (the substance had already converged across all
three reviewers). Re-verified instead by re-running the full local gate set
(gofmt/goimports/vet/golangci/`gosec@latest` Issues:0/govulncheck/`go test
-race`) + the real-binary `embed-smoke.sh` (the new JSON-envelope assertion
passes). Same proportionality call as SOW-0017; a fresh 3-reviewer round on
no-logic polish would be disproportionate spend.

## Validation

- Unit (package `presenter`): `TestRoot_ClientRouteServesSPA`,
  `…ClientRouteHeadEmptyBody`, `…ClientRouteMethodNotAllowed`,
  `…ApiUnknownStillJSON404`, `…NotBuiltNoticeOnClientRoute`; existing
  `TestServeAsset_MissingReturns404` / `TestServePublicFile_MissingReturns404`
  unchanged and green. `go test -race ./...` all pass.
- Gates (master-run): gofmt/goimports/vet 0; `golangci-lint` 0; standalone
  `gosec` Issues:0; `govulncheck` 0 called; attribution scan PASS.
- Real-binary smoke: `GET /sessions/<id>` → real hashed-asset shell;
  `GET /api/<unknown>` → 404 application/json NOT_FOUND; `/`, `/assets/<hash>`,
  `/favicon.svg`, `/api/health` all still correct.

## Outcome

Hard reload / deep-link / bookmark of a client route (`/sessions/:id`,
`/sources`, …) now loads the app instead of a JSON 404, while `/api/*` and
`/assets/*` still surface real errors. Single localized handler change; no
schema/API/frontend-source change. Merged to master.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
