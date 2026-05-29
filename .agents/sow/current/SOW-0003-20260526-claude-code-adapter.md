# SOW-0003 - claude-code adapter (Scan + Tail + cursor + sub-agents + compaction)

## Status

Status: in-progress

Sub-state: blanket Phase-2 sign-off (2026-05-29); moved to current/. SOW-0001 prerequisite is completed. Pre-Implementation Gate filled; spec delta D1 landed; adapter built (C2-C7) + gate-verified by the orchestrator; external review pending before merge + close.

## Requirements

### Purpose

Deliver the claude-code adapter end-to-end: Scan + Tail of `~/.claude/projects/<sanitized-cwd>/<sessionId>.jsonl` transcripts (and their `subagents/agent-<agentId>.jsonl` sidechains + `.meta.json` sidecars), faithful mapping to the canonical event model per `adapter-claude-code.md`, durable byte-offset cursors, full fixture coverage of every observed record type, auto-discovery probe so operators on a workstation with Claude Code installed get the source wired automatically, and a fuzz target on the JSONL parser. Outcome: the operator can open ai-viewer and see every Claude Code session they have ever run on this workstation, with sub-agent linkage rendered as topology and compaction rendered as a first-class event on the timeline.

### User Request

From the operator's 2026-05-26 milestone list (recorded in conversation while planning post-Phase-1 work): "Add claude-code, codex, and opencode adapters next, one SOW each, so each can be reviewed and scoped independently." This SOW is the claude-code half of that instruction and inherits its full scope (parser + Scan + Tail + cursor + tests + fixtures + auto-discovery + spec sync).

### Assistant Understanding

Facts:

- The operator's workstation has 3,614 root-level `*.jsonl` transcripts across 70 project directories under `~/.claude/projects/` plus subagent sidechains under `<sessionId>/subagents/agent-<agentId>.jsonl` (observed 2026-05-26, recorded in `adapter-claude-code.md` §2.3, §12).
- The encoded-cwd directory naming is `cwd.replace(/[^a-zA-Z0-9]/g, '-')` truncated at 200 chars with a hash suffix when over (`adapter-claude-code.md` §2.2; `jarmuine/claude-code @ <commit> :: src/utils/sessionStoragePortable.ts:311-319`). Bun and Node hash differently, producing distinct dir names for the same long cwd.
- Sub-agent linkage is **structural**: subagent jsonl lives at `<parent-jsonl-dir>/<parent-sessionId>/subagents/agent-<agentId>.jsonl` with sidecar `agent-<agentId>.meta.json` carrying `toolUseId` that joins back to the parent's `assistant.tool_use` block (`adapter-claude-code.md` §4, §8). Subagent records carry the same `sessionId` as the parent; the adapter synthesizes `NativeID = <parentSessionId>:agent:<agentId>` to avoid collision (§5.1, §5.2).
- Compaction is explicit: `system.subtype="compact_boundary"` records carry `compactMetadata{trigger, preTokens, postTokens, durationMs, preservedSegment, preservedMessages}` (§3.3, §9). Canonical mapping is a synthetic op `OpKind='compaction'` (per `canonical-events.md`).
- No cost data anywhere in claude-code transcripts (`adapter-claude-code.md` §3.2, §5.6); cost is computed via `internal/pricing/` static catalog using `(provider="anthropic", model)` keys.
- No native session-end signal (§11.11); claude-code sessions remain `status='running'` indefinitely (resumable). The adapter never emits `SessionFinalizedEvent` for claude-code.
- Files are append-only with `appendFileSync` (`jarmuine/claude-code @ <commit> :: src/utils/sessionStorage.ts:2572-2584`); no atomic rename. Byte-offset tail is correct.
- Phase 1 Foundation (SOW-0001) delivers `internal/canonical/`, `internal/ingest/`, `internal/store/`, `internal/adapters/registry.go`, the `canonical.Adapter` interface, pricing catalog, fixture sanitization tooling, and CI gates that this SOW reuses unchanged.

Inferences:

- The encoded-cwd hash-suffix divergence between Bun (CLI) and Node (SDK) is a real risk for any operator running both with a >200-char cwd; the adapter must surface both project dirs in `/health` without merging (`adapter-claude-code.md` §10.4).
- Backfill of 3,614 root jsonls + sidechains will be I/O-bound, not CPU-bound; expected wall-clock under 5 min single-threaded based on file sizes observed (typical 10-500 KB per session, occasional >100 MB).
- The fuzz target should cover the JSONL line parser (every observed `type` value plus unknown-type tolerance), not the file walker; the walker is deterministic.

Unknowns:

- Exact wall-clock perf on the operator's 3,614 transcripts — measured during implementation. Below 5 min single-thread per inference; if not, parallelize.
- Whether any sub-agent sidechain on this workstation exhibits the Bun-vs-Node hash divergence (i.e. paired duplicate project dirs). Resolved by an exploratory `find` during Pre-Implementation Gate authoring.
- Whether `compactMetadata.trigger` values beyond `"manual"` (e.g. `"auto"`, `"clear"`) appear in any observed file. Documented in spec as "tolerate any string"; resolved by `jq` aggregation when fixtures are curated.

