# SOW-0105 - UI vs DB contract gap analysis

## Status

Status: open

Sub-state: Proposed. Gap analysis created from current specs, presenter DTOs, frontend API types, UI components, and a structural live-DB snapshot. No implementation has started.

## Requirements

### Purpose

Establish a complete, evidence-backed contract map between what ingestion persists in SQLite and what the UI can actually receive, type, render, filter, and test. Every persisted field that matters to the operator must be either:

- exposed and rendered intentionally,
- exposed only through an explicit debug/proof surface, or
- documented as internal-only with a reason.

The long-term goal is to stop losing captured data at the DB/API/UI boundary.

### User Request

Create a new SOW doing a gap analysis of what the UI supports vs what the DB provides, focused on data formats, contracts, and related surfaces.

### Assistant Understanding

Facts:

- The DB schema has first-class session, turn, op, source, and payload-proof columns beyond the currently narrow session-detail/list DTOs.
- The frontend API types explicitly mirror the Go presenter DTOs, so Go DTO omissions become frontend contract omissions.
- The Turn View renders payloads by array position for key cases, while the DB now records semantic payload kinds such as `tool_request`, `tool_response`, `llm_request`, `llm_response`, `llm_reasoning`, `sdk_request`, and `sdk_response`.
- SOW-0103 covers some captured-but-unsurfaced UX fields, but it predates the final SOW-0097 parity shape and still carries provisional assumptions about new op kinds.

Inferences:

- The highest-risk gap is not just missing labels in the UI. It is a contract drift problem: DB schema, REST examples, Go DTOs, TypeScript interfaces, component assumptions, and tests no longer describe the same data model.
- Fixing this well should start with a field-intent matrix, not by blindly adding every DB column to every response.
- Some proof fields (`location_uri`, `sha256`, exact selectors) are useful for operator audit/debugging but may leak sensitive local path details if displayed in primary UI chrome.

Unknowns:

- Whether SOW-0103 should be closed as superseded after this SOW is approved, or kept as a smaller UX follow-up.
- Whether proof metadata should be visible only in a debug/proof drawer or also in normal payload rows.
- Final live DB population counts after the recent DB rebuild completes. The counts below are evidence that fields are populated, not final totals.

### Acceptance Criteria

- A DB-to-REST-to-TypeScript-to-UI matrix exists for the current session, turn, op, source, and payload-ref contracts.
- Each gap is classified by severity and implication.
- The SOW identifies the spec deltas that must land before tests/code.
- The SOW includes a future validation plan for contract tests, UI tests, and a drift gate.
- No raw payload content, prompts, credentials, private endpoint details, or workstation-private paths are written into durable artifacts.
- No implementation code changes are made under this SOW until it is approved and moved to `current/`.

## Analysis

Sources checked:

- `.agents/sow/specs/data-model.md`
- `.agents/sow/specs/rest-api.md`
- `.agents/sow/specs/frontend-architecture.md`
- `.agents/sow/specs/ui-pages.md`
- `.agents/sow/specs/ui-turn-view.md`
- `.agents/sow/pending/SOW-0103-20260622-ux-captured-surfaces.md`
- `internal/presenter/session_detail.go`
- `internal/presenter/session_detail_ops.go`
- `internal/presenter/sessions_list.go`
- `internal/presenter/sources.go`
- `internal/presenter/presenter.go`
- `frontend/src/api/types.ts`
- `frontend/src/api/payloads.ts`
- `frontend/src/components/TurnView/TurnStep.tsx`
- `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx`
- Structural SQLite counts from `/opt/ai-viewer/data/index.db`, with no raw payload reads.

Current state:

- `data-model.md:88-108` defines session columns including `provider`, `provider_alias`, `cwd`, `call_path`, `error_message`, `duration_us`, cache-token columns, `first_user_message_hash`, and `extras_json`.
- `data-model.md:132-133` states `cwd`, `provider_alias`, and `call_path` are first-class because they drive filter or grouping paths in the UI.
- `data-model.md:138-153` defines turn-level `error_class`, cache-token columns, and `extras_json`.
- `data-model.md:165-198` defines op-level `tool_namespace`, `provider_alias`, `reasoning_kind`, cache-token columns, byte/char counters, context fields, child linkage, and `extras_json`.
- `data-model.md:228-238` defines payload proof fields including `location_uri` and `sha256`.
- `data-model.md:261-288` describes payload refs as proof objects, not just UI preview pointers.
- `rest-api.md:96-106` documents a narrow `/api/sessions` response that omits several first-class session columns.
- `rest-api.md:116-143` documents `/api/sessions/:id` with a partial session/turn/op shape and payload refs that omit proof fields.
- `rest-api.md:159-189` documents `/api/sessions/:id/payload_refs` returning only `id`, `op_id`, `kind`, `format`, `compression`, `original_bytes`, and `stored_bytes`.
- `frontend-architecture.md:41-51` says `frontend/src/api/types.ts` mirrors Go DTOs.
- `frontend-architecture.md:106-122` limits URL filters to `agents`, `models`, `tools`, `status`, `sources`, `from`, `to`, and `q`; it does not include `cwd`, `provider_alias`, or `call_path`.
- `ui-turn-view.md:13-24` defines Turn View as a semantic step view over ops and names the user prompt, reasoning, tool, and assistant-message pattern.
- `internal/presenter/session_detail.go:22-47` exposes session detail fields but omits `provider_alias`, `cwd`, `call_path`, `error_message`, `duration_us`, `first_user_message_hash`, and `extras_json`.
- `internal/presenter/session_detail.go:50-60` exposes turn detail but omits turn `error_class`, cache-token columns, and `extras_json`.
- `internal/presenter/session_detail.go:67-86` exposes op detail but omits `tool_namespace`, `provider_alias`, `reasoning_kind`, cache-token columns, `bytes_in`, `bytes_out`, `chars_in`, `chars_out`, and `extras_json`.
- `internal/presenter/session_detail.go:93-101` exposes payload refs but omits `location_uri` and `sha256`.
- `internal/presenter/session_detail_ops.go:62-64`, `98-102`, and `184-190` confirm the SQL projections do not select the omitted turn/op/payload fields.
- `internal/presenter/sessions_list.go:13-35` and `79-86` show the list DTO/query is narrower than the session table.
- `internal/presenter/sources.go:28-44` exposes `sources.meta_json` as `meta`, but `frontend/src/api/types.ts:718-731` omits `meta` from `SourceItem`.
- `internal/presenter/presenter.go:261-280` registers `/api/payloads/`, while `frontend/src/api/payloads.ts:1-7` still says the route is not registered.
- `frontend/src/api/types.ts:187-217`, `233-241`, `244-274`, and `278-289` mirror the narrow session, payload, op, and turn DTOs.
- `frontend/src/api/types.ts:740-749` omits health-source `meta`, while backend health/source DTOs may emit adapter metadata.
- `frontend/src/components/TurnView/TurnStep.tsx:55-60` detects payload language using obsolete kinds `request` and `response`.
- `frontend/src/components/TurnView/TurnStep.tsx:203-212` fetches first payload for non-tool ops and first two payloads for tool ops by array position, not by semantic payload kind.
- `frontend/src/components/TurnView/TurnStep.tsx:260-293` labels the first tool payload as "Params" and second as "Response" regardless of actual `payload_refs.kind`.
- `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx:263-322` renders a useful op inspector but cannot render fields that the REST contract omits.
- `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx:420-465` can preview payload bytes by ID but cannot show payload selector/hash proof metadata.

Live DB structural snapshot, taken during the current rebuild:

| Table | Evidence of populated fields |
|---|---|
| `sessions` | 152306 rows; `provider_alias` 9890; `cwd` 18802; `call_path` 132718; `error_message` 27181; cache-read rows 25122; cache-write rows 11299; `extras_json` 152306 |
| `turns` | 670068 rows; `error_class` 1198; cache-read rows 288795; cache-write rows 31619; `extras_json` 16190 |
| `ops` | 3403752 rows; `tool_namespace` 1039106; `provider_alias` 285874; `reasoning_kind` 613767; cache-read rows 405534; cache-write rows 150440; `bytes_in` 1063411; `bytes_out` 899708; `chars_in` 219820; `chars_out` 208553; `extras_json` 1739894 |
| `payload_refs` | 4724633 rows; `location_uri` 4705482; `sha256` 186218; 10 distinct payload kinds; 4 distinct formats |

