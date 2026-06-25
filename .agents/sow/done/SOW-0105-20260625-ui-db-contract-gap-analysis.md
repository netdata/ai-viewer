# SOW-0105 - UI vs DB contract gap analysis

## Status

Status: completed

Sub-state: The post-regression SOW-0105 implementation review converged after the 2026-06-26 lint correction. All six reviewers (`glm`, `minimax`, `kimi`, `mimo`, `deepseek`, `qwen`) voted `PRODUCTION GRADE`; no accepted code, test, or contract P0/P1/P2 remains. Local and reviewer-run contract/spec/frontend/backend checks are green. The aggregate benchmark blocker remains tracked separately under SOW-0106 because it is adapter scan performance outside SOW-0105's touched code. Gap review converged on 2026-06-25 with 6/6 reviewers voting `NOTHING MORE CAN BE DONE`; implementation-plan review converged on 2026-06-25 with 6/6 reviewers voting `READY FOR IMPLEMENTATION`.

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
- SOW-0103 covers captured-but-unsurfaced UI work, but it predates the final SOW-0097 parity shape and carries provisional assumptions about new `user_input` and `assistant` op kinds.
- SOW-0103 overlaps this SOW's DB/API/UI contract work and is superseded by SOW-0105. SOW-0105 owns the contract, payload-kind, and UI-surfacing matrix.
- SOW-0104 is operational restart debt found during SOW-0097 install, not adapter parity or UI/DB contract remediation.

Inferences:

- The highest-risk gap is not just missing labels in the UI. It is a contract drift problem: DB schema, REST examples, Go DTOs, TypeScript interfaces, component assumptions, and tests no longer describe the same data model.
- Fixing this well should start with a field-intent matrix, not by blindly adding every DB column to every response.
- Some proof fields (`location_uri`, `sha256`, exact selectors) are useful for operator audit/debugging but may leak sensitive local path details if displayed in primary UI chrome.

Unknowns:

- Whether proof metadata should be visible only in a debug/proof drawer or also in normal payload rows.
- Final live DB population counts after the recent DB rebuild completes. The counts below are evidence that fields are populated, not final totals.

### Acceptance Criteria

- A DB-to-REST-to-TypeScript-to-UI matrix exists for the current session, turn, op, source, payload-ref, trace, payload streaming, compare, stats, topology, timeline, search, logs, related, subscription/SSE, health, and parse-error contracts.
- Each gap is classified by severity and implication.
- Each field is classified as primary UI, detail UI, debug/proof UI, API-only, internal-only, or explicitly out of scope.
- Each field also has a contract state: exposed, exposed-via-include, missing-default, missing-completely, or internal-only.
- Heavy/debug/proof fields have an explicit `?include=` delivery rule before any DTO change.
- `extras_json` has a per-key exposure policy or an explicit internal-only rule.
- Canonical payload-kind names and aliases have one authoritative taxonomy before UI tests are rewritten.
- Any new filter/group dimension has an explicit index/query-cost decision before it is added to list, stats, or subscription contracts.
- A concrete machine-readable contract-matrix drift gate is specified before implementation planning.
- SOW-0103 is closed as superseded or narrowed before this SOW moves to implementation; the selected path is superseded/absorbed by this SOW.
- The SOW identifies the spec deltas that must land before tests/code.
- The SOW includes a future validation plan for contract tests, UI tests, and a drift gate.
- No raw payload content, prompts, credentials, private endpoint details, or workstation-private paths are written into durable artifacts.
- No spec, test, or implementation code changes are made under this SOW until the implementation-plan reviewer gate converges with `READY FOR IMPLEMENTATION`.

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
- `internal/presenter/session_trace.go`
- `internal/presenter/session_payload_refs.go`
- `internal/presenter/payloads.go`
- `internal/presenter/compare.go`
- `internal/presenter/session_compare.go`
- `internal/presenter/session_topology.go`
- `internal/presenter/topology.go`
- `internal/presenter/session_timeline.go`
- `internal/presenter/session_related.go`
- `internal/presenter/session_logs.go`
- `internal/presenter/search.go`
- `internal/presenter/search_content.go`
- `internal/presenter/stats.go`
- `internal/presenter/stats_aggregate.go`
- `internal/presenter/stats_top.go`
- `internal/presenter/sources.go`
- `internal/presenter/health.go`
- `internal/presenter/events_sse.go`
- `internal/presenter/subscription_filter.go`
- `internal/presenter/presenter.go`
- `internal/canonical/events.go`
- `internal/canonical/property_test.go`
- `frontend/src/api/types.ts`
- `frontend/src/api/payloads.ts`
- `frontend/src/components/TurnView/payloadStore.ts`
- `frontend/src/components/TurnView/TurnStep.tsx`
- `frontend/src/components/TurnView/TurnView.test.tsx`
- `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx`
- Structural SQLite counts from `/opt/ai-viewer/data/index.db`, with no raw payload reads.

Current state:

- `data-model.md:88-108` defines session columns including `provider`, `provider_alias`, `cwd`, `call_path`, `error_message`, `duration_us`, cache-token columns, `first_user_message_hash`, and `extras_json`.
- `data-model.md:132-133` states `cwd`, `provider_alias`, and `call_path` are first-class because they drive filter or grouping paths in the UI.
- `data-model.md:138-153` defines turn-level `error_class`, cache-token columns, and `extras_json`.
- `data-model.md:165-198` defines op-level `tool_namespace`, `provider_alias`, `reasoning_kind`, cache-token columns, byte/char counters, context fields, child linkage, and `extras_json`.
- `data-model.md:228-238` defines payload proof fields including `location_uri` and `sha256`.
- `data-model.md:261-288` describes payload refs as proof objects, not just UI preview pointers.
- `data-model.md:116-124` defines indexes for `provider`, `cwd`, `first_user_message_hash`, and duration, but not for `provider_alias` or `call_path`.
- `data-model.md:297-302` documents parity proof keys in `log_entries.extras_json`; these keys must not be treated as safe generic UI extras.
- `data-model.md:553-570` records cross-format field population differences: `cwd`, `call_path`, `provider_alias`, cache tokens, `reasoning_kind`, byte/char counters, and payload refs are not uniformly populated by every adapter.
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
- `internal/presenter/session_detail.go:103-122` defines the nested `childSummary` DTO for child-session trees; it is a separate contract from `SessionListItem`.
- `internal/presenter/session_detail.go:341-344` selects child summary columns and does not include `error_class`, while `frontend/src/api/types.ts:292-308` declares `error_class?: string`.
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
- `internal/presenter/session_trace.go:24-67` defines a separate trace op DTO with an intentionally narrow field set; it must be analyzed separately from session detail.
- `internal/presenter/session_trace.go:102-109` already uses `?include=payload_refs`; the SOW must preserve this opt-in behavior and define how future include tokens compose.
- `internal/presenter/session_payload_refs.go:39-72` exposes the dedicated payload-ref endpoint, and `87-115` confirms it returns the same narrow `payloadRef` shape without `location_uri` or `sha256`.
- `internal/presenter/payloads.go:18-25` defines payload preview/full caps: 4 KiB preview, 1 MiB JSON preview, and 10 MiB full response.
- `internal/presenter/payloads.go:37-44`, `166-204`, and `207-219` enforce source-root containment and resolve `file://` plus `opencode-sqlite://` payload selectors.
- `internal/presenter/payloads.go:92-120` implements JSON-aware truncation and gzip-aware body resolution.
- `internal/presenter/payloads.go:140-151` sets payload response headers and supports HEAD with headers only.
- `internal/presenter/subscription_filter.go:16-35`, `47-56`, and `118-128` show subscription filters are limited to time/source/agent/model/tool/status/session/root; they do not support `cwd`, `provider_alias`, `call_path`, or `error_class`.
- `internal/presenter/session_related.go:10-31` and `85-99` define related-session reasoning via first-user-message hash first and `cwd` fallback only when hash is absent.
- `internal/presenter/session_related.go:32-50` defines the related DTO shape; it does not include provider alias or call path.
- `internal/presenter/compare.go:26-47`, `83-92`, and `175-233` define compare responses over session list items, summary metrics, tools, errors, models, agents, and kind distribution.
- `internal/canonical/events.go:345-347` documents an older payload-kind set that omits `sdk_request`, `sdk_response`, and `reasoning_stream`.
- `internal/canonical/property_test.go:789-791` samples only `llm_request`, `llm_response`, `tool_request`, `tool_response`, and `log`, so the test taxonomy lags the live DB and specs.
- `frontend/src/api/types.ts:112-182` defines compare response contracts over `SessionListItem`, summaries, tools, errors, models, agents, and kind distributions.
- `frontend/src/api/types.ts:317-368`, `370-452`, `454-487`, `489-657`, `659-714`, `772-841` define topology, timeline, trace, related, logs, stats, search, subscription, and SSE TypeScript contracts that need explicit matrix coverage.
- `frontend/src/components/TurnView/payloadStore.ts:49-60` is the real typed client for `GET /api/payloads/:id`, while `frontend/src/api/payloads.ts:1-7` is a stale dead stub.
- `frontend-architecture.md:49` still labels `frontend/src/api/payloads.ts` as a Phase-2 helper, but the route is now registered and used.
- `frontend-architecture.md:113-122` and `134-141` define URL/SSE filters over a narrow set of dimensions and intentionally drop full-text `q` from subscription filters.
- `frontend/src/components/TurnView/TurnView.test.tsx:99-224`, `227-307`, `338-361`, `382-439`, and `442-574` use invented payload kinds including `request`, `response`, `reasoning`, and `raw`; these fixtures must migrate to canonical payload kinds.

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

Canonical payload-kind taxonomy drift:

- Specs and live DB now include both canonical long names and adapter-facing aliases: `llm_request`, `llm_response`, `llm_sdk_request`, `llm_sdk_response`, `sdk_request`, `sdk_response`, `llm_reasoning`, `reasoning_stream`, `tool_request`, `tool_response`, and `log`.
- `internal/canonical/events.go` and `internal/canonical/property_test.go` do not yet cover the alias set, so they are not authoritative enough for UI contract work.
- SOW-0105 implementation must establish a single authoritative source for payload-kind taxonomy before changing Turn View rendering or fixtures.

Gap matrix:

