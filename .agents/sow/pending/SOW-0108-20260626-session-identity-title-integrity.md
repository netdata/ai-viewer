# SOW-0108 - Session Identity And Title Integrity

## Status

Status: open

Sub-state: fit-for-purpose gap analysis drafted; external gap review rounds 1
through 28 completed and findings incorporated; gap-review rerun pending after
round-28 changes.

## Requirements

### Purpose

Make every session immediately recognizable before the user studies the heavy
views. The title, agent name, source/client, status, and compact stats must be
correct, useful, and vertically efficient on laptop screens.

### User Request

Data integrity issues on Session Detail:

- `ai-agent v3` shows `parent` instead of the real agent name such as `reddit`
  or `neda`.
- Some clients provide a session title.
- If no title is present, use the first 80 characters of the first user input.

### Assistant Understanding

Facts:

- The current top block spends 88px on breadcrumb/title/pin/id at 1366x768.
- The current stat ribbon spends another 83.5px.
- `agent_name` is the visible H1 fallback in `SessionDetail.tsx`.
- Existing ingestion work has introduced richer payload and first-user-message
  contracts, but this view is not yet fit for recognition.
- Current canonical/store/presenter contracts have no first-class session title
  field. Existing `first_user_message_hash` is irreversible and cannot produce
  an 80-character title.
- The schema has `first_user_message_hash`, but round-2 evidence found no
  ingest writer currently populating it. The title work must own the writer or
  explicitly document why the hash remains separate.
- Some adapters already store title-like values in `sessions.extras_json`.
  Opencode stores `session.title` as `extras_json.title`; claude-code stores
  `customTitle` and `aiTitle` in extras. The remaining privacy question is
  normalization/indexing into `display_title`, not whether title-like text has
  ever entered the database.
- The ai-agent v3 adapter currently maps `session_start.agentId` directly to
  canonical `agent_name`. The v3 spec documents `parent` and `spawn-parent` as
  possible literal agent module names, so the SOW must prove where any better
  human label comes from before changing display behavior.

Inferences:

- The title contract belongs in the canonical/presenter API, not only the
  frontend, so list/detail/search surfaces stay consistent.
- Agent identity and session title are separate concepts. An agent can be
  `reddit`; a session title can be the client title or first user request.
- If title fallback uses user input, it must use sanitized/stored content and
  avoid exposing raw secrets in logs/specs/tests.

Unknowns / required evidence:

- Exact ai-agent v3 field source for a better user-facing label must be
  verified in source ledgers and adapter code before implementation. Candidate
  fields include `agentId`, `callPath`, `headendId`, parent-side
  `childSessions[].agentId`, source location metadata, or adapter extras.
- Exact client title fields differ by source adapter and need per-source audit.
- The storage contract for titles/previews must be decided before implementation:
  a durable bounded display field is likely required for list/search/compare
  consistency.

### Acceptance Criteria

- Session Detail H1/title area shows:
  - session title when present,
  - otherwise first 80 display characters of the first user input,
  - with agent/client/source shown separately as metadata.
- ai-agent v3 sessions do not show a generic `parent` label when a better
  verified label is present in source data. If no better source field exists,
  the UI must show an honest non-misleading label from the verified metadata
  rather than inventing an agent name.
- The same session title appears consistently in Session List, Search,
  Compare, and Session Detail wherever a title is shown.
- The compact title/stats ribbon stays under 96px below the global app topbar at
  1366x768.
- Tests cover ai-agent v3 agent-name extraction, title fallback, missing title,
  missing user input, and redaction/sanitization behavior.
- The implementation plan names any schema migration, reingestion/backfill path,
  and API fields required for display titles.
- The display-title contract defines `display_title_source` enum values:
  `client_title`, `first_user_input`, and `session_id_fallback`. Agent/client
  labels are shown as metadata, never as title sources.
- The display-agent-label contract defines `display_agent_label_source` enum
  values: `agent_id`, `call_path`, `kind`, `headend_id`, `source_format`, and
  `session_id_fallback`.
- The per-adapter audit includes ai-agent v2, ai-agent v3, claude-code,
  opencode, and codex.
- Schema migration, ingest writer, and backfill/reingestion behavior are named
  before implementation. Adding fields without a production writer is not
  acceptable; `first_user_message_hash` already demonstrates that risk.

## Analysis

Sources to check during implementation:

- `internal/adapters/aiagent_v3`
- `internal/canonical`
- `internal/presenter`
- `internal/ingest/writer.go`
- `internal/store/migrations/0009_first_user_message_hash.sql`
- `frontend/src/api/types.ts`
- `frontend/src/pages/SessionDetail`
- `frontend/src/pages/SessionsList`
- `frontend/src/pages/Search`
- `.agents/sow/specs/canonical-events.md`
- `.agents/sow/specs/rest-api.md`
- `.agents/sow/specs/ui-pages.md`

Current state:

- Session identity is too weak: a generic `parent` is not useful for operator
  recognition.
- The title/id/pin area is vertically inefficient.
- There is no durable, cross-surface title contract described for Session
  Detail.
- Existing source specs already identify some title-like fields:
  - ai-agent v2: `sessionTitle`.
  - ai-agent v3: no verified title field in `session_start` yet.
  - claude-code: `customTitle` / `aiTitle` metadata is captured in extras/parity.
  - opencode: session `title` is already copied into `sessions.extras_json.title`
    by the adapter, despite the sensitive-content section of the opencode spec
    needing reconciliation.
  - codex: no verified title field yet.
- Existing `first_user_message_hash` is a hash, not display text. Round-2
  reviewer evidence also found no ingest writer currently populating it outside
  tests, so it is not usable as a display-title source.

Per-adapter title audit evidence to verify and keep current:

| Source | Current title-like evidence | Required outcome |
|---|---|---|
| ai-agent v2 | Spec and adapter evidence mention `sessionTitle`. | Map verified client title to `display_title` with `client_title` source. |
| ai-agent v3 | `session_start.agentId` maps to `agent_name`; spec documents literal `parent` / `spawn-parent`; no verified `session_start` title. Candidate context includes `callPath`, `headendId`, parent `childSessions[]`, and extras. | Do not invent labels. Use verified better label only when present; otherwise fall through honestly. |
| claude-code | Spec documents `custom-title` and `ai-title` snapshots in extras, with custom title winning. | Map title snapshots to `display_title` without treating them as agent identity. |
| opencode | Adapter currently stores source `title` into `sessions.extras_json.title`; the opencode spec also contains a sensitive-content statement that reads as if titles are not copied. | Reconcile spec drift, then decide whether to normalize/index this already-stored title as `display_title`. |
| codex | No verified session-title field found yet; adapter currently carries metadata such as originator/role/nickname. | Add explicit audit result and fallback behavior. |

External gap review round 1 findings incorporated:

- All reviewers voted `NEEDS WORK`.
- Reviewers identified two blocking gaps:
  - There is no first-class title column/API field, so title fallback cannot be
    consistent across list/search/compare/detail without a data contract.
  - The `parent` display issue was not proven. The v3 producer can write
    `agentId=parent`, so implementation must first verify where a better
    display identity exists, or explicitly define a non-misleading fallback.
- Reviewers also required a per-adapter title audit, a first-user-input
  extraction path, reingestion/backfill planning, and sanitization rules.

External gap review round 2 findings incorporated:

- Reviewers found `first_user_message_hash` has schema/API presence but no
  current ingest writer, so this SOW must own first-user preview and hash writer
  behavior or keep the hash explicitly separate.
- Reviewers found codex was missing from the adapter audit. Codex is now a
  required audit row with no assumed title field.
- Reviewers found the title privacy contract conflicts with the opencode spec's
  current "do not copy sensitive titles/text into ai-viewer DB" rule. The
  storage decision is now an explicit operator/risk decision.