Current payload kind distribution in that snapshot:

| Payload kind | Rows |
|---|---:|
| `tool_request` | 1491482 |
| `tool_response` | 1447083 |
| `llm_response` | 702064 |
| `llm_reasoning` | 618467 |
| `llm_request` | 358433 |
| `sdk_request` | 46419 |
| `sdk_response` | 46417 |
| `log` | 9990 |
| `llm_sdk_request` | 2316 |
| `llm_sdk_response` | 2305 |

Gap matrix:

| Surface | DB/spec provides | API/TS/UI today | Gap and severity |
|---|---|---|---|
| Session identity and grouping | `provider`, `provider_alias`, `cwd`, `call_path`, indexed and documented as first-class | Detail exposes `provider`; list/detail omit `provider_alias`, `cwd`, `call_path`; URL filters omit all three | P1. Spec says these drive UI paths, but the UI cannot receive or filter them through the main contracts. |
| Session errors and duration | `error_class`, `error_message`, `duration_us`, `last_activity_ts`, cache-token columns | Detail exposes `error_class`, `last_activity_ts`, cache tokens; omits `error_message` and `duration_us`; list omits cache tokens and provider | P2. Failure explanation and exact persisted duration are stranded for session-level diagnosis. |
| Turn detail | `error_class`, cache-token columns, `extras_json` | Turn DTO exposes status, input/output tokens, cost, op count, ops | P1. Turn-level errors and cache economics are in DB but unavailable to Turn View. |
| Op detail | `tool_namespace`, `provider_alias`, `reasoning_kind`, cache tokens, bytes/chars, `extras_json` | Op DTO exposes model/provider, tokens in/out, cost, context, errors, child link, payload refs | P1. Tool namespace, provider alias, reasoning kind, byte/char scale, and cache economics are captured but not inspectable per op. |
| Payload refs | `kind`, `format`, `compression`, `location_uri`, byte sizes, `sha256`; proof contract requires exact selectors | API/TS expose preview metadata but not `location_uri` or `sha256`; UI fetches preview by `id` | P1/P2. UI can preview bytes, but cannot prove selector/hash identity. This is P1 for debug/audit workflows and P2 for everyday UX. |
| Payload rendering | Semantic kinds distinguish tool request/response, LLM request/response, SDK request/response, reasoning, logs | TurnStep picks first/second payload by position and has obsolete language detection for `request`/`response` | P1. The UI can mislabel payloads or render the wrong semantic artifact when order or kind mix changes. |
| Source metadata | Backend source DTO emits adapter `meta` | TS `SourceItem` and `HealthSource` omit `meta` | P2. Source diagnostics can be silently untyped/lost in frontend contracts. |
| Payload route contract | Server registers `/api/payloads/` | `frontend/src/api/payloads.ts` says the route is not registered | P1. Frontend docs/code comments contradict runtime behavior. |
| Tests | DB stores real canonical payload kinds | Turn View tests use invented payload kinds such as `request`, `response`, `reasoning`, and `raw` | P2. Tests can pass while the real data contract is broken. |
| Drift gate | Specs, Go DTOs, TS types, UI assumptions all define pieces of the contract | No automated field matrix gate found | P1. The same drift can recur after the next ingestion/schema change. |

Risks:

- Exposing every column everywhere would increase payload size, UI clutter, and sensitive-path leakage.
- Hiding proof fields forever would make parity/debugging harder even though ingestion now records proof-grade selectors.
- Changing DTO shapes is additive but can still break frontend tests and handwritten fixtures if optionality is not handled carefully.
- Any filter/grouping change can affect query performance and URL compatibility.
- SOW-0103 may duplicate or contradict this work unless explicitly reconciled.

## Pre-Implementation Gate

Status: ready

Implementation must not begin until this SOW is approved and moved to `current/`.

Problem / root-cause model:

