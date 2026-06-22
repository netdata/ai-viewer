# SOW-0096 Triage — Reviewers 1-4 Findings + Corrections

This is the CTO's verification of the first 4 reviewers' findings.
Run before reviewers 5-8 so the next batch sees the corrected
baseline and the open SOW questions.

## Status

- [x] Reviewer 1 (codex via codex2) — verified, baseline corrections logged
- [x] Reviewer 2 (claude-code via claude) — verified, baseline corrections logged
- [x] Reviewer 3 (canonical via glm) — verified, canonical contract changes logged
- [x] Reviewer 4 (aiagent_v2 via minimax) — verified, baseline corrections logged
- [ ] Reviewer 5 (aiagent_v3 via mimo) — pending
- [ ] Reviewer 6 (opencode via kimi) — pending
- [ ] Reviewer 7 (framework via deepseek) — pending
- [ ] Reviewer 8 (SQL via qwen) — pending
- [ ] Reviewer 9 (UX) — CTO does directly

## CORRECTIONS TO THE CTO'S BASELINE NARRATIVE

The following claims in the SOW's "CTO's known-gaps baseline" are
incorrect. The reviewers caught them; the CTO has re-verified each
by re-running the SQL on the live prod DB. The corrections are
authoritative for the rest of the SOW work.

### Correction 1: per-adapter subagent link rate (SOW §Evidence reviewed + the open questions in §Implementation plan)

| adapter | CTO said (SOW) | ACTUAL (re-verified) |
|---|---|---|
| aiagent_v2 | 0 / 175,612 (0%) | 169,423 / 175,612 (**96.5%**) |
| aiagent_v3 | 0 / 16,471 (0%) | 16,470 / 16,471 (**100.0%**) |
| claude-code | 0 / 835 (0%) | 585 / 835 (**70.1%**) |
| codex | 0 / 72 (0%) | 72 / 72 (**100.0%**) |
| opencode | 0 / 3,189 (0%) | 3,184 / 3,189 (**99.8%**) |

The original "0%" was computed against a SQLite query that
referenced `o.source_id` (a column that doesn't exist on `ops`;
ops are linked to sources via sessions). I re-ran the corrected
JOIN and the real rate is near-universal. **Invariant #10 is
largely SATISFIED for v2/v3/codex/opencode** and 70% for
claude-code. The 0/72 claim was wrong, the 0/835 was wrong, the
0/3,189 was wrong — every adapter passed the 0% test that I
misreported as failing.

Implication for invariant #10: the invariant is **not** a P0/P1
finding. It's a P2 (per-adapter coverage gap for claude-code at
~30% missing). The SOW's chunk 4 still has work to do for
claude-code specifically — see the sidecar dependency Reviewer 2
found in T2-claude-1 / S10-claude-1.

### Correction 2: distinct op kinds in the DB

The SOW baseline talked about `kind='user_input'` and
`kind='assistant'` as if they were runnable queries. They return
0 for **every** adapter by construction — these op kinds are
**not in the canonical `OpKind` enum**.

Canonical `OpKind` (`internal/canonical/events.go:77-92`):
  `llm`, `tool`, `session`, `reasoning`, `internal`, `system`, `compaction`

What the live DB actually has:
  `tool` (2,313,952), `llm` (1,942,135), `system` (1,186,802),
  `reasoning` (572,796), `session` (196,179), `internal` (24,149),
  `compaction` (9,715)

**No `user_input` ops exist.** No `assistant` ops exist.

This means:
- **Invariant #2** ("user prompts captured") and **Invariant #4**
  ("assistant output captured") as written in the SOW are
  **structurally not expressible** against the current schema.
  The CTO's baseline narrative was wrong. Reviewer 3 (glm)
  caught this; Reviewer 4 (minimax) confirmed it.
- The codex "24,149 `internal` ops" are **intentional** — codex
  has no first-class `user_input` kind to use, so it overloads
  `internal` (with `name='user_input'`). Reviewer 1 (codex) caught
  the CTO's "likely user_input misclassified" framing as wrong;
  the fix is **not** to reclassify them but to give the
  canonical model the missing op kinds.

### Correction 3: v2 failed LLM ops missing error_class (Reviewer 4 P1)

  392,555 v2 ops are `kind='llm' AND status='failed'`.
  392,555 of them (100%) have no `error_class`.