| Surface | DB/spec provides | API/TS/UI today | Gap and severity |
|---|---|---|---|
| Canonical payload-kind taxonomy | Specs/live DB include SDK aliases and reasoning aliases; parity maps aliases to artifact classes | Go event comments, property tests, frontend fixtures, and UI language detection do not share one taxonomy | P1. Rendering and contract tests can remain green while real payload kinds are mislabeled or ignored. |
| Session identity and grouping | `provider`, `provider_alias`, `cwd`, `call_path`; only `provider`/`cwd` have direct supporting indexes today | Detail exposes `provider`; list/detail omit `provider_alias`, `cwd`, `call_path`; URL filters omit all three | P1. Spec says these drive UI paths, but the UI cannot receive or filter them through the main contracts. `provider_alias` and `call_path` must not become filters/groups without index or query-cost work. |
| Session list | Session table has `provider`, `provider_alias`, `cwd`, `call_path`, `duration_us`, `tokens_cache_read`, `tokens_cache_write`, `first_user_message_hash`, `extras_json` | `SessionListItem` exposes neither `provider` nor `provider_alias`, omits `cwd`, `call_path`, `duration_us`, cache tokens, hash, and extras | P1/P2. List/search/filter workflows cannot expose first-class grouping fields; heavy/path/debug fields must be opt-in or omitted by matrix. |
| Session detail | Session table has `provider_alias`, `cwd`, `call_path`, `error_message`, `duration_us`, `first_user_message_hash`, `extras_json` | Detail exposes `provider`, `error_class`, last activity, tokens, cache tokens; omits the listed fields | P1. Detail view is the natural place for most fields, but proof/path/debug fields need masking and intent classification. |
| Child-session tree | Nested child summaries are derived from sessions and power the Overview execution tree | Backend child summary omits many list/detail fields; TypeScript declares `error_class` even though backend does not emit it | P2/P3. The contract must either add `error_class` and selected list-like fields deliberately, or remove stale frontend assumptions. |
| Effective session status | Presenter exposes `effective_status` on list/detail-style contracts | Matrix/spec examples are not yet audited against this already-exposed field | P2. Do not accidentally remove or duplicate current status semantics while adding fields. |
| Session errors and duration | `error_class`, `error_message`, `duration_us`, `last_activity_ts`, cache-token columns | Detail exposes `error_class`, `last_activity_ts`, cache tokens; omits `error_message` and `duration_us`; list omits cache tokens, provider, and duration | P2. Failure explanation and exact persisted duration are stranded for session-level diagnosis. |
| First user message hash | `first_user_message_hash` with a partial index for duplicate/correlation workflows | No REST/TS/UI surface; no plan for display | P2. Classify as API-only/internal-only unless a real dedup UI is planned. Do not display raw prompt content. |
| Turn detail | `error_class`, cache-token columns, `extras_json` | Turn DTO exposes status, input/output tokens, cost, op count, ops | P1. Turn-level errors and cache economics are in DB but unavailable to Turn View. |
| Op detail | `tool_namespace`, `provider_alias`, `reasoning_kind`, cache tokens, bytes/chars, `extras_json` | Op DTO exposes model/provider, duration, error class/message, tokens in/out, cost, context, child link, payload refs | P1. Tool namespace, provider alias, reasoning kind, byte/char scale, cache economics, and adapter extras are captured but not inspectable per op. Existing op fields such as `duration_us`, `error_class`, and `error_message` are already exposed and must be marked present, not reimplemented. Payload `compression` is already exposed under `payload_refs`, not as an op field. |
| Payload refs inline and dedicated endpoint | `kind`, `format`, `compression`, `location_uri`, byte sizes, `sha256`; proof contract requires exact selectors | API/TS expose `compression` and byte sizes but not `location_uri` or `sha256`; `/api/sessions/:id/payload_refs` returns the same narrow shape | P1/P2. UI can preview bytes, but cannot prove selector/hash identity. This is P1 for debug/audit workflows and P2 for everyday UX. |
| Payload streaming route | `GET`/`HEAD /api/payloads/:id` returns bounded text bytes, truncation headers, JSON-aware previews, gzip handling, and source-root enforcement | Real client is in `TurnView/payloadStore.ts`; `frontend/src/api/payloads.ts` is a dead stale stub; architecture spec still calls it Phase-2 | P1. This is a missing typed byte-streaming client/server contract, not just a stale comment. |
| Payload rendering | Semantic kinds distinguish tool request/response, LLM request/response, SDK request/response, reasoning, logs | TurnStep picks first/second payload by position and has obsolete language detection for `request`/`response` | P1. The UI can mislabel payloads or render the wrong semantic artifact when order or kind mix changes. |
| Trace endpoint | `session_trace.go` has its own `traceOp` DTO and opt-in payload refs | Trace intentionally omits many detail fields for performance; matrix does not state which omissions are intentional | P1. Trace powers major visual tabs and must be analyzed separately from session detail. Fields omitted for performance must be explicitly marked API/detail-only elsewhere. |
| Compare endpoint | Compare uses `SessionListItem`, summary metrics, tools, error refs, models, agents, and kind distributions | Missing list fields propagate into compare; no provider-alias/cwd/call-path decision | P2. Compare may not need every new field, but it needs explicit coverage to avoid stale side-by-side diagnostics. |
| Stats and rollups | Stats expose totals, model/tool/agent/status/source rows; aggregate/top support `provider` and `cwd` groupings | No `provider_alias` group; `cwd` is available in rollup dimensions but not necessarily visible in the main stats rows; subscription filters omit `cwd`/alias | P2. Decide whether fields become group/filter dimensions or stay detail-only. |
| Topology and timeline | Topology/timeline are compact visual contracts | They intentionally omit most detail fields | P2. Mark as intentionally compact or add fields only if a UI workflow needs them. Avoid payload bloat. |
| Search/FTS | Search returns op/log/content hits; FTS indexes selected text | Search result DTOs omit provider alias, namespace, error class for op hits; extras indexing policy not stated | P2. Decide whether search snippets need richer context and explicitly block raw extras from FTS unless intentional. |
| Related and logs endpoints | Related uses first-user-message hash plus `cwd` fallback; logs expose `extras` separately | Related DTO is compact; logs extras and parity keys lack a UI privacy policy | P2. Related is verified as mostly unaffected, but logs extras need explicit internal/debug classification. |
| Subscription and SSE events | Subscriptions filter by time/source/agent/model/tool/status/session/root; SSE events are minimal invalidation frames | No `cwd`, `provider_alias`, `call_path`, `error_class` filters; SSE events carry ids/timestamps only | P2. Default should remain id/source invalidation plus REST refetch. New dimensions require index and subscription-filter work, not just TypeScript fields. |
| Source metadata | Backend source DTO emits adapter `meta` | TS `SourceItem` and `HealthSource` omit `meta` | P2. Source diagnostics can be silently untyped/lost in frontend contracts. |
| Health and parse errors | Health exposes source status and parse errors; source meta may include adapter diagnostics | TS omits `meta`; parse-error detail surfaces need review | P2. Health must remain compact but should not silently drop useful source diagnostics. |
| `extras_json` / `meta` blobs | Session, turn, op, log-entry, source, and health metadata can contain adapter-specific or parity-specific data | No endpoint-level key policy; exposing raw blobs risks path/parity/debug leakage | P1. Define known-key classifications and keep raw extras/meta internal-only by default unless typed or explicitly debug-gated. |
| Tests | DB stores real canonical payload kinds | Turn View tests use invented payload kinds such as `request`, `response`, `reasoning`, and `raw` | P2. Tests can pass while the real data contract is broken. |
| Drift gate | Specs, Go DTOs, TS types, UI assumptions, and live schema all define pieces of the contract | No automated field matrix gate found | P1. The same drift can recur after the next ingestion/schema change. The gate needs a machine-readable matrix and must fail closed. |

Adapter population and index policy:

| Candidate field | Population evidence | Index evidence | Default SOW-0105 policy |
|---|---|---|---|
| `provider` | Broadly populated on sessions/ops | Indexed at session level | Eligible for list/detail/grouping if the matrix says the UI needs it. |
| `cwd` | Populated by claude-code, codex, opencode, and some ai-agent rows | Indexed at session level | Eligible for detail and selected grouping/filtering with masking. |
| `provider_alias` | Mostly opencode/provider-specific | Not indexed directly | Detail/debug field by default; no filter/group unless an index/query plan is added. |
| `call_path` | Mostly ai-agent v3 and some ai-agent v2 lineage | Not indexed directly | Detail/debug field by default; no filter/group unless an index/query plan is added. |
| `reasoning_kind` | Codex-oriented op metadata | No standalone index | Op detail field; not a list filter. |
| `bytes_in` / `bytes_out` | Broad for payload-heavy/tool paths | No standalone index | Detail scale indicators; hide null/zero noise. |
| `chars_in` / `chars_out` | Mostly ai-agent v2 tool accounting | No standalone index | Detail scale indicators; hide null/zero noise. |
| Payload proof fields | Broad `location_uri`, sparse producer `sha256` | Payload refs indexed by op | Debug/proof UI only; never list default. |

Field contract states:

- `exposed`: present by default in the intended REST DTO, TypeScript type, and UI surface.
- `exposed-via-include`: present only when a documented include token is requested.
- `missing-default`: should be present by default but is absent.
- `missing-completely`: should be exposed somewhere but has no REST/TS/UI contract.
- `internal-only`: intentionally not exposed; the matrix must record the reason.

Field delivery policy to decide before implementation:

| Field class | Default delivery |
|---|---|
| Primary list fields | Default only when the field directly powers visible list rows or URL filters. |
| Detail fields | Default on `/api/sessions/:id`; not default on list endpoints unless separately justified. |
| Debug/proof fields | Behind an explicit include token such as `?include=proof`; never default on list endpoints. |
| Adapter extras | Session/turn/op extras are internal-only by default. Existing source/health metadata and log extras keep their documented default contracts; known safe keys may be lifted into typed fields. |
| Payload refs | Preserve existing `?include=payload_refs` on detail/trace and dedicated lazy endpoint. Proof metadata composes with, but does not replace, payload-ref inclusion. |

Include-token policy:

- Preserve `?include=payload_refs` on session detail/trace and `?include=cursors` on `/api/sources`.
- Multiple include tokens compose through comma-separated values (`?include=payload_refs,proof`) on endpoints that accept more than one include token.
- Unknown include tokens fail with a structured 400 contract error on endpoints that parse `include`.
- Include tokens do not change URL/SSE subscription filters by themselves.
- Per-endpoint include allowlist:
  - `/api/sessions/:id`: `payload_refs`, `proof`; `proof` requires `payload_refs`.
  - `/api/sessions/:id/trace`: `payload_refs`, `proof`; `proof` requires `payload_refs`.
  - `/api/sessions/:id/payload_refs`: `payload_refs` as a no-op compatibility token, and `proof`; refs are inherently present, so `proof` does not require `payload_refs`.
  - `/api/sources`: `cursors`.
  - `/api/health`: no include tokens in this SOW; source metadata remains default-emitted when populated.

Known `extras_json` policy:

- Do not newly expose raw session, turn, or op `extras_json` by default on any endpoint.
- Preserve the already-documented default emission of `/api/sources` metadata, `/api/health` source metadata, and `/api/sessions/:id/logs` `extras`; SOW-0105 aligns TypeScript and UI handling with those existing contracts instead of narrowing them.
- Classify known keys per adapter during spec work before code. Candidate typed/detail keys include stable operator-facing metadata such as session final-report status or adapter turn identifiers. Raw parity diagnostics, selectors, local paths, source filenames, and producer-private blobs are internal-only or debug/proof-only unless they are already part of a documented default-emitted endpoint contract.
- If a future endpoint needs session/turn/op extras, use explicit opt-in and typed DTO fields where possible. This SOW does not add `?include=extras`. The UI must not parse arbitrary untyped adapter blobs for primary behavior.
- Parity proof keys such as native artifact ids, selector URIs, and JSON pointers are debug/proof metadata, not normal extras.

Payload streaming contract policy:

- `frontend/src/api/payloads.ts` becomes the documented typed client for `GET`/`HEAD /api/payloads/:id`; `TurnView/payloadStore.ts` and `SpanDetailDrawer.tsx` consume it instead of each owning transport details.
- The contract must cover request path, abort behavior, text response, `X-Payload-Truncated`, `X-Payload-Total-Bytes`, `X-Payload-Preview-Bytes`, `X-Payload-Format`, JSON-aware truncation, gzip handling, source-root containment errors, HTTP errors, cache policy, and retry semantics.
- Persisted payload `kind` remains source-facing. UI dispatch uses the derived `artifact_class` contract defined below, so `sdk_request` and `llm_sdk_request` can remain distinct source values while sharing SDK rendering.

SOW-0103 reconciliation:

| SOW-0103 chunk | SOW-0105 disposition |
|---|---|
| Type extensions for session/turn/op fields | Absorbed into SOW-0105 contract matrix and DTO work. |
| `user_input` / `assistant` op-kind rendering | Superseded. SOW-0097 did not approve those op kinds; rendering must use canonical payload kinds and existing op semantics unless specs change. |
| Turn header cache/error surfacing | Absorbed. |
| Span detail rows for reasoning, bytes/chars, provider alias, call path | Absorbed and expanded. |
| Session-row and stats provider/call-path display | Absorbed as field-intent decisions, not automatic UI additions. |
| Backend verification including trace DTO | Absorbed and expanded to all endpoint surfaces. |
| Documentation/spec updates | Absorbed. |

Reingest/operational classification:

- Most SOW-0105 gaps are "existing data, additive DTO/UI contract only" because live DB counts prove the columns are populated.
- Payload proof visibility uses existing `payload_refs.location_uri` and `sha256` where present; no adapter change is planned by default.
- If implementation discovers a field is not populated for a source that should populate it, that becomes adapter debt under the relevant adapter SOW, not a UI-contract shortcut.
- A full DB rebuild/reingest is still the clean operational state after the recent ingestion changes, but deleting or moving the live DB is a destructive operation and is tracked separately from this SOW unless explicitly approved.

Privacy and masking policy:

- `cwd` and `location_uri` are path-sensitive. Primary UI shows only a shortened/masked form such as the final path segment plus source label, or `~` for the home prefix if the configured root makes that safe.
- Full paths/selectors may appear only in an explicit debug/proof surface with copy action, and tests must use synthetic paths.
- Screenshots and durable artifacts must not include workstation-private absolute paths or raw payload bodies.
- Masking algorithm for primary UI: show only the final path segment plus the source label when a source label is available. For paths under a configured source root, show at most the source label plus the last two relative segments. For `file://` selector URIs, show the scheme, final path segment, and selector suffix only. Full path or URI text is available only inside the explicit proof/debug drawer copy affordance.

Drift gate design:

- Add `testdata/contracts/field-matrix.yaml` before implementation. Required keys per row: `entity`, `field`, `db_column` or `derived_from`, `rest_surfaces`, `typescript_types`, `ui_surfaces`, `state`, `intent`, `include_token`, `privacy_class`, `adapter_population`, `index_status`, `stats_dimension_eligible`, `subscription_filter_eligible`, `internal_reason`, `sow_ref`, and `test_refs`. Payload-kind rows additionally require `artifact_class`.
- Add `scripts/check-contract-matrix.sh` and call it from `scripts/spec-drift.sh`; include it in the normal gates through the existing spec-drift path.
- The check is fail-closed: fields marked `exposed`, `exposed-via-include`, `missing-default`, or `missing-completely` must have matching Go JSON tags and TypeScript properties or an explicit `internal-only`/`deferred` reason accepted by this SOW.
- Internal-only fields must be explicit allowlist entries with a reason. Avoid "where practical" language.
- Gate resolution workflow: if the gate fires, either code/types/tests are changed to match the matrix, or the matrix row is changed with `sow_ref` plus `internal_reason` or a follow-up `pending_ref`. Exposed-to-internal transitions are not allowed as drive-by edits.

Risks:

- Exposing every column everywhere would increase payload size, UI clutter, and sensitive-path leakage.
- Hiding proof fields forever would make parity/debugging harder even though ingestion now records proof-grade selectors.
- Changing DTO shapes is additive but can still break frontend tests and handwritten fixtures if optionality is not handled carefully.
- Any filter/grouping change can affect query performance and URL compatibility.
- SOW-0103 may duplicate or contradict this work unless explicitly reconciled.
- SOW-0104 is separate operational restart debt, but SOW-0105 must avoid schema/index changes that assume flawless restart behavior. If implementation adds indexes or migrations, sequence or coordinate that work with SOW-0104.

## Pre-Implementation Gate

Status: ready for implementation

Implementation must not begin until the implementation-plan reviewer gate converges with `READY FOR IMPLEMENTATION`.

Problem / root-cause model:

- SQLite has accumulated richer canonical fields across the earlier schema work and the SOW-0097 parity wave, but the REST/TypeScript/UI contracts were not advanced as one unit.
- Presenter SQL projections are the choke point. If a column is not selected into the Go DTO, the TypeScript type and UI cannot use it.
- The Turn View treats payload order as meaning. The DB now treats payload kind and selector metadata as meaning.
- Tests do not currently encode the real DB payload kind taxonomy, so semantic rendering bugs are easy to miss.
- SOW-0103's remaining useful work is the same UI-contract problem, but its provisional op-kind model is superseded. Keeping it open separately creates duplicated scope and stale assumptions.

Evidence reviewed:

- DB schema and proof contract: `.agents/sow/specs/data-model.md:88-108`, `138-153`, `165-198`, `228-288`.
- REST response examples: `.agents/sow/specs/rest-api.md:96-143`, `159-189`.
- Frontend type mirror contract: `.agents/sow/specs/frontend-architecture.md:41-51`.
- URL filter contract: `.agents/sow/specs/frontend-architecture.md:106-122`.
- Turn View semantic contract: `.agents/sow/specs/ui-turn-view.md:13-48`.
- Presenter DTOs and projections: `internal/presenter/session_detail.go:22-101`, `internal/presenter/session_detail_ops.go:62-190`, `internal/presenter/sessions_list.go:13-86`.
- Trace endpoint contract: `internal/presenter/session_trace.go:24-67`, `102-109`, `150-167`.
- Dedicated payload-ref endpoint: `internal/presenter/session_payload_refs.go:39-72`, `87-115`.
- Source metadata DTO: `internal/presenter/sources.go:28-44`.
- Frontend endpoint contracts: `frontend/src/api/types.ts:77-105`, `112-182`, `187-289`, `317-487`, `489-841`.
- Payload route registration: `internal/presenter/presenter.go:261-280`.
- Frontend API types: `frontend/src/api/types.ts:187-289`, `718-749`.
- Turn View payload selection: `frontend/src/components/TurnView/TurnStep.tsx:55-60`, `203-293`.
- Turn View stale test fixtures: `frontend/src/components/TurnView/TurnView.test.tsx:99-224`, `227-307`, `338-361`, `382-439`, `442-574`.
- Payload streaming client/stub split: `frontend/src/components/TurnView/payloadStore.ts:49-60`, `frontend/src/api/payloads.ts:1-7`.
- Payload preview inspector: `frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx:263-340`, `420-465`.
- Child-session tree DTO mismatch: `internal/presenter/session_detail.go:103-122`, `341-344`, `frontend/src/api/types.ts:292-308`.
- Stale payload helper comment: `frontend/src/api/payloads.ts:1-7`.
- Payload streaming server contract: `internal/presenter/payloads.go:18-25`, `37-44`, `92-120`, `140-151`, `166-219`.
- Subscription filter contract: `internal/presenter/subscription_filter.go:16-35`, `47-56`, `118-128`.
- Related-session DTO/heuristics: `internal/presenter/session_related.go:10-50`, `85-99`.
- Compare aggregation contract: `internal/presenter/compare.go:26-47`, `83-92`, `175-233`.
- Schema indexes and adapter population matrix: `.agents/sow/specs/data-model.md:116-124`, `553-570`.
- Payload-kind taxonomy drift: `internal/canonical/events.go:345-347`, `internal/canonical/property_test.go:789-791`, `.agents/sow/specs/data-model.md:231`.
- Live DB structural counts listed above.

Affected contracts and surfaces:

- SQLite derived schema and data-model spec.
- Presenter SQL projections and JSON DTOs.
- REST API examples and endpoint contracts.
- TypeScript API interfaces.
- Trace, compare, stats, topology, timeline, search, related, logs, subscription/SSE, health, and parse-error surfaces.
- Payload streaming client contract.
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

Spec deltas:

1. `.agents/sow/specs/canonical-events.md`
   - Make the payload-kind taxonomy authoritative for canonical events and UI contracts.
   - Record accepted source-facing payload kinds and their derived artifact classes. `sdk_request` and `llm_sdk_request` remain distinct persisted values but both map to the SDK request artifact class; `sdk_response` and `llm_sdk_response` remain distinct persisted values but both map to the SDK response artifact class; `llm_reasoning` and `reasoning_stream` map to the existing parity artifact class `reasoning_text`.
   - State that UI code must dispatch payload rendering by `payload_refs.kind`, not array order.
   - Update the matching Go source comment in `internal/canonical/events.go` and the property-test payload-kind sample in `internal/canonical/property_test.go`; the spec, code comment, property test, and field matrix must agree.

2. `.agents/sow/specs/data-model.md`
   - Confirm the `payload_refs.kind` comment stays aligned with the authoritative payload-kind taxonomy; current evidence shows it already has the full persisted-kind set, so no no-op edit is required if it remains aligned.
   - Add field-intent notes for path-sensitive fields (`cwd`, `location_uri`), proof fields (`sha256`, selector URI), and adapter extras/meta.
   - Record index status for `provider`, `cwd`, `first_user_message_hash`, `duration_us`, and the absence of direct `provider_alias` / `call_path` indexes.

3. `.agents/sow/specs/rest-api.md`
   - Update endpoint examples and optionality for `/api/sessions`, `/api/sessions/:id`, nested `child_sessions`, `/api/sessions/:id/trace`, `/api/sessions/:id/payload_refs`, `/api/payloads/:id`, `/api/sources`, `/api/health`, `/api/search`, `/api/stats*`, `/api/topology`, `/api/sessions/:id/{timeline,topology,related,logs}`, and `/api/sessions/compare`.
   - Define include-token grammar before code: existing `payload_refs` and `cursors`; new `proof`; no `meta` or raw `extras` include token in this SOW because source/health metadata and log extras keep their existing default contracts.
   - Define fail-closed handling for unknown include tokens.

4. `.agents/sow/specs/frontend-architecture.md`
   - Replace stale `frontend/src/api/payloads.ts` Phase-2 wording with the typed client contract for `GET`/`HEAD /api/payloads/:id`.
   - Document that `payloadStore.ts` and `SpanDetailDrawer.tsx` consume the shared API helper instead of owning transport behavior.
   - Add a shared frontend include-token builder used by session detail, trace, sources, and payload-ref clients.
   - State that URL/SSE filters remain unchanged unless the field matrix adds index-backed filter work; SOW-0105 does not add `provider_alias` or `call_path` filters.

5. `.agents/sow/specs/ui-turn-view.md`
   - Define semantic payload lookup by kind for LLM, SDK, tool, reasoning, and log artifacts.
   - Define fallback rendering when a kind is absent, duplicated, unavailable, or source-unavailable.
   - Define proof affordance behavior and masking rules for selector/path metadata.

6. `.agents/sow/specs/ui-pages.md`
   - Add UI placement rules for session detail metadata, child-session tree metadata, Turn View metadata, Span Detail proof rows, Sources metadata, Health metadata, and parse-error visibility.
   - Keep full paths out of primary visual chrome; full selector/path copy remains an explicit debug/proof action.

7. `.agents/sow/specs/sse-protocol.md`
   - State that SOW-0105 keeps SSE as id/source invalidation plus REST refetch.
   - Explicitly exclude `cwd`, `provider_alias`, `call_path`, and `error_class` from subscription filters unless a future SOW adds matching indexed query support.

8. `.agents/sow/specs/testing-strategy.md`
   - Add backend/frontend contract-test requirements for the field matrix, include tokens, payload-kind taxonomy, payload streaming, and path-masking behavior.

9. `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-quality-gates/SKILL.md`
   - Add the contract-matrix gate once the script exists, including command, fail-closed behavior, and self-test.

10. `.agents/sow/specs/security.md`
    - Replace stale Phase-2 payload-route wording with the current registered
      `/api/payloads/:id` safety contract.

No adapter-contract spec change is planned. SOW-0025's deferred attachment schema remains out of scope; SOW-0105 updates payload-kind taxonomy for existing persisted payload refs and does not add an attachment payload kind or nullable `payload_refs.op_id`.

Implementation plan:

The numbered steps below are execution order. Runtime implementation for each
slice starts only after the relevant specs and failing tests for that slice have
landed.

0. Spec deltas first.
   - Land the ten spec deltas listed above before adding runtime behavior.
   - Specs define the target behavior for the matrix, include grammar, artifact
     classes, payload streaming, metadata/extras policy, UI placement, SSE
     non-change, testing strategy, and contract-matrix gate.

1. Field matrix and taxonomy first.
   - Add `testdata/contracts/field-matrix.yaml` as the durable machine-readable matrix.
   - Required row keys: `entity`, `field`, `entity_kind`, `db_column` or `derived_from`, `rest_surfaces`, `typescript_types`, `ui_surfaces`, `state`, `intent`, `include_token`, `privacy_class`, `adapter_population`, `index_status`, `stats_dimension_eligible`, `subscription_filter_eligible`, `internal_reason`, `sow_ref`, and `test_refs`. Payload-kind rows additionally require `artifact_class`.
   - Populate rows for sessions, child-session tree, turns, ops, payload refs, payload streaming, sources, health, logs, trace, compare, stats, topology, timeline, related, search, parse errors, subscriptions/SSE, and canonical payload kinds.
   - Authoritative payload-kind set: `llm_request`, `llm_response`, `llm_sdk_request`, `llm_sdk_response`, `sdk_request`, `sdk_response`, `llm_reasoning`, `reasoning_stream`, `tool_request`, `tool_response`, `log`.
   - `artifact_class` enum: `llm_request`, `llm_response`, `llm_sdk_request`, `llm_sdk_response`, `reasoning_text`, `tool_request`, `tool_response`, `log`.
   - Payload-kind to artifact-class mapping:
     - `llm_request -> llm_request`
     - `llm_response -> llm_response`
     - `llm_sdk_request` and `sdk_request -> llm_sdk_request`
     - `llm_sdk_response` and `sdk_response -> llm_sdk_response`
     - `llm_reasoning` and `reasoning_stream -> reasoning_text`
     - `tool_request -> tool_request`
     - `tool_response -> tool_response`
     - `log -> log`
  - Mapping location: presenter payload-ref DTOs expose derived `artifact_class` using a shared Go helper; frontend types carry the field and UI trusts the backend-derived value. The raw `kind` is never rewritten. No duplicate TypeScript fallback mapping is added in this development-phase SOW. Apply the helper at every payload-ref construction site, including inline session detail refs, trace-attached refs, and the dedicated session payload-refs endpoint.
   - Naming note: `reasoning_text` is retained because it is already the parity artifact-class name in `ingestion-parity.md`, `canonical-events.md`, and `data-model.md`.
   - Matrix states: `exposed`, `exposed-via-include`, `missing-default`, `missing-completely`, `internal-only`.
   - Matrix allowed values are closed:
     - `entity`: `session`, `child_session`, `turn`, `op`, `payload_ref`, `payload_streaming`, `source`, `health`, `log`, `trace`, `compare`, `stats`, `topology`, `timeline`, `related`, `search`, `subscription`, `sse`, `parse_error`, `payload_kind`.
     - `entity_kind`: `session`, `turn`, `op`, `payload_ref`, `source`, `health`, `log`, `endpoint`, `ui`, `gate`.
     - `intent`: `primary_list`, `detail`, `debug_proof`, `api_only`, `internal_only`.
     - `privacy_class`: `public`, `path_sensitive`, `content_sensitive`, `hash_linkable`, `internal`.
     - `adapter_population`: `broad`, `partial`, `sparse`, `none`, `not_applicable`.
     - `index_status`: `indexed`, `partial_index`, `rollup_indexed`, `not_indexed`, `not_applicable`.
     - `stats_dimension_eligible`: `eligible`, `excluded`, `not_applicable`.
     - `subscription_filter_eligible`: `eligible`, `excluded`, `not_applicable`.
   - `entity_kind` categorizes `entity` for gate output and reports; `entity` remains the exact API/UI/spec surface.
   - Endpoint exclusion rule: a field with `intent=detail` is intentionally absent from list/trace/search/rollup endpoints unless `rest_surfaces` explicitly names that endpoint. This avoids confusing "not on this endpoint by design" with a missing contract.
   - Index rule: any field used as a REST list filter or grouping dimension must have `index_status=indexed`, `partial_index`, or `rollup_indexed`; `provider_alias` and `call_path` default to detail/debug only and cannot become list filters or stats group dimensions in this SOW.
  - Deferred fields do not get a separate state. They use `missing-default`, `missing-completely`, or `internal-only` plus `internal_reason` and, when needed, a `pending_ref`.
  - Matrix rows are authored from the current contract state first. A row flips
    to `exposed` or `exposed-via-include` in the same implementation slice that
    adds the matching Go/TypeScript evidence, so the matrix and code never drift
    across commits.
   - Default matrix decisions before code:
     - `/api/sessions` gains `provider` as a primary/list field. `duration_us`, cache tokens, `provider_alias`, `cwd`, `call_path`, `first_user_message_hash`, and `extras_json` remain detail/API-only or internal-only; they are not list defaults in this SOW.
     - `child_sessions[]` gains `provider` and `error_class`; it does not gain `effective_status` in this SOW.
    - `first_user_message_hash` is API-only on session detail, not rendered in primary UI.
    - `effective_status` is already exposed on session list/detail contracts and
      remains `state=exposed`; this SOW must not duplicate or weaken existing
      status semantics.
     - `failure_count` remains an exposed session and child-session field.
    - Trace keeps its current narrow fields: `id`, `turn_seq`, `kind`, `name`, `parent_op_id`, `start_ts`, `end_ts`, `duration_us`, `status`, `error_class`, `error_message`, `child_session_id`, `session_id`, `session_agent_name`, `session_kind`, and opt-in `payload_refs`. Trace does not gain `model`, `provider`, `tokens_in`, `tokens_out`, `cost_usd`, `ctx_used`, `ctx_max`, `tool_namespace`, `provider_alias`, `reasoning_kind`, cache tokens, byte counters, or char counters in this SOW. The matrix records those trace omissions as `internal-only` or `missing-completely` with an explicit performance/detail-endpoint reason.
     - Compare inherits `provider` through `SessionListItem` if compare uses the updated list item. It does not gain `provider_alias`, `cwd`, `call_path`, `tool_namespace`, `reasoning_kind`, byte/char counters, or proof fields in this SOW.
     - Stats keep current `provider` and `cwd` dimensions; `cwd` remains a stats dimension, not a `/api/sessions` filter. Do not add `provider_alias` or `call_path` stats dimensions.
     - Subscription filters keep their current eligible dimensions: time/source/agent/model/tool/status/session/root. `cwd`, `provider_alias`, `call_path`, and `error_class` are excluded.
     - Search and search-content DTOs are evaluated in the matrix; richer context is added only if the matrix records an explicit UI need.
     - Existing default `/api/sources`, `/api/health`, and `/api/sessions/:id/logs` metadata/extras contracts are preserved unless a spec delta explicitly records a breaking change. This plan does not make that breaking change.

