# SOW-0039 - Verify + harden ai-viewer's consumption of ai-agent's session-lineage fix

## Status

Status: in-progress

Sub-state: opened 2026-06-03 under the operator's standing backlog mandate (no open decisions need operator input). Driven by the operator report that ai-agent session snapshots were not linked to parents/origins, now fixed upstream. A read-only cross-repo assessment confirmed ai-viewer's v3 adapter ALREADY consumes the lineage correctly — so this SOW is test + spec hardening (adopt the real upstream fixtures + an end-to-end lineage golden + fix 2 stale spec lines), NOT an adapter rewrite.

## Requirements

### Purpose

ai-viewer's reason to exist includes seeing agent activity **across sub-agents**, so parent/origin **session lineage is a core capability**. The ai-agent producer just made child→parent/origin linkage explicit in the snapshot format (commit `8a0078bc`). Because that is a consumed-contract change to a core feature, "untested ≡ broken" applies: ai-viewer must **prove** it ingests the now-real lineage end-to-end against the producer's actual wire format, and its specs must stop describing the pre-fix reality.

### User Request

Operator: *"We had an issue with ai-agent session snapshots not being linked to their parents and origin sessions. They have been fixed. Check the latest commits."* Then, on scope: *"you are the CTO. I don't know. Pick the one you see fit for the purpose."* → CTO decision (this SOW): adopt the real upstream fixtures + an end-to-end lineage golden test + fix the spec drift; NOT verify-only (would leave it untested), NOT a heavyweight adapter SOW (lineage decode/map already correct).

### Assistant Understanding

Facts (from the cross-repo assessment, with evidence):

- The fix is ai-agent `8a0078bc` "Add session lineage and LLM request id evidence" (origin/master, 2026-06-03), implementing the `evidence-explicit-parent-id-on-child` SOW: it guarantees `parentSessionId` and adds `parentOpId` on the child's v3 `session_start` and on parent-side `childSessions[]` refs; adds lineage body fields to the v2 `.json.gz` envelope; and adds `attributes.llmRequestId` to LLM ops. (`ai-agent@8a0078bc` src/evidence/session-recorder.ts:366, src/session-tree.ts:178-184, src/persistence.ts:56-61, .agents/sow/specs/snapshots.md.)
- ai-viewer's **v3 adapter already decodes** `parentSessionId`/`parentOpId`/`originId`/`childSessions[]` (`internal/adapters/aiagent_v3/parser.go:71-117`) and **maps** them to canonical `ParentNativeID`/`ParentOpKey`/`RootNativeID` (`mapper.go:114-124`), with a parent-side synthesizer for the residual no-`parentSessionId` cases (`mapper.go:229-260`). `attributes.llmRequestId` is captured generically into `extras["attr.llmRequestId"]` (`ops.go:78-80`).
- v2 lineage is recursion-based via the opTree (`mapper.go:88-93`); the new envelope body fields are read-ignored by design (`parser.go:18-44`). The v2 `.json.gz` filename was always `<originId>.json.gz` (suffix-only discovery, `scanner.go:256,262`); the "`session-`" prefix existed only in a stale `snapshots.md` doc example, now corrected upstream.
- The real upstream fixtures (`ai-agent@8a0078bc` src/tests/fixtures/v3-evidence/sub-agent-with-parent-id/session/{root,child}-session.jsonl) are synthetic and PII-clean (verified): ids are `root-session`/`child-session`/`root-agent`/`child-agent`, llmRequestId `chatcmpl-fixture`.

Inferences:

- No adapter decode/map change is needed for lineage. The value is durable coverage pinned to the **real** producer wire format (current v3 fixtures are hand-synthesized, structurally faithful but not byte-identical to upstream), plus closing the spec drift the producer fix created.

Unknowns:

- None blocking.

### Acceptance Criteria