### Acceptance Criteria

1. `internal/adapters/claude_code/` package compiles, lints clean, and is registered in `internal/adapters/registry.go`. **Verification**: `go build ./...` exits 0; `golangci-lint run` exits 0; `internal/adapters/registry_test.go` asserts the adapter is enumerable by name `"claude-code"`.
2. Scan + Tail correctly ingests every record type listed in `adapter-claude-code.md` §3 (envelope + 12 observed types + at least 6 declared-but-not-observed types tolerated). **Verification**: golden tests per scenario (see #5) plus a tolerance test that feeds 20 unknown `type` strings and asserts exactly one `SourceError` per unknown variant.
3. Sub-agent linkage is correct: the adapter emits a separate canonical Session per `subagents/agent-<agentId>.jsonl` with `Kind='sub_agent'`, `NativeID=<parentSessionId>:agent:<agentId>`, `ParentNativeID=<parentSessionId>`, and `AgentName` populated from `.meta.json::agentType`. **Verification**: golden test on a real-data sub-agent fixture asserts the synthesized NativeID and the parent's `Agent` tool_use op carries `ChildSessionNativeID` matching.
4. Compaction is mapped to a first-class `OpKind='compaction'` event with `Ts=boundary.timestamp`, `EndTs=Ts+durationMs*1000`, `BytesIn=preTokens`, `BytesOut=postTokens`, `Extras=compactMetadata`; the post-compaction `isCompactSummary:true` user message does NOT start a new turn (per `adapter-claude-code.md` §9.2). **Verification**: golden test on a real compaction fixture asserts the synthesized op exists and the next operator-typed user prompt opens turn N+1, not the summary record.
5. Golden tests cover at minimum: (a) happy-path single-session no sub-agents, (b) main session with one sub-agent and its sidechain, (c) session with one `compact_boundary` mid-stream, (d) session with mid-stream cwd change (§2.5), (e) session with `file-history-snapshot` and `permission-mode` last-wins records, (f) older "orphan-root" session that has only a `subagents/` dir and no parent jsonl (§10.1), (g) resumed session (`claude --resume`) appending to an existing file the adapter has already cursored. **Verification**: each golden test reads a sanitized fixture under `testdata/claude_code/<scenario>/`, runs the adapter to completion, and diffs the emitted canonical event stream against a committed `.golden.json`.
6. Byte-offset cursor is durable across restart: stopping the adapter mid-stream and restarting it produces zero duplicate events and zero gaps. **Verification**: integration test that ingests half a fixture, persists cursor, restarts, ingests rest, asserts identical end state to a one-shot ingest of the same fixture.
7. Fuzz target on the JSONL line parser passes the SOW-0001 gate (`go test -fuzz=Fuzz... -fuzztime=30s` zero crashes). **Verification**: `internal/adapters/claude_code/parser_fuzz_test.go` is added; CI runs it on the standard fuzz budget.
8. Auto-discovery probe detects `~/.claude/projects/` (and `$CLAUDE_CONFIG_DIR/projects` when set) at startup, registers the source automatically, and exposes counts in `/api/health`. **Verification**: unit test on the probe with a tmpdir layout containing one fake project dir; manual run on the operator's workstation registers the real source and `/api/sources` reports the project-dir count.

## Analysis

Sources checked:

- `.agents/sow/specs/adapter-claude-code.md` (full spec, all 12 sections) — primary contract.
- `.agents/sow/specs/canonical-events.md` — target event types, including `OpKind='compaction'`, `Kind='sub_agent'`, the indefinite-`running` SessionStatus, cache-token fields, `ParentOpSeq`.
- `.agents/sow/specs/data-model.md` — SQLite schema, especially `sessions.cwd`, `sessions.extras_json`, `payload_refs.location_uri`, cross-format compatibility matrix.
- `.agents/sow/done/SOW-0002-20260526-cross-format-data-model-analysis.md` — analysis context confirming claude-code's structural quirks.
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` — infrastructure the adapter plugs into.
- Real evidence on the operator's workstation: `~/.claude/projects/` (3,614 root jsonls across 70 project dirs as of 2026-05-26).
- Upstream reverse-engineered TypeScript at `jarmuine/claude-code @ <commit>` (frozen mirror) — `src/utils/sessionStoragePortable.ts`, `src/utils/sessionStorage.ts`, `src/types/logs.ts` per `adapter-claude-code.md` §12.

Current state:

- SOW-0001 (in-progress) delivers canonical event types, SQLite store, ingest pipeline, adapter registry, pricing catalog, fixture sanitization tooling, CI gates, and the ai-agent v3/v2 adapters end-to-end. This SOW assumes that infrastructure is in place; if SOW-0001 is not yet completed, this SOW remains in `pending/`.
- No `internal/adapters/claude_code/` package exists yet (the bootstrap only documented the format).

Risks:

- **R1 — Encoded-cwd hash divergence (Bun vs Node).** Long paths produce different directory names from CLI vs SDK (`adapter-claude-code.md` §10.4). Mitigation: the adapter does NOT invert sanitization; it reads each project dir as-is and surfaces both in `/health`. The cursor is keyed on relative path within `~/.claude/projects/`, so duplicate dirs do not collide.
- **R2 — Resume-of-resumed sessions and partial line parking.** `claude --resume` appends to existing files; OS page-cache boundaries can leave the trailing line incomplete (`adapter-claude-code.md` §6.3, §10.2). Mitigation: the tail loop parks partial bytes (anything past the last `\n`), resumes on next WRITE event; covered by acceptance #6 integration test.
- **R3 — Sidechain ordering and pre-parent observation.** A subagent jsonl may be observed before the parent's `assistant.tool_use` block that spawned it (file mtimes are independent). Mitigation: emit the child `SessionStartedEvent` with `ParentNativeID` populated immediately (the path encodes the parent), then let the ingester's resolver pass attach the parent op when it appears. Same pattern as Phase 1's ai-agent v2 resolver per SOW-0001 plan step 6.
- **R4 — Old claude-code versions and unknown record types.** Observed `version` ranges 2.1.76 → 2.1.150 (`adapter-claude-code.md` §1, §3). Older formats may lack fields; producer's `Entry` union declares record types never observed (`summary`, `task-summary`, `tag`, `marble-origami-*`, etc.). Mitigation: defensive parsing with one informational `SourceError` per unknown variant; acceptance #2 pins this. Specs §3.12 already enumerates the declared-but-unobserved set.
- **R5 — Missing pricing for some Claude models.** `pricing.json` (delivered in SOW-0001) is the cost source; if `assistant.message.model` is a value not in the catalog, cost is left empty rather than guessed. Mitigation: emit a structured WARN with the unknown model id once per source-startup; the operator runs `scripts/refresh-pricing.sh` to update.

## Pre-Implementation Gate

Picked up 2026-05-29 (blanket Phase-2 sign-off). This is a greenfield adapter that
plugs into the SOW-0001 infrastructure and mirrors `aiagent_v3` — no behavior
change to existing code, only additions.

**Evidence reviewed (this session):**
- Reference pattern (Explore): `internal/adapters/aiagent_v3/` = adapter.go
  (Name/Format/Scan/Tail/ParseCursor + `adapters.Register` in `init`), cursor.go
  (per-file offset map + Version; String/After), parser.go (line JSONL decoder),
  mapper.go / ops.go (record→canonical Events; SourceSeq packing), scanner.go
  (list + walk from cursor), tailer.go (fsnotify on the root), payloads.go;
  `*_test.go` + `fuzz_test.go`; golden fixtures under the adapter's `testdata/`.
- Auto-discovery: `cmd/ai-viewer-ingest/sources.go` probes `~/.ai-agent/sessions`
  and surfaces counts in `/api/health`; the claude-code probe slots into the same
  array (format `claude-code`, location `~/.claude/projects`, `$CLAUDE_CONFIG_DIR`
  honored).
- Canonical targets: `internal/canonical/events.go` already defines
  `OpCompaction = "compaction"` and `SessionKind` `sub_agent` — no canonical
  schema change needed.
- **Workstation recon (live `~/.claude/projects`, read-only):** 56 project dirs,
  397 root transcripts, 622 sub-agent sidechains + 622 `.meta.json`. Longest
  encoded dir name = 72 chars (far below the 200-char truncation point) → **no
  Bun/Node hash-divergence pairs on this workstation** (R1 cannot be exercised
  against a live pair; the adapter still handles it generically per spec via
  read-dir-as-is + prefix tolerance). `compactMetadata.trigger` observed values:
  `manual` (24) and `auto` (8) → fixtures must cover both; adapter tolerates any
  string. Real `compact_boundary` records exist → a real compaction fixture is
  curatable. Backfill of 397 transcripts is well under the 5-min target
  single-threaded (R-perf resolved; no parallelization).

**Decisions (CTO):**
1. **Compaction → `OpCompaction`** (first-class), per acceptance #4 and
   `events.go`. `adapter-claude-code.md §9.1` currently says `Kind='internal'` —
   that is stale drift against the canonical model + the SOW; it is corrected as a
   spec delta below.
2. No SourceSeq cardinality cleverness beyond what `aiagent_v3` does; claude-code
   has no native seq, so SourceSeq = a stable per-file monotonic derived from
   record order (byte offset is the durable cursor; SourceSeq is observability
   only, per the SourceSeq-semantics convention).
3. Sessions are always emitted `running` (no terminal signal); never emit
   `SessionFinalizedEvent` for claude-code.
4. Sub-agent NativeID = `<parentSessionId>:agent:<agentId>`,
   ParentNativeID = `<parentSessionId>`; rely on the ingester resolver (SOW-0001)
   for child-before-parent ordering (R3) — and the resolver now emits notify on
   linkage (PR #26), so an open UI refreshes.

**Spec deltas to land BEFORE tests/code** (both in
`.agents/sow/specs/adapter-claude-code.md`):
- **D1:** §9 — change the compaction synthetic-op mapping from `Kind='internal',
  Name='compact'` to `OpKind='compaction'` (`Ts=boundary.timestamp`,
  `EndTs=Ts+durationMs*1000`, `BytesIn=preTokens`, `BytesOut=postTokens`,
  `Extras=compactMetadata`), matching acceptance #4 + `canonical-events.md`.
- **D2:** ~~add a consolidated mapping table~~ — **already satisfied**: §5.4
  "Per-record mapping" is exactly that table (verified on pickup). No delta needed;
  D1 brought its compaction row into line with the canonical model.

**Affected surfaces:** new `internal/adapters/claude_code/` package; one line in
`internal/adapters/registry.go` (blank import) + `init` `Register`; one probe in
`cmd/ai-viewer-ingest/sources.go`; `/api/health` source list gains a `claude-code`
entry when the dir exists; new `testdata/claude_code/<scenario>/` fixtures;
the spec deltas above. No migration, no canonical-schema change, no API shape
change (sessions/ops already carry the needed fields).

**Risk / blast radius:** additive. The only cross-cutting touch is the
auto-discovery probe (guard: only registers when the dir exists; a workstation
without Claude Code is unaffected — unit-tested with a tmpdir). R1-R5 per the
Analysis; R1 has no live instance here (handle generically), R3 covered by the
resolver.

**Sensitive-data plan:** every fixture is produced via `scripts/sanitize-fixture.sh`
(real cwd → `<HOME>`, emails/secrets redacted); the `scripts/scan-secrets.sh` gate
runs in CI; no real path, prompt, or tool I/O reaches a committed artifact. Curate
fixtures from the operator's real transcripts then sanitize — never commit raw.

**Implementation plan (chunks, mirroring the aiagent_v3 build order):**
- C1: spec deltas D1 + D2 (this gate's prerequisite).
- C2: `parser.go` (pure JSONL line decoder, every observed `type` + unknown
  tolerance) + `parser_fuzz_test.go` + unit tests.
- C3: `mapper.go`/`ops.go` (record → canonical Events incl. compaction op,
  sub-agent session synthesis, metadata-snapshot → session-property updates,
  no-new-turn-on-compact-summary) + unit tests.
- C4: `cursor.go` (per-relative-path byte offset map; String/After) + restart
  integration test (acceptance #6).
- C5: `scanner.go` + `tailer.go` (walk project dirs + sidechains; fsnotify tail
  with partial-line parking, R2) + `adapter.go` + registry registration.
- C6: auto-discovery probe in `sources.go` + probe unit test (acceptance #8).
- C7: curate + sanitize the 7 golden fixtures (acceptance #5 a-g) + golden tests.
- Each chunk: full gates + the SOW-0001 cycle.

**Validation plan (named):** `parser_test.go` + `parser_fuzz_test.go` (#2, #7);
`mapper_test.go` (#3 sub-agent, #4 compaction); `cursor_test.go` +
`adapter_restart_test.go` (#6); `scanner_test.go`/`tailer_test.go`;
`registry_test.go` (#1, enumerable as `claude-code`); `sources_test.go` (#8 probe);
golden tests reading `testdata/claude_code/<scenario>/` diffed against committed
`.golden.json` for scenarios a-g (#5). `go build`/`golangci-lint`/`gosec`/
`go test -race`/`scan-secrets`/fuzz all green.

**Open decisions requiring operator:** none — all within the signed-off Phase-2
scope.

## Implementation

Delivered 2026-05-29 (one build, mirroring `aiagent_v3`; orchestrator-verified
before review). `internal/adapters/claude_code/` — `adapter.go` (Name/Format
`claude-code` + Scan/Tail/ParseCursor + `init` Register), `parser.go` (pure JSONL
decoder, unknown-`type` tolerance via `errUnknownRecordType`; wired at
`scanner.go:398` Scan + `:614` Tail), `mapper.go`/`ops.go` (record→canonical per
§5.4: turns, llm/tool/reasoning ops, **compaction → `OpKind='compaction'`**,
sub-agent session synthesis, metadata-snapshot → property/log, no-new-turn on
`isCompactSummary`), `cursor.go` (per-relative-path byte offset + `metaSeen`),
`scanner.go`/`tailer.go` (tree walk + sidechains + orphan-root; fsnotify with
partial-line parking + new-dir catch-up). Sessions always `running` (no terminal
signal). Sub-agent NativeID = `<parent>:agent:<agentId>` (structural, always) +
op-level `ChildSessionNativeID` when the `.meta.json` carries `toolUseId`
(present in 226/623 real metas — best-effort, no error when absent).

Registered in `registry_init_test.go`; auto-discovery probe added to
`cmd/ai-viewer-ingest/sources.go` (`~/.claude/projects` + `$CLAUDE_CONFIG_DIR`).
`scripts/sanitize-fixture.sh` + `scripts/lib/sanitize-rules.jq` gained a
`--format=claude_code` mode. 7 golden fixtures (`testdata/claude_code/{a..g}_*`)
are synthetic-but-shape-verified (real compaction transcripts are multi-MB; goldens
stay small + deterministic), each diffed by `TestGolden`.

**Orchestrator verification (all green, re-run on master):** `go build`/`vet`/
`golangci-lint`/`gosec` 0; `govulncheck` 0 called; `go test -race -count=1`
claude_code + cmd pass at **83.5%** coverage; fuzz 30s = 11.3M execs, 0 crashes;
`scan-secrets.sh` PASS (442 files — fixtures clean); confirmed `parseLine`/
`classifyUserContent` are production-wired (the IDE unused-func hints were
false positives golangci agrees with). **Real-data backfill** of the operator's
`~/.claude/projects`: 1,020 sessions, 5,268 turns, 190,102 op-starts, **0 source
errors**. External review pending.

## Validation

(Empty placeholder. Filled at SOW close.)

## Reviews

### Round 1 (2026-05-29) — codex + glm + minimax

minimax + glm → "safe to merge" (P3 only). **codex → not safe: 2 P1 + 5 P2**, all
verified real against the code (the recurring pattern: codex finds contract gaps
the others miss). Adjudication + fix plan:

- **[P1a] Sub-agent op→child link lost in SQLite.** Parent `Agent` op is emitted
  before the child session exists; `internal/ingest/writer.go:425` stores
  `ops.child_session_id` only if the child row already exists and the resolver
  re-links only *sessions*, not ops → the parent op can permanently show no child.
  **Fix (ingester):** the resolver must also re-link `ops.child_session_id` when a
  child session with the matching native id lands later; persist the pending child
  native id. Add an integration test (child-after-parent op linkage). Verify
  aiagent adapters are unaffected/benefit.
- **[P1b] Parent `Agent` ops never finalized.** Spec §543 wants deferred finalize
  on subagent EOF; code finalizes only on `user.tool_result`, which claude-code
  parents lack → ops stuck `running`. **Fix (adapter):** finalize the `Agent` op
  when the subagent sidechain ends (EOF), not on a tool_result that never comes.
- **[P2a] Compaction incomplete.** §547 wants `LogEntry INF` + the op; code emits
  only the op and drops `preservedSegment`/`preservedMessages` from Extras.
  **Fix:** emit the log + carry full `compactMetadata` in Extras.
- **[P2b] No PayloadRef emission.** §5.4 wants PayloadRefs for `toolUseResult`,
  `compact_file_reference`, file attachments, compaction summaries; the adapter
  has no `payloads.go`. **Fix:** implement payload-ref emission per §5.4 (refs/
  metadata only; serving stays Phase 2).
- **[P2c] Scan→Tail skips during-Scan appends.** Tail snapshots current EOF
  instead of resuming from the persisted cursor offset (`adapter.go:96,140`) →
  data loss. **Fix:** Tail resumes from the cursor; restart/append integration
  test.
- **[P2d] Unknown-type tolerance is per-occurrence, not per-variant** (acceptance
  #2). **Fix:** dedupe — one SourceError per distinct unknown type; test with
  repeated occurrences of one variant.
- **[P2e] No symlink containment** (`security.md` §"No symlink traversal escape").
  **Fix:** `EvalSymlinks` + verify resolved path stays inside the source root, in
  Scan + Tail; mirror the aiagent adapters. Test with a symlink escaping root.
- **[P3]** metadata-snapshot fidelity (`file-history-snapshot` → store fileHistory;
  `custom-title` last-wins vs `ai-title`). Address opportunistically.

Not merged. Fixes delegated; re-review (same scope + fix notes) before merge.

#### Fixes applied (2026-05-29) — all 7 findings landed + pinned by tests

A prior fix subagent crashed mid-edit (transient API error), leaving the tree
non-compiling (undefined `payloadEmitted`, `resolveWithinRoot`; `flushDirty`
signature drift). Completed to a green state:

- **P1a (ingester) — DONE.** `internal/ingest/writer.go:431-443` stashes the
  child native id in `ops.extras_json.aiViewer.childNativeId` when the child
  session is absent at parent-op write time; `internal/ingest/resolver.go:200-243`
  adds `linkOpChildren` — an `UPDATE ops … RETURNING session_id` that re-links
  `ops.child_session_id` once the child lands and adds the **parent** session to
  the resolver notify set (`session_changed`). Pinned by
  `internal/ingest/resolver_op_child_test.go` (`TestResolver_LinksOpChildWhenChildArrives`,
  `TestResolver_OpChildNoOpWhenChildAbsent`). aiagent_v2/v3 (which also emit
  op `ChildSessionNativeID`) strictly benefit; their suites stay green.
  *(This was the one finding the crashed agent never started — no `internal/ingest/`
  edit existed in the partial tree.)*
- **P1b (adapter) — DONE (prior agent, completed/verified).** Deferred Agent-op
  finalize on subagent EOF: `internal/adapters/claude_code/ops.go` `agentOps` map
  + `agentFinalizeEvent`; `scanner.go` `collectAgentDeferral`/`emitAgentFinalizations`;
  `tailer.go` loop-lifetime `tailDeferral` + `emitTailAgentFinalizations`. Pinned by
  the regenerated `b_subagent_sidechain` golden (trailing parent `op_finalized Seq=2`).
- **P2a — DONE.** `ops.go` `compactionExtras` carries the FULL `compactMetadata`
  (preservedSegment + preservedMessages) on BOTH the compaction op and a new
  `compact_boundary` `LogEntry INF`. Pinned by `mapper_test.go::TestMapper_CompactionOp`
  + the `c_compaction` golden.
- **P2b — DONE.** New `internal/adapters/claude_code/payloads.go` emits PayloadRefs
  for `toolUseResult` (tool_response), compaction summary (log), and `file`
  attachments with inline content (tool_request). `compact_file_reference` targets
  live outside the served root → no ref (spec §3.4). Wired in `ops.go` (mapUser,
  `payloadEmitted` guard) + `mapper.go` (attachment dispatch). Pinned by the
  a/c/d goldens carrying `payload_ref` rows.
- **P2c — DONE.** `adapter.go` records Scan's final cursor on the instance
  (`scanCursor`); `Tail` resumes from it (cold Tail still snapshots EOF). `tailer.go`
  `catchUpFromCursor` reads each known file from its offset to EOF at Tail startup
  (fsnotify does not fire for pre-watch appends). Pinned by
  `adapter_restart_test.go::TestScanThenTail_NoLossInWindow` (+ cold-Tail-skips-history
  still green).
- **P2d — DONE (prior agent).** `parser.go` `unknownTypeError` + `scanner.go`
  `shouldSurfaceParseError` dedup one SourceError per distinct unknown type per
  file. Pinned by `scanner_test.go::TestScan_UnknownTypePerVariantDedup`.
- **P2e — DONE.** `payloads.go` `resolveWithinRoot` (EvalSymlinks + within-resolved-root
  via `filepath.Rel`); `scanner.go` `withinSourceRoot` guards every discovered
  transcript; `tailer.go` resolves the root once and guards every watched dir.
  Pinned by `scanner_test.go::TestScan_SymlinkEscapeRefused`.
- **P3 — DONE (prior agent).** `ops.go` `customTitleSeen` (custom-title wins over a
  later ai-title) + `fileHistoryBackups` (stores `trackedFileBackups`, not a bool).
  Pinned by `TestMapper_CustomTitleWinsOverLaterAITitle`,
  `TestMapper_FileHistorySnapshotStoresBackups`.

Spec deltas landed same-change: `adapter-claude-code.md` §6.3 (P2c offset handoff +
catch-up read mechanism). All other spec deltas (§3.4/§3.7/§3.11/§3.12/§5.4/§6.1/§8.1/§9.2)
were landed by the prior agent. Golden `expected.jsonl` for a/b/c/d/e regenerated;
`golden_test.go::encodeEvents` now rewrites the absolute root in `LocationURI` too
(portability + no operator path in fixtures — secret scan PASS). No migration needed
(the `ops.child_session_id` column + `extras_json` already exist).

**Gates (this completion):** `go build ./...` 0; `gofmt -l` clean; `go vet ./...` 0;
`golangci-lint run` 0 issues; `go test -race -count=1 ./internal/adapters/...
./internal/ingest/... ./cmd/...` all pass (incl. aiagent_v2/v3); `FuzzParseLine` 30s =
11.0M execs / 0 crashes (`FuzzParseCursor` 15s / 0 crashes); `scan-secrets.sh` exit 0
(478 files); `scan-ai-attribution.sh` exit 0. Coverage: claude_code 81.6%, ingest 88.9%.

### Round 2 (2026-05-29) — codex + glm + minimax (full scope + Round-1 fix notes)

minimax + glm → "safe to merge" again (0 blockers). **codex → NOT safe: 3 P1 + 4 P2**,
ALL verified real against ground truth (zero false positives; the others missed every
one). Critically, codex's full-scope re-review showed three Round-1 "fixes" were
INCOMPLETE and one introduced a crash — this is why the review scope was NOT narrowed
to "review the fixes". Adjudication + authoritative fix design (CTO decisions):

- **[P1.1] PayloadRefs reference a non-existent op → FK rollback of the whole ingest
  batch.** `payload_refs.op_id` is `NOT NULL REFERENCES ops(id)`
  (`migrations/0001_initial.sql:147`); `writer.go:714` derives `op_id` from
  `(TurnSeq,OpSeq)` with NO existence guard (unlike `applyLogEntry:743`, whose column
  is nullable). The Round-1 P2b payloads emit op seq 0: `payloads.go:119`
  (compaction summary, no turn/op) + `:149` (file attachment, no OpSeq) — golden
  `c_compaction/expected.jsonl:10` shows `TurnSeq:0,OpSeq:0`. The adapter-only golden
  never runs the writer, so the seam was invisible. **A real claude-code compaction or
  file attachment breaks ingestion of that batch.**
  **Fix (design):** (a) compaction-summary payload attaches to the **compaction op**
  (mapper remembers the last compaction op `(turnSeq,opSeq)`; `emitSummaryPayload`
  uses them). (b) `file` attachments have no owning op in our model → do NOT emit an
  orphan payload; record `filename`/`displayPath`/`type` in the attachment LogEntry
  extras instead (also satisfies P2.6). (c) Defense-in-depth in the ingester:
  `applyPayloadRef` verifies the op exists; if absent, surface a SourceError + SKIP the
  ref (no silent drop, no batch crash). **NEW test (the missing seam):** run claude_code
  compaction+attachment events through the real writer → no FK error, summary attached
  to the compaction op; orphan-ref → SourceError + batch survives.
- **[P1.2] Agent-op finalize lost across the Scan→Tail boundary.** A parent fully read
  in Scan returns an empty-`agentOps` mapper on the EOF early-return (`scanner.go:372`);
  Tail starts a fresh deferral (`tailer.go:67`) and only learns pending parents from
  re-read files → child completing in Tail never finalizes the parent. The Round-1 P1b
  child-EOF deferral does not survive the boundary.
- **[P2.4] Child byte-EOF ≠ semantic completion** for a live subagent (`scanner.go:400`):
  a long-running subagent is marked completed after its first flushed record in Tail.
  **Fix for P1.2+P2.4 (design grounded in VERIFIED format facts):** an earlier draft of
  this fix proposed finalizing on a *parent* Task `tool_result` — that is FALSE for
  claude-code. Per §485 (verified against a real transcript): the parent's `Agent`
  `tool_use` has NO matching `tool_result`; the subagent result is implicit (the last
  `assistant` text record in `agent-<agentId>.jsonl`). So the completion signal is
  inherently **child-side**, and sessions are modeled "always running" (no end marker).
  Therefore keep child-side finalization but fix both gaps:
  - **P1.2 (durability):** make the parent-Agent-op deferral durable across Scan→Tail.
    Tail's `catchUpFromCursor` must rebuild the deferral by replaying parent transcripts
    (emit-nothing, like the counter rebuild) so a parent `Agent` op emitted during Scan
    is finalizable when its child completes during Tail. Today the EOF early-return
    (`scanner.go:372`) returns an empty-`agentOps` mapper, so the parent is invisible to
    Tail.
  - **P2.4 (not premature):** finalize an `Agent` op only on a **quiescent** child EOF —
    child fully read AND the child file is NOT in the current flush's just-appended dirty
    set. A static Scan has no dirty set ⇒ historical children finalize (correct). In live
    Tail, a child appended in this flush stays `running`; it is finalized on a later
    flush/tick when it sits at EOF with no new appends. A subagent that never goes
    quiescent stays `running` (correct).
  **Tests:** parent `Agent` op in Scan + child completes in Tail → finalized (durability);
  child actively appended this flush → stays `running` (not premature); quiescent child
  at EOF → finalized at child's last-record ts.
- **[P1.3] Symlink containment is incomplete.** Only Scan transcript discovery is
  guarded. Meta reads bypass it (`scanner.go:262`,`:313` — `os.ReadFile` on walk paths,
  no `resolveWithinRoot`; `tailer.go:155-158` self-documents the gap as a TODO), and the
  Tail transcript read path (`transcriptForRel`→`readTranscript`) joins `root+rel`
  without `EvalSymlinks`. A symlinked `.jsonl`/`.meta.json` created after Tail starts
  reads outside the source root. **Fix:** apply `resolveWithinRoot` uniformly to meta
  collection/read (`collectMetaPaths`/`metaHashes`/`readSessionMetas`), Tail transcript
  read (`transcriptForRel`/`readTranscript`), and Tail meta hash (`hashFile`). **Tests:**
  symlinked `.meta.json` escape refused (scan + tail); symlinked `.jsonl` in a watched
  dir during tail refused.
- **[P2.5] Oversized line skips the REST of the file.** `errLineTooLong` sets
  `off = fileSize` and returns (`scanner.go:445-452`) → all later valid records lost
  permanently. **Fix:** discard bytes to the next `\n` and continue; one SourceError for
  the skipped line. **Test:** `[valid, >8MB line, valid]` → both valid records ingested,
  one SourceError.
- **[P2.6] Attachment `displayPath` dropped.** Spec §333 says the adapter records
  `displayPath` in the attachment LogEntry extras; the generic `logEntry` records only
  recordType/subtype. **Fix:** attachment LogEntry carries `filename`/`displayPath`/
  `type` (honors §333/§338; bundled with P1.1(b)). **Test:** extras contain the fields.
- **[P2.7] PR links overwrite instead of appending.** Spec §397 says
  `sessions.extras_json.prLinks[]` (array — a session may make several PRs); `ops.go:396`
  emits a singular `prLink` object, and json_patch overwrites → only the last PR
  survives. **Fix:** accumulate all pr-links in file state; emit `{"prLinks":[…]}` (full
  array; replay-from-0 ⇒ last-wins on the complete array). **Test:** two pr-links →
  `prLinks` length 2.

Spec deltas to land same-change: §5.4 (payload op-scoping), §338 (attachment → LogEntry
extras, not orphan payload), §8.1 (Agent-op finalize is CHILD-SIDE — explicitly NO parent
Task tool_result, which does not exist per §485 — made durable across Scan→Tail and
fired only on a quiescent child EOF), §333 + §397 (code must now honor the promised shapes), §6.x
(oversized-line skip-not-truncate), §6.1/§7 (containment on meta + Tail reads), plus
`ingester.md` (applyPayloadRef defensive op-existence skip). Migration 0001 unchanged
(no schema change — payloads stay op-scoped).

Not merged. Fix round delegated (spec + failing tests incl. the ingester seam test +
code); re-review same scope + these notes before merge.

### Round 3 (2026-05-29) — codex + glm + minimax (full scope + Round-2 fix notes)

glm + minimax → "ready to merge" again. **codex → NOT safe: 2 P1 + 2 P2**, all verified
real against ground truth. The P1.1 payload-ref FK fix, the containment fix, the
oversized-line fix, P2.6, P2.7 are all confirmed correct. The blockers are concentrated
in the **Agent-op finalize machinery** — which codex has now broken in TWO consecutive
rounds (R2: durability + premature; R3: quiescence-insufficient + double-finalize). That
is the signal: the quiescent-EOF design (cycle counter + childAtEOF + parkChildEnds +
sweep + write-only `done`) is the wrong abstraction. **CTO decision: replace it**, do not
patch it again.

Findings (verified):
- **[P1.3a] Quiescence ≠ completion.** `sweepQuiescentFinalizations` (`tailer.go:449`)
  finalizes on `fullyRead` (byte EOF) + one quiet cycle, with NO check of the terminal
  record type. A child paused mid-tool (last record a user/tool_use) for one
  `tailTickInterval` is wrongly finalized. The "not premature" test cancels before the
  tick that finalizes, so it never exercised the gap.
- **[P1.3b] Late `.meta.json` permanently loses child linkage.** The Agent op records
  `agentOps`+`ChildSessionNativeID` only if `toolUseToAgent[blk.ID]` is already populated
  from the sidecar (`ops.go:200-212`). If Tail reads the parent `Agent` tool_use before
  the `.meta.json`, the op is written with no child link and no deferral, and a later
  meta-only flush does not re-replay the parent (`tailer.go` meta-only path), so the
  resolver (which needs the stashed `childNativeId`) can never repair it.
- **[P2.3a] Double-finalize on catch-up replay.** `readTranscript` suppresses record
  events but rebuilds deferral state; `parkChildEnds` re-parks an already-finalized child
  and `def.done` is written (`tailer.go:472`) but NEVER read, so a re-read re-emits the
  finalize. Idempotent at the DB layer, but violates "emits nothing on replay" + redundant
  notifies + dead field.
- **[P2.3b] Turn-0 compaction summary payload silently dropped.** `emitSummaryPayload`
  (`payloads.go:125`) drops when `turnSeq == 0 || opSeq == 0`, but a compaction op
  legitimately exists at turn 0 (compaction before any user turn). Guard must key on
  `opSeq == 0` only (the real "no owning op" sentinel; compaction always sets opSeq≥1).
- **[P2-perf, glm] Redundant `EvalSymlinks`.** `scanAll`/`catchUpFromCursor` pass the
  UNRESOLVED root to `readSessionMetas`/`metaHashes`, so the root is symlink-resolved once
  per file instead of once. Not a security gap (containment is correct) — pre-resolve once
  and thread `resolvedRoot` (mirror `discoverTranscripts`); drop the `_ = resolvedRoot`.

Redesign (authoritative — grounded in VERIFIED real-data completion semantics):
- The ONLY reliable completion signal in this format is §485's "the child's last record is
  an assistant message with text content". Confirmed on real workstation transcripts:
  11/12 completed sidechains end with `{assistant, content[0]=text}`; the one outlier ends
  with a `user` record (an interrupted child — correctly NOT complete).
- **Finalize an Agent op iff the child sidechain is fully read AND its terminal record is
  an assistant-text record**, emitting the finalize **gated by `emitFrom`** (so a replay /
  catch-up whose terminal record is below the resume offset emits nothing — fixes P2.3a),
  with the parent op resolved through the existing `agentOps` deferral (preserves R2's
  P1.2 durability). A child terminated by a user/tool record stays `running` (fixes P1.3a's
  premature finalize without any timing heuristic).
- **Remove** `cycle`, `childAtEOF`, `parkChildEnds`, `sweepQuiescentFinalizations`, and the
  write-only `done`. Replace with: a `childCompleted` set (terminal-assistant-text observed
  this pass) + a `finalized` set that IS READ before emitting (no re-finalize). Pairing is
  event-driven (when the parent op is observed via `collectAgentDeferral`), not tick-driven.
- **[P1.3b]** A meta-dirty Tail flush re-replays the parent transcript(s) of the affected
  session(s) so a late `.meta.json` repairs the Agent op's `toolUseToAgent`→`childNativeId`
  linkage (then the resolver links the child).
- **[P2.3b]** `emitSummaryPayload` guard → `opSeq == 0` only.
- **[glm perf/P3]** Pre-resolve root once in `scanAll`; thread `resolvedRoot`; rename the
  misleading `withinSourceRoot(resolvedRoot, …)` param; document the `prLinks` fresh-map
  invariant.

Tests: finalize on terminal-assistant-text (scan + tail); child terminated by tool_use/
user stays `running` AND a test that drives PAST a tick to prove it (close codex's
test-gap note); no double-finalize across a catch-up replay; late-meta linkage repair;
turn-0 compaction summary payload emitted. Spec §8.1 rewritten (completion = terminal
assistant-text, emitFrom-gated; remove quiescence wording), §5.4/§9.2 (turn-0 allowed).

Not merged. Redesign delegated; re-review same scope + these notes (Round 4) before merge.

## Outcome

Pending.

## Lessons / Follow-Ups

Pending.