2. Drift gate before runtime code.
   - Add `scripts/check-contract-matrix.sh`.
   - Add `scripts/test/check-contract-matrix-test.sh` with hermetic fixture cases for missing matrix, empty matrix, duplicate row, missing required key, invalid enum, missing include token for `exposed-via-include`, missing internal reason for `internal-only`, missing Go JSON tag for exposed REST field, missing TypeScript field for exposed frontend field, and unknown TypeScript contract type names.
  - Integration decision: `scripts/check-contract-matrix.sh` is a standalone parser/checker, and `scripts/spec-drift.sh` invokes it as a sixth fail-closed `contract-matrix` indicator. The separate script keeps the matrix parser testable; the spec-drift wrapper preserves the existing single entry point.
  - Update `scripts/spec-drift.sh` and `scripts/test/spec-drift-test.sh`
    comments, diagnostics, and self-test expectations from five indicators to
    six indicators when the contract-matrix indicator is wired.
  - `scripts/test/spec-drift-test.sh` gains a planted contract-matrix mismatch case, and `scripts/test/check-contract-matrix-test.sh` tests the parser/checker directly. `scripts/gates.sh` runs the contract-matrix self-test before the live spec-drift detector through the normal gate sequence.
   - Gate algorithm:
     - Parse only the documented YAML subset used by `field-matrix.yaml`; fail closed on malformed rows.
     - `scripts/check-contract-matrix.sh` is a wrapper. The parser/checker logic lives in a small repo-local Go helper under `scripts/lib/` so Go DTO extraction can use `go/parser` / `go/ast` and the restricted matrix syntax can be parsed without external dependencies.
     - Extract Go JSON tags from `internal/presenter/*.go`, excluding tests and comments, normalizing tag modifiers such as `omitempty` and handling pointer fields.
     - Extract TypeScript interface properties from the contract-bearing files
       `frontend/src/api/types.ts`, `frontend/src/api/payloads.ts`, and
       `frontend/src/viz/trace.ts` with a deliberately restricted text parser
       for flat exported interfaces. It supports optional properties such as
       `field?:`, but does not resolve `extends`, intersections, or type
       aliases; if the API types move to those constructs, the gate must be
       upgraded in the same SOW.
     - Do not try to prove UI component rendering by grep. Any row with `ui_surfaces` must list one or more tests in `test_refs`.
     - Rows with `state=exposed` or `exposed-via-include` must have matching Go/TypeScript evidence unless the row is endpoint-only or UI-only with an explicit reason.
     - Rows with `state=exposed-via-include` must name exactly one include token.
    - Rows with `state=internal-only` must provide `internal_reason`.
    - Rows with `state=missing-default` or `missing-completely` must be absent
      from the listed Go/TypeScript contract evidence unless they include a
      staged-transition note tied to the current SOW slice.
     - Rows changed from `exposed` or `exposed-via-include` to a less visible state must include `sow_ref` plus `internal_reason` or `pending_ref`.
     - Rows with `ui_surfaces` must list `test_refs`; the gate verifies references exist, and humans verify the test semantics.
     - Non-JSON contracts such as `/api/payloads/:id` streaming headers are verified through `test_refs`, not Go JSON tag extraction.
   - The check must not require live DB access or raw source payloads.

3. Failing tests before runtime implementation.
   - Backend tests:
     - Field matrix gate self-test and live-tree matrix check.
     - Include-token parser tests for `payload_refs`, `proof`, `cursors`, comma composition, invalid tokens, per-endpoint allowlists, and proof-without-payload_refs rejection.
     - Presenter contract tests for sessions list/detail, child summaries, turns, ops, trace, payload refs, payload streaming, sources, health, search, stats/top/aggregate, compare, topology/timeline/related/logs, subscription filters, and parse-error surfaces.
     - Payload streaming tests for GET, HEAD, caps, headers, gzip, JSON-aware truncation, source-root containment, and error codes.
     - Subscription filter tests that reject `cwd`, `provider_alias`, `call_path`, and `error_class` as unsupported filter keys in this SOW.
     - Canonical/property tests that sample every accepted payload kind and artifact-class mapping.
   - Frontend tests:
     - Replace Turn View fixtures using `request`, `response`, `reasoning`, and `raw` with canonical payload kinds before implementation changes rely on them.
     - Fixture migration map:
       - LLM request examples: `request -> llm_request`.
       - LLM response examples: `response -> llm_response`.
       - Reasoning payload examples: payload-ref `reasoning -> llm_reasoning`; op kind `reasoning` remains unchanged.
       - Tool parameter examples: tool-op `request -> tool_request`.
       - Tool result examples: tool-op `response -> tool_response`.
       - Generic raw examples: the current generic raw JSON fixture becomes `log`; any future raw fixture must use the semantic kind that matches the op or be recorded as `internal-only` in the matrix.
     - Add tests for kind-based payload lookup, missing/duplicate payload-kind fallback, SDK payload labeling, reasoning payload rendering, and source-unavailable payload refs.
     - Payload client tests for text response, truncation headers, abort, HTTP errors, retry/no-retry behavior, and per-id caching.
     - Sources/Health tests for default `meta` typing and safe/collapsed UI handling.
     - Logs tests proving default `extras` contract is preserved.
     - Span Detail tests for cache tokens, byte/char counters, tool namespace, provider alias, reasoning kind, artifact class, and proof metadata masking.
     - Playwright + axe coverage is required if proof/debug UI becomes a new route, modal, or drawer; if it remains rows inside the existing Span Detail drawer, focused Vitest coverage plus the existing route-level axe coverage is sufficient.

4. Include-token and privacy implementation.
   - Preserve existing `?include=payload_refs` on session detail/trace and `?include=cursors` on `/api/sources`.
   - Add one shared backend parser in `internal/presenter/include.go` and use it at every include-token endpoint. No endpoint hand-rolls include parsing.
   - Parser behavior: a missing/empty include returns no tokens; a single token such as `?include=payload_refs` keeps current behavior; comma-separated values compose; whitespace is trimmed; duplicate tokens are accepted once; unknown tokens return structured `BAD_REQUEST`.
  - Add one shared frontend include-token builder in `frontend/src/api/client.ts` and use it from session detail, trace, sources, and payload-ref clients.
   - This intentionally changes unknown include tokens on the listed include-aware endpoints from silent no-op to 400; tests must prove internal clients send only allowed tokens.
   - Add `?include=proof` for payload-ref proof fields on `/api/sessions/:id`, `/api/sessions/:id/trace`, and `/api/sessions/:id/payload_refs`; `proof` is valid only when payload refs are present or the endpoint is itself a payload-ref endpoint.
   - `?include=proof` without `payload_refs` on session detail or trace returns `BAD_REQUEST` with a stable code/message that says `proof` requires `payload_refs`.
   - On the dedicated `/api/sessions/:id/payload_refs` endpoint, refs are inherently present, so `?include=proof` augments rows with proof fields and does not require `payload_refs`; `?include=payload_refs` is accepted as a no-op compatibility token.
   - Preserve current default raw `meta` emission on `/api/sources` and `/api/health` because it is already documented in `rest-api.md`. SOW-0105 aligns TypeScript and UI handling with that contract instead of making a breaking default change.
   - Preserve current default log `extras` emission on `/api/sessions/:id/logs` because it is already documented and implemented. SOW-0105 does not add raw session/turn/op extras.
   - Do not add `?include=extras` in this SOW. Raw session/turn/op extras remain internal-only unless a known safe key is lifted into a typed field in the matrix.

5. Backend presenter contracts.
   - Extend DTOs and SQL projections additively according to the matrix.
   - Backend SQL projection deltas must be listed in the implementation commit before code changes land. Minimum expected deltas:
     - sessions list: add `provider`; do not add `duration_us`, cache tokens, `cwd`, `provider_alias`, `call_path`, `first_user_message_hash`, or `extras_json` to list defaults.
     - session detail: add `provider_alias`, `cwd`, `call_path`, `error_message`, `duration_us`, and API-only `first_user_message_hash` if matrix marks them detail/API-safe.
     - child-session tree: add `provider` and `error_class`; do not add `effective_status`.
     - turn detail: add `error_class`, `tokens_cache_read`, and `tokens_cache_write`.
     - op detail: add `tool_namespace`, `provider_alias`, `reasoning_kind`, cache tokens, byte counters, and char counters.
     - payload refs: keep `kind` raw, add derived `artifact_class`, keep `compression` under payload refs, and add `location_uri` / selector URI and `sha256` only when proof is included.
     - search/search-content: add richer context only if the matrix names the exact DTO and UI need.
   - Turn additions: `error_class`, `tokens_cache_read`, `tokens_cache_write` when matrix marks them detail UI.
   - Op additions: `tool_namespace`, `provider_alias`, `reasoning_kind`, cache tokens, byte/char counters; keep `compression` on payload refs.
   - Payload-ref additions behind `proof`: `location_uri` / selector URI and `sha256`; never include them in list endpoints.
   - Source metadata: preserve backend default emission; add TypeScript fields
     and Sources UI handling consistent with the matrix. The Sources page may
     show a safe metadata key-count summary, but must not dump raw metadata
     values into primary UI chrome.
   - Health source metadata: preserve backend default emission and TypeScript
     coverage as an API-only contract. The compact health status UI must not grow
     a raw metadata surface in this SOW.
   - Logs: preserve default `extras` in the REST contract. UI may keep extras collapsed or diagnostic-only, but the API contract is not narrowed in this SOW.
   - Payload proof null handling: if `sha256` is NULL, omit `sha256` or return null according to the REST spec; do not fail the request. UI labels it unverified. A proof row with a selector but no hash is still useful.
   - Do not add new SQLite columns or migrations unless reviewers identify a plan defect; SOW-0105 is additive contract/UI work over existing data.
   - Rollback is commit-level revert. Implementation should be staged so matrix/gate, backend DTOs, frontend types/client, and UI rows can be isolated during review if a stage fails.

6. Payload streaming client consolidation.
   - Implement `frontend/src/api/payloads.ts` as the typed client for `GET` and `HEAD /api/payloads/:id`.
   - Preserve existing cache and abort behavior from `TurnView/payloadStore.ts`.
   - Return a typed result carrying text, truncation flag, total bytes, preview bytes, format header, and status/error classification.
   - Update `payloadStore.ts` and `SpanDetailDrawer.tsx` to consume the shared client.
   - Preserve the current manual-retry pattern. The client does not automatically retry HTTP errors; callers may expose a retry action that re-fetches.
   - Review comments around payload concurrency while consolidating the client; preserve the current cap unless tests justify changing it.

7. Frontend type and UI contracts.
   - Update `frontend/src/api/types.ts` from the backend DTO contract and matrix.
   - Replace Turn View array-position payload selection with kind-based lookup.
   - Update language detection from format + canonical kind, not obsolete `request` / `response` strings.
   - Render SDK request/response through the LLM request/response path with a visible `SDK` label/badge.
   - Render payload refs whose `artifact_class` is `reasoning_text` as reasoning text, not JSON request/response and not a generic raw payload.
   - Add proof/debug UI in `SpanDetailDrawer` or the existing payload inspector, with masked path display by default and explicit copy/expand action for full selectors.
   - Update the Sources component for its existing default metadata contract; do
     not dump raw metadata into primary UI. Keep health source metadata typed and
     API-only unless a later SOW adds a dedicated diagnostic health surface.
   - Preserve the canonical `reasoning` op kind; only replace invented payload-ref kinds such as `request`, `response`, `reasoning`, and `raw`.

8. Validation and gates.
   - Focused checks during implementation: targeted Go presenter tests, `go test ./internal/canonical ./internal/presenter`, targeted frontend Vitest files, and `npm run typecheck`.
   - Required before implementation review: `scripts/test/check-contract-matrix-test.sh`, `scripts/spec-drift.sh`, `scripts/test/spec-drift-test.sh`, `scripts/check-ingestion-parity.sh --fixtures`, `go test -race -count=1 ./...`, frontend lint/typecheck/unit coverage, build/bundle, Playwright/axe if UI changed, and the aggregate `scripts/gates.sh` unless blocked by a documented pre-existing unrelated failure.
   - Record any unrelated pre-existing gate failure separately; do not claim SOW-0105 implementation complete until SOW-owned gates are green and unrelated failures are either fixed or formally tracked.

9. Implementation review and close-out.
   - Run all six external reviewers on the completed diff with the accepted gap analysis, accepted plan, specs, tests, code, and gate evidence.
   - Positive vote required: `PRODUCTION GRADE`.
   - P0/P1/P2 findings are fixed or rejected with evidence; P3 findings may be documented.
   - Commit only the SOW/spec/test/code/doc files in scope, with SOW-0105 referenced in the commit message.

Artifact impact plan:

- AGENTS.md: no expected update unless the contract-matrix gate becomes a top-level commitment.
- Runtime project skills: expected update to `project-quality-gates` when the contract-matrix gate is added.
- Specs: expected updates to REST, frontend architecture, UI pages, UI turn view, data model notes, and testing strategy.
- End-user/operator docs: likely no update unless this changes visible operator behavior materially.
- End-user/operator skills: likely unaffected.
- SOW lifecycle: SOW-0103 is superseded by this SOW and has moved to `done/` as a closed superseded record.

Open-source reference evidence:

- None checked for this initial SOW. The work is an internal DB/API/UI contract alignment task over ai-viewer's own schema and presenter contracts, not a protocol/library design question.

Open decisions:

- See "Implications And Decisions". SOW-0103 absorption is already selected because it is technical scope hygiene, not a product tradeoff.

## Implications And Decisions

1. What should happen to SOW-0103?

   - Option A - Absorb/supersede it in SOW-0105. Selected, long-term-best.
     - Pros: one owner for the DB/API/UI contract; removes provisional assumptions from SOW-0103; avoids duplicate UI work.
     - Cons: SOW-0105 becomes larger.
     - Risk: lower long-term risk because the contract is fixed once.
   - Option B - Keep SOW-0103 separate.
     - Pros: smaller SOW-0105.
     - Cons: two SOWs may edit the same UI surfaces and disagree on payload/op semantics.
     - Risk: stale SOW-0103 assumptions may leak into implementation.
   - Decision: Option A. SOW-0103 is superseded and its useful chunks are absorbed by this SOW before implementation planning.

2. How should proof fields be surfaced?

   - Option A - Show `location_uri` and `sha256` in normal payload rows.
     - Pros: always visible.
     - Cons: noisy; path leakage risk; too much detail for normal usage.
     - Risk: sensitive local paths become easier to expose in screenshots.
   - Option B - Show proof fields only in an explicit debug/proof surface. Selected, long-term-best.
     - Pros: auditability without clutter; easier masking/truncation; aligns with proof fields being advanced diagnostics.
     - Cons: one more UI affordance to build.
     - Risk: low if discoverable from payload rows.
   - Option C - Keep proof fields hidden from UI.
     - Pros: simplest UI.
     - Cons: operator cannot inspect exact selector/hash proof from the app.
     - Risk: parity/debug work remains dependent on DB/sqlite inspection.
   - Decision: Option B as the default contract. Normal rows may show a small proof-available affordance; full selector/hash/path metadata stays in explicit debug/proof UI.

3. What should be the field exposure model?

   - Option A - Add every DB column to API and TS types.
     - Pros: fastest to make data available.
     - Cons: bloated responses; weak privacy boundaries; unclear UI ownership.
     - Risk: future schema changes become accidental public API changes.
   - Option B - Classify each field as primary, detail, debug/proof, API-only, or internal-only. Selected, long-term-best.
     - Pros: stable contracts; clear intent; supports automated drift checks without exposing everything.
     - Cons: requires upfront matrix work.
     - Risk: low, and explicitly managed.
   - Option C - Keep current narrow API and use DB-only diagnostics for the rest.
     - Pros: minimal code.
     - Cons: contradicts the product goal of a low-friction UI over captured agent activity.
     - Risk: high; captured data remains stranded.
   - Decision: Option B. This SOW must not create a generic DB-row dump API.

4. Should a contract drift gate be added?

   - Option A - Add a lightweight automated gate tied to the field-intent matrix. Selected, long-term-best.
     - Pros: prevents recurrence; reviewers get concrete evidence; future schema changes must declare UI/API intent.
     - Cons: extra maintenance for intentional internal fields.
     - Risk: low if the matrix has internal-only allowlists.
   - Option B - Keep this as manual review discipline.
     - Pros: no gate work.
     - Cons: this exact drift can recur.
     - Risk: medium/high because ingestion evolves quickly.
   - Decision: Option A. The exact mechanism is specified in the drift-gate design section and must be implemented before closing this SOW.

5. Should SOW-0104 be absorbed?

   - Option A - Absorb SOW-0104 here.
     - Pros: one broad cleanup pass.
     - Cons: mixes operational restart behavior with UI/DB contracts; makes review scope incoherent.
     - Risk: higher chance of missing issues because the SOW combines unrelated surfaces.
   - Option B - Keep SOW-0104 separate. Selected, long-term-best.
     - Pros: preserves focused scope; SOW-0104 is operational restart debt, not UI/DB contract debt.
     - Cons: leaves another active SOW to implement.
     - Risk: low if tracked explicitly.
   - Decision: Option B. SOW-0104 remains active and must still be addressed under the broader active goal, but not under SOW-0105.

## Plan

1. Gap-analysis gate: completed, 6/6 reviewers voted `NOTHING MORE CAN BE DONE`.
2. SOW-0103 lifecycle: completed, SOW-0103 is closed as superseded by SOW-0105.
3. SOW-0105 lifecycle: completed, moved to `current/` for implementation planning.
4. Implementation-plan gate: completed, 6/6 reviewers voted `READY FOR IMPLEMENTATION`.
5. Update specs first with the field matrix, REST/UI contract deltas, include-token grammar, payload-streaming contract, metadata/extras policy, SSE non-change, testing strategy, and contract-matrix gate definition.
6. Add contract-matrix artifact and fail-closed gate before runtime implementation.
7. Add failing backend/frontend tests against the new specs and matrix.
8. Implement additive presenter DTO/query changes, include-token parsing, payload proof/meta contracts, and payload streaming client consolidation.
9. Implement frontend type/UI changes, semantic payload rendering, proof/debug UI, and metadata handling.
10. Run focused tests, full local gates, and the implementation reviewer gate for `PRODUCTION GRADE`.
11. Commit and push the SOW/spec/test/code/doc updates together; keep SOW-0104 open for the separate operational restart debt.

## Execution Log

### 2026-06-25

- Created pending SOW from local specs, presenter code, frontend types/components, and structural DB counts.
- No runtime code, tests, specs, or docs changed.
- No raw payload contents were read or copied into this SOW.
- Ran external gap-analysis reviewers on this SOW and the SOW-0097 through
  SOW-0105 lineage question. Gate result: not converged. Four reviewers voted
  `NEEDS WORK`; two reviewers voted `NOTHING MORE CAN BE DONE`.
- Re-ran the gap-analysis reviewers on request. Gate result: not converged.
  `glm`, `kimi`, `mimo`, `deepseek`, and `qwen` voted `NEEDS WORK`.
  `minimax` voted `NOTHING MORE CAN BE DONE`, but the response showed path/tool
  confusion and is treated as low-confidence evidence, not convergence.
- Integrated accepted reviewer findings into the SOW body instead of leaving them
  only in the review appendix: expanded surface matrix, `extras_json` policy,
  include-token policy, payload-streaming contract, SOW-0103 absorption,
  drift-gate design, privacy masking, reingest classification, and test
  inventory.
- Re-ran the same-scope gap-analysis reviewers after that revision. Gate result:
  not converged. Accepted findings required another SOW update covering
  canonical payload-kind taxonomy, exact server-side payload streaming behavior,
  source/health/log metadata privacy, index availability for candidate filters,
  subscription/SSE filter implications, compare/related evidence, concrete
  contract-matrix artifact schema, and SOW-0104 sequencing risk.
- Re-ran the same-scope gap-analysis reviewers after the second revision. Gate
  converged: all six reviewers voted `NOTHING MORE CAN BE DONE`; one P3 wording
  issue about `compression` being a payload-ref field was fixed in the SOW.
- Moved SOW-0105 from `pending/` to `current/` after gap-gate convergence.
- Drafted the implementation-plan gate candidate in the Pre-Implementation Gate:
  concrete spec deltas, field-matrix artifact, contract-matrix gate, include
  token policy, backend/frontend implementation slices, test plan, quality gates,
  implementation review gate, and close-out sequence.
- Ran implementation-plan review round 1. Gate did not converge. Accepted
  findings required plan changes for backward-compatible `meta` and log
  `extras`, closed matrix enums, include-token endpoint grammar, central backend
  include parser, frontend include builder, contract-matrix gate algorithm and
  `spec-drift.sh` wiring, child-session `provider`/`error_class`, session-list
  `provider`, trace/detail defaults, search-content coverage, stats dimension
  policy, payload-kind source/comment/property-test synchronization, payload
  client retry semantics, and SDK/reasoning visual treatment.
- Ran implementation-plan review round 2. Gate did not converge. Accepted
  findings required plan changes for stale `extras_json` / `meta` wording,
  detailed spec-test-code sequencing, concrete `artifact_class` enum and mapping,
  `reasoning_stream` taxonomy wording, per-endpoint include-token allowlists,
  `session_payload_refs` proof include handling, session-list `provider`
  decision, compare/trace/default dimension decisions, matrix gate resolution
  workflow, Turn View fixture migration map, and rollback/staging notes.
- Ran implementation-plan review round 3. Gate did not converge. Accepted
  findings required plan changes for the existing `reasoning_text` parity
  artifact class, the actual narrow trace DTO field set, backend-only
  `artifact_class` mapping, the contract-matrix parser mechanism, non-JSON
  payload-streaming header validation through tests, primary UI path/URI
  masking, subscription filter rejection tests, exact raw fixture migration, and
  include parser file placement. `deepseek` failed technically before a final
  vote; retry is deferred until the accepted findings are fixed and the whole
  reviewer batch is rerun.
- Ran implementation-plan review round 4 after the round-3 fixes. Gate
  converged: all six reviewers voted `READY FOR IMPLEMENTATION`; `mimo` required
  one retry after a technical failure. Positive reviews included only P3
  clarifications, which are documented in the plan and do not block
  implementation.
- Updated specs first for the additive REST/session/detail/trace/payload-ref
  contract, include-token grammar, metadata/extras policy, payload-kind
  artifact-class taxonomy, payload streaming client contract, SSE non-change,
  contract-matrix gate, and testing strategy.
- Added `testdata/contracts/field-matrix.yaml` plus
  `scripts/check-contract-matrix.sh`, the Go helper under
  `scripts/lib/check-contract-matrix/`, and self-tests for the new gate and the
  six-indicator `scripts/spec-drift.sh` contract.
- Added backend presenter tests for include-token rejection, proof-gated
  payload refs, artifact-class derivation, payload streaming headers, list/detail
  provider fields, child-session summaries, trace proof behavior, and stats
  field coverage.
- Added frontend API/component tests for include-query construction, typed
  payload byte-streaming calls, TypeScript contract fixtures, and span drawer
  payload fetching through the shared client.
- Implemented the additive presenter/API/frontend slice:
  session/provider fields, child summary fields, turn/op diagnostics,
  payload-ref `artifact_class`, proof-gated `location_uri`/`sha256`, shared
  include parsing, and replacement of the stale frontend payload stub with a
  typed `GET`/`HEAD /api/payloads/:id` client.
- Fixed the static-analysis finding in the repo-local contract-matrix helper by
  documenting the two repo-controlled `os.ReadFile` call sites with scoped
  `#nosec G304` justifications. This is limited to the local gate helper, not
  runtime request handling.
- Ran the full local aggregate gate. Result: all pre-benchmark stages passed,
  but the benchmark gate failed on adapter scan regressions outside the
  SOW-0105 UI/DB contract surface. This is tracked separately as SOW-0106.

## Validation

Acceptance criteria evidence:

- `testdata/contracts/field-matrix.yaml` is the machine-readable DB/API/TS/UI
  field matrix for the implemented slice.
- `scripts/check-contract-matrix.sh` verifies 53 matrix rows against presenter
  JSON tags, TypeScript API interfaces, proof include-token policy,
  payload-kind artifact-class mapping, and referenced tests.
- `scripts/spec-drift.sh` now includes the contract-matrix indicator alongside
  REST, SSE, data-model, canonical event kind, and adapter-probe drift checks.
- SOW-0103 is closed as superseded in `.agents/sow/done/`; this SOW owns the
  absorbed UI/API contract work.
- Heavy proof fields are opt-in via `include=proof`; session detail and trace
  require `include=payload_refs` before `proof`, while the dedicated
  `/payload_refs` endpoint has payload refs inherently.

Tests or equivalent validation:

- `git diff --check -- .agents/sow/current/SOW-0105-20260625-ui-db-contract-gap-analysis.md .agents/sow/done/SOW-0103-20260622-ux-captured-surfaces.md` passed after moving SOW-0105 to `current/` and SOW-0103 to `done/`.
- `go test ./...` passed during focused implementation validation.
- `npm --prefix frontend run test -- --run` passed during focused frontend
  validation.
- `npm --prefix frontend run typecheck` passed.
- `bash scripts/lint.sh` passed.
- `bash scripts/test.sh` passed: Go race/coverage and 923 frontend tests.
- `bash scripts/check-contract-matrix.sh` passed: 53 rows verified.
- `bash scripts/test/check-contract-matrix-test.sh` passed: 6/6 cases.
- `bash scripts/spec-drift.sh` passed: no drift across all 6 indicators.
- `bash scripts/test/spec-drift-test.sh` passed: 29/29 cases.
- Earlier full-gate runs passed before reviewer round 6. The latest aggregate
  gate rerun is not green: `scripts/check-bench.sh` reproduced
  `ClaudeScan_SyntheticCorpus` at `+27.73% sec/op` and
  `CodexScan_SyntheticCorpus` at `+26.72% sec/op`. SOW-0105 does not touch
  `internal/adapters/`, `internal/ingest/`, `internal/parity/`, or `bench/`, so
  this is recorded as separate benchmark-gate debt under SOW-0106, not as a
  defect in the UI/DB contract implementation.
- `scripts/scan-secrets.sh` passed inside `scripts/gates.sh` for tracked files.
- Targeted changed-file scan for the user's personal name and workstation-private
  home path passed after the final SOW update.
- `git diff --check` passed after the final SOW update.

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
- Gap review round 2, 2026-06-25:
  - `glm`: `NEEDS WORK`.
  - `minimax`: `NOTHING MORE CAN BE DONE`, low confidence due path/tooling confusion.
  - `kimi`: `NEEDS WORK`.
  - `mimo`: `NEEDS WORK`.
  - `deepseek`: `NEEDS WORK`.
  - `qwen`: `NEEDS WORK`.
- Gap review round 3, 2026-06-25:
  - `glm`: `NEEDS WORK`.
  - `minimax`: technical failure before a final vote; not counted as convergence.
  - `kimi`: `NOTHING MORE CAN BE DONE` with one accepted P3 implementation-plan note.
  - `mimo`: `NOTHING MORE CAN BE DONE` / P3-only.
  - `deepseek`: `NEEDS WORK`.
  - `qwen`: `NOTHING MORE CAN BE DONE`.