- Reviewers found `display_title_source`, truncation, sanitization, fallback
  format, and cross-surface propagation were under-specified. They are now
  first-class requirements.

External gap review round 3 findings incorporated:

- Reviewers found the round-2 privacy framing was stale: opencode and
  claude-code already store title-like strings in `extras_json`. This SOW now
  frames `display_title` as normalization/indexing plus spec reconciliation,
  not as the first time titles enter the DB.
- Reviewers found v3 identity was over-deferred. `call_path` is already a stored
  column, and `headendId` already influences session `kind` while also living in
  extras. These fields must be audited as concrete candidates, not vague future
  possibilities.
- Reviewers required the `first_user_message_hash` writer decision to be closed.
  This SOW now owns implementing the writer unless a later SOW explicitly
  removes the column and related-session behavior.
- Reviewers required codex fallback, reingestion overwrite semantics, and more
  concrete sanitization fixtures.

Risks:

- Title fallback from user input can leak sensitive content if not bounded,
  redacted, and treated as display-only.
- Fixing only frontend display would leave search/list/detail inconsistent.
- Adapter-specific title extraction can drift if not covered by fixtures.

## Pre-Implementation Gate

Status: needs-review

Problem / root-cause model:

- Session recognition is currently driven by insufficient fields. The UI treats
  `agent_name` as the primary title, but the user's task requires both a correct
  agent identity and a human-readable session title.
- `agent_name` and display title are separate contracts. `agent_name` is the
  producer/adapter identity; display title is the human label for the session.
- A display title must be stored or derived through an explicit bounded path. It
  must not be computed by reading arbitrary payload bodies for every row in
  Session List, Search, or Compare.

Evidence reviewed:

- User reported ai-agent v3 `parent` display for sessions that should identify
  real agents.
- Headless UI measurements showed title/id/pin consumes 88px before stats.
- `SessionDetail.tsx` currently renders `session.agent_name` as the H1.
- `internal/adapters/aiagent_v3/mapper.go` maps `body.AgentID` to
  `AgentName`.
- `.agents/sow/specs/adapter-aiagent-v3.md` documents `parent` /
  `spawn-parent` as possible `agentId` values.
- `internal/presenter/session_detail.go` and frontend API types expose
  `agent_name` but no `title`.
- `internal/store/migrations/0009_first_user_message_hash.sql` adds only a
  hash, not display text.
- Repository search found no production ingest writer currently updating
  `sessions.first_user_message_hash`; this must be implemented, rejected, or
  documented as intentionally separate.
- `internal/adapters/opencode/mapper.go` stores `session.Title` in extras, and
  `internal/adapters/claude_code/ops_snapshot.go` stores `aiTitle` /
  `customTitle` in extras. Specs must be reconciled with this implementation
  before adding `display_title`.

Affected contracts and surfaces:

- Adapter extraction, canonical session fields, presenter response types,
  frontend API types, Session List, Search, Compare, Session Detail.

Existing patterns to reuse:

- Existing first-user-message hash/backfill work.
- Existing session list/detail presenter shape.
- Existing fixture/golden adapter tests.

Target data contract to decide before code:

- This SOW owns two related but separate UI outputs:
  - `display_title`: what the session is called, sourced from client titles or
    bounded first-user-input fallback.
  - `display_agent_label`: who/what ran the session, sourced from verified
    agent/source identity fields.
  - `display_client_label`: which client/headend/surface originated the
    session, sourced from verified source-system metadata. This field exists to
    satisfy the statistics requirement to group by client without forcing stats
    code to parse adapter-specific JSON ad hoc.
  These must not be conflated. The canonical `agent_name` field remains the raw
  source identity for parity unless an adapter has a verified source-native
  better value. The first implementation should avoid rewriting canonical
  `agent_name` just to hide ai-agent v3 literals such as `parent`; instead the
  presenter/UI exposes a sanitized display-agent label that falls back through
  `agent_name`, `call_path`, `kind`, `extras_json.headendId`, and source format
  according to rules proven in the implementation plan.
- Human-label verification is the first implementation-plan step for v3. If
  `headendId`, `callPath`, `kind`, and parent-side `childSessions[]` do not
  yield a verified human label such as the user's reported `reddit`/`neda`
  cases, the SOW must stop for an operator decision between an explicit mapping
  table and an honest generic fallback. Do not silently invent a label.
  The plan must point to the exact source field or sanitized fixture that
  contains those reported labels. If the labels are not present in source data,
  the honest outcome is an unavailable/generic label plus a recorded follow-up
  decision, not a guessed transformation from `parent`.
	  This is evidence-gated, not aspirational: before SOW-0108 plan review closes,
	  the plan must inspect a real or sanitized ai-agent v3 fixture that demonstrates
	  the reported `reddit`/`neda` labels or explicitly record that current source
	  data does not contain them.
	  The evidence fixture is committed or generated under
	  `testdata/fixtures/session-identity-v1/` before implementation-plan review
	  can close. It must cover literal `agentId="parent"`, literal
	  `agentId="spawn-parent"`, a purely numeric `agentId`, at least one root-client
	  `headendId` value such as `cli` when present, and a better human-label
	  candidate such as `reddit` or `neda` when the sanitized source data contains
	  it. If sanitized source data lacks `reddit`/`neda`, the fixture manifest records
	  that absence explicitly and the SOW uses the proof-failure fallback below
	  instead of inventing a label.
  Proof-failure fallback is explicit:

  | If the audit finds... | `display_agent_label` shows... | Source enum value |
  |---|---|---|
  | `headendId` is verified as a useful human label | sanitized `headendId` | `headend_id` |
  | `callPath` contains a useful agent segment | the sanitized least-misleading segment | `call_path` |
  | only normalized session role is useful | normalized `kind` label | `kind` |
  | no useful source identity exists | `Session <short-id>` | `session_id_fallback` |

  If none of these satisfy the user's reported `reddit`/`neda` cases, the plan
  records that the source data lacks those labels and asks for a product decision
  only between an explicit mapping table and the honest fallback above.
  The `headend_id` source is adapter-specific until proven otherwise. For
  ai-agent v3, `headendId` values `sub-agent`, `tool_output`,
  `history_compaction`, `parent`, `spawn-parent`, `default`, and `root` are not
  useful human agent labels and must fall through to `call_path`, `kind`, or
  `session_id_fallback` unless fixture evidence proves a different source-system
  meaning. Tests must assert these generic values do not produce
  `display_agent_label_source=headend_id`.
  Current evidence before fixture audit:

  | Evidence | Finding | Impact |
  |---|---|---|
  | `.agents/sow/specs/adapter-aiagent-v3.md` `session_start` fields | `agentId` can literally be `parent` / `spawn-parent`; `callPath` is a colon-separated hint; `headendId` is an entry-point enum, not a guaranteed human agent name. | The reported `reddit` / `neda` labels are not proven by the spec alone. |
  | `internal/adapters/aiagent_v3/mapper.go` `mapSessionStart` | Stores `headendId` and `callPath` in extras and maps canonical `AgentName` from `body.AgentID`. | Current UI can show raw `parent`; implementation must add display-label derivation. |
  | `internal/adapters/aiagent_v3/mapper.go` synthesized child session path | Parent-side child refs can carry `agentId` and `callPath`; synthesized rows are linkage hints, not authoritative over a real child `session_start`. | Parent evidence may help label missing children but must not overwrite real metadata. |
  | `internal/presenter/session_related.go` | The deterministic related-session branch reads `sessions.first_user_message_hash`, while current ingest/adapters do not write it. | Hash writing is real runtime debt owned here, not a speculative future feature. |

  The implementation-plan audit must replace this table with exact fixture/file
  evidence for the reported labels or an explicit "not present in source data"
  conclusion before code.