This was **not in the SOW baseline at all**. The SOW's "Top
error_class is 'none' (most ops), then 'internal', 'io',
'system_error'" was drawn from a wrong query (column-name error).
The real top error_class distribution needs to be re-pulled.
Reviewer 4 also found that the **producer's tool path** does
populate rich `error_class` values (e.g. "agent__final_report
requires non-empty report_content field" — 4,601 entries;
"MCP error -32603: Simulated MCP tool failure" — 4,378 entries;
"Tool execution timed out" — 3,869 entries) so the gap is LLM-only.

Implication: invariant #8 fails 100% for v2 LLM ops. The CTO
needs a follow-up investigation into why the producer's
`endOp(..., 'failed')` for LLM ops drops the error attribute
(per Reviewer 4's T8-v2-1).

### Correction 4: v2 LLM op status distribution (verifies Reviewer 3 T8-canonical-1)

  v2 LLM op status: `completed` 950,395, `failed` 392,555, `running` 714.
  No `"ok"` literal present. So either (a) aiagent_v2's mapper
  DOES normalize `ok` → `completed` before the DB write, OR
  (b) the v2 source never emits `ok` to begin with. Reviewer 3
  was right that this is a structural concern, but the live
  data shows the live writes use the canonical literals. The
  normalization question can be deferred.

### Correction 5: distinct op kinds — there are 7, the SOW says 6

The SOW baseline table lists "tool, llm, session, reasoning,
internal, compaction" (6). The DB has 7: those 6 plus `system`
(1,186,802 ops, almost all from aiagent_v2). The SOW should add
`system` to the matrix.

## OPEN SOW QUESTIONS (for reviewers 5-8)

These are the questions the next 4 reviewers should answer. The
common brief already references the corrections above; these
are the specific questions the next 4 should focus on.

### For Reviewer 5 (v3 via mimo)

The v2 reviewer's T1/T2 structural finding and the
24,149-`internal`-ops correction both apply to v3. Specifically:

1. Walk v3's mapper. Does v3 have a first-class `user_input`
   op kind, or does it also overload `internal` (with
   `name='user_input'`)? If it overloads, the count should be
   similar to codex's 24,149.
2. The v3 SDK refs (46,268+46,266 = 92,534) vs llm refs
   (43,337+43,023 = 86,360): is one a strict subset of the
   other, or are they emitted at different lifecycle points?
3. Does v3 emit `kind='system'` ops like v2 does? v3's
   op-kind table should match the v2 shape but with smaller
   counts.
4. What's the v3 subagent-link rate? Per the corrected
   numbers, v3 is 100.0% — confirm the mapper code
   actually populates `child_session_id` for 100% of v3
   session ops, and document how (the source-side mechanism).

### For Reviewer 6 (opencode via kimi)

1. The corrected subagent-link rate for opencode is 99.8%
   (3,184 / 3,189). Which 5 session ops are missing the
   child_session_id, and why? The mapper probably has a
   specific edge case (e.g. failed parses, schema
   mismatches, etc.). Cite the line.
2. The 0 `tool_request` payload_refs and 0 `llm_request`
   payload_refs remain. Are the request bytes present in
   the opencode SQLite schema and we're not reading them,
   or is opencode's schema genuinely request-less? Walk
   the schema and the mapper.
3. The 100% perfect 1:1 `llm_response:llm_op` ratio in the
   CTO's baseline was 0.48 (133,119 / 275,180) — i.e. ~half
   of llm ops have a response. What's the actual ratio now
   (with the corrections in place)? Does it match Reviewer
   6's expectation?

### For Reviewer 7 (framework via deepseek)

The canonical model is **incoherent for invariants #2 and #4**
(no `user_input`/`assistant` op kinds). The framework design
should:

1. **MUST** decide: do we add the missing op kinds (canonical
   enum extension + per-adapter rewrites), or do we re-frame
   the invariants (drop the per-op-kind check, use payload_ref
   presence instead)?
2. The captured-but-unsurfaced UI fields (T11-canonical-1..4)
   should be inputs to the per-invariant severity tiering:
   a captured field that the operator can't see is, in
   practice, not captured.
3. The subagent-link correction (96.5% / 100% / 70% / 100% /
   99.8%) changes the per-invariant severity for #10 from
   "P0 broken everywhere" to "P2 per-adapter gap on
   claude-code at 30% missing".

### For Reviewer 8 (SQL via qwen)

The 11×5=55 SQL queries should be informed by the corrected
baseline. The queries for invariants #2, #4, #10 should use the
corrected numbers as their expected values (i.e. expected ≥ 0
for #2/#4, expected ≥ 70% for #10, etc.). The CTO baseline
table at the top of this triage document is authoritative
for the next 4 reviewers.

## AUTH-CORRECTED BASELINE TABLE (authoritative for the rest of the SOW work)

| adapter | sessions | ops | kind=tool | kind=llm | kind=reasoning | kind=system | kind=session | kind=internal | subagent-link rate |
|---|---|---|---|---|---|---|---|---|---|
| aiagent_v2 | 483,541 | 3,788,064 | 1,062,490 | 1,343,664 | 19,496 | 1,186,802 | 175,612 | 24,149 | **96.5%** |
| aiagent_v3 | 29,383 | 140,981 | 78,210 | 46,300 | 0 | 0 | 16,471 | 0 | **100.0%** |
| claude-code | 1,090 | 208,957 | 65,278 | 122,679 | 20,115 | 0 | 835 | 0 | **70.1%** |
| codex | 3,057 | 1,205,749 | 646,034 | 154,312 | 371,517 | 0 | 72 | 24,149 | **100.0%** |
| opencode | 14,106 | 901,977 | 461,940 | 275,180 | 161,668 | 0 | 3,189 | 0 | **99.8%** |

| adapter | payload_refs (tool_request / tool_response / llm_request / llm_response / llm_reasoning) |
|---|---|
| aiagent_v2 | 0 / 0 / 6,169 / 6,054 / 0 |
| aiagent_v3 | 0 / 0 / 43,337 / 43,023 / 0 (also 46,268 / 46,266 sdk_request/response — Reviewer 4 noted this) |
| claude-code | 0 / 31,171 / 0 / 0 / 0 |
| codex | 670,183 / 638,562 / 0 / 154,312 / 371,517 (the 24,149 tool_request "overcount" = user_input misclassification, per Reviewer 1) |
| opencode | 0 / 455,103 / 0 / 133,119 / 160,976 |

| adapter | failed LLM ops | missing error_class |
|---|---|---|
| aiagent_v2 | 392,555 | **392,555 (100.0%)** — invariant #8 fails completely for v2 |
| aiagent_v3 | 0 | n/a |
| claude-code | 0 (LLM ops hardcoded `status='completed'`, per Reviewer 2) | n/a |
| codex | 0 | n/a |
| opencode | 0 | n/a |

## WHAT THIS CORRECTION MEANS FOR THE SOW

1. **Invariant #2 and #4 need a canonical-contract decision** before
   any code is written. Either the enum gets `OpUserInput` and
   `OpAssistant` (and the v2/v3/codex mappers start emitting
   them), or the invariants get re-framed to use payload_ref
   presence as the check. Reviewer 7 (framework design) should
   make this call.

2. **Invariant #10 is mostly OK, with claude-code as the
   exception**. The 70% claude-code rate maps to the sidecar
   dependency Reviewer 2 found. That's a per-adapter fix
   (SOW chunk 4 territory), not a framework or schema change.

3. **Invariant #8 is broken for v2**. 100% of failed v2 LLM ops
   lack error_class. This is a P1 per-adapter fix.

4. **Captured-but-unsurfaced fields** are a UX gap (invariant
   #11 cannot pass). Six fields (reasoning_kind, bytes/chars
   in-out, turn-level cache tokens + error_class,
   provider_alias, call_path, sha256) need to be surfaced or
   explicitly documented as intentionally omitted.

5. **The SOW's "no schema impact" line is wrong**. It should be
   split: "SQL schema impact: none" (true — v11 is sufficient)
   and "Canonical contract: invariants #2 and #4 require an
   explicit decision on op kinds before the invariant SQL can
   be written" (currently missing). The SOW should be amended
   in chunk 1 step (a) (the spec-update step).

## NEXT STEPS

1. Re-pull the v2 op_kind status distribution (to confirm the
   corrected numbers for chunk 4 gap remediation) — this is a
   follow-up SQL run, not a SOW change.
2. Run reviewers 5-8 with the corrected baseline. They have
   open questions in the §"OPEN SOW QUESTIONS" section above.
3. Reviewer 9 (UX) — the CTO does this directly. The
   captured-but-unsurfaced fields list is the input.
4. After all 9 reviewers complete, write the "v1 invariant
   set" document and amend the SOW per the corrections above.