- The ingestion and parity work made SQLite richer and more proof-oriented, but the REST/TypeScript/UI contracts were not advanced as one unit.
- Presenter SQL projections are the choke point. If a column is not selected into the Go DTO, the TypeScript type and UI cannot use it.
- The Turn View treats payload order as meaning. The DB now treats payload kind and selector metadata as meaning.
- Tests do not currently encode the real DB payload kind taxonomy, so semantic rendering bugs are easy to miss.

Evidence reviewed:

- DB schema and proof contract: `.agents/sow/specs/data-model.md:88-108`, `138-153`, `165-198`, `228-288`.
- REST response examples: `.agents/sow/specs/rest-api.md:96-143`, `159-189`.
- Frontend type mirror contract: `.agents/sow/specs/frontend-architecture.md:41-51`.
- URL filter contract: `.agents/sow/specs/frontend-architecture.md:106-122`.
- Turn View semantic contract: `.agents/sow/specs/ui-turn-view.md:13-48`.
- Presenter DTOs and projections: `internal/presenter/session_detail.go:22-101`, `internal/presenter/session_detail_ops.go:62-190`, `internal/presenter/sessions_list.go:13-86`.
- Source metadata DTO: `internal/presenter/sources.go:28-44`.
- Payload route registration: `internal/presenter/presenter.go:261-280`.
- Frontend API types: `frontend/src/api/types.ts:187-289`, `718-749`.
- Turn View payload selection: `frontend/src/components/TurnView/TurnStep.tsx:55-60`, `203-293`.
- Payload preview inspector: `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx:263-340`, `420-465`.
- Stale payload helper comment: `frontend/src/api/payloads.ts:1-7`.
- Live DB structural counts listed above.

Affected contracts and surfaces:

- SQLite derived schema and data-model spec.
- Presenter SQL projections and JSON DTOs.
- REST API examples and endpoint contracts.
- TypeScript API interfaces.
- Turn View payload selection/rendering.
- Span detail/payload inspector.
- Sources and health frontend types.
- URL filter and stats drilldown contracts.
- Component tests and backend presenter tests.
- Spec-drift or contract-drift quality gates.
- SOW-0103 lifecycle.

Existing patterns to reuse:

- Presenter DTOs as explicit endpoint contracts, not generic DB-row dumps.
- Lazy payload preview via `/api/payloads/:id`, so full payload bytes remain on demand only.
- `SpanDetailDrawer` key-value field rows for advanced/op details.
- Turn View `kind + name` step dispatch, but updated to use semantic payload refs within each step.
- URL-synced filters in `frontend/src/state/filters.ts`.
- Existing `scripts/spec-drift.sh` pattern for lightweight contract checks.

Risk and blast radius:

- Backend: additive DTO/query changes under `internal/presenter`; no schema migration expected for this SOW.
- Frontend: session detail, Turn View, Span Detail Drawer, Sources/Health panels, tests.
- Performance: adding fields to full session detail is probably acceptable; adding wide fields to list endpoints needs care.
- Security/privacy: `cwd`, `location_uri`, payload previews, and source metadata can reveal local paths or sensitive content. Primary UI should avoid exposing full paths by default.
- Compatibility: additive JSON fields are safe for clients; changing existing field meanings is not allowed.
- Operational: if the drift gate is too strict too early, it may block development on intentional internal-only fields. The matrix needs explicit intent categories.

Sensitive data handling plan:

- Do not write raw payload bodies, prompts, tool outputs, source locations containing workstation-private paths, credentials, tokens, private endpoints, or customer/community identity into SOWs, specs, tests, or docs.
- Use structural counts, field names, relative repo file paths, and redacted examples.
- Treat `cwd` and `location_uri` as sensitive by default. If exposed in UI, display a shortened/masked form by default and reserve full copy for explicit debug actions.
- Tests should use synthetic fixtures with fake paths and fake payloads.

Implementation plan:

1. Define the contract matrix.
   - Add or update specs with a field-intent table for session, turn, op, source, and payload-ref fields.
   - Categories: primary UI, detail UI, debug/proof UI, API-only, internal-only.
   - Files likely touched: `data-model.md`, `rest-api.md`, `frontend-architecture.md`, `ui-pages.md`, `ui-turn-view.md`, `testing-strategy.md`.

