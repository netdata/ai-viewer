# SOW-0033 - payload byte-preview endpoint (`GET /api/payloads/:ref`) + drawer wire

## Status

Status: open

Sub-state: proposed follow-up (P2). Split out of SOW-0006 (APM tracing UI) on 2026-05-31: SOW-0006 AC#4 specified a span-drawer payload BYTE-preview (first 4 KB via `GET /api/payloads/:ref`, with a download-full link), but that endpoint is a security-sensitive source-file reader and was already a documented Phase-2 deferral (`rest-api.md §GET /api/payloads`; `presenter.go` falls through to notImplemented). SOW-0006 delivered the drawer's LIVE part (op fields + `payload_refs` list + open/Esc/outside-click/focus-trap a11y) and narrowed AC#4 to that; this SOW delivers the byte-preview.

## Requirements

### Purpose

Let an operator preview what an op actually sent/received — the first ~4 KB of a payload (LLM request/response, tool I/O) — inline in the span detail drawer, with a link to download the full payload. This is core APM value (seeing the actual content behind a span), but it requires reading byte ranges out of the source snapshot files, which is a security surface that must be designed deliberately.

### Assistant Understanding

Facts:
- The canonical model carries `payload_refs` on ops (a ref addressing a byte range in a source file). The drawer (`frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx`) already renders each ref's metadata + a DISABLED "preview coming soon" control, structured to wire in the byte-preview trivially.
- `rest-api.md §GET /api/payloads` documents the endpoint as Phase-2. `presenter.go` routes unimplemented `/api/payloads` to notImplemented.
- SOW-0006 R6: serve the preview via a `Range: bytes=0-4095` request; show a truncation marker; link to full download.

Inferences / design to resolve in the gate:
- **Ref → bytes resolution.** Determine the exact `payload_ref` format (how it encodes source file + offset/length) by reading the adapters' `payloads.go` (each adapter builds payload refs). The endpoint resolves a ref to (file, offset, length) and reads only that range.
- **SECURITY (the reason this is its own SOW).** The endpoint reads arbitrary byte ranges from files on the operator's disk. It MUST: (a) resolve refs ONLY within the configured source roots (no path traversal — canonicalize + verify the resolved path is under an allowed root; reject `..`, symlinks escaping the root, absolute paths); (b) bound the read (4 KB preview; a capped full-download); (c) never serve a path not derived from a stored, validated `payload_ref`; (d) localhost-only (consistent with the existing bind). Treat this like the scanner/source-path contracts already in the codebase.
- **Range semantics.** `GET /api/payloads/:ref` returns the first 4 KB by default (or honor `Range: bytes=0-4095`); a `?full=1` (or a separate download route) streams the whole payload with a sane cap. Truncation marker in the response/headers.

### Acceptance Criteria

1. `GET /api/payloads/:ref` returns the first ~4 KB of the referenced payload; a control byte / unknown / out-of-range ref → `BAD_REQUEST` / `NOT_FOUND`; non-GET/HEAD → 405. **Verification**: presenter tests over fixtures (valid ref → bytes; truncation marker; bad/unknown ref → 4xx).
2. **Path-traversal / arbitrary-read is impossible**: a crafted ref cannot read outside the configured source roots. **Verification**: explicit security tests (ref with `..`, absolute path, symlink-escape → rejected); document the containment invariant.
3. The drawer's disabled "preview coming soon" control becomes a live 4 KB preview + truncation marker + download-full link. **Verification**: frontend unit/E2E (the SOW-0006 `viz-drawer.spec.ts` "No payloads / coming soon" path updates to the live preview).
4. Specs reconciled: `rest-api.md §GET /api/payloads` from Phase-2 → implemented (request/Range/response/security contract); `ui-pages.md` drawer note. **Verification**: spec-drift sweep.

## Analysis

Sources: `internal/adapters/*/payloads.go` (ref format), `internal/presenter/presenter.go` (route fall-through), `frontend/src/components/SpanDetailDrawer/*`, `rest-api.md §GET /api/payloads`, the existing source-root/localhost contracts. Discovered 2026-05-31 (SOW-0006 E2E review).

Risks:
- **R1 — Security (primary).** Arbitrary file read if ref→path resolution is not contained. Mitigation: AC#2 security tests + canonicalize-under-root invariant; gosec review.
- **R2 — Ref format variance across adapters.** Each adapter may encode refs differently. Mitigation: audit all adapters' `payloads.go`; a single resolver with per-format handling + tests per format.
- **R3 — Large payloads.** Full download of a multi-MB payload. Mitigation: stream + cap; preview is always bounded to 4 KB.

## Pre-Implementation Gate

(To be filled on pickup. Must: nail the `payload_ref` format from every adapter's `payloads.go`; design the ref→path resolver with the containment invariant; write the security tests FIRST; decide preview-vs-full route shape.)

## Implementation / Validation / Reviews / Outcome

(Empty placeholders.)

## Lessons / Follow-Ups

Pending. Parent: SOW-0006 (APM tracing UI). The drawer is already built to consume this; the gate is mostly the secure ref→bytes resolver + its tests.