1. The two real upstream fixtures are adopted **verbatim** into `internal/adapters/aiagent_v3/testdata` (PII-clean confirmed). Verify: files present + byte-faithful to `ai-agent@8a0078bc`.
2. An **end-to-end lineage golden test** (ingest → store → presenter) pins: (i) the child session resolves `parent_session_id` + `parent_op_id` from its own ledger; (ii) the **same** linkage resolves from the parent's `childSessions[]` alone (parent-side synthesizer path); (iii) `root_session_id` chains to the origin (`root-session`); (iv) `attr.llmRequestId == "chatcmpl-fixture"` is queryable in the child's LLM-op extras. Mutation-proven where feasible (e.g. flipping the mapped parent field fails the test). Verify: `go test ./internal/...` green; the assertions reference the real fixture values.
3. The 2 stale spec lines are corrected in the same commit: `adapter-aiagent-v2.md` (envelope lineage fields are now **persisted** by the producer, still **ignored by design** by the v2 adapter) and `adapter-aiagent-v3.md` (`parentSessionId` is now a first-class producer guarantee + `parentOpId` added). Verify: spec diff.
4. **No adapter decode/map logic change** (lineage already correct). Verify: `git diff` touches only testdata, the new test, and specs — no change to `parser.go`/`mapper.go`/`ops.go` decode/map logic.
5. All gates green; ≥3 external reviewers converged; PR opened + self-merged to master.

## Analysis

Sources checked: the cross-repo assessment (ai-agent `8a0078bc` diff: snapshots.md, session-recorder.ts, session-tree.ts, persistence.ts, types.ts, the upstream fixtures; ai-viewer aiagent_v3 parser/mapper/ops, aiagent_v2 parser/mapper/scanner, canonical/events.go, adapter-aiagent-{v2,v3}.md, ingest e2e tests).

Current state: ai-viewer correctly ingests the fixed lineage today; the gap is (a) no golden pinned to the real upstream artifacts, and (b) two spec lines that now misdescribe the producer.

Risks: low. Test + spec only; no runtime behavior change. Main risk is a fixture that does not actually exercise the parent-side path — mitigated by asserting linkage resolves from the parent ledger alone.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The producer fix is correct and ai-viewer already consumes it, but ai-viewer has no test pinned to the **real** post-fix wire format, and 2 spec lines describe the pre-fix reality. For a core capability (sub-agent lineage), unverified ≡ broken.

Evidence reviewed:

- Cross-repo assessment (file:line on both sides, recorded in Assistant Understanding). The upstream fixtures inspected directly via `git -C ../ai-agent.git show 8a0078bc:<path>`.

Affected contracts and surfaces:

- New: `internal/adapters/aiagent_v3/testdata/...` (the two upstream fixtures) + a new golden/e2e test file.
- Modified: `.agents/sow/specs/adapter-aiagent-v2.md`, `.agents/sow/specs/adapter-aiagent-v3.md` (drift fixes).
- NOT modified: adapter decode/map logic (`parser.go`, `mapper.go`, `ops.go`) — lineage already correct.

Spec deltas to land before any test/code:

- `adapter-aiagent-v2.md`: correct the claim that the envelope's `sessionId/originId/timestamp` "are NOT persisted … persistence.ts strips them" — the producer now persists `sessionId, originId, originTxnId, parentId, parentTxnId, timestamp` (`ai-agent@8a0078bc` src/persistence.ts:56-61); the v2 adapter still ignores them by design (reads only `{version,reason,opTree}`). Update the rationale, keep the "ignored" contract.
- `adapter-aiagent-v3.md`: note that `parentSessionId` is now a first-class producer guarantee (+ `parentOpId` added) per the upstream lineage fix, superseding the "76% present / 3.2% early-format leftovers" framing; the two-path resolver remains the safety net.

Existing patterns to reuse:

- The adapter golden/integration test patterns (`aiagent_v3` mapper/coverage tests; `internal/ingest` e2e + resolver tests). Fixture adoption mirrors how other adapters keep real-artifact fixtures under `testdata/`.

Risk and blast radius: low (test + spec only). No migration, no API/UI change.

Sensitive data handling plan: the two fixtures are synthetic + PII-clean (verified: ids `root-session`/`child-session`/`root-agent`, `chatcmpl-fixture`, 2026-06-03 timestamps; no home paths/emails/IPs/secrets). `scan-secrets.sh` + `scan-ai-attribution.sh` run before commit.

Implementation plan:

1. Land the 2 spec-drift fixes (adapter-aiagent-{v2,v3}.md).
2. Adopt the two upstream fixtures verbatim into `aiagent_v3/testdata`.
3. Write the end-to-end lineage golden test (ingest→store→presenter) per AC #2; confirm it meaningfully fails under a mutation (flip the mapped parent field) before passing.
4. Run all gates; verify no decode/map logic changed.
5. External review (≥3) to convergence; PR; self-merge.

Validation plan:

- The new golden test (named in AC #2) + existing aiagent_v3 + ingest e2e tests; `go test -race ./internal/...`; coverage gate; the no-logic-change git-diff check.

Artifact impact plan:

- AGENTS.md: no change. Runtime skills: no change (no gate semantics change). Specs: adapter-aiagent-{v2,v3}.md updated (drift). Docs: none. SOW lifecycle: completed + moved to done/ on close.

Open decisions: none requiring operator input.

## Implications And Decisions

CTO decision (operator delegated scope): do this as a focused fixture + golden + spec-hygiene SOW. Rejected alternatives: verify-only (leaves the core lineage capability untested ≡ broken); a heavyweight adapter SOW (no decode/map change is needed — the assessment proves the v3 adapter already maps every lineage field). The `llmRequestId` first-class surfacing and the v2 generic-attribute capture are deliberately OUT of scope and tracked as follow-ups.

## Plan

1 commit (spec + fixtures + golden test) per the implementation plan; final commit moves the SOW to done/.

## Execution Log

### 2026-06-03

- Branched `sow-0039-aiagent-lineage-fixtures` off master (`bfa8b98`).
- Cross-repo assessment completed; upstream fixtures inspected + PII-confirmed clean.
- Implementation (delegated): adopted the two upstream fixtures byte-identically
  (`internal/adapters/aiagent_v3/testdata/sub-agent-with-parent-id/session/{root,child}-session.jsonl`,
  sha-verified vs `ai-agent@8a0078bc`); added `internal/ingest/e2e_aiagent_lineage_test.go`
  (3 ingest→store golden tests + a shared `ingestV3Dir` harness); fixed the spec
  drift in `adapter-aiagent-v2.md` (×4 spots) + `adapter-aiagent-v3.md` (×4 spots).
- The golden is split into 3 path-isolating tests because the combined two-ledger
  test is NOT mutation-sensitive to child-side mapping (the parent-side synthesizer
  re-supplies parent/op/root on UPSERT and masks a wrong child-side field):
  `_ChildSideOwnLedger` (child ledger only → child-side fast path),
  `_ParentSideSynthesizer` (root ledger only → `childSessions[]` path),
  `_RealUpstreamFixtures` (both → integrated end-state). Mutation-sensitivity was
  proven empirically (ParentNativeID flip, ParentOpKey drop, synthesizer
  OriginID flip, llmRequestId drop each turn the relevant test RED; code byte-restored).
- Orchestrator verification (run myself, not the subagent's word): the 3 lineage
  tests pass under `-race` (1.2s); `aiagent_v3` ok; `go vet` + `gofmt` clean;
  `parser.go`/`mapper.go`/`ops.go` UNCHANGED vs HEAD (no decode/map logic touched);
  both fixtures byte-identical to `ai-agent@8a0078bc`; secret + AI-attribution
  scans clean. Spec diffs reviewed for accuracy (drift class swept, not just cited lines).
- External review (≥3) + completion + PR + self-merge: recorded below as they complete.

## Validation

Pending.

## Reviews

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- **Surface `llmRequestId` as first-class** (optional, product/UX call): today it is captured losslessly on v3 (`extras["attr.llmRequestId"]`) but has no canonical field / store column / presenter / UI. Surfacing it (for provider-request-id ↔ support-ticket correlation) would touch the canonical schema + a migration + UI — its own small SOW if the operator wants it visible.
- **v2 generic-attribute capture**: the legacy v2 adapter reads only specific op-attribute keys, so `llmRequestId` (and any future producer attribute) is silently dropped on v2 (`mapper.go:571-581`). Mirroring v3's generic `extras["attr."+k]` copy would make v2 lossless. Low priority (v2 is the pre-migration cohort).

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file.