2. Reconcile SOW-0103.
   - Compare SOW-0103 findings against this matrix.
   - Either close SOW-0103 as superseded or reduce it to purely visual presentation once this SOW owns the contract work.

3. Update REST contracts before code.
   - Decide which fields belong in `/api/sessions`, `/api/sessions/:id`, `/api/sessions/:id/payload_refs`, `/api/sources`, and `/api/health`.
   - Update examples and optionality rules.
   - Define path masking and proof metadata visibility.

4. Write contract tests before implementation.
   - Backend tests assert presenter DTOs include chosen fields and omit internal-only fields.
   - Frontend tests use real canonical payload kinds.
   - Add a small drift check or test that compares the field-intent matrix against Go JSON tags and TypeScript fields where practical.

5. Implement backend DTO/query additions.
   - Extend presenter structs and SQL projections additively.
   - Avoid adding heavyweight fields to list endpoints unless matrix says primary/list UI needs them.

6. Implement frontend type and UI changes.
   - Extend `frontend/src/api/types.ts`.
   - Replace order-based payload selection with semantic kind selection.
   - Update `detectLanguage` for canonical payload kinds and formats.
   - Add debug/proof metadata in an explicit advanced surface.
   - Add source metadata typing/rendering where useful.

7. Run focused and full validation.
   - Presenter tests, frontend unit tests for Turn View and Span Detail Drawer, typecheck, lint, full local gates as appropriate.
   - Run external reviewer gates at the SOW plan and implementation milestones if the work remains non-trivial.

Validation plan:

- Backend:
  - Presenter tests for `/api/sessions`, `/api/sessions/:id`, `/api/sessions/:id/payload_refs`, `/api/sources`, and `/api/health` field presence/absence.
  - Tests should verify semantic proof fields are either exposed in the chosen debug contract or intentionally omitted from primary contracts.

- Frontend:
  - TypeScript fixtures using real payload kinds: `tool_request`, `tool_response`, `llm_request`, `llm_response`, `llm_reasoning`, `sdk_request`, `sdk_response`, `llm_sdk_request`, `llm_sdk_response`, and `log`.
  - Turn View tests that tool params/response are selected by kind, not array order.
  - Reasoning tests that `reasoning_kind` is shown when exposed.
  - Span Detail Drawer tests for cache tokens, byte/char counters, tool namespace, provider alias, and proof metadata according to the matrix.
  - Sources/Health tests for `meta` if the matrix exposes it.

- Drift gate:
  - Add a lightweight contract matrix fixture or script that fails when a field marked primary/detail/debug in specs is absent from Go DTOs or TypeScript types.
  - Keep internal-only fields in an allowlist so the gate is strict without forcing every DB column into the UI.

- Manual smoke:
  - Open a session with tool payloads and confirm tool request/response labels match payload kinds.
  - Open a session with reasoning and confirm reasoning metadata appears in the chosen detail surface.
  - Open payload proof/debug metadata and confirm paths are masked/truncated unless explicitly expanded.

Artifact impact plan:

- AGENTS.md: no expected update unless a new general contract rule is discovered.
- Runtime project skills: no expected update unless the drift-gate workflow becomes a reusable rule.
- Specs: expected updates to REST, frontend architecture, UI pages, UI turn view, data model notes, and testing strategy.
- End-user/operator docs: likely no update unless this changes visible operator behavior materially.
- End-user/operator skills: likely unaffected.
- SOW lifecycle: this SOW should either absorb/supersede SOW-0103 or explicitly depend on it with narrowed scope.

Open-source reference evidence:

- None checked for this initial SOW. The work is an internal DB/API/UI contract alignment task over ai-viewer's own schema and presenter contracts, not a protocol/library design question.

Open decisions:

- See "Implications And Decisions". These are not blockers for creating this proposed SOW, but they must be resolved before implementation starts.

## Implications And Decisions

1. What should happen to SOW-0103?

   - Option A - Absorb/supersede it in SOW-0105. Recommended, long-term-best.
     - Pros: one owner for the DB/API/UI contract; removes provisional assumptions from SOW-0103; avoids duplicate UI work.
     - Cons: SOW-0105 becomes larger.
     - Risk: lower long-term risk because the contract is fixed once.
   - Option B - Keep SOW-0103 separate.
     - Pros: smaller SOW-0105.
     - Cons: two SOWs may edit the same UI surfaces and disagree on payload/op semantics.
     - Risk: stale SOW-0103 assumptions may leak into implementation.

