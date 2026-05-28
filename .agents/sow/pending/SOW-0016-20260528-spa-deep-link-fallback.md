# SOW-0016 - SPA deep-link fallback for client-side routes

## Status

Status: open

Sub-state: awaits operator approval before moving to current/. Surfaced by an external reviewer during SOW-0001 Chunk 17 (single-binary serve) review; filed as tracked follow-up rather than expanding Chunk 17's agreed scope (the Chunk 17 gate explicitly scoped "SPA fallback unchanged").

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

Status: needs-user-decision

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

1. **SPA-fallback policy** — (a) Catch-all: any path that is not `/api/*` and not `/assets/*` serves the SPA shell (standard SPA behavior, zero maintenance, but a typo'd non-API path shows the app's in-router NotFound rather than a server 404); (b) Allowlist: only known route prefixes from `App.tsx` serve the shell, everything else 404s (stricter, but the server must be updated whenever a client route is added). Recommendation: (a) — it is the conventional SPA contract, keeps the server ignorant of client routes, and the app already renders its own `NotFound` for unknown client paths. Decision pending operator/CTO sign-off at SOW activation.

## Implications And Decisions

Pending operator approval (see Open decisions #1).

## Plan

1. Spec + tests + `rootHandler` change for SPA fallback; external review; commit. Low risk, contained to the `/` catch-all.

## Execution Log

None yet.

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