- Purely numeric `agentId` values are not human display labels by default. They
  must fall through to verified `call_path`, `kind`, client/source context, or
  session-id fallback unless fixture evidence proves the numeric value is an
  operator-facing agent name. Tests must cover numeric ids so they are not
  surfaced as misleading agent labels.
- `call_path` segment derivation must be deterministic and proven before it is
  used as a display label. Planning baseline: split `callPath` on `:`, trim
  whitespace, drop empty and known-generic segments (`parent`, `spawn-parent`,
  `sub-agent`, `tool_output`, `history_compaction`, `default`, `root`), sanitize
  remaining segments with the shared display sanitizer, then choose the rightmost
  remaining segment as the current-session display-agent candidate. If fixture
  evidence proves ai-agent v3 uses the leftmost segment for the operator-facing
  agent instead, the implementation plan may switch to that rule, but it must
  record the fixture evidence. `display_client_label` may use a verified
  leftmost/root segment only when the audit proves that segment is a client or
  headend label. If all segments are generic or unverified, `call_path` is not a
  display-label source.
- Recommended long-term-best direction: add a bounded `display_title` field and
  a `display_title_source` enum to the canonical/store/presenter contract. Store
  only the sanitized display string needed by the UI, not full user input. This
  is a normalization/indexing step for existing title-like extras and a bounded
  new preview for first-user-input fallback.
- Recommended companion direction: add a bounded `display_agent_label` and
  `display_agent_label_source` to the canonical/store/presenter contract
  independently of the title-storage privacy decision. Generic source literals
  (`parent`, `spawn-parent`, empty string, and similar placeholders) are never
  the final display label when verified context exists.
- Recommended client-label direction: add a bounded `display_client_label` and
  `display_client_label_source` to the canonical/store/presenter contract when
  a verified client/headend/source value exists. For ai-agent v3, candidate
  fields include `extras_json.headendId`, `source_format`, and any verified
  source-side client/headend identifier discovered during fixture audit. If no
  verified client label exists for a source, stats must fall back to
  `source_format` and record that `client` is unavailable for that source; it
  must not invent labels.
- Existing rows must be covered: `display_agent_label` is recomputed
  idempotently on reingest/backfill from `agent_name`, `call_path`, `kind`,
  `extras_json.headendId`, and verified adapter-specific context. This is as
  important as `display_title` backfill because the user complaint is about
  already-ingested sessions showing `parent`.
- Planning baseline: `display_title`, `display_title_source`,
  `display_agent_label`, `display_agent_label_source`,
  `display_client_label`, and `display_client_label_source` are persisted on
  `sessions` through the next numbered migration when their respective storage
  decisions are approved. The `display_agent_label` storage decision is separate
  from `display_title`: even if stored titles are rejected on privacy grounds,
  the agent-label fix still needs either a persisted normalized field or a
  presenter-level derivation that SOW-0111/SOW-0112 can query without
  reintroducing `parent`. The presenter may derive a fallback for rows not yet
  backfilled, but list/search/compare consistency requires a deterministic
  migration or reingestion/backfill path before the SOW can close.
- `display_agent_label_source` enum values are:
  - `agent_id` when a source-native agent id is useful and not a placeholder;
  - `call_path` when a stored call path is the least-misleading label;
  - `kind` when normalized session kind is the best verified context;
  - `headend_id` when a verified headend id is the useful human label;
  - `source_format` when only the adapter/source family is known;
  - `session_id_fallback` as terminal fallback.
- `display_title_source` enum values are `client_title`, `first_user_input`, and
  `session_id_fallback`. Agent/client/source labels are metadata and must never
  become title fallbacks in this SOW.
- `display_client_label_source` enum values are:
  - `headend_id` when a verified source headend/client id is useful;
  - `source_format` when the adapter/source family is the best available
    client grouping;
  - `source_id` when a configured source row is the useful operator-facing
    grouping;
  - `session_kind` when normalized session kind is the least-misleading client
    grouping;
  - `session_id_fallback` as terminal fallback.
- Client-label source audit for planning:

  | Source | Client label candidate | Required outcome |
  |---|---|---|
  | ai-agent v3 | `extras_json.headendId` for root headends (`cli`, `api`, `web`, `embed`, `slack`); otherwise normalized `source_format` / `source_id` fallback. Sub-agent values such as `sub-agent`, `tool_output`, and `history_compaction` are not useful client labels by themselves. Non-enumerated future headend values are not silently treated as root-client labels: the audit either proves the value is a user-facing client/headend and stores sanitized `headend_id`, or falls back to `source_format` / `source_id` and records the unknown value in diagnostics. | Map to `display_client_label` with `headend_id`, `source_format`, or `source_id`, with fixture evidence. |
  | ai-agent v2 | Adapter/source audit must identify a title/client/headend field, or prove none exists. | Use verified field, else `source_format`. |
  | claude-code | Adapter/source audit must identify whether any client surface exists beyond source format. | Use verified field, else `source_format`. |
  | codex | Adapter/source audit must identify whether any client surface exists beyond source format. | Use verified field, else `source_format`. |
  | opencode | Adapter/source audit must identify whether session metadata has a useful client/surface field; provider/model is not a client label. | Use verified field, else `source_format`. |

  Every non-v3 `Adapter/source audit` entry must be replaced with file/fixture
  evidence before implementation; until then those sources fall back to
  `source_format` for first-pass client grouping.
- Migration plan is explicitly split after the current latest migration
  `0011_topology_sort_indexes.sql`: `0012_display_title.sql` owns
  `display_title` / `display_title_source`, `0013_display_agent_label.sql` owns
  `display_agent_label` / `display_agent_label_source`, and
  `0014_display_client_label.sql` owns `display_client_label` /
  `display_client_label_source`. Splitting the migrations preserves the
  decoupled privacy/risk decisions: the agent/client label fixes can ship even
  if stored title previews are deferred.
- Recommended field bound: every `display_title` value is normalized and
  truncated to at most 256 display characters before storage. Client-provided
  titles use a lighter display sanitizer tier: remove obvious secrets/control
  bytes and normalize whitespace, but do not redact ordinary file paths or
  email-shaped text unless they match the secret scanner. First-user previews use the
  strict sanitizer tier below because raw prompts have higher sensitive-data
  risk. The first-user fallback contributes only the first 80 display characters
  after strict sanitization. Truncation is by Unicode rune unless the
  implementation adopts and tests a grapheme-cluster helper. Tests must include
  a client title containing a path and prove it remains recognizable. Tests also
  include a multi-rune grapheme cluster at the 80-character boundary and assert
  the chosen behavior, whether rune truncation or a named grapheme helper.
- Title precedence should be explicit:
  1. Future operator/manual title, if such a feature is later added. This SOW
     does not implement manual titles, but reserves the precedence rule so
     reingestion never overwrites operator intent later.
  2. Client/user-provided title.
  3. First user input preview.
  4. Short stable session identifier as last resort, formatted as
     `Session <short-id>` using the stable internal session id prefix.
  Verified agent/client/source labels render only as metadata next to the title
  and are never used as the H1/title text.
- Display-agent-label precedence mirrors the same future-proofing: if a future
  operator/manual agent-label correction feature is added, reingestion must not
  overwrite that operator intent. This SOW does not implement manual agent
  labels, but reserves the precedence slot before source-derived labels.
- The first-user-input preview path must define:
  - which op/message counts as first user input per adapter,
  - how payload text is read during ingest/reingest,
  - how invalid UTF-8, control bytes, multiline text, JSON wrappers, and binary
    payloads are handled,
  - the exact truncation rule for 80 display characters,
  - a redaction/sanitization rule and tests.