2. How should proof fields be surfaced?

   - Option A - Show `location_uri` and `sha256` in normal payload rows.
     - Pros: always visible.
     - Cons: noisy; path leakage risk; too much detail for normal usage.
     - Risk: sensitive local paths become easier to expose in screenshots.
   - Option B - Show proof fields only in an explicit debug/proof surface. Recommended, long-term-best.
     - Pros: auditability without clutter; easier masking/truncation; aligns with proof fields being advanced diagnostics.
     - Cons: one more UI affordance to build.
     - Risk: low if discoverable from payload rows.
   - Option C - Keep proof fields hidden from UI.
     - Pros: simplest UI.
     - Cons: operator cannot inspect exact selector/hash proof from the app.
     - Risk: parity/debug work remains dependent on DB/sqlite inspection.

3. What should be the field exposure model?

   - Option A - Add every DB column to API and TS types.
     - Pros: fastest to make data available.
     - Cons: bloated responses; weak privacy boundaries; unclear UI ownership.
     - Risk: future schema changes become accidental public API changes.
   - Option B - Classify each field as primary, detail, debug/proof, API-only, or internal-only. Recommended, long-term-best.
     - Pros: stable contracts; clear intent; supports automated drift checks without exposing everything.
     - Cons: requires upfront matrix work.
     - Risk: low, and explicitly managed.
   - Option C - Keep current narrow API and use DB-only diagnostics for the rest.
     - Pros: minimal code.
     - Cons: contradicts the product goal of a low-friction UI over captured agent activity.
     - Risk: high; captured data remains stranded.

4. Should a contract drift gate be added?

   - Option A - Add a lightweight automated gate tied to the field-intent matrix. Recommended, long-term-best.
     - Pros: prevents recurrence; reviewers get concrete evidence; future schema changes must declare UI/API intent.
     - Cons: extra maintenance for intentional internal fields.
     - Risk: low if the matrix has internal-only allowlists.
   - Option B - Keep this as manual review discipline.
     - Pros: no gate work.
     - Cons: this exact drift can recur.
     - Risk: medium/high because ingestion evolves quickly.

## Plan

1. Approve the scope and decisions above.
2. Move this SOW to `current/`.
3. Update specs first with the field-intent matrix and REST/UI contract deltas.
4. Write backend and frontend contract tests using real canonical payload kinds.
5. Implement additive presenter DTO/query updates.
6. Implement frontend type and UI changes.
7. Add the drift gate or targeted contract test.
8. Reconcile SOW-0103.
9. Run local quality gates and reviewer gates appropriate to the final implementation size.
10. Commit the SOW/spec/test/code/doc updates together.

## Execution Log

### 2026-06-25

- Created pending SOW from local specs, presenter code, frontend types/components, and structural DB counts.
- No runtime code, tests, specs, or docs changed.
- No raw payload contents were read or copied into this SOW.
- Ran external gap-analysis reviewers on this SOW and the SOW-0097 through
  SOW-0105 lineage question. Gate result: not converged. Four reviewers voted
  `NEEDS WORK`; two reviewers voted `NOTHING MORE CAN BE DONE`.

## Validation

Acceptance criteria evidence:

- Pending until implementation. This SOW itself contains the initial gap matrix and evidence references.

Tests or equivalent validation:

- `git diff --no-index --check /dev/null .agents/sow/pending/SOW-0105-20260625-ui-db-contract-gap-analysis.md` passed for the untracked SOW file.
- A targeted scan for the user's personal name and workstation-private home path returned no matches.
- `scripts/scan-secrets.sh` passed for tracked files; the new untracked SOW file was checked separately by the targeted pattern scan above.

Real-use evidence:

- Structural DB counts were queried from `/opt/ai-viewer/data/index.db` without reading payload bodies.

Reviewer findings:

- Gap review round 1, 2026-06-25:
  - `glm`: `NEEDS WORK`.
  - `minimax`: `NEEDS WORK`.
  - `kimi`: `NEEDS WORK`.
  - `mimo`: `NEEDS WORK`.
  - `deepseek`: `NOTHING MORE CAN BE DONE`.
  - `qwen`: `NOTHING MORE CAN BE DONE`.
- Accepted P1/P2 finding classes to fold into the next SOW-0105 revision:
  - Decide `extras_json` per-key exposure and explicitly block raw parity/path
    metadata from default UI/API surfaces.
  - Reframe the stale `frontend/src/api/payloads.ts` issue as a missing typed
    payload byte-streaming client/route contract, not just a stale comment.
  - Reconcile opt-in heavy-field delivery (`?include=...`) before adding proof or
    extras metadata to any response.
  - Analyze the dedicated payload refs endpoint, inline payload-ref includes,
    session trace, compare, stats, topology, SSE, source/health, search/FTS, and
    parse-error/health surfaces in the matrix.
  - Split already-exposed op-level fields from truly missing session/turn/list
    fields, especially `duration_us`, `error_class`, `error_message`, and
    compression metadata.
  - Make SOW-0103 ownership reconciliation a precondition before this SOW moves
    to implementation.
  - Add concrete test-file inventory for invented frontend payload kinds and add
    accessibility coverage for new proof/debug UI.
  - Add a machine-readable contract-matrix artifact or an equivalent explicit
    drift-gate source of truth.
- Reviewer lineage consensus:
  - SOW-0098 does not exist.
  - SOW-0099 through SOW-0102 are the direct adapter-remediation backlog tied to
    the SOW-0096/SOW-0097 ingestion-accuracy program.
  - SOW-0103 is downstream UI surfacing work and overlaps with this SOW.
  - SOW-0104 is an operational restart defect found during SOW-0097 install, not
    parity remediation.
  - SOW-0105 is complementary DB/API/UI contract work, not direct SOW-0097
    parity remediation.
- Reviewer disagreement to resolve:
  - One reviewer argued SOW-0097 itself should be reopened because open adapter
    remediation means the parity goal is incomplete.
  - The other reviewers and local file evidence support a narrower conclusion:
    SOW-0097 is complete as a parity-gate framework SOW, but the parent
    ingestion-accuracy/parity program remains open through SOW-0096 and
    SOW-0099 through SOW-0103. This must be stated explicitly before anyone
    claims "parity is done".
- Local conclusion for planning:
  - Do not reopen the exact SOW-0097 file unless a regression is found in the
    parity-gate framework it delivered.
  - Do not claim the broader ingestion/parity goal is finished while SOW-0099
    through SOW-0103 and this UI/DB contract SOW still carry accepted follow-up
    work.
  - Treat SOW-0104 separately: it is operational install/restart debt found
    during SOW-0097 closure, not adapter parity debt.

Same-failure scan:

- Initial scan found no frontend hits for key DB fields such as `provider_alias`, `call_path`, `reasoning_kind`, `bytes_in`, `bytes_out`, `chars_in`, `chars_out`, `sha256`, `location_uri`, and `tool_namespace`.

Sensitive data gate:

- Durable content uses repo-relative file paths, schema field names, and aggregate counts only. It does not include raw prompts, payload bodies, credentials, private endpoints, or workstation-private paths.

Artifact maintenance gate:

- AGENTS.md: not updated; this is a proposed SOW only.
- Runtime project skills: not updated; this is a proposed SOW only.
- Specs: not updated yet; listed as implementation prerequisites.
- End-user/operator docs: not updated; no user-visible behavior changed.
- End-user/operator skills: not updated; no operator workflow changed.
- SOW lifecycle: created in `.agents/sow/pending/`.

Specs update:

- Pending approval.

Project skills update:

- None for SOW creation.

End-user/operator docs update:

- None for SOW creation.

End-user/operator skills update:

- None for SOW creation.

Lessons:

- The UI/data contract needs an explicit field-intent matrix because DB schema, REST examples, Go DTOs, TypeScript types, component assumptions, and tests can drift independently.

Follow-up mapping:

- SOW-0103 must be resolved after this SOW is approved: either superseded by SOW-0105 or narrowed to presentation-only work.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