- Gap review round 4, 2026-06-25:
  - `glm`: `NOTHING MORE CAN BE DONE` with one accepted P3 wording note about
    `compression` belonging to `payload_refs`, not `ops`.
  - `minimax`: `NOTHING MORE CAN BE DONE`.
  - `kimi`: `NOTHING MORE CAN BE DONE`.
  - `mimo`: `NOTHING MORE CAN BE DONE`.
  - `deepseek`: `NOTHING MORE CAN BE DONE` on rerun after one technical failure.
  - `qwen`: `NOTHING MORE CAN BE DONE`.
- Implementation plan review round 1, 2026-06-25:
  - `glm`: `NEEDS WORK`.
  - `minimax`: `NEEDS WORK`.
  - `kimi`: `READY FOR IMPLEMENTATION` with P2 implementation clarifications.
  - `mimo`: `NEEDS WORK`.
  - `deepseek`: technical failure before a final vote; not counted as convergence.
  - `qwen`: `NEEDS WORK`.
- Implementation plan review round 2, 2026-06-25:
  - `glm`: `NEEDS WORK`.
  - `minimax`: `NEEDS WORK`.
  - `kimi`: `NEEDS WORK`.
  - `mimo`: `READY FOR IMPLEMENTATION` with P2/P3 clarifications.
  - `deepseek`: `READY FOR IMPLEMENTATION` with P2/P3 clarifications.
  - `qwen`: `READY FOR IMPLEMENTATION` with P2/P3 clarifications; one SOW-0103
    file-location note was rejected as a tooling/glob false positive because
    local evidence shows SOW-0103 in `.agents/sow/done/`.
- Implementation plan review round 3, 2026-06-25:
  - `glm`: `NEEDS WORK`.
  - `minimax`: `READY FOR IMPLEMENTATION` with P3-only notes.
  - `kimi`: `READY FOR IMPLEMENTATION` with accepted P2/P3 clarifications.
  - `mimo`: `READY FOR IMPLEMENTATION` with P3-only notes.
  - `deepseek`: technical failure before a final vote; not counted as
    convergence.
  - `qwen`: `READY FOR IMPLEMENTATION` with one accepted P3 wording note about
    trace field inventory.
- Implementation plan review round 4, 2026-06-25:
  - `glm`: `READY FOR IMPLEMENTATION` with P3-only clarity notes.
  - `minimax`: `READY FOR IMPLEMENTATION` with P3-only clarity notes.
  - `kimi`: `READY FOR IMPLEMENTATION` with P3-only observations.
  - `mimo`: technical failure on the first attempt, then
    `READY FOR IMPLEMENTATION` on retry with P3-only observations.
  - `deepseek`: `READY FOR IMPLEMENTATION` with P3-only observations.
  - `qwen`: `READY FOR IMPLEMENTATION` with P3-only observations.
- Implementation review round 1, 2026-06-25:
  - `glm`: `NEEDS WORK`; accepted findings covered missing Turn View semantic
    rendering/tests, missing Span detail op/proof rendering, and stale
    canonical payload-kind taxonomy comments/tests.
  - `minimax`: technical timeout before a final vote; not counted as
    convergence.
  - `kimi`: `PRODUCTION GRADE` with P3-only observations.
  - `mimo`: `NEEDS WORK`; accepted findings covered missing session
    `error_message`, missing trace proof/include frontend support, missing trace
    proof happy-path tests, and TypeScript contract strictness.
  - `deepseek`: returned a contradictory positive vote with P2 findings; the
    accepted findings were stale canonical payload-kind taxonomy comments/tests.
  - `qwen`: technical failure/no final vote; not counted as convergence.
- Implementation review round-1 fixes before retry:
  - Added session `error_message` to presenter detail DTO/query, TypeScript
    contract, overview diagnostics, and backend/frontend tests.
  - Added trace frontend `include=payload_refs,proof` support and trace proof
    tests.
  - Added Span detail rendering for op diagnostics and explicit proof metadata,
    including masked default selector display and copyable full selector on
    demand.
  - Reworked Turn View payload selection to use normalized `artifact_class`
    semantics instead of array position, including SDK badge rendering and
    canonical fixture migration.
  - Updated canonical payload-kind taxonomy comments/property tests for
    persisted aliases and normalized artifact classes.
  - Added list/compare provider tests, subscription rejection tests for
    unsupported fields, and contract-matrix updates for the new exposed fields.
  - Re-ran focused checks and the full local aggregate gate; all local gates are
    green before implementation review round 2.
- Implementation review round 2, 2026-06-25:
  - `glm`: `NEEDS WORK`; accepted findings covered the proof/debug UI being
    unit-tested but not reachable in live UI, missing field-matrix rows for
    topology/timeline/search/related/SSE/parse-error, and duplicate TypeScript
    fallback payload-kind mapping.
  - `minimax`: `NEEDS WORK`; accepted findings covered the field-matrix
    coverage gap, `/api/sources` hand-rolled include parsing, and the
    contract-matrix helper skipping evidence for real `tokens_cache_read` and
    `tokens_cache_write` fields.
  - `kimi`: technical timeout before a final vote; not counted as convergence.
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `deepseek`: `PRODUCTION GRADE` with P3-only observations.
  - `qwen`: `PRODUCTION GRADE` with P3-only observations.
- Implementation review round-2 fixes before retry:
  - Added live proof loading from the Span detail drawer through the dedicated
    `/api/sessions/:id/payload_refs?op=:op&include=proof` path, keeping default
    trace/session responses slim and adding frontend client/component tests.
  - Added field-matrix rows for `/api/sources` cursor include, topology,
    timeline, related sessions, search content, SSE session-change payloads, and
    parse-error surfaces. The matrix now verifies 51 rows.
  - Migrated `/api/sources` to the shared include parser with a source-handler
    test proving unknown include tokens return structured `BAD_REQUEST`.
  - Tightened the contract-matrix helper so token-cache fields are validated as
    real fields, and added a self-test that would fail if
    `tokens_cache_read` drift became invisible again.
  - Removed the frontend payload-kind fallback mapper from Turn View; UI
    dispatch now trusts the backend-derived `artifact_class`.
  - Re-ran focused checks and the full local aggregate gate after these fixes;
    all local gates are green before implementation review round 3.
- Implementation review round 3, 2026-06-25:
  - `glm`: `NEEDS WORK`; accepted one P2 privacy/policy finding that the
    Overview tab masked `cwd` text but still exposed the full path through the
    native HTML `title` tooltip outside an explicit proof/debug surface.
  - `minimax`: technical malformed output; the process exited successfully but
    returned analysis only and no final vote.
  - `kimi`: `PRODUCTION GRADE` with P3-only observations.
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `deepseek`: `PRODUCTION GRADE` with P3-only observations.
  - `qwen`: `PRODUCTION GRADE` with P3-only observations.
- Implementation review round-3 fix before retry:
  - Removed the full `cwd` tooltip from the session Overview diagnostics row.
    The primary UI now shows only the masked path and does not expose the full
    path via `title`; full selectors/paths remain limited to explicit
    proof/debug affordances.
  - Updated the Overview tab test to assert the full fixture path is not rendered
    and not present as a tooltip attribute.
  - Re-ran the focused Overview tab test; it passed.
- Implementation review round 4, 2026-06-25:
  - `glm`: `NEEDS WORK`; accepted P2 findings covered proof-field negative
    tests for default/detail/trace/dedicated payload-ref surfaces, turn-level
    `error_class` round-trip assertion, runtime test coverage for the
    persisted-kind to `artifact_class` helper, reasoning-region semantics, and
    non-OK coverage for the payload byte-streaming client's error parser.
  - `minimax`: `PRODUCTION GRADE` with P3-only observations.
  - `kimi`: `PRODUCTION GRADE`; the reviewer ran a long local gate attempt and
    hit an internal benchmark timeout, but returned a final positive vote after
    checking the SOW scope and focused tests.
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `deepseek`: `PRODUCTION GRADE` with P3-only observations.
  - `qwen`: `PRODUCTION GRADE` with P3-only observations.
- Implementation review round-4 fixes before retry:
  - Added negative backend tests proving proof-only fields remain omitted from
    session detail `?include=payload_refs`, trace `?include=payload_refs`, and
    dedicated `/payload_refs?include=payload_refs` responses.
  - Added a turn-level `error_class` assertion for the session-detail response.
  - Added a table-driven presenter test for all persisted payload-ref kind to
    `artifact_class` mappings, including aiagent_v3 SDK aliases and reasoning
    aliases.
  - Changed the Turn View reasoning payload region label from `Prose` to
    `Reasoning` and strengthened the test to assert the semantic region after
    payload content loads.
  - Added payload byte-streaming client tests for structured error envelopes and
    plain-text fallback failures.
  - Re-ran focused checks: `go test ./internal/presenter`, frontend payload
    client + Turn View Vitest, frontend typecheck, frontend lint, and
    `git diff --check`; all passed before implementation review round 5.
- Implementation review round 5, 2026-06-25:
  - `glm`: `PRODUCTION GRADE` with P3-only observations.
  - `minimax`: `PRODUCTION GRADE` with P3-only observations.
  - `kimi`: technical non-vote. First attempt returned a status summary without
    a final vote; retry used the same broad scope plus a malformed-output note
    and exited without a vote after extended inspection. No P0/P1/P2 finding was
    produced in either output.
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `deepseek`: `PRODUCTION GRADE` with P3-only observations.
  - `qwen`: `PRODUCTION GRADE` with P3-only observations.
- Round-5 reviewer disposition:
  - Accepted outcome is 5/6 valid positive votes plus one technical reviewer
    failure. This round did not converge and was superseded by later review
    rounds; round 8 has the current implementation-review disposition.
- Post-round-5 local gate status:
  - Re-ran `scripts/gates.sh` after the round-4 fixes. All gates before the
    benchmark step passed: lint/static/security, frontend typecheck/build,
    secret scan, contract-matrix self-test, spec drift, ingestion parity,
    installer/systemd, and build/bundle-size.
  - The aggregate gate failed at `scripts/check-bench.sh` on adapter benchmark
    `sec/op` regressions. Repeated runs have varied in which adapter benchmarks
    cross the threshold.
  - SOW-0105 does not touch `internal/adapters/`, `internal/ingest/`,
    `internal/parity/`, or `bench/`, so this is recorded as a working-tree gate
    blocker outside the SOW-0105 implementation surface, not as evidence against
    the UI/DB contract implementation.
- Implementation review round 6, 2026-06-25:
  - `glm`: `PRODUCTION GRADE` with P3-only observations.
  - `minimax`: `PRODUCTION GRADE` with P3-only observations.
  - `kimi`: `NEEDS WORK`; accepted P2 finding that TypeScript marked required
    backend fields as optional (`TurnDetail.tokens_cache_read/write`,
    `OpDetail.bytes_in/out`) and accepted P3 finding that Turn View fixtures
    inferred `artifact_class` from `kind` in test code.
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `deepseek`: `PRODUCTION GRADE` with a P3 observation that op-level
    cache-token fields were omitted from the detail contract. This was upgraded
    to an accepted blocking SOW-0105 gap because the implementation plan already
    required op cache-token exposure.
  - `qwen`: `NEEDS WORK`; rejected the `location_uri` empty-string finding as a
    false positive. The DB schema and data-model spec define
    `payload_refs.location_uri` as `TEXT NOT NULL`, so the presenter scanner does
    not need nullable handling for valid rows.
- Implementation review round-6 fixes before retry:
  - Exposed `ops.tokens_cache_read` and `ops.tokens_cache_write` through
    `/api/sessions/:id` op details, TypeScript `OpDetail`, Span detail metrics,
    REST/UI specs, and the contract matrix.
  - Made TypeScript `OpDetail.bytes_in/out` and
    `TurnDetail.tokens_cache_read/write` required to match the Go presenter JSON
    contract.
  - Removed test-only artifact-class inference from Turn View and Span detail
    payload fixture helpers; fixtures now state both raw `kind` and normalized
    `artifact_class` explicitly.
  - Re-ran focused checks: `go test ./internal/presenter`,
    `bash scripts/check-contract-matrix.sh`, targeted frontend Vitest for API
    types, Span detail, and Turn View, `npm --prefix frontend run typecheck`,
    `npm --prefix frontend run lint`, and `git diff --check`; all passed.
- Implementation review round 7, 2026-06-25:
  - `glm`: `PRODUCTION GRADE` with P3-only observations.
  - `minimax`: `NEEDS WORK`; accepted P1 finding that
    `scripts/test/check-contract-matrix-test.sh` no longer planted the intended
    `TurnDetail.tokens_cache_read` TypeScript drift because the test still
    searched for the old optional syntax (`tokens_cache_read?: number;`) after
    the field became required.
  - `kimi`: `PRODUCTION GRADE` with P3-only observations.
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `deepseek`: `PRODUCTION GRADE` with P3-only observations.
  - `qwen`: `PRODUCTION GRADE` with P3-only observations.
- Implementation review round-7 fix before retry:
  - Updated the contract-matrix self-test planted mutation to target
    `tokens_cache_read: number;` specifically inside the `TurnDetail` interface,
    so the self-test proves token-cache field evidence remains fail-closed after
    the TypeScript contract became required.
  - Re-ran focused checks: `bash scripts/test/check-contract-matrix-test.sh`
    passed 6/6 cases, `bash scripts/check-contract-matrix.sh` passed with 53
    rows, `bash scripts/spec-drift.sh` passed across all six indicators, and
    `git diff --check` passed.