- First-user-input extraction contract for planning:

  | Adapter | First-user-input source for preview | If unproven |
  |---|---|---|
  | ai-agent v2 | First canonical user-input step/op produced from v2 turn data; plan must prove the exact source field and payload selector from adapter fixtures. | Disable preview for v2 and fall through to agent/source label. |
  | ai-agent v3 | First turn user-input record or payload ref that maps to the visible user step; plan must prove whether this is a canonical `internal/user_input` op, a payload ref, or source ledger field before the hash writer can run for v3. | Disable preview/hash for v3, keep `first_user_message_hash` NULL for v3 rows, and fall through to title/agent/source label. |
  | claude-code | First source user message that currently renders as the TurnView user step. | Disable preview for claude-code and use title/agent fallback. |
  | codex | First source message item with user role that maps to the visible user step; no title-like field is verified today. | Disable preview for codex and use agent/source/session-id fallback. |
  | opencode | Existing `user_prompt` payload ref emitted by `internal/adapters/opencode/`, paired to the first assistant turn by `parentID` or immediate-predecessor rule documented in the opencode adapter spec. The plan must prove from a real or sanitized opencode fixture that `prompt.text` is populated for the first user message. | Disable preview for opencode and use client title/agent fallback. |

  The implementation plan must replace each "plan must prove" entry with
  file/fixture evidence before code. A source without proof must not store a
  first-user preview.
- The first-user hash writer path is in scope and cannot remain implicit.
  Planning baseline: extraction happens during ingest/reingest in the canonical
  writer path when the first visible user-input op/message is known. If the
  existing event flow cannot carry this data directly, the plan may use an
  equivalent writer-side post-op extraction in the same ingest transaction, but
  it must still update `sessions.first_user_message_hash` deterministically and
  test related-session matching on real ingested rows, not only test seeds.
  The writer trigger is explicit: after inserting/upserting the first canonical
  user-visible `user_input` op or verified first-user payload reference for a
  session, the ingest writer derives the normalized first prompt, computes the
  hash, and updates the session row in the same transaction. Reingest recomputes
  the value idempotently. If adapter events cannot expose the prompt text at that
  point, the implementation must add a typed canonical extraction field or an
  adapter-specific writer hook before claiming the hash path works; leaving the
  column populated only by tests/seeds is a SOW failure.
  Within SOW-0108, the label migrations/contracts land first, then the hash
  writer lands and is validated before this SOW can complete. That sequencing
  lets SOW-0111/SOW-0112 plan against the label contracts without waiting on
  per-adapter first-user extraction, but it does not defer the hash writer out of
  SOW-0108. The plan must still close the hash writer before SOW-0108 completes
  because the schema and presenter already depend on it.
- `first_user_message_hash` algorithm extends the existing migration-0009
  contract. Existing durable specs only define a SHA-256 hash over normalized
  first-user text; this SOW adds the missing exact normalization contract:
  Unicode NFC normalization, lowercasing, whitespace collapse, then SHA-256 hex
  digest, with raw prompt text never stored in the sessions row.
	  The implementation plan must specify the exact normalization helper and add
	  cross-adapter tests proving reingest produces the same hash for the same first
	  user input. If a source cannot safely extract normalized prompt text, the hash
	  remains NULL for that source and related-session matching uses the existing
	  fallback path.
	  If the normalized first-user input is empty after Unicode NFC normalization,
	  lowercasing, control-byte removal, and whitespace collapse, the writer stores
	  NULL and related-session matching falls back to the existing cwd/source
	  heuristic for that session. It must not store the SHA-256 of an empty string.
	  Required spec delta: update `data-model.md` and the migration-0009 comment/spec
	  reference to the full algorithm in the same change, including a golden fixture
	  whose decomposed/composed Unicode input hashes identically after NFC.
- Truncation rule: normalize invalid UTF-8 by replacement, remove control
  bytes, collapse all whitespace/newlines to single spaces, extract a simple
  `content` or `text` field from JSON wrappers only when the source adapter
  proves that wrapper shape, then truncate to 80 Unicode runes unless the
  implementation adopts and tests a grapheme-cluster helper.
- Sanitization minimum: the title/preview sanitizer must meet or exceed the
  repository secret/PII scanner baseline in `scripts/scan-secrets.sh` before
  stored preview/search participation is allowed. It must remove obvious
  bearer/token/API-key assignments, known secret-token shapes, absolute private
  paths, and email addresses from title/preview text before storage. If this
  proves too lossy or too uncertain for a source, the implementation plan must
  record the tradeoff and keep the preview disabled for that source.
- Sanitization fixture minimum:
  - bearer/token/API-key assignment patterns such as `Bearer <token>`,
    `api_key=<value>`, `token: <value>`, and `sk-...`;
  - provider/service secret shapes covered by `scan-secrets.sh`, including
    `sk-ant-`, `AKIA...`, `ghp_...`, `github_pat_...`, `glpat-...`, `xoxb-`,
    `xoxp-`, `xoxa-`, and `xoxs-`;
  - private-key/header-like secrets and high-entropy bearer-like tokens when the
    scanner flags them;
  - absolute private paths under `/home/`, `/Users/`, and Windows drive roots;
  - email-address patterns.
- Sanitizer ownership: this SOW owns the shared display sanitizer as a reusable
  utility for title text, first-user previews, display-agent labels, stats
  dimension values, topology/stat resolver summaries, error-class strings, and
  aggregate predicate components. SOW-0111 and SOW-0112 must consume the shared
  utility rather than copying weaker local sanitizers.
- Sanitizer implementation plan must pin both API shape and language location.
  Planning baseline: Go owns the authoritative sanitizer under
  `internal/display/sanitize` with an API equivalent to
  `(sanitized string, didRedact bool)`, and frontend copy/display utilities use
  a small TypeScript mirror only for client-side text that cannot be sanitized
  server-side. Golden fixtures must be shared between Go and TypeScript where a
  TS mirror exists. This API and path are the cross-SOW contract; child SOWs must
  import or wrap this sanitizer instead of inventing local string filters. If the
  implementation needs structured-field sanitization later, it must add a new
  named API without weakening this string contract.
- Display sanitizer is not HTML escaping. Frontend rendering of display titles,
  display-agent labels, display-client labels, first-user previews, statistics
  dimension values, and topology labels must render them as text through React's
  default escaping or an equivalent safe text renderer. `dangerouslySetInnerHTML`
  or HTML interpretation is rejected for these fields.
- Sanitizer failure behavior is fail-closed and non-fatal. If sanitizer
  execution panics or returns an unexpected internal error while ingest/backfill
  derives a display title, preview, or label, the writer stores an empty value or
  falls through to the terminal session-id/source fallback, logs structured
  context, and continues the ingest transaction. A sanitizer failure must not
  store unsanitized text and must not fail ingestion of unrelated rows.
- Sanitizer tests are part of the contract. The implementation plan must add
  `internal/display/sanitize/sanitize_test.go` and, if a TypeScript mirror is
  needed, a colocated frontend test that reuses the same golden fixture cases.
- Cross-surface propagation must name every edited surface: Session List row
  label/sort/search, Session Detail title and breadcrumb, Session Detail
  agent/source metadata, Compare rows and agent breakdowns, and the global
  cross-session topology page/API (`internal/presenter/topology_cross.go`) so
  global topology does not continue surfacing raw ai-agent v3 `parent` labels
  after Session Detail is fixed. Search propagation is first-pass only where
  Search already has a session-row/title surface. Current Search primarily
  renders op/log/content hits, so adding a new session-hit section is out of
  scope unless a separate SOW expands Search.
- Retained per-session topology is also a propagation surface while
  `GET /api/sessions/:id/topology` remains compatible. The legacy response shape
  must preserve existing consumers, but it must either include
  `display_agent_label` / `display_agent_label_source` beside raw
  `agent_name`, or return a structured deprecation marker accepted by SOW-0107.
  A verified ai-agent v3 display label must not regress to visible raw
  `parent` in retained per-session topology consumers.
