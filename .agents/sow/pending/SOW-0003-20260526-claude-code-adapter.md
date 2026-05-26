# SOW-0003 - claude-code adapter (Scan + Tail + cursor + sub-agents + compaction)

## Status

Status: open

Sub-state: awaits operator approval before moving to current/. Prerequisite: SOW-0001 Phase 1 Foundation completed (canonical event types + ingest pipeline + store) — this SOW reuses that infrastructure.

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

(To be filled by the assistant picking this SOW up. Required before moving to `current/`.)

## Implementation

(Empty placeholder. Filled as chunks complete.)

## Validation

(Empty placeholder. Filled at SOW close.)

## Reviews

(Empty placeholder. Filled as external reviewers run.)

## Outcome

Pending.

## Lessons / Follow-Ups

Pending.