- Implementation review round 8, 2026-06-25:
  - `glm`: `PRODUCTION GRADE` with P3-only observations.
  - `minimax`: technical timeout on the first attempt while running the
    benchmark gate; retry returned an explicit `PRODUCTION GRADE` with P3-only
    observations, but did not put the vote on the first line as requested.
  - `kimi`: `PRODUCTION GRADE` with P3-only observations.
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `deepseek`: `PRODUCTION GRADE` with P3-only observations.
  - `qwen`: `PRODUCTION GRADE` with P3-only observations.
- Round-8 reviewer disposition:
  - Implementation review is converged for SOW-0105: 6/6 positive reviewer
    votes after the `minimax` technical-timeout retry, with the `minimax` retry
    carrying a vote-format deviation but no blocking finding.
  - Reviewers agreed the remaining aggregate gate blocker is not caused by
    SOW-0105. Local benchmark runs have produced noisy adapter benchmark
    regressions while the SOW-0105 diff does not touch `internal/adapters/`,
    `internal/ingest/`, `internal/parity/`, or `bench/`.
- Accepted P1/P2 finding classes folded into this SOW revision:
  - Scope default-deny `extras_json` policy to session/turn/op extras while
    preserving the documented default source/health metadata and log extras
    contracts.
  - Reframe the stale `frontend/src/api/payloads.ts` issue as a missing typed
    payload byte-streaming client/route contract, not just a stale comment.
  - Reconcile opt-in heavy-field delivery (`?include=...`) before adding proof or
    proof metadata to any response.
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
  - Add trace, compare, stats, search, topology, timeline, related, logs,
    subscription/SSE, and parse-error contract coverage.
  - Add reingest classification, privacy masking rules, and include-token grammar.
  - Add a single authoritative payload-kind taxonomy source before rewriting
    frontend tests.
  - Capture the server-side payload route contract, including caps, headers,
    HEAD behavior, JSON-aware truncation, gzip handling, and source-root safety.
  - Record which candidate fields are not indexed and therefore cannot become
    filters/groups without query-cost or schema work.
  - Keep SSE/subscription events as id/source invalidation plus REST refetch by
    default; do not add new filter dimensions by TypeScript-only changes.
  - Treat source/health metadata and log extras as existing API contracts that
    need TypeScript/UI alignment and safe presentation, not narrowing.
  - Define `testdata/contracts/field-matrix.yaml` and
    `scripts/check-contract-matrix.sh` as the concrete drift-gate artifacts.
  - Add child-session tree summaries to the matrix and resolve the stale
    `ChildSummary.error_class` TypeScript/backend mismatch.
  - Add concrete `artifact_class` values and a persisted-kind to artifact-class
    mapping while preserving raw `payload_refs.kind`.
  - Define a gate-resolution workflow for contract matrix mismatches.
  - Reorder the detailed plan so failing tests precede runtime code.
  - Add Turn View fixture migration rules for invented payload-ref kinds.
  - Preserve the existing `reasoning_text` parity artifact class instead of
    introducing a conflicting `reasoning` artifact class.
  - Correct the trace field inventory to match the current slim trace DTO and
    avoid adding model/provider/token/cost/context fields to trace in this SOW.
  - Keep payload-ref artifact-class mapping in the backend presenter helper and
    avoid a duplicate TypeScript fallback mapper.
  - Implement the contract-matrix checker as a repo-local Go helper behind
    `scripts/check-contract-matrix.sh`, using Go AST extraction and a restricted
    TypeScript interface parser.
  - Verify non-JSON contracts such as payload-streaming headers through
    `test_refs`, not Go JSON tag extraction.
  - Define the primary UI path/URI masking algorithm before implementing proof
    surfaces.
  - Add explicit subscription filter rejection tests for `cwd`,
    `provider_alias`, `call_path`, and `error_class`.
  - Map the current generic raw JSON frontend fixture to `log`; future raw
    fixtures must be semantic or internal-only in the matrix.
  - Place the shared backend include parser in `internal/presenter/include.go`.
- P3 implementation-plan notes documented and accepted:
  - Matrix rows start from current-state values and flip to `exposed` or
    `exposed-via-include` in the same implementation slice that adds matching
    Go/TypeScript evidence.
  - Payload-ref `artifact_class` helper must be applied at inline detail, trace,
    and dedicated payload-ref construction sites.
  - `effective_status` remains an already-exposed session list/detail field and
    must not be duplicated or weakened.
  - Trace omissions are explicitly recorded with a performance/detail-endpoint
    reason in the matrix.
  - The shared frontend include-token builder lives in `frontend/src/api/client.ts`.
  - `scripts/spec-drift.sh` comments and self-tests must move from five to six
    indicators when the contract-matrix gate is wired.
  - SOW-0096 remains current as the parent ingestion-accuracy audit; SOW-0105 is
    the downstream UI/API contract slice and does not close the broader program.
- Reviewer lineage consensus:
  - SOW-0098 does not exist.
  - SOW-0099 through SOW-0102 are the direct adapter-remediation backlog tied to
    the SOW-0096/SOW-0097 ingestion-accuracy program.
  - SOW-0103 was downstream UI surfacing work and is now closed as superseded by
    this SOW.
  - SOW-0104 is an operational restart defect found during SOW-0097 install, not
    parity remediation.
  - SOW-0105 is downstream DB/API/UI contract debt from the same ingestion/parity
    program, not an adapter source-extractor SOW.
- Reviewer disagreement to resolve:
  - One reviewer argued SOW-0097 itself should be reopened because open adapter
    remediation means the parity goal is incomplete.
  - The other reviewers and local file evidence support a narrower conclusion:
    SOW-0097 is complete as a parity-gate framework SOW, but the parent
    ingestion-accuracy/parity program remains open through SOW-0096 and the
    remaining SOW-0104/SOW-0105 work. Completed SOW-0099 through SOW-0103 are
    historical follow-ups, not proof that the broader program is closed.
- Local conclusion for planning:
  - Do not reopen the exact SOW-0097 file unless a regression is found in the
    parity-gate framework it delivered.
- Do not claim the broader ingestion/parity program is finished while SOW-0104,
  SOW-0105 lifecycle bookkeeping, or SOW-0106 remain open.
  - Treat SOW-0104 separately: it is operational install/restart debt found
    during SOW-0097 closure, not adapter parity debt.
- Implementation review round 9, 2026-06-25, user-requested re-review:
  - `mimo`: `PRODUCTION GRADE` with P3-only observations.
  - `kimi`: `PRODUCTION GRADE` with P3-only observations.
  - `minimax`: `NEEDS WORK`. Rejected its `TraceOpFields` finding as a false
    positive because `frontend/src/viz/trace.ts` exports the interface and
    `SpanDetailDrawer` imports it intentionally as a viz structural type.
    Accepted its P2 source metadata finding: `SourceItem.meta` was typed but the
    Sources page had no safe metadata handling despite the SOW/matrix naming a
    UI surface. The `health.sources[].meta` half was corrected to API-only
    because no dedicated Health UI surface exists in this SOW.
  - `glm`: `NEEDS WORK`. Accepted P2 findings for stale matrix TypeScript names,
    a contract-matrix gate gap for non-`api/types.ts` contract interfaces,
    missing payload-client abort/no-retry tests, stale REST examples for
    `error_class`, and incomplete contract-matrix self-test cases.
  - `deepseek`: technical failure/no usable final vote captured.
  - `qwen`: technical failure/no usable final vote captured.
  - Failed reviewers were not retried before fixes because accepted P2 findings
    existed; the whole implementation review gate must rerun after fixes.
- Implementation review round-9 fixes before retry:
  - Replaced stale matrix type names with real exported TypeScript contracts:
    `AggregateResponse`, `TopResponse`, `SubscriptionFilterRequest`, and
    `LogItem`.
  - Extended the contract-matrix checker to validate TypeScript contract type
    existence across `frontend/src/api/types.ts`,
    `frontend/src/api/payloads.ts`, and `frontend/src/viz/trace.ts`, so
    `PayloadContent` and `TraceOpFields` are verified instead of invisible to
    the gate.
  - Expanded the contract-matrix self-test from 6 to 13 cases, adding missing
    matrix, empty matrix, duplicate row, missing required key, missing include
    token, missing internal reason, and unknown TypeScript contract type cases.
  - Updated the spec-drift hermetic fixture builder to copy all
    contract-bearing TypeScript files used by the contract-matrix checker.
  - Added payload byte-streaming client tests for `AbortSignal` propagation,
    abort failure propagation, and no automatic retry on HTTP/abort failures.
  - Added shared include-token parser tests for empty input, comma composition,
    whitespace, duplicate collapse, endpoint allowlists, unknown tokens, empty
    tokens, and proof-without-payload-ref validation.
  - Updated REST examples so session-list `error_class` matches the backend's
    empty-string default and completed turn examples omit nullable
    `error_class` when absent.
  - Added a Sources table metadata summary that shows only a key count and never
    raw metadata values; health source metadata remains typed API-only in this
    SOW.
  - Re-ran focused checks: `bash scripts/test/check-contract-matrix-test.sh`
    passed 13/13 cases, `bash scripts/check-contract-matrix.sh` passed with 53
    rows, `bash scripts/spec-drift.sh` passed across all six indicators,
    `bash scripts/test/spec-drift-test.sh` passed 29/29 cases,
    `go test ./internal/presenter` passed, targeted frontend Vitest for
    `payloads.test.ts` and `Sources.test.tsx` passed 21/21 tests,
    `npm --prefix frontend run typecheck` passed, and `git diff --check`
    passed.
- Implementation review round 10, 2026-06-25, rerun after round-9 fixes:
  - `glm`: `PRODUCTION GRADE`. P3 only: plan-vs-code filename drift for the
    shared include parser. Accepted and fixed in this SOW update by naming the
    implemented `internal/presenter/include.go` file.
  - `mimo`: `PRODUCTION GRADE`. P3 only: local `PayloadContent` naming
    collision in `payloadStore.ts`, and the already-tracked benchmark gate
    blocker under SOW-0106. Documented; no SOW-0105 code change required.
  - `deepseek`: `PRODUCTION GRADE`. No blocking findings. It also noticed the
    same include-parser filename drift, fixed in this SOW update.
  - `kimi`: `NEEDS WORK`. Accepted the stale status-line finding and fixed it
    in this SOW update. Rejected the claim that the round-10 reviewer rerun had
    not happened as a temporal false positive: the reviewer was participating
    in that rerun before the SOW could record it. Its P3 `PayloadContent` naming
    collision matches `mimo` and is documented.
  - `minimax`: technical failure/no usable final vote captured.
  - `qwen`: technical failure/no usable final vote captured.
  - Disposition: rerun the full implementation review gate once more after this
    SOW bookkeeping fix, with the same broad SOW-0105 scope plus these fix
    notes.
- Implementation review round 11, 2026-06-25, rerun after round-10 SOW
  bookkeeping fixes:
  - `glm`: `PRODUCTION GRADE`. P3-only observations: local
    `PayloadContent` naming collision, session/turn/op `extras_json` policy is
    prose-enforced rather than separate machine-matrix rows, transient ingest
    E2E flake under full-suite race load, and benchmark blocker tracked under
    SOW-0106. Accepted as non-blocking; the stale SOW-0104 wording observation
    was fixed by changing "pending" to "active".
  - `kimi`: `PRODUCTION GRADE`. No P0/P1/P2 findings; P3-only
    `PayloadContent` naming collision and benchmark blocker notes.
  - `qwen`: `PRODUCTION GRADE`. No P0/P1/P2 findings; P3-only
    `PayloadContent` naming collision and benchmark blocker notes.
  - `mimo`: initial round-11 session ended without a usable final output;
    retry voted `PRODUCTION GRADE`. It verified SOW lineage, the 53-row
    contract matrix, include/proof helpers, DTO/type alignment, source metadata
    handling, and SOW-0104 active/current wording.
  - `deepseek`: initial round-11 session ended without a usable final output;
    retry voted `PRODUCTION GRADE`. It ran and verified
    `scripts/check-contract-matrix.sh`, `scripts/test/check-contract-matrix-test.sh`,
    `scripts/spec-drift.sh`, `scripts/scan-secrets.sh`, `scripts/lint.sh`,
    `go build ./...`, focused presenter/canonical race tests, frontend Vitest,
    frontend typecheck, and frontend lint.
  - `minimax`: technical non-result after retry. It timed out under the
    required 30-minute reviewer timeout after running extensive read-only
    verification, including `go test -race -count=1 ./...`,
    `bash scripts/test.sh`, `bash scripts/check-coverage.sh`,
    `bash scripts/lint.sh`, `bash scripts/spec-drift.sh`,
    `bash scripts/test/spec-drift-test.sh`, and `bash scripts/scan-secrets.sh`.
    Per the reviewer protocol, this failed reviewer is skipped for the current
    implementation gate after one retry because all usable reviewer responses
    are positive or P3-only.
  - Disposition: implementation review gate converged. Remaining P3 items are
    documented; no SOW-0105 P0/P1/P2 remains.