- Trace-driven Session Detail surfaces are also propagation surfaces. If
  `/api/sessions/:id/trace` remains a waterfall/event-list data source, it must
  expose display-safe session labels in addition to raw `session_agent_name`,
  for example `session_display_agent_label` and
  `session_display_agent_label_source`. Waterfall bars, event rows, and trace
  filters must not continue showing ai-agent v3 `parent` when a SOW-0108
  display label exists.
- Child-session summaries are a required propagation surface. The presenter
  `childSummary` shape in `internal/presenter/session_detail.go` must expose
  `display_agent_label`, `display_agent_label_source`, and when available
  `display_client_label` / `display_client_label_source` so content, waterfall,
  and topology subagent boundaries do not reintroduce raw `parent` labels.
  Required DTO delta baseline:
  `display_agent_label string`, `display_agent_label_source string`,
  `display_client_label string`, `display_client_label_source string`,
  `effective_status string`, `last_activity_ts *int64`,
  `direct_child_count int`, and `duration_us *int64` where `duration_us` is
  `end_ts - start_ts` when both timestamps are known and omitted/null while
  running or unavailable. Empty strings represent unavailable labels; the source
  fields must be empty only when the label is empty. `direct_child_count` is
  computed from a direct SQL/count path from the start, not from the length of the
  returned `child_sessions` slice. This prevents future child pagination or
  bounded projections from silently corrupting the count.
	  `effective_status` is computed with the same presenter derivation as the
	  Session Detail header, using the child's raw `status`, `end_ts`, and
	  `last_activity_ts`; this keeps waterfall child boundaries and topology child
	  nodes from disagreeing about running versus stale children. This workbench
	  child-summary extension supersedes earlier decisions that kept
	  `child_sessions` minimal only for generic detail views; if implementation
	  chooses a separate workbench child-summary DTO instead, that DTO must carry the
	  same fields.
  If decisions 6 and 7 land as recommended, SOW-0108 is the first writer of the
  extended `childSummary` contract; SOW-0110 and SOW-0111 consume these fields
  after they land and must not write competing DTO extensions for the same
  labels/status fields in parallel.
- Breadcrumb ancestor summaries are a required propagation surface. SOW-0107
  owns header behavior; this SOW owns making ancestor label fields available
  without the frontend fetching every ancestor's full Session Detail response.
  Required first-pass DTO shape is `ancestors[]` on Session Detail, ordered
  root-to-parent, with `id`, `display_title`, `display_title_source`,
  `display_agent_label`, and `display_agent_label_source`. Optional
  `display_client_label` / `display_client_label_source` may be included only if
	  SOW-0107's 96px header proof needs them. The backend resolves ancestors with
	  one bounded indexed parent walk plus a 64-ancestor cap and cycle/depth
	  protection. Tests cover root/no-parent, one-level child, multi-level child,
	  malformed cycles, a too-deep chain, and a broken parent pointer where a
	  referenced parent row is unavailable. Broken chains return resolvable ancestors
	  up to the break plus a structured unavailable marker for SOW-0107's breadcrumb
	  state; they do not loop, fetch full details repeatedly, or hide the break.
	  `display_client_label` is required as a DTO field for
	  child summaries because tables/waterfall/topology use child boundaries as
  diagnostic nodes; the value may still be the documented fallback when no
  meaningful client label exists. It is optional for breadcrumbs because the 96px header budget
  may need to hide client metadata while still showing title/agent identity.
  Ancestor labels use the same derivation/backfill contract as the current
  session; no raw `parent` fallback is allowed when a display label exists.
  These fields are consumed by SOW-0107's header breadcrumb; field-name, cap, or
  ordering changes require SOW-0107 plan review because they affect the 96px
  header contract.
- Transition behavior during migration/backfill is explicit: presenter APIs use
  `display_agent_label` when present and fall back to raw `agent_name` only when
  the display field is NULL/empty. That fallback is accepted only between schema
  migration and completed backfill/reingestion, so an un-backfilled ai-agent v3
  row may still show raw `parent` temporarily. The implementation plan must
  minimize and test this window; after backfill, fixtures with verified display
  labels must not show raw `parent` on Detail, Compare, trace/waterfall, child
  summaries, or global topology.
- Codex fallback is explicit: because no title field is currently verified,
  use first-user-input preview only after the implementation-plan audit proves the
  codex first-user extraction path. Until then, codex falls through to
  agent/source label, then stable session id.
  `source_id` and source-derived labels are display metadata only. They mean the
  configured source label/name exposed through the sources table or presenter,
  not a raw foreign key or opaque database id, unless a field is explicitly named
  `source_id`.
- Reingestion semantics: recompute `display_title`, `display_title_source`, and
  `first_user_message_hash` idempotently on every reingest. Explicit client
  title changes win on reingest; first-user fallback updates only if the first
  user input source changes.
- Canonical/ingest contract: the implementation plan must either extend
  `SessionStartedEvent` and `SessionUpdatedEvent` with optional title/hash
  fields or document an equivalent writer-side extraction path. Preferred
  direction is optional canonical fields for `DisplayTitle`,
  `DisplayTitleSource`, `FirstUserMessagePreview` as a transient extraction
  value, and `FirstUserMessageHash`. `FirstUserMessagePreview` is not a
  required persisted column; if it only wins title fallback, the persisted value
  is the sanitized bounded `display_title`. The implementation plan must close
  the concrete writer path before code: exact canonical event or writer-side
  trigger, per-adapter extraction function, first-visible-user selector,
  transaction boundary, and reingest/backfill behavior.
  The first implementation chooses one DTO strategy for child summaries before
  code. Baseline: extend the existing presenter `childSummary` everywhere the
  Session Detail API returns child summaries, with the fields listed above
  (`display_*`, `effective_status`, `last_activity_ts`, `direct_child_count`,
  `duration_us`). If measurement later forces a lighter workbench-specific DTO,
  it must preserve identical semantics and field names for these shared fields,
  and the divergence must be named in `rest-api.md` before any frontend consumes
  it.
- Related-session behavior change is explicit risk. Once this writer populates
  `first_user_message_hash`, `internal/presenter/session_related.go` switches a
  source session from cwd-based heuristic matching to deterministic hash matching.
  A session that previously showed cwd-related candidates may show fewer or zero
  related sessions after backfill if its hash is unique. This is intentional only
  if validation documents the transition; it must not be a silent surprise.
  The implementation must add an operator-facing note in the relevant product/
  operator documentation or release evidence before the hash writer/backfill is
  installed. The note must explain that related-session matches become stricter
  once first-user hashes are available and that older cwd-based related matches
  may disappear.
- Backfill contract: existing title-like extras are normalized either by a SQL
  backfill using `json_extract(extras_json, ...)` for verified keys or by
  deterministic reingestion. The plan must state which path updates existing
  rows and how it avoids weakening the privacy contract.
  `display_agent_label` and `display_client_label` backfill for existing rows
  uses a Go-side one-time backfill path following the existing
  `internal/ingest/*_backfill.go` pattern, reusing the same derivation logic as
  ingest-time. A SQL-only fallback chain is rejected for these labels because the
  multi-level precedence is fragile and hard to test. `display_title` backfill
  may use SQL `json_extract` only for verified single-key title extras; first
  user fallback and sanitizer-sensitive derivations stay in Go.
- Backfill execution is a planned operational step. The implementation plan must
  state whether the Go-side backfill runs inline during install/ingest startup or
  as an explicit repair/reprocess command, record an expected row-count/latency
  budget on the installed database, and avoid blocking normal live ingest longer
  than the plan's recorded threshold.
  The backfill plan must account for live-ingest write contention under SQLite
  WAL: writes serialize, so the implementation must either run the backfill
  before live ingest starts, batch it with bounded transactions, or handle
  `SQLITE_BUSY` with tested retry/backoff instead of treating busy errors as
  silent partial success.
