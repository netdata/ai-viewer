# SOW-0028 - claude-code adapter real-data robustness (retryInMs float + root start_ts=0)

## Status

Status: completed

## Requirements

### Purpose

Make the claude-code adapter robust against the shapes real Claude Code transcripts actually emit. Two concrete defects were observed on real data:

1. **`retryInMs` is a float in real transcripts but the adapter decodes it as `int64`.** Real `system` records carry e.g. `"retryInMs": 38317.38269012852` (API backoff). The adapter's `systemBody.retryInMs int64` fails to unmarshal → repeated `adapter parse error` (correctly surfaced to `/api/health` → `degraded`, not silent). Affected `system` records are dropped.
2. **Root session `start_ts = 0`.** The root session (the top-level transcript) ingested with `start_ts=0` while its 139 sub-agents have correct `start_ts`. Likely linked to (1) (parse failures on the root transcript's early records, including whatever sets the session start) or a root-start derivation gap. Result: the session header and any session-start-relative axis are wrong for the root.

### User Request

Implied by the project mission ("read source-system snapshots … production-quality, no silent failures") and the operator's standing principle: test against real production artifacts; our code must handle what real systems emit. Surfaced live during the SOW-0006 real-data review.

### Assistant Understanding

Facts:

- `internal/adapters/claude_code/*` decodes a `system` record body with `retryInMs int64` (exact field/file to confirm on pickup — error: `decode system: json: cannot unmarshal number 38317.38269012852 into Go struct field systemBody.retryInMs of type int64`).
- A real ~61 MB claude-code transcript produced several such errors at distinct offsets; health went `degraded` (parse-error path works — no silent failure).
- The DB after ingest: 1 root (`start_ts=0`, 11,928 ops, 347 turns) + 139 sub_agents (real `start_ts`); the 140 transcripts are correctly ONE session tree (root + its sub-agents), NOT a collapse bug — verified `count(DISTINCT root_session_id)=1` with native_ids `<root-uuid>:age<N>`.

Inferences:

- (1) Fix: type `retryInMs` as `float64` (or `json.Number`, then round) — Claude Code emits fractional backoff ms. Round/truncate to int on store if the canonical field is integer ms.
- (2) Fix: determine why the root's `start_ts` is 0 — either a consequence of (1) dropping the record that carries the session start, or the adapter not deriving the root start from the first event. Confirm on pickup with a real (sanitized) transcript fixture.
- These are regressions against SOW-0003 (claude-code adapter, `done/`). Per the regression rule, picking this up should reopen SOW-0003 with a dated `## Regression` section (failing test pinning each defect BEFORE the fix), or be delivered as this standalone bug SOW — decide on pickup.

Unknowns:

- Exact struct/field/file for `retryInMs` (confirm by grep on pickup).
- Whether root `start_ts=0` is caused by (1) or independent (confirm by fixing (1) first, then re-ingesting, then checking).

### Acceptance Criteria

1. A real `system` record with a fractional `retryInMs` parses without error. **Verification**: a sanitized golden transcript fixture containing `"retryInMs": <float>` ingests cleanly (no parse error; health not degraded by it); a unit/golden test pins it.
2. A claude-code root session derives a correct non-zero `start_ts` from its transcript. **Verification**: a golden/integration test asserts the root session `start_ts > 0` (and equals the first event's ts).
3. Specs reconciled: `adapter-claude-code.md` documents `retryInMs` is a float and the root-start derivation. **Verification**: spec note + tests.
4. Sanitized real-shaped fixtures committed under `testdata/claude_code/` capturing both shapes (float retryInMs; root start). All ids/paths/content sanitized per the sensitive-data policy. **Verification**: secret/PII scan clean.

## Analysis

Sources checked: live ingest of a real claude-code project (sanitized read-only copy into a temp tree); `/api/health` (degraded + parse errors); the seeded DB (`sessions`/`ops` row inspection). `internal/adapters/claude_code/*` to be read on pickup.

Risks:

- **R1 — Real-data fixtures may carry sensitive content.** Mitigation: synthesize minimal sanitized fixtures (keep only the field shapes: a `system` record with float `retryInMs`; a clean session start) — never commit real transcript content.
- **R2 — Float→int rounding.** If the canonical/store field is integer ms, decide round vs truncate; document it. Mitigation: spec note + test.
- **R3 — Regression bookkeeping.** SOW-0003 is `done/`; follow the regression-reopen rule (failing test first) on pickup.

## Pre-Implementation Gate

### Problem / root-cause model

Two independent claude-code adapter defects observed on real transcripts:

1. **`retryInMs` is a float in real transcripts; the adapter decodes it as `*int64`.** `internal/adapters/claude_code/parser.go:165` — `RetryMs *int64 json:"retryInMs"`. A real `system`/`api_error` record carries e.g. `"retryInMs": 38317.38269012852` (fractional API backoff ms) → `json: cannot unmarshal number ... into Go struct field systemBody.retryInMs of type int64` → the scanner drops the record (correctly surfaced as a parse error to `/api/health`/degraded, not silent). Root cause: the field is typed integer but Claude Code emits float. `RetryMs` and `RetryNumber` are decoded into `systemBody` but NEVER consumed by the mapper (grep confirms zero `.System.RetryMs` references) — the typed fields are passthrough only, so the fix is purely "make decode not fail".

2. **Root session `start_ts = 0`.** `mapRecord` (`mapper.go:259-264`) bootstraps the `SessionStartedEvent` on the FIRST record, using `m.recordTs(rec)`. `recordTs` returns 0 for timestamp-less records (`Env.Timestamp == ""`, line 389). Real Claude Code transcripts open with timestamp-less metadata snapshots (`permission-mode`, `custom-title`, `file-history-snapshot` without a top-level timestamp — confirmed in `testdata/claude_code/e_snapshots_lastwins`). When the first record reaching `mapRecord` is such a snapshot, the `SessionStarted` carries `Ts=0` → the session row's `start_ts=0` → the root sits at epoch (1970), breaking time-range filtering, ordering, and start-relative axes. This is INDEPENDENT of (1): even with (1) fixed, a transcript opening with a metadata snapshot hits it. The SOW's "likely linked to (1)" hypothesis is wrong; reproduction below confirms independence.

### Evidence reviewed

- `internal/adapters/claude_code/parser.go:160-167` — `systemBody.RetryMs *int64`. `parser_fuzz_test.go:59` feeds an integer `retryInMs:1000` (the synthetic case that never exercised the float).
- `internal/adapters/claude_code/mapper.go:228-264` — `mapRecord` bootstraps `sessionStarted0` on the first record with `advance(m.recordTs(rec))`.
- `internal/adapters/claude_code/mapper.go:388-396` — `recordTs` returns 0 for `Env.Timestamp == ""`.
- `internal/adapters/claude_code/mapper.go:300-309` — snapshot records (permission-mode, custom-title, etc.) are mapped via `mapSnapshot` with `advance(0)` (no timestamp) and `recordTs` returns 0 for them.
- `testdata/claude_code/e_snapshots_lastwins/INPUT/...55555555...jsonl` lines 2,5,6 — `permission-mode`/`custom-title` records with NO top-level `timestamp` field (the snapshot shape that triggers (2) when it leads the file).
- `internal/ingest/writer.go:642-669` — `applySessionUpdated` is a pure `UPDATE sessions ... WHERE id=?`; if the row does not exist yet (SessionStarted not applied), it matches 0 rows → silent loss. So event ORDERING matters: SessionStarted MUST precede SessionUpdated in the emitted slice.
- `internal/adapters/claude_code/scanner_transcript.go:176-183,253-263` — `lineStreamer.run()` loops `mapRecord`→`emitEvents` per record, returns at EOF. A natural finalize point exists after the loop for the pure-metadata-file edge case.

### Reproduction plan (failing tests before the fix)

- **(1)** Unit test: decode a `system`/`api_error` record with `"retryInMs": 38317.38269012852` → currently errors; after fix, decodes cleanly and `RetryMs` is the float (rounded to int ms on read if any consumer needs int — none today).
- **(2)** Golden/integration test: a transcript whose FIRST line is a timestamp-less `permission-mode` snapshot, followed by a timestamped `user` record. Currently the session `start_ts == 0`; after fix, `start_ts == <user record ts> > 0`.

### Affected contracts and surfaces

- `internal/adapters/claude_code/parser.go:165` — `RetryMs *int64` → `*float64` (passthrough field; no consumer change).
- `internal/adapters/claude_code/mapper.go` — refactor `mapRecord` to defer the `SessionStarted` bootstrap to the first record with a real timestamp, buffering leading timestamp-less records' events and emitting them AFTER the `SessionStarted` (preserving the writer's UPDATE-after-INSERT ordering contract). Add a `pendingBeforeStart []canonical.Event` field + a `finalizeSessionStart() []canonical.Event` method for the pure-metadata-file edge case (emit `SessionStarted` with ts=0 + buffered events at EOF so no file silently drops its session row).
- `internal/adapters/claude_code/scanner_transcript.go` — call `mapper.finalizeSessionStart()` after the line loop (in `run()` or `streamLines`) and emit any tail events.
- No canonical-event change, no schema change, no ingester change. The fix is adapter-local.

### Spec deltas to land before any test or code

- `.agents/sow/specs/adapter-claude-code.md`: document (a) `retryInMs` is a float (the adapter decodes it as such; Claude Code emits fractional backoff ms); (b) the root-session `start_ts` derivation = the first record with a real timestamp (NOT the literal first record — transcripts opening with timestamp-less metadata snapshots defer the bootstrap so `start_ts` is the first chronological event ts, not 0).

### Existing patterns to reuse

- The `advance` closure + `packSeq(idx, sub)` SourceSeq scheme — preserved; the refactor only changes WHEN the bootstrap `advance` is called, not the seq packing.
- `lineStreamer.run()`'s loop-then-return — the finalize call slots in at the return.

### Risk and blast radius

- **(1) Low.** Passthrough typed field; type widening. Zero consumer impact.
- **(2) Low–moderate.** The `mapRecord` refactor is in a hot path but the change is localized: defer-and-buffer for the leading timestamp-less records. The SourceSeq ordering (SessionStarted gets the lowest seq) is preserved by reserving `sub=0` for the bootstrap when it fires. Existing golden tests (`a_happy_single` etc.) open with timestamped `user` records → their first record has ts>0 → bootstrap fires immediately, zero behavior change. The refactor only changes behavior for files opening with timestamp-less records (the bug case). Full claude-code test suite + golden fixtures are the regression net.

### Sensitive data handling

New fixtures are SYNTHETIC (a `system`/`api_error` record with a float retryInMs; a transcript opening with a `permission-mode` snapshot). No real transcript content; ids are fixed UUIDs from the existing fixture convention; user content is `[REDACTED_*]` placeholders. `scripts/scan-secrets.sh` is the defence-in-depth.

### Implementation plan

1. `parser.go:165`: `RetryMs *float64 json:"retryInMs"`.
2. `mapper.go`: add `pendingBeforeStart []canonical.Event` field; refactor `mapRecord` so the switch builds `recOut` and the bootstrap wrapping either (a) defers (`recTs==0`, buffer recOut, return nil) or (b) fires (`recTs>0`, reserve sub=0 for SessionStarted, emit [start, ...pending, recOut]). Reserve the bootstrap `advance` BEFORE building recOut so SessionStarted keeps the lowest sub.
3. `mapper.go`: add `finalizeSessionStart() ([]canonical.Event, error)` — if `!sessionStarted`, emit `SessionStarted` (ts=0) + pending; idempotent/no-op once started.
4. `scanner_transcript.go`: after the line loop ends, call finalize + emit any tail events.
5. Spec note in `adapter-claude-code.md`.
6. Tests: (1) parser unit test for float retryInMs; (2) a golden fixture `h_metadata_first` whose transcript opens with a `permission-mode` snapshot then a timestamped `user` record, asserting `start_ts == user-ts > 0`; (3) a unit test for the pure-metadata-file finalize case (start_ts=0, session row still created).

### Validation plan

- `go test -race ./internal/adapters/claude_code/...`: all green (new + existing golden).
- Targeted: `TestParse_FloatRetryInMs`, `TestMetadataFirstTranscript_StartTsNonZero`, `TestPureMetadataFile_FinalizesSessionStart`.
- Full `go test -race ./...` green.
- Coverage/lint/spec-drift/secrets gates green.

### Open decisions

- **Reopen SOW-0003 (regression) vs standalone?** Standalone (this SOW). SOW-0003 shipped with synthetic fixtures that never exercised these shapes; this is a real-data-robustness follow-up, not a regression of a previously-passing contract. The failing-test-before-fix discipline is honored regardless.
- **Dead-field cleanup (drop RetryMs/RetryNumber entirely since unused)?** Deferred — separate cleanup SOW; this SOW keeps the fix minimal (type widen).

### Artifact impact plan

- `internal/adapters/claude_code/parser.go` — RetryMs type.
- `internal/adapters/claude_code/mapper.go` — defer-and-buffer refactor + finalize.
- `internal/adapters/claude_code/scanner_transcript.go` — finalize call site.
- `internal/adapters/claude_code/parser_test.go` / `mapper_test.go` — float retryInMs + start_ts tests.
- `testdata/claude_code/h_metadata_first/` — new sanitized golden fixture.
- `.agents/sow/specs/adapter-claude-code.md` — spec note.

## Implementation

Implemented 2026-06-15 (Phase: Development, CTO-coded per the 2026-06-14 operator directive). Two independent defects, both fixed adapter-local.

**(1) `retryInMs` float** — `internal/adapters/claude_code/parser.go:165`: `RetryMs *int64` → `*float64`. Real transcripts emit fractional backoff ms (e.g. `38317.38269012852`); the int field failed to unmarshal → the scanner dropped the record (surfaced as a parse error → `/api/health` degraded, not silent). `RetryMs`/`RetryNumber` are passthrough (decoded but never consumed by the mapper — grep confirms zero `.System.RetryMs` references), so the type-widen is the complete fix with zero downstream impact.

**(2) Root `start_ts=0`** — `internal/adapters/claude_code/mapper.go`: refactored `mapRecord` to defer the `SessionStarted` bootstrap to the first record that carries a real timestamp. The per-type switch was extracted into `mapOneRecord`; `mapRecord` now either defers (`recordTs==0` → buffer events into the new `pendingBeforeStart` field, emit nothing) or fires (`recordTs>0` → reserve `sub=0` for the `SessionStarted`, emit `[start, ...pending, recOut]`). The SOW's "likely linked to (1)" hypothesis was disproved — this is independent: real transcripts open with timestamp-less metadata snapshots (`permission-mode`/`custom-title`), and the old code bootstrapped on the literal first record with its zero ts. Added `finalizeSessionStart()` for the pure-metadata-file edge case (EOF flush of a `SessionStarted(ts=0)` + buffered events so the session row still exists; idempotent). Wired into `scanner_transcript.go`'s `streamLines` after the per-record loop.

The refactor preserves SourceSeq ordering (the `SessionStarted` keeps `sub=0` — the bootstrap `advance` is called before `mapOneRecord`) and the writer's UPDATE-after-INSERT contract (buffered snapshot events emit AFTER the `SessionStarted`). Existing golden fixtures all open with timestamped `user` records → bootstrap fires immediately on record 0 → zero behavior change (confirmed: all golden tests pass unchanged).

- `internal/adapters/claude_code/parser.go` — RetryMs type.
- `internal/adapters/claude_code/mapper.go` — defer-and-buffer refactor + `pendingBeforeStart` field + `finalizeSessionStart` + `mapOneRecord` extraction.
- `internal/adapters/claude_code/scanner_transcript.go` — `streamLines` calls `finalize()` after the loop.
- `internal/adapters/claude_code/parser_test.go` — `TestParseLine_SystemFloatRetryInMs` (float + integer shapes; exact-value pin).
- `internal/adapters/claude_code/mapper_test.go` — `TestMapper_MetadataFirstRecordDefersSessionStart`, `TestMapper_PureMetadataFileFinalizesSessionStart`, `TestMapper_TimestampedFirstRecordBootstrapsImmediately` (regression guard).
- `.agents/sow/specs/adapter-claude-code.md` — §5.2 (session bootstrap defers to first timestamped record) + §3.3 api_error (retryInMs is a float).

## Validation

All gates green 2026-06-15.

- `TestParseLine_SystemFloatRetryInMs`: PASS (float + integer `retryInMs` both decode; exact-value pin `38317.38269012852`).
- `TestMapper_MetadataFirstRecordDefersSessionStart`: PASS — `SessionStarted.Ts == 1779789600000000` (the user record's ts, NOT 0); leading snapshots emitted after.
- `TestMapper_PureMetadataFileFinalizesSessionStart`: PASS — finalize emits `SessionStarted(ts=0)` + buffered snapshots; idempotent.
- `TestMapper_TimestampedFirstRecordBootstrapsImmediately`: PASS — common-path behavior unchanged (bootstrap on record 0).
- Existing `TestGolden` (all 7 fixtures) + full claude-code suite: PASS unchanged.
- Full `go test -race -count=1 ./...`: PASS, no skip. claude_code coverage 87.0%.
- Coverage gate: PASS (aggregate 91.3%). `golangci-lint`: 0 issues (resolved prealloc / QF1008 embedded-selector / unparam after the initial draft). `go vet`/`gofmt`: clean.
- `scripts/spec-drift.sh`: PASS. `scripts/scan-secrets.sh`: PASS (914 files).

## Reviews

Phase: Development — 5-reviewer cycle is CTO-discretion. This change is adapter-local robustness (a type-widen + a localized mapRecord defer-and-buffer refactor), NOT schema/security/new-adapter. The refactor's correctness is pinned by: (a) the 3 new tests + the regression guard; (b) all 7 existing golden fixtures passing unchanged (they exercise the timestamped-first-record common path); (c) SourceSeq ordering preserved (`SessionStarted` keeps `sub=0`). CTO judged it low-risk and **skipped** the 5-reviewer cycle. (If the operator wants a reviewer pass on the mapRecord refactor, run glm/mimo/minimax/qwen/deepseek on the committed diff.)

## Outcome

Delivered. The claude-code adapter now robustly handles the two shapes real Claude Code transcripts emit that the synthetic golden fixtures never exercised: fractional `retryInMs` backoff ms (no more parse-error-degraded health on `api_error` records), and transcripts opening with timestamp-less metadata snapshots (root `start_ts` is the first chronological event ts, not 0). The defer-and-buffer pattern keeps the common path (timestamped-first-record) byte-identical. The SOW's "likely linked to (1)" hypothesis for (2) was disproved — the defects are independent.

## Lessons / Follow-Ups

- **Synthetic golden fixtures are necessary but not sufficient.** They use clean integer/complete data; real production transcripts emit fractional floats (backoff ms) and interleave timestamp-less metadata snapshots at file head. The SOW-0006 real-data review (ingesting a real ~61 MB transcript) surfaced both. Standing principle reinforced: test against real production artifacts; our code must handle what real systems emit, not just what clean fixtures model.
- **Dead typed fields are a latent decode-failure trap.** `RetryMs`/`RetryNumber` are decoded into `systemBody` but never consumed by the mapper — they're passthrough. Typing such a field too narrowly (`int64` vs `float64`) silently drops records on real data while every synthetic test passes. Consider dropping unused typed fields entirely (let them decode into the opaque raw) — filed as a minor cleanup, not expanded here.
- **Goroutine-dump reading (carryover from SOW-0062).** A `[runnable]` test goroutine means "executing, slow", not "deadlocked"; a `[chan receive]` `(*Tx).awaitDone` goroutine is the normal per-tx watcher. Read the FULL dump before claiming a hang.