- Implementation review round 12, 2026-06-25, operator-requested re-review of
  SOW-0105 and its SOW-0097 lineage:
  - `glm`: `PRODUCTION GRADE`. Re-ran the contract matrix, spec drift, focused
    presenter/canonical race tests, frontend Vitest, coverage, lint, typecheck,
    and secrets scan. P3-only observations: local `PayloadContent` naming
    collision, `fetchOpPayloadRefs` / `fetchTurnPayloadRefs` query-string style,
    prose-enforced `extras_json` policy, and benchmark blocker tracked under
    SOW-0106.
  - `minimax`: `PRODUCTION GRADE`. Re-ran `scripts/check-contract-matrix.sh`,
    `scripts/test/check-contract-matrix-test.sh`, `scripts/spec-drift.sh`,
    focused presenter/canonical race tests, and frontend tests. P3-only
    observations matched the naming/prose-policy notes.
  - `kimi`: `PRODUCTION GRADE`. Re-ran `go test ./... -race -count=1`,
    `scripts/check-ingestion-parity.sh --fixtures`,
    `scripts/test/spec-drift-test.sh`, and `scripts/lint.sh`. No P0/P1/P2
    findings.
  - `mimo`: `PRODUCTION GRADE`. Verified backend/frontend contracts, focused
    tests, contract matrix, and SOW lineage. P3-only observations matched the
    naming/prose-policy notes.
  - `deepseek`: first attempt ended before a usable final vote; the retry voted
    `PRODUCTION GRADE` after reviewing the SOW and diff, running focused
    presenter tests and frontend typecheck, and verifying the contract-matrix and
    spec-drift surfaces. No P0/P1/P2 findings.
  - `qwen`: first attempt ended before a usable final vote; the retry voted
    `PRODUCTION GRADE` after reviewing the SOW, contract matrix, backend
    presenter DTO/query alignment, frontend types, proof masking, tests, and
    SOW-0097 lineage. No P0/P1/P2 findings.
  - Lineage disposition: SOW-0099 through SOW-0102 are direct adapter-parity
    follow-ups, SOW-0103 is superseded by SOW-0105, SOW-0104 is separate restart
    debt found during SOW-0097 validation, SOW-0105 is downstream DB/API/UI
    contract debt, and SOW-0106 is benchmark-gate debt. The exact SOW-0097 file
    stays completed as a parity-gate framework unless that framework regresses,
    but the broader ingestion/parity program must not be reported finished while
    SOW-0104 or SOW-0106 remain open.
  - Disposition: implementation review gate converged with 6/6 usable positive
    votes. Remaining P3 items are documented and do not block SOW-0105.

Same-failure scan:

- Initial scan found no frontend hits for key DB fields such as `provider_alias`, `call_path`, `reasoning_kind`, `bytes_in`, `bytes_out`, `chars_in`, `chars_out`, `sha256`, `location_uri`, and `tool_namespace`.

Sensitive data gate:

- Durable content uses repo-relative file paths, schema field names, and aggregate counts only. It does not include raw prompts, payload bodies, credentials, private endpoints, or workstation-private paths.

Artifact maintenance gate:

- AGENTS.md: not updated; no new top-level rule has been added yet.
- Runtime project skills: `.agents/skills/project-quality-gates/SKILL.md` now
  lists `scripts/check-contract-matrix.sh` and its self-test as quality gates.
- Specs: updated for the REST/API, UI, frontend architecture, data model,
  security, testing strategy, quality-gate, SSE, presenter, architecture, and
  canonical-event contracts touched by this SOW.
- End-user/operator docs: not updated; no user-visible behavior changed.
- End-user/operator skills: not updated; no operator workflow changed.
- SOW lifecycle: moved from `.agents/sow/pending/` to `.agents/sow/current/`
  after the gap-analysis gate converged; SOW-0103 moved to `.agents/sow/done/`
  as superseded by SOW-0105.

Specs update:

- Complete for SOW-0105: REST/API, UI, frontend architecture, data model,
  security, testing strategy, quality-gate, SSE, presenter, architecture, and
  canonical-event specs were updated with the contract-matrix and include-token
  behavior.

Project skills update:

- None for SOW creation.

End-user/operator docs update:

- None for SOW creation.

End-user/operator skills update:

- None for SOW creation.

Lessons:

- The UI/data contract needs an explicit field-intent matrix because DB schema, REST examples, Go DTOs, TypeScript types, component assumptions, and tests can drift independently.

Follow-up mapping:

- SOW-0103 is superseded by SOW-0105. Its remaining useful work is absorbed into
  this SOW's contract matrix, and its provisional `user_input` / `assistant`
  op-kind assumption is rejected as stale.

## Outcome

Implementation review is complete and converged for SOW-0105. Lifecycle closure
is pending final commit/move bookkeeping and the broader SOW-0097 lineage must
remain open until SOW-0104 and SOW-0106 are resolved.

## Lessons Extracted

- DB/API/UI contract drift needs a machine-readable matrix, not prose-only
  review. SOW-0105 adds that gate and wires it into spec drift.
- SOW-0097 can be complete as a parity-gate framework while the broader
  ingestion/parity program remains open through separately tracked lineage debt.

## Followup

P3-only raw-status UI consistency evidence from the post-regression review is
recorded in the dated regression entry below. It does not block SOW-0105
closure, but it should be considered before future status-surface UI work.

## Implementation Review - 2026-06-26

Reviewer: CTO second-opinion implementation review (self-verification after round-12 convergence).

### Verification performed

- `go test ./internal/presenter -race -count=1 -timeout 120s` → PASS (all presenter tests).
- Frontend Vitest (`npm test`) → 76 files passed, 928 tests passed.
- `scripts/check-contract-matrix.sh` → PASS (53 rows verified against Go JSON tags, TypeScript interfaces, include-token policy, artifact-class mapping, and test-ref existence).
- `scripts/test/check-contract-matrix-test.sh` → 13 passed, 0 failed.
- `scripts/spec-drift.sh` → PASS (6 indicators green: REST, SSE, data-model, canonical, adapter-probes, contract-matrix).
- `scripts/test/spec-drift-test.sh` → 29 passed, 0 failed.
- Verified subscription filter rejection tests exist for `cwd`, `provider_alias`, `call_path`, `error_class` in `internal/presenter/subscription_filter_test.go`.
- Read `frontend/src/components/TurnView/payloadStore.ts` to confirm `PayloadContent` naming collision.

### Findings

No P0 or P1 findings. Two P3 observations remain; both are documented as non-blocking.

1. **P3**: `PayloadContent` interface naming collision.
   - `frontend/src/api/payloads.ts` defines `PayloadContent` (typed client for `GET/HEAD /api/payloads/:id`).
   - `frontend/src/components/TurnView/payloadStore.ts` also defines `PayloadContent` with a compatible but slightly different shape (no `headers` nesting, no `previewBytes`).
   - The two types are not unified. This is a minor maintenance friction. No runtime defect because the shapes are compatible at the call sites. Recommendation: unify into a single `PayloadContent` type in `frontend/src/api/payloads.ts` and import it from `payloadStore.ts` in a follow-up SOW.

2. **P3**: Consider masking `call_path` in `OverviewTab` primary UI.
   - `OverviewTab.tsx` renders `call_path` as-is (`rootA>childA`).
   - The SOW-0105 privacy policy mandates masking for `cwd` and `location_uri` but does not explicitly extend masking to `call_path`.
   - `call_path` is classified as a detail/debug field in the matrix, but it can contain path-like strings that may reveal local directory structure.
   - Recommendation: apply the same `maskPath` algorithm to `call_path` in primary UI, or explicitly document the decision not to mask in `security.md` and `data-model.md`.

### Verdict

`PRODUCTION GRADE`. All automated gates pass. No blocking findings. The two P3 items are tracked above and do not prevent SOW closure.

## External Review Follow-Up - 2026-06-26

Reviewer gate: implementation review rerun after the accepted `PayloadRef.op_id`
contract finding was fixed.

Fixes reviewed:

- `PayloadRef.op_id` is required in `frontend/src/api/types.ts`, locked by a
  compile-time assertion in `frontend/src/api/types.contract.test.ts`, and
  recorded as an exposed row in `testdata/contracts/field-matrix.yaml`.
- `SessionListItem.effective_status`, `SessionListItem.error_class`,
  `SessionListItem.last_activity_ts`, `SessionDetail.effective_status`, and
  `SessionDetail.last_activity_ts` are required in TypeScript where the Go
  presenter always emits the JSON key.
- `internal/presenter/payloads_test.go` covers plain preview truncation, gzip
  decompression, JSON boundary truncation, and source-root containment rejection.

Validation evidence:

- `go test ./internal/presenter -run 'TestPayloadPreview|TestSessionDetail|TestTrace|TestPayloadRefs|TestParseIncludeOptions|TestRequireProofPayloadRefs' -count=1`
  passed.
- `go test ./internal/presenter -count=1` passed.
- `npm --prefix frontend run typecheck` passed.
- Focused frontend Vitest contract/component/page suite passed: 9 files, 101
  tests.
- `bash scripts/check-contract-matrix.sh` passed: 54 rows verified.
- `bash scripts/test/check-contract-matrix-test.sh` passed: 13 passed, 0 failed.
- `bash scripts/spec-drift.sh` passed: 6 indicators green.
- `bash scripts/test/spec-drift-test.sh` passed: 29 passed, 0 failed.
- `git diff --check` passed.

Reviewer votes:

- `glm`: `PRODUCTION GRADE`; P3-only row-count/log and follow-up notes.
- `minimax`: `PRODUCTION GRADE`; no P0/P1/P2 findings.
- `kimi`: `PRODUCTION GRADE`; P3-only optionality/style note.
- `mimo`: first retry session failed technically; same-scope retry voted
  `PRODUCTION GRADE`; no P0/P1/P2 findings.
- `deepseek`: `PRODUCTION GRADE`; P3-only privacy/style notes.
- `qwen`: `PRODUCTION GRADE`; P3-only notes.

Lineage disposition verified:

- No SOW-0098 file exists.
- SOW-0099 through SOW-0102 are completed adapter/parity follow-ups.
- SOW-0103 is closed as superseded by SOW-0105.
- SOW-0104 remains open and explicitly records SOW-0097 lineage restart debt.
- SOW-0106 remains open and explicitly records SOW-0097 cleanup/benchmark-gate
  lineage debt.
- The SOW-0097 parity-gate framework deliverable remains completed, but the
  broader SOW-0097 ingestion/parity lineage must not be reported finished until
  SOW-0104 and SOW-0106 are resolved.

Remaining P3 items:

- Historical validation log entries still mention 53 matrix rows; the current
  contract-matrix gate verifies 54 rows after the `payload_ref.op_id` row.
- `PayloadContent` has two compatible local TypeScript definitions.
- `call_path` masking remains a policy clarification/follow-up; `cwd` and
  `location_uri` masking are already covered.

Disposition: implementation review gate converged with 6/6 usable positive
votes. No P0/P1/P2 findings remain.

## Regression Log

See dated regression entries below.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.

## Regression - 2026-06-26

### Trigger

The operator requested another external implementation review of SOW-0105. The
review was run against a clean detached worktree at commit `6f30d66`.

### Finding

The committed SOW-0105 state was not production-grade because the frontend lint
gate failed:

- `npm --prefix frontend run lint` failed with 7
  `@typescript-eslint/no-unnecessary-condition` errors.
- Affected files:
  - `frontend/src/pages/Compare/Compare.tsx`
  - `frontend/src/pages/SessionDetail/UnifiedView/OverviewTiles.tsx`
  - `frontend/src/pages/SessionDetail/UnifiedView/UnifiedView.tsx`
  - `frontend/src/pages/SessionsList/SessionsList.tsx`

### Root Cause

SOW-0105 correctly made these frontend contract fields required:

- `SessionListItem.effective_status`
- `SessionDetail.effective_status`
- `PayloadRef.op_id`

Existing UI files still carried defensive fallbacks for the older optional
contract. Those files were outside the committed SOW-0105 diff, but the lint
failure is still SOW-0105 fallout because the new required TypeScript contract
made the stale fallbacks invalid.

### Correction Plan

- Remove stale `effective_status ?? status` fallbacks where `effective_status`
  is now guaranteed by the API contract.
- Remove the impossible `PayloadRef.op_id === undefined` guard where every
  returned payload ref now carries `op_id`.
- Re-run frontend lint, TypeScript, focused component/page tests, contract
  matrix gates, and diff whitespace checks.
- Re-run the same SOW-0105 implementation review gate after the correction.

### Validation

- `npm --prefix frontend run lint` passed.
- `npm --prefix frontend run typecheck` passed.
- `npm --prefix frontend test -- --run src/pages/Compare/Compare.test.tsx src/pages/SessionDetail/UnifiedView/UnifiedView.test.tsx src/pages/SessionsList/SessionsList.test.tsx`
  passed: 3 files, 43 tests.
- `bash scripts/check-contract-matrix.sh` passed: 54 rows verified.
- `bash scripts/test/check-contract-matrix-test.sh` passed: 13 passed, 0
  failed.
- `bash scripts/spec-drift.sh` passed: 6 indicators green.
- `git diff --check` passed.

### Review

- `glm`: `PRODUCTION GRADE`; P3-only notes for stale comments, redundant
  nullable-field fallback style, the earlier validation command naming a
  non-existent `OverviewTiles.test.tsx`, and the lesson that lint must run after
  DTO optionality changes. The stale comment and validation command have been
  corrected in this entry.
- `minimax`: `PRODUCTION GRADE`; P3-only notes for raw `status` checks in
  secondary UI decisions, the previously recorded `call_path` masking policy
  question, the local `PayloadContent` type duplication, and the validation
  command file-count mismatch.
- `kimi`: `PRODUCTION GRADE`; P3-only notes for raw `status` rendering on
  secondary status surfaces (`OverviewTab`, `SessionRow`, Agent/Tool/Model
  detail tables, and SessionsList status sort), missing explicit
  `status != effective_status` UI fixtures, and the previously recorded
  `call_path` masking policy question.
- `mimo`: `PRODUCTION GRADE`; P3-only notes for raw `status` error-badge checks,
  the previously recorded `call_path` masking policy question, the local
  `PayloadContent` type duplication, and historical matrix row-count wording.
- `deepseek`: `PRODUCTION GRADE`; P3-only note for `SessionRow` using raw
  `status` in a secondary/non-primary row component, plus previously recorded
  `PayloadContent` and `call_path` notes.
- `qwen`: `PRODUCTION GRADE`; P3-only note for `SessionRow` raw `status`
  rendering, plus previously recorded `PayloadContent` and `call_path` notes.

Reviewer-gate disposition: no P0/P1/P2 findings remain. The correction removes
the stale fallbacks that caused the frontend lint regression and does not change
runtime behavior for valid API responses. The broader raw-status UI consistency
notes are accepted as P3 follow-up evidence; they are outside this lint
correction and do not block SOW-0105 closure.