- Index contract: display-label migrations must include explicit index
  decisions. If display-title filtering or server-side title sorting is in
  scope, `0012_display_title.sql` must add a supporting
  `sessions(display_title)` index with ordering/collation matching the cursor
  and fingerprint sort contract. If server-side title sorting is deferred, the
  implementation plan must include EXPLAIN evidence for any `display_title`
  filter path or add a partial non-empty-title index. `0013_display_agent_label`
  and `0014_display_client_label` must either add supporting label indexes or
  record EXPLAIN evidence from SOW-0112 bounded-subtree grouping queries proving
  a label index is unnecessary for the first-pass API budget.
- Required fixtures:
  - ai-agent v3 fixture coverage for literal `agentId="parent"` and
    `agentId="spawn-parent"` with the verified fallback chain asserted;
  - ai-agent v3 fixture coverage for a purely numeric `agentId` proving numeric
    values do not become display labels by default;
  - ai-agent v3 fixture coverage for `headendId` values such as `cli`, when
    present in sanitized source data, proving root-client labels are separate
    from agent labels;
  - ai-agent v3 fixture manifest evidence for `reddit`/`neda` or an explicit
    "not present in source data" record before plan review closes;
  - ai-agent v3 fixture coverage for a useful non-placeholder `agentId` when
    the source audit proves the direct `agent_id` path, asserting
    `display_agent_label_source=agent_id`;
  - title-like extras for ai-agent v2, claude-code, and opencode;
  - missing-title/missing-user-input rows proving terminal
    `Session <short-id>` fallback;
  - decomposed/composed Unicode inputs proving NFC normalization produces the same
    `first_user_message_hash`;
  - sanitizer fixtures listed above.

Risk and blast radius:

- Backend + frontend contract change. Medium blast radius, high value.
- Privacy/security risk: deriving titles from user input stores a sensitive
  snippet in the ai-viewer database and API responses. This must be intentional,
  bounded, and documented.

Sensitive data handling plan:

- Fallback title must be display-limited and sanitized. Tests must use redacted
  fixtures, not real user text.
- Normalizing title-like extras into `display_title` is privacy-sensitive
  because it makes already-stored strings first-class, indexed, and easier to
  display. Storing first-user previews is a new bounded sensitive snippet. Specs
  that currently conflict with actual extras storage or prohibit copying
  title/text content must be reconciled before implementation.
- Opencode authority decision: the current adapter behavior is authoritative for
  this SOW. `internal/adapters/opencode/mapper.go` already stores
  `session.Title` in `sessions.extras_json.title`; the conflicting
  `adapter-opencode.md` sensitive-content statement must be corrected before
  code so it distinguishes already-normalized session metadata from raw payload
  bytes. The implementation-plan audit must reconcile both the adapter/code
  behavior and the opencode spec's own internal tension between its extras list
  that includes `title` and its later sensitive-content warning. Only after that
  spec correction may opencode `extras_json.title` be normalized into
  `display_title`, subject to the shared sanitizer.
  Required spec correction target: update `adapter-opencode.md` so the extras
  taxonomy explicitly permits normalized session metadata such as
  `extras_json.title`, while the sensitive-content section continues to forbid
  copying raw message text, reasoning text, tool inputs/outputs, patches, and
  payload bytes outside payload references. Do not "fix" the drift by removing
  the adapter's existing `extras_json.title` population unless a separate SOW
  rejects opencode titles entirely.
  The exact spec diff must distinguish:
  - normalized session metadata persisted in ai-viewer's database, including
    `extras_json.{title,directory,project_id,version,slug,providerID,variant}`;
  - raw message content, reasoning text, tool I/O, patches, and payload bytes,
    which remain unavailable except through payload references and on-demand
    presenter reads.
  The implementation plan must include the concrete `adapter-opencode.md` diff
  text before any code normalizes opencode titles into `display_title`; a
  high-level statement that the spec "will be corrected" is insufficient.
  The open "display title storage" decision below explicitly includes opencode
  `extras_json.title`: choosing storage option A is approval to normalize and
  index sanitized opencode titles after this spec correction; choosing B or C
  leaves opencode titles in extras/presenter-only paths for the first pass.

Implementation plan:

1. Audit source fields for ai-agent v3 display identity, client/headend label,
   and client title with fixture evidence, including proof for the reported
   `reddit`/`neda` cases or an explicit finding that the source data lacks those
   labels.
2. Audit every adapter for title-like fields and first-user-input extraction.
   For first-user extraction and hash writing, cite each adapter's exact mapper
   function and selector that identifies the first visible user input. Adapters
   without proof disable preview/hash derivation for that source and fall through
   to the documented title/agent fallback.
3. Decide and spec the display title and display-agent-label store/API contract,
   including canonical event fields or writer extraction, privacy exception,
   migration, reingestion/backfill, source enums, field bounds, and
   `scan-secrets.sh` sanitizer parity.
   If decisions 1, 6, and 7 land as recommended, migrations owned by this SOW
   currently start at `0012_display_title.sql`, `0013_display_agent_label.sql`,
   and `0014_display_client_label.sql`. Each migration must include companion
   store migration tests. Chain-head ownership follows SOW-0107's umbrella rule:
   if `0014` is the highest migration present when this SOW lands, it bumps
   `presenter.SchemaVersion` and owns the
   `TestMigration0014_ChainHeadSchemaVersion`-style assertion; if a higher UI
   SOW migration already exists, this SOW must not assert `0014` as the chain
   head and instead updates the single highest chain-head test/constant in the
   same commit.
4. Add adapter/presenter tests for display identity, explicit title, first-user
   fallback, missing title, missing user input, and sanitization.
5. Implement the ingest/reingest writer path for `display_title`,
   `display_title_source`, `display_agent_label`,
   `display_agent_label_source`, `display_client_label`,
   `display_client_label_source`, and `first_user_message_hash`. The hash
   writer is in this SOW's scope because the column already exists and presenter
   behavior already depends on it.
6. Extend the Session Detail child-session DTO in
   `internal/presenter/session_detail.go` (`childSummary`) with
   `display_agent_label`, `display_agent_label_source`,
   `display_client_label`, `display_client_label_source`, and
   `effective_status`, `last_activity_ts`, `direct_child_count`, and
   server-derived `duration_us`, with golden tests covering populated, stale,
   running/absent, and fallback values.
6a. Extend trace presenter output or the selected waterfall data source with
    display-safe session labels so event rows and waterfall subagent tags do not
    render raw v3 `parent` after display-label derivation exists.
6b. Extend Session Detail metadata with the breadcrumb ancestor summary contract
    consumed by SOW-0107, including golden tests for root, one-level child,
    multi-level child, and cycle/depth protection.
6c. Extend Compare presenter DTOs and buckets so compare cards and agent
    breakdowns use `display_title` and `display_agent_label` with raw
    `agent_name` fallback. Required code path includes
    `internal/presenter/compare.go`, where the current agent bucket uses raw
    `AgentName`.
    Compare links into Session Detail must emit canonical workbench URLs rather
    than legacy `tab=` / `op=` params. First-pass default is
    `/sessions/:id?view=waterfall&table=turns`; if Compare can identify a
    concrete turn, it may also emit `sel=turn:<id>`. Tests must assert Compare
    "open/view session" links do not trigger the legacy-link repair path on
    normal navigation.
6d. Extend global cross-session topology presenter labels to use
    `display_agent_label` with raw `agent_name` fallback, and add a regression
    proving ai-agent v3 `parent` is not rendered on the global topology page
    when a display label exists. Also extend retained per-session topology
    responses or their structured deprecation marker per SOW-0107, with a
    regression proving `GET /api/sessions/:id/topology` does not render raw
    `parent` when the fixture has a verified display label.
7. Implement canonical/store/presenter title contract.
8. Update UI title ribbon and all cross-surfaces to use display title + separate
   metadata efficiently.
9. If Session List filtering or sorting changes touch `sessions_list.go`,
   `filters.go`, or `filters_sql.go`, run the existing `handleSessionsList`
   benchmark gate. Any regression over the local 20% `sec/op` threshold must be
   fixed or re-baselined with justification in the same SOW evidence.

Validation plan:

- Go adapter/presenter tests.
- Real-ingest hash-writer validation: ingest the small sanitized real-shape
  fixture through the production pipeline into a temp database, with two
  sessions sharing the same first visible user input and one negative case, then
  assert `first_user_message_hash` is written and
  `internal/presenter/session_related.go` returns only the related pair. A
  transition test seeds cwd-heuristic related sessions first, then populates a
  unique hash through the production writer and proves the cwd heuristic
  disappears for that source session, documenting the intentional behavior
  change.
- Hash normalization validation includes decomposed/composed Unicode input,
  lower/uppercase variants, and whitespace variants, proving the canonical helper
  produces stable hashes and `data-model.md` documents the same algorithm.
- Golden trace/detail tests prove child summaries and trace rows expose display
  labels and do not regress to raw ai-agent v3 `parent` when a better label is
  derived.
- `TestEffectiveStatus_HeaderAndChildSummaryAgree`-style presenter coverage
  proves the header and child-summary `effective_status` values use the same
  captured `now`, stale threshold, raw status, end timestamp, and
  `last_activity_ts` rules for a parent with completed, running, stale, and
  missing-data children.
- Ancestor-chain tests prove Session Detail supplies display labels for every
  breadcrumb ancestor without loading full turn/op detail for each ancestor.
- Compare golden tests prove agent breakdowns and cards use display labels and
  do not expose raw v3 `parent` when a better label exists.
- Compare link tests prove emitted Session Detail hrefs use canonical workbench
  params and omit legacy `tab=` / `op=` for normal Compare navigation.
- Global topology tests prove cross-session topology labels consume
  `display_agent_label` with raw `agent_name` fallback.
- Frontend component tests for title ribbon.
- Playwright geometry and visible-title assertions.

Artifact impact plan:

- Specs: `canonical-events.md`, `data-model.md`, `rest-api.md`,
  `ui-pages.md`, and `adapter-opencode.md`.
- `ui-pages.md` must update the `/compare?ids=...` section so Compare summary
  cards use `display_title` / `display_title_source` and
  `display_agent_label` / `display_agent_label_source` instead of relying on
  raw `agent_name` in card headers. The same spec delta must state the canonical
  workbench Session Detail href emitted from Compare.
- SOW lifecycle: child of SOW-0107.

Open-source reference evidence:

- Not required for gap analysis. If needed, inspect tracing/session viewers for
  title/identity presentation before plan finalization.

Open decisions:

1. Display title storage:
   - A. Store bounded sanitized `display_title` and `display_title_source` in
     `sessions` during ingest/reingest.
   - B. Derive title at presenter query time from payload refs.
   - C. Derive title only in the frontend.
   Recommendation: A, long-term-best. It is the only option that keeps List,
   Search, Compare, and Detail consistent without expensive per-row payload
   reads.
2. ai-agent v3 `parent` display:
   - A. Use a verified better source label when one exists; otherwise show a
     transparent fallback such as headend/call-path context.
   - B. Force-rewrite `parent` to another guessed label.
   Recommendation: A, long-term-best. The project must not invent identities
   that are not present in source data.
3. Backfill strategy:
   - A. Automatic one-time backfill/reingestion path fills existing sessions.
   - B. Existing rows remain NULL until normal reingestion.
   - C. Presenter derives missing values lazily until reingestion.
   Recommendation: A for consistency, unless privacy decision 1 rejects stored
   previews.
4a. Client-title search/index participation:
    - A. Sanitized client-provided titles participate in list/search/compare
      filtering and sort.
    - B. They are display-only.
   Recommendation: A for filtering/display if storage is approved. First-pass
   sorting default is display/filter-only, with title sorting deferred unless the
   operator explicitly approves the broader cursor/index work. Server-side
   full-result sort requires a new supported sort key, keyset cursor/tie-breaker,
   `display_title` index, and NULL/empty-title ordering; client-side page-only
   sort is not equivalent and must be labeled as such if chosen. If server-side
    sort is chosen, the plan must name the `cursor.go` and `fingerprint.go`
    changes that include the new sort key in cursor validation and result-set
    fingerprinting. If those changes are not in scope, `display_title` is
    display/filter-only for the first pass and title sorting is deferred.
4b. First-user-preview search/index participation:
    - A. Sanitized first-user-input-derived display titles participate in
      filtering/search the same way client titles do.
    - B. Store and display first-user previews, but exclude them from search and
      indexed filtering.
    - C. Do not store first-user previews; use only client titles and agent/source
      fallback.
    Recommendation: B unless the operator explicitly accepts the stronger privacy
    exposure. Searchability amplifies sensitive first-message snippets beyond
    display, even after sanitizer redaction. Choosing A later does not require
    reingestion for rows that already store `display_title`; it is a query/index
    and privacy-scope change unless the stored preview contract changes.
5. First-user hash writer:
   - A. Implement it with the display-title extraction path.
   - B. Remove or deprecate the hash column and related-session deterministic
     match.
   Recommendation: A, long-term-best. The schema and presenter already depend
   on the hash, so leaving it unwritten keeps existing behavior half-built.
   Validation must prove the deterministic related-session branch in
   `internal/presenter/session_related.go` returns related sessions on real
   ingested rows after the writer lands, not only on seeded test rows.
   The validation fixture is a small sanitized real-shape ingest fixture with at
   least two sessions sharing the same first visible user input and one unrelated
   negative case; the test ingests through the production pipeline into a temp DB
   before calling the presenter query.
6. Display-agent-label storage:
   - A. Persist bounded `display_agent_label` and
     `display_agent_label_source` during ingest/reingest, independent of stored
     display-title approval.
   - B. Derive `display_agent_label` only in presenter queries.
   Recommendation: A, long-term-best. Topology and statistics need a queryable
   label that does not fall back to raw `parent` for already-ingested rows.
7. Display-client-label storage:
   - A. Persist bounded `display_client_label` and
     `display_client_label_source` during ingest/reingest, independent of stored
     display-title approval.
   - B. Let SOW-0112 group by `json_extract(extras_json, '$.headendId')` with
     query/index proof and no persisted client label.
   - C. Cut client grouping from the first redesign and file a follow-up SOW.
   Recommendation: A, long-term-best. The user explicitly asked for statistics
   grouped by client, and a queryable normalized label prevents every stats query
   from knowing adapter-specific JSON paths.

## Plan

1. Run external gap review.
2. Resolve reviewer findings.
3. Rerun the gap-analysis gate.
4. Draft implementation plan after gap review converges.

## Execution Log

### 2026-06-26

- Created focused SOW from Session Detail identity/title feedback.
- Incorporated external reviewer round-1 findings: title schema/data contract,
  ai-agent v3 `parent` source verification, per-adapter title audit,
  reingestion/backfill, and sanitization.
- Incorporated external reviewer round-2 findings: missing hash writer,
  codex audit, privacy/spec conflict, source enum, truncation/sanitization
  rules, cross-surface propagation, and explicit backfill/index decisions.
- Incorporated external reviewer round-3 findings: titles already stored in
  extras for opencode/claude-code, v3 `call_path`/`headendId` concrete
  candidates, owned hash writer, codex fallback, reingestion overwrite
  semantics, and sanitization fixture minimum.
- Incorporated external reviewer round-4 findings: sanitizer parity with the
  repository scanner, uniform client-title truncation, canonical event/writer
  data flow, existing-extras normalization path, and future manual-title
  precedence.
- Round 5 produced no separately recorded SOW-0108-specific accepted changes in
  this ledger; round 6 below is the next accepted identity/title update.
- Incorporated external reviewer round-6 findings: separated session
  `display_title` from agent/source display identity, preserved canonical
  `agent_name` as raw source identity unless a verified adapter field exists,
  and required cross-surface propagation for agent/source metadata as well as
  titles.
- Incorporated external reviewer round-7 findings: opencode adapter behavior is
  the authority and its spec drift must be corrected before title normalization;
  first-user preview extraction now has an adapter-by-adapter proof/disable
  contract; v3 human-label verification is the first implementation-plan step;
  and existing-row `display_agent_label` backfill is explicitly required.
- Incorporated external reviewer round-8 findings: `display_agent_label_source`
  enum values are explicit, title and agent-label fields require a numbered
  migration plus deterministic backfill/reingestion, the first-user hash writer
  path is owned here, v3 literal `parent`/`spawn-parent` fixture coverage is
  required, and the opencode title spec/code/spec contradiction must be
  reconciled before normalizing `extras_json.title`.
- Incorporated external reviewer round-9 findings: display-agent-label storage
  is decoupled from display-title storage, manual-label precedence is reserved,
  Compare page spec propagation is explicit, `adapter-opencode.md` correction
  targets are pinned, and the shared sanitizer is owned here for title, label,
  dimension, error-class, resolver-summary, and aggregate-predicate inputs.
- Incorporated external reviewer round-10 findings: `display_client_label` is
  owned here so SOW-0112 can deliver client grouping, display/client/agent label
  migrations are split, child summaries must expose display labels, the shared
  sanitizer API/location and golden fixtures are pinned for planning, v3
  `reddit`/`neda` label proof must cite exact source fields, and the opencode
  title spec correction distinguishes normalized metadata from raw content.
- Incorporated external reviewer round-11 findings: v3 display-agent proof has
  an explicit failure fallback table, `display_client_label` has a per-adapter
  audit table, Search propagation is scoped to existing session-title surfaces,
  display-title sort must choose server-side vs client-side semantics before
  implementation, the first-user hash writer must prove deterministic related
  sessions on real ingested rows, and Session List changes must respect the
  benchmark gate.
- Incorporated external reviewer round-12 findings: v3 identity evidence now
  records the exact current spec/code boundary before fixture audit, the
  first-user hash writer is sequenced after display-label contracts so topology
  and stats are not unnecessarily blocked, each adapter must cite an exact
  first-user extraction point or disable the preview/hash path, and
  display-agent/client label backfill must use a Go-side path that reuses
  ingest-time derivation logic.
- Round 13 reviewer rerun returned no new actionable findings for this SOW; the
  only P2 finding was scoped to SOW-0113 server-key highlight behavior.
- Round 14 completed on 2026-06-27 with accepted findings about opencode title
  privacy approval, unknown v3 headend handling, first-user hash writer
  sequencing, sanitizer API as a cross-SOW contract, childSummary DTO ownership,
  display-title cursor/fingerprint scope, and real-ingest related-session
  validation.
- Round 15 completed on 2026-06-27 with accepted findings clarifying
  deterministic `call_path` segment selection, trace `/session_agent_name`
  display-label propagation, concrete `childSummary` field deltas including
  `direct_child_count`, sanitizer test ownership, migration `SchemaVersion`
  companions for `0012` through `0014`, and real-ingest
  `first_user_message_hash` writer validation in the validation plan.
- Round 16 completed on 2026-06-27 with accepted findings adding breadcrumb
  ancestor-summary DTO/data-source ownership and explicit index decisions for
  `display_title`, `display_agent_label`, and `display_client_label`
  migrations.
- Round 17 completed on 2026-06-27 with accepted findings adding global
  topology and Compare propagation, ai-agent v3 generic `headendId` exclusions,
  concrete first-user hash writer-path requirements, opencode spec-diff
  requirements, child-summary `duration_us`, ancestor depth cap/rationale, and a
  separate privacy decision for search/index participation of first-user-preview
  titles.
- Round 18 completed on 2026-06-27 with accepted findings clarifying tiered
  title sanitization, path-bearing client-title tests, exact child-summary
  `duration_us` and `direct_child_count` semantics, display-label transition
  fallback during migration/backfill, and the future search/index path for
  stored first-user-preview titles.
- Round 19 completed on 2026-06-27 with accepted findings clarifying that
  numeric ai-agent ids are not display labels by default, breadcrumb ancestor
  DTO changes require SOW-0107 header review, and Unicode truncation must be
  tested at a multi-rune grapheme boundary.
- Round 20 completed on 2026-06-27 with accepted findings removing agent/client
  labels from title-source precedence, adding the exact
  `first_user_message_hash` algorithm contract, defining sanitizer fail-closed
  behavior, adding `effective_status` and `last_activity_ts` to child summaries,
  and aligning migration chain-head tests with SOW-0107's umbrella rule.
- Round 21 completed on 2026-06-27 with accepted findings clarifying the
  production writer trigger for `first_user_message_hash`, Unicode NFC in the
  hash normalization path, source-label display semantics, and the default
  `childSummary` DTO extension strategy.
- Round 22 completed on 2026-06-27 with accepted findings clarifying the
  ai-agent v3 human-label evidence gate, NFC/data-model spec delta for
  `first_user_message_hash`, related-session behavior changes when the hash is
  populated, frontend safe-text rendering for display labels, display-label
  backfill execution budget, direct SQL/count ownership for `direct_child_count`,
  codex title fallback gating, and first-pass display-title sorting deferral.
- Round 23 completed on 2026-06-27 with accepted findings clarifying broken
  ancestor-chain DTO behavior for missing parent rows.
- Round 24 completed on 2026-06-27 with accepted findings requiring a committed
  or generated `session-identity-v1` v3 fixture/manifest before plan review,
  explicit empty-normalized-first-user hash behavior, v3 hash disablement when
  extraction is unproven, first-writer ownership for extended `childSummary`,
  numeric/root-client v3 fixture cases, child-summary/header
  `effective_status` consistency tests, and live-ingest/backfill WAL contention
  handling.
- Round 25 completed on 2026-06-27 with accepted findings clarifying
  operator-facing documentation for the related-session behavior change caused
  by `first_user_message_hash`, plus canonical Compare -> Session Detail
  workbench link emission so common Compare navigation does not rely on legacy
  URL repair.
- Round 26 completed on 2026-06-27 with no identity-specific P0/P1/P2 changes.
  Accepted wording clarified that child-summary `display_client_label` is a
  required DTO field, not a promise that every source has a meaningful client
  label beyond the documented fallback.
- Round 27 completed on 2026-06-27 with accepted data-integrity findings
  clarifying that display-label propagation covers retained per-session topology
  as well as global topology while legacy topology endpoints remain compatible.
- Round 28 completed on 2026-06-27 with no identity-specific P0/P1/P2 changes;
  the review confirmed SOW-0108's title/label evidence gates and migrations are
  still consumed by the child workbench SOWs.
- Round 29 completed on 2026-06-27 with no identity-specific P0/P1/P2 changes;
  P3 bookkeeping/conditional wording was clarified for the round-5 ledger gap
  and display-label migration decisions.
- Round 31 completed on 2026-06-27 with no identity-specific P0/P1/P2 changes.
  Mimo's non-positive v3 identity-proof finding was rejected as a phase-boundary
  false positive because this SOW already requires the proof fixture or explicit
  "not present in source data" record before implementation-plan review closes.

## Validation

Pending.

## Outcome

Pending.

## Followup

None yet.

## Regression Log

None yet.
