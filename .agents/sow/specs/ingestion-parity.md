# Ingestion Parity

## TL;DR

Ingestion is correct only when ai-viewer's canonical SQLite store can be proven to match the source records on disk. Adapter golden tests and SQL row-count checks are not sufficient: they can prove that code emitted some canonical rows, but they cannot prove that every source-visible artifact was emitted, classified correctly, and remains resolvable without truncation or ambiguity.

This spec defines the deterministic parity gate. The gate compares two independently-built manifests:

- **Source manifest**: what exists in the native source files/databases.
- **Canonical manifest**: what ai-viewer ingested into canonical sessions, turns, ops, payload refs, and logs.

A full parity pass means every source-available artifact has exactly one canonical artifact with matching identity, class, selector, hash domain, byte/character length, and content hash where bytes exist. Missing, empty, blank, partial, duplicate, extra, or unverifiable data is a failure unless the adapter spec explicitly documents that the source itself does not carry the artifact.

## Scope

The first parity gate covers every source-visible artifact that can affect the
operator's understanding of a session. This is broader than payload bodies:
boundaries, links, statuses, errors, compaction, and metadata are data too. If
the source carries it, ai-viewer must ingest it or the gate must fail.

| Artifact class | Required parity proof |
|---|---|
| `session_boundary` | Every source session/root/child start and final state maps to one canonical session with matching native id, parent/root relation, status, start/end timestamps where present, and source identity. |
| `turn_boundary` | Source-derived turn model matches canonical turn count, order, status, and timestamps where source timestamps exist. Synthetic turn models are allowed only when the adapter matrix documents the source-native pivot records. |
| `op_boundary` | Every source-visible operation boundary maps to one canonical op with matching sequence/order, kind/class translation, status, and timestamps where present. |
| `user_prompt` | Every source user prompt text/object is represented as one canonical artifact with exact selector, length, and hash. |
| `user_image` | Every source user image/file reference used as model input is represented as canonical attachment or payload metadata with exact selector and canonical JSON hash when source-visible. |
| `assistant_message` | Every assistant text/final message/report is represented exactly. |
| `reasoning_text` | Every reasoning/thinking/summary/raw reasoning artifact is represented exactly, including `reasoning_kind` when the source distinguishes kinds. |
| `llm_request` | Every source-available LLM request envelope/prompt/messages payload is represented exactly, or the adapter spec documents source-unavailable request bodies. |
| `llm_response` | Every source-available LLM response envelope/body is represented exactly. |
| `llm_sdk_request` | Every SDK-level model request capture is represented exactly when the source stores SDK envelopes separately from provider HTTP payloads. |
| `llm_sdk_response` | Every SDK-level model response capture is represented exactly when the source stores SDK envelopes separately from provider HTTP payloads. |
| `tool_request` | Every source tool name + arguments/input object is represented exactly. |
| `tool_response` | Every source tool output/result/error body is represented exactly. |
| `llm_error` | Every source model/API error maps to canonical failed status plus error class/message where the source has them. |
| `tool_error` | Every source tool failure maps to canonical failed status plus error class/message where the source has them. |
| `subagent_link` | Every source parent-child session relation maps to a deterministic canonical parent/child link or an explicit source-unavailable exception. |
| `system_op` | Every source-visible lifecycle/system/handoff op maps to canonical `OpSystem` or an explicitly documented artifact class translation. |
| `compaction_event` | Every source compaction/context-summary event maps to canonical `OpCompaction` with trigger, token counts, duration, and timestamps where the source carries them. |
| `session_metadata` | Every source-visible session metadata value used by ai-viewer, including model, provider, cwd/project, agent name, and title when present, is preserved or explicitly documented as source-unavailable. |
| `log_entry` | Every source log/diagnostic line intentionally surfaced to the operator maps to `log_entries` with matching severity, scope, and message hash. |
| `attachment_metadata` | Every source-visible file/image/attachment reference maps to canonical log/payload metadata or an explicit deferred attachment representation with source evidence. |
| `patch_metadata` | Every source-visible file-change/patch metadata record maps to canonical op metadata with the source patch id, owning op sequence, patch hash when present, and a deterministic hash/count of changed-file paths. |

Derived indexes such as FTS are out of scope for this gate. They may get a separate derived-artifact parity gate later, but source-to-canonical parity is the foundation.

`internal_op` is not a blanket escape hatch. Adapter-internal bookkeeping that is
not source-visible is out of scope; source-visible records currently represented
as `OpInternal` are covered by the artifact class they actually carry
(`user_prompt`, `system_op`, `log_entry`, or another class above).

## Manifest Model

### Source Artifact

Each source extractor emits `SourceArtifact` records. The extractor reads the source format directly and must not infer expected artifacts from canonical DB rows.

```json
{
  "schema_version": 1,
  "adapter": "codex",
  "source_id": "codex:<root>",
  "source_file": "2026/06/22/rollout-abc.jsonl",
  "native_session_id": "session-123",
  "native_turn_id": "turn-456",
  "native_artifact_id": "line:42:/msg/content/0/text",
  "class": "assistant_message",
  "availability": "available",
  "hash_domain": "semantic_text",
  "selector": {
    "uri": "file://<root>/2026/06/22/rollout-abc.jsonl#L42",
    "json_pointer": "/msg/content/0/text"
  },
  "bytes": 1482,
  "chars": 1482,
  "computed_sha256": "<hex>",
  "producer_sha256": "",
  "source_record_counted": true,
  "synthetic": false,
  "synthetic_reason": ""
}
```

Required fields:

- `adapter`: stable adapter name.
- `source_id`: configured source id.
- `native_session_id`: source-native session id.
- `native_artifact_id`: stable source-native artifact id. It must be stable across repeated scans of unchanged source data.
- `class`: one artifact class from this spec.
- `availability`: one availability state from this spec.
- `selector`: source-native location of the exact logical artifact.
- `hash_domain`: one hash domain from this spec when the artifact has bytes or
  identity fields.
- `bytes` / `computed_sha256`: required when `availability=available`,
  `source_empty`, `partial_source`, or `redacted` and the artifact has bytes.
- `chars`: required only for text artifacts; omitted or `-1` for binary
  artifacts.

`producer_sha256` records a hash supplied by the source, when present. The
source extractor also computes `computed_sha256` over the gate's logical bytes.
If both are present and differ, the source artifact is `source_corrupt` and the
run is `INCOMPLETE`. The canonical extractor always compares against
`computed_sha256`, not blindly against a producer-supplied hash.
Producer-declared byte lengths are integrity metadata too. When a source payload
descriptor declares uncompressed or stored/compressed byte counts and the
resolved source bytes disagree, the source artifact is also `source_corrupt`.
This prevents a producer hash omission or collision of metadata paths from
hiding a truncated, appended, replaced, or stale payload file.

Every artifact with `availability=source_corrupt` MUST include at least one
`integrity_failures[]` entry. Each entry identifies the failed source-integrity
field and the producer-declared value versus the resolved source value:

```json
{
  "field": "original_bytes",
  "expected": "108300",
  "actual": "209998"
}
```

Allowed fields are adapter-specific but must use stable lower-snake-case names
such as `original_bytes`, `compressed_bytes`, and `sha256`. The diff engine
copies these failures into the `source_corrupt` finding so a live gate result
proves the exact corruption class without exposing payload bodies. A generic
`source artifact is corrupt` message without typed failure evidence is not a
valid SOW-0097 diagnostic.

The manifest must not contain raw private content. Tests use sanitized fixtures. Live runs emit only ids, selectors, lengths, hashes, and mismatch metadata.

### Selector Grammar

Selectors identify the exact logical artifact, not just a containing transcript.
All extractors use the same normalized selector grammar:

- `uri`: normalized source location. File URIs are absolute
  `file:///<path>` URIs with root-containment checks. JSONL line anchors use
  `#L<n>` with 1-based line numbers. SQLite selectors use an adapter-specific
  read-only URI such as `opencode-sqlite://?part_id=<id>&field=<field>` or
  `opencode-sqlite://?input_id=<id>&field=prompt.text`; array indexes in
  opencode field paths are decimal path tokens, e.g.
  `prompt.files.0` for the first persisted prompt file object.
- `json_pointer`: optional RFC 6901 pointer into the selected JSON value. Array
  indexes are decimal without leading zeros. Missing keys, ambiguous pointers,
  or pointers resolving to a different type than the artifact class expects are
  failures.
- `field_path`: optional adapter-native field selector for non-JSON-pointer
  stores such as opencode's `part.data` fields.
- `byte_range`: optional `[start,end)` byte offsets when a source format gives a
  stable byte slice for the exact artifact.

`payload_refs.location_uri` alone is sufficient only when it already identifies
one standalone logical artifact. A line selector such as `file://...#L42` is not
enough for a nested JSON field; the canonical artifact must also carry a JSON
pointer or equivalent selector metadata.

For `log_entry` artifacts represented in `log_entries`, selector metadata may
live in `extras_json.aiViewer.parity` with `nativeArtifactId`, `selectorURI`,
and `jsonPointer`. Without that metadata, the canonical extractor falls back to
a deterministic `log://...` selector derived from the row identity; that fallback
is valid only for derived logs that are not claiming parity with one exact
source field.

### Hash Domains

The parity hash is always computed over uncompressed logical artifact bytes.
Compressed storage bytes are never the parity hash domain.

| Domain | Rule |
|---|---|
| `semantic_text` | Decode the source string value and hash its UTF-8 bytes. JSON string quotes, escaping, and surrounding object syntax are not part of the hash. |
| `canonical_json` | Parse a source JSON object/array/value and hash the project's deterministic canonical JSON encoding: UTF-8, sorted object keys, no insignificant whitespace, no HTML escaping. Re-serializing with an unstable encoder is forbidden. |
| `raw_bytes` | Hash the exact uncompressed bytes of a standalone source artifact, such as a payload file, HTTP envelope, SSE stream, binary attachment, or exact byte range. |
| `identity_json` | For non-payload artifacts, hash the deterministic canonical JSON encoding of the identity fields listed in the class shape. |

Every adapter availability matrix must state the hash domain for each class. If
the source format cannot provide or reconstruct bytes for a source-visible
artifact, the artifact is not silently skipped; it is `source_unavailable`,
`redacted`, `compacted_away`, or `unverifiable` with evidence.

Source extractors that resolve standalone payload files use the same default
1 GiB per-artifact safety cap as the canonical resolver. The cap applies before
materializing compressed storage bytes and again after decompression; exceeding
it is a source-extractor error and an incomplete parity run, not an available
artifact.

### Canonical Artifact

The canonical extractor reads SQLite and payload refs after ingestion. It emits `CanonicalArtifact` records shaped like the source manifest, plus canonical evidence:

```json
{
  "schema_version": 1,
  "adapter": "codex",
  "source_id": "codex:<root>",
  "session_id": "<canonical-session-id>",
  "turn_id": "<canonical-turn-id>",
  "op_id": "<canonical-op-id>",
  "payload_ref_id": 123,
  "native_session_id": "session-123",
  "native_artifact_id": "line:42:/msg/content/0/text",
  "class": "assistant_message",
  "availability": "available",
  "hash_domain": "semantic_text",
  "selector": {
    "uri": "file://<root>/2026/06/22/rollout-abc.jsonl#L42",
    "json_pointer": "/msg/content/0/text"
  },
  "bytes": 1482,
  "chars": 1482,
  "computed_sha256": "<hex>",
  "payload_ref_id": 123,
  "synthetic": false,
  "synthetic_reason": ""
}
```

Canonical artifacts may be built from:

- `sessions`, `turns`, and `ops` rows for boundaries, status, errors, and links.
- `payload_refs` rows plus read-only payload resolution for content artifacts.
- `log_entries` rows only when the adapter spec says the source artifact is intentionally represented as a log artifact.

For `session_boundary` lineage, the canonical extractor uses resolved
`sessions.parent_session_id` / `sessions.root_session_id` when those foreign
keys exist. If an out-of-order or partial corpus leaves a native lineage edge
unresolved, the extractor may use the writer-stashed
`sessions.extras_json.aiViewer.parentNativeId` and `rootNativeId` as evidence
for the native parent/root ids. This preserves source-native parity without
inventing missing session rows; the unresolved database foreign keys remain
unresolved until the ingester resolver can link them.

If the extractor cannot prove the exact logical source artifact because selector, length, or hash data is absent, it emits an `unverifiable` finding. A row that exists but cannot be verified is not a pass.

### Canonical Artifact Shapes By Class

Payload-like classes (`user_prompt`, `user_image`, `assistant_message`,
`reasoning_text`, `llm_request`, `llm_response`, `llm_sdk_request`,
`llm_sdk_response`, `tool_request`, `tool_response`, `log_entry`) use selector,
length, and hash proof. They may come from `payload_refs` or from `log_entries`
only when the adapter matrix explicitly states that the source artifact is
represented as a log artifact. Adapter specs may also name a canonical extras
field as the exact source artifact proof for a narrow class; aiagent_v2
`reasoning.final` is represented by the synthetic reasoning op's
`ops.extras_json["reasoning.final"]`, and aiagent_v2 `finalReport` is
represented by the session row's `sessions.extras_json["final_report"]`.
Both require matching source/canonical selectors and hashes; `reasoning.final`
uses `hash_domain=semantic_text`, while `finalReport` uses
`hash_domain=canonical_json`.

Canonical payload kind names are adapter-facing strings, so the parity extractor
normalizes known aliases before assigning artifact classes. In particular,
aiagent_v3 persists SDK payload refs as `sdk_request` / `sdk_response`; those
map to parity classes `llm_sdk_request` / `llm_sdk_response`. The canonical
aliases `llm_sdk_request` / `llm_sdk_response` remain valid for adapters that
emit the longer names directly.

Canonical op rows can also emit class-specific structural artifacts beyond the
generic `op_boundary` when the source format carries required metadata. For
Claude Code, `kind=compaction,name=compaction` ops emit `compaction_event`
artifacts from `ops.bytes_in`, `ops.bytes_out`, timing fields, and compact
metadata in `ops.extras_json`.

Structural classes use `identity_json` over required fields:

| Class | Required identity fields |
|---|---|
| `session_boundary` | `native_session_id`, `parent_native_session_id`, `root_native_session_id`, `kind`, `status`, `started_at`, `ended_at` when the source has them. |
| `turn_boundary` | `native_session_id`, `native_turn_id` or source pivot id, turn sequence, status, start/end timestamps where present. |
| `op_boundary` | `native_session_id`, turn sequence, op sequence, translated canonical op kind/name, status, start/end timestamps where present. |
| `llm_error` / `tool_error` | owning native op id when the source ties the error to an op, otherwise the documented native turn/session error pivot, status, error class, error message hash, and timestamp where present. |
| `subagent_link` | parent native session/op/tool-use id, child native session id, link kind, and source selector for the field carrying the relation. Direction is part of the identity. |
| `system_op` | native op id, system subtype/name, status, timestamp, and source-visible metadata fields. |
| `compaction_event` | native op/event id, trigger/subtype, pre/post token counts, duration, timestamp, and summary selector when present. |
| `session_metadata` | native session id plus the adapter-defined persisted metadata fields and value hashes that are not already proven by `session_boundary`. |
| `attachment_metadata` | native attachment id, persisted attachment type, filename/display path where present, and source selector. Attached file/image content is not implied unless the adapter matrix explicitly says a payload artifact exists. |
| `patch_metadata` | native patch id, owning native session/turn/op, source selector, patch content hash when present, changed-file count, and hash over the deterministic changed-file list. Raw changed-file paths are not part of parity identity JSON. |
| `source_corruption` | native source-corruption id, corrupted source selector, byte range when known, and source file/session context recovered before the corruption. |

Structural artifacts still get `native_artifact_id`, `hash_domain=identity_json`,
and `computed_sha256`. A structural artifact with no bytes is not exempt from
matching; its identity JSON is the matchable proof.

Structural identity fields are source-native. If canonical storage has an
unresolved structural FK because a related source artifact is absent, but the
adapter preserved the source-native id in documented canonical metadata, the
canonical manifest extractor MUST use that preserved native id for parity
identity. Falling back to an empty parent or self-root identity while the
source-native parent/root id is present in canonical metadata is an
unverifiable canonical artifact.

`source_corruption` is parity-only evidence, not a canonical event class. It
uses `availability=source_corrupt`; the diff records a `source_corrupt` finding
and does not require a canonical match for that artifact. When the corrupt byte
range is known, the source artifact carries `hash_domain=raw_bytes`, the exact
selector byte range, `bytes`, and `computed_sha256` over the corrupt bytes so
the report proves which source fragment was rejected.

## Availability States

| State | Meaning | Gate behavior |
|---|---|---|
| `available` | Source contains the artifact. | Must match exactly one canonical artifact. |
| `source_unavailable` | The source record proves an artifact logically existed, but the source does not carry retrievable bytes for it. | Allowed only when the adapter spec documents the source limitation. When emitted as a concrete artifact, it must still match exactly one canonical `source_unavailable` artifact with the same native id, class, and selector/metadata evidence. |
| `source_empty` | Source explicitly carries an empty artifact and emptiness is valid for that artifact class. | Canonical must preserve the empty artifact with `bytes=0`, `chars=0` for text (`chars=-1` for raw/binary classes), and `computed_sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. Dropping it fails. |
| `partial_source` | Source itself marks the artifact as partial/truncated, or the adapter spec documents that the source proves only a strict subset of the canonical artifact identity. | Canonical must preserve the partial/truncated marker or documented partial-identity marker. |
| `redacted` | Source explicitly says content existed but was redacted before ai-viewer could read it. | Canonical must preserve the redacted marker and any source-provided metadata. It must not invent content. |
| `compacted_away` | Source explicitly indicates earlier content was summarized/compacted away before it became available as individual artifacts. | Canonical must preserve the compaction event and summary artifact; individual pre-compaction content is not required unless the source still carries it. |
| `source_corrupt` | Source record or producer hash is inconsistent/corrupt. | Run is `INCOMPLETE`, never `PASS`; canonical parity is not judged for that artifact. Recoverable corruption must emit a `source_corruption` artifact in addition to `errors[]` so findings are machine-countable. Line-delimited or row-delimited formats treat malformed JSON inside a known record/row boundary as recoverable corruption and continue with later records. File-delimited snapshot formats treat a corrupted bounded file, such as a zero-byte or invalid-gzip aiagent_v2 snapshot, as recoverable corruption for that file and continue with later files. Resource-safety failures such as over-cap files remain hard errors. Unknown native discriminators are schema drift, not corruption; they remain hard extractor errors so new source-visible artifact types cannot be ignored. There is no alternate "clean except source corruption" pass state; source corruption is acceptable only as a fail-closed diagnostic result with typed evidence. |

Synthetic artifacts are not an availability state. They are canonical-only helper
artifacts with `synthetic=true` and a required `synthetic_reason`.

Allowed synthetic reasons:

- `turn_synthesis`: canonical turn boundary derived from documented source pivot
  records.
- `status_inference`: canonical final status derived from documented source
  absence/timeout rules.
- `linkage_resolution`: canonical relationship derived from documented sidecar or
  cross-file source evidence.
- `orphan_repair`: documented adapter rule for a child/source sidecar whose
  parent transcript is absent.
- `adapter_helper`: non-source helper required for UI grouping; must never match
  a source artifact.

Synthetic canonical artifacts must use a `native_artifact_id` prefix
`synthetic:<reason>:`. A synthetic artifact whose id collides with a source
artifact id is a failure.

Missing mapper support is never `source_unavailable`. If the source extractor
sees a concrete metadata-only artifact, such as an uncaptured payload ref, the
canonical side must preserve that absence explicitly. A missing canonical
metadata-only artifact is a parity failure, not a waiver.

## Matching Algorithm

The diff engine matches on this exact key:

```text
(schema_version, adapter, source_id, native_session_id, class, native_artifact_id)
```

`native_turn_id` and canonical row ids are evidence, not primary keys. A
canonical extractor must synthesize the same `native_artifact_id` from canonical
selector metadata that the source extractor emits from source data. If two
artifacts share `(adapter, source_id, native_session_id, native_artifact_id)` but
disagree on `class`, the diff emits one `class_mismatch` finding instead of a
separate missing/extra pair.

Class translations are allowed only when the adapter matrix documents them. For
example, a source `user_prompt` currently represented as canonical
`kind='internal', name='user_input'` is still matched as artifact class
`user_prompt`; the canonical op taxonomy does not define parity class identity.

## Diff Rules

The parity diff is fail-closed:

- Source artifact with no canonical match: failure. This includes concrete
  `source_unavailable` artifacts emitted for uncaptured or metadata-only source
  records; the canonical side must preserve the explicit absence.
- Canonical artifact with no source match: failure unless `synthetic=true` with documented reason.
- More than one canonical artifact matching one source artifact: failure.
- Class mismatch for the same source-native artifact id: failure.
- Hash mismatch: failure.
- Byte/character length mismatch: failure.
- `available` source artifact represented as empty/blank canonical content: failure.
- `source_empty` source artifact dropped from canonical: failure.
- `partial_source` source artifact ingested without partial marker: failure.
- Adapter spec says `source_unavailable`, but source extractor emits `available`: failure.
- Canonical artifact lacks exact selector, length, or hash needed for proof: failure.
- Payload resolver returns a preview/truncated body while the gate needs full bytes: failure.
- Source extractor parse errors make the run `INCOMPLETE`, never `PASS`. A
  recoverable parse error in one record/file must not hide other valid source
  data: the extractor records the error, continues where the source format can
  safely continue, and the runner still compares every source and canonical
  artifact it could extract. Fatal errors such as context cancellation,
  unreadable roots, containment failures, or corrupted stream state may stop the
  extractor immediately, but they still return `INCOMPLETE`.
- Source record with no mapped artifact and no explicit ignored-record rule:
  failure.
- Turn status/order mismatch, op order mismatch, subagent parent/child inversion,
  missing parent op, or missing child session: failure.
- Runner timeout applies to diffing as well as source/canonical extraction. The
  diff engine checks context while indexing and comparing artifacts and must not
  pre-sort full source/canonical artifact lists before observing cancellation.
  Deterministic output is achieved by sorting the final finding list, not by
  sorting millions of passing artifacts before comparison.

Finding severities:

| Severity | Examples | Blocks |
|---|---|---|
| P0 | Missing available user prompt, user image, assistant message, reasoning, LLM/tool request/response, or subagent link; corrupted/truncated canonical content. | CI fixture gate and live full gate. |
| P1 | Missing error metadata, duplicate artifacts, hash-domain mismatch, or unverifiable payload selector/hash where content exists. | CI fixture gate and live full gate. |
| P2 | Source-unavailable matrix mismatch, synthetic artifact without documented reason, weak status/timestamp parity. | CI fixture gate unless explicitly documented in the active SOW. |
| P3 | Diagnostic/reporting clarity issues that do not affect the proof. | Document; does not block by itself. |

## Source Extractors

Each supported adapter has an independent source extractor:

- The extractor may reuse low-level source parsers that decode bytes into source-native structs only when those parsers expose every source record and do not apply mapper cursor/dedup/filter decisions.
- The extractor must not call adapter mapper functions that emit `canonical.Event`.
- The extractor must not read canonical SQLite rows.
- The extractor must emit stable artifact ids and selectors.
- The extractor must open source files read-only. Source SQLite databases use
  `?mode=ro` and build each source result from a single read-only
  transaction/snapshot.
- The extractor must parse all source records in scope, including records the
  mapper would skip because of cursor position, deduplication, or recovery.
- The extractor emits source-record accounting per file/table: every top-level
  JSONL line, JSON record, snapshot node, or SQLite row is classified as
  `mapped`, `ignored_with_reason`, `source_unavailable_evidence`, or
  `parse_error`. Unknown record types are failures until the adapter matrix
  documents them.
- Runtime adapter tolerance does not exempt the parity extractor. A live adapter
  may warn and continue on forward-compatible native records to keep ingestion
  available, but the parity proof must fail closed until the new native record
  shape is mapped or explicitly ignored with evidence and tests.
- File-oriented extractors continue after a recoverable per-file error when the
  next file can still be parsed independently. They return the partial manifest
  plus the accumulated error list. Runner output must include non-zero artifact
  counts and any diff findings from the partial manifest while keeping the
  source result `INCOMPLETE`.

Adapter-specific selector requirements:

| Adapter | Required selector shape |
|---|---|
| `aiagent_v3` | Ledger record identity plus payload file path/ref metadata. Producer-provided `sha256`, `originalBytes`, and compressed/stored bytes are used when present; missing hashes are computed from logical payload bytes. |
| `aiagent_v2` | Snapshot identity plus payload ref path/metadata when present. Captured producer refs use the same file-relative native id as canonical payload refs (`file:<relative path>`). Uncaptured/pathless producer refs use the stable op/kind ordinal id (`op:<turn>:<op>:payload:<kind>:<ordinal>`). Inline request/response payload artifacts use `file://<snapshot>.json.gz?json_pointer=<pointer>` with `Compression=gzip`; their native id is `file:<snapshot basename>:<pointer>` so source and canonical manifests compare the exact logical fragment. |
| `claude-code` | JSONL file plus line number and JSON pointer into the record. Whole-transcript refs alone are not sufficient for exact fragment parity. |
| `codex` | JSONL rollout file plus line number and JSON pointer into the record. Existing `file://...#L<line>` refs are the file/line layer; parity also needs the field pointer for exact logical bytes. |
| `opencode` | SQLite table/row identity. `part_id` plus field path (`text`, `state.input`, `state.output`) resolves from `part.data`; `input_id` plus prompt field path (`prompt.text`, `prompt.files.<index>`) resolves from `session_input.prompt`. Both selectors use `opencode-sqlite://?...&field=<field>` and both source and canonical manifests resolve the selected field read-only before hashing. |

## Canonical Extractor

The canonical extractor:

- Reads `sources`, `sessions`, `turns`, `ops`, `payload_refs`, and `log_entries`.
- Uses a dedicated parity resolver that follows the same containment and
  read-only source rules as the presenter but never uses UI preview caps.
- Computes SHA-256 locally when the canonical payload ref lacks a source-provided hash.
- Emits `unverifiable` when a canonical artifact points only to a containing file with no exact selector.
- Reports canonical evidence: source id, canonical ids, payload ref id, selector, lengths, and hashes.
- Does not mutate SQLite or source files.

The parity resolver reads exact logical bytes for the selector and fails closed
on truncation. Its default safety cap is 1 GiB per artifact, configurable for
local diagnostics; exceeding the cap is `INCOMPLETE`, not `PASS`. The cap is
enforced before materializing a whole-file selector and again after gzip
decompression, so compressed payloads cannot expand without bound during proof
calculation. It decompresses before hashing. For `file://...#L<n>` line-anchor
selectors, it reads with the same bounded-line discipline as the source
extractors: no line read may exceed 16 MiB before selector resolution, and an
oversized selected or skipped line is an `unverifiable` canonical artifact /
incomplete parity run, not a JSON decode fallback and not an unbounded
allocation. The cap is intentionally above observed live Codex rollout lines of
about 14 MiB while still bounding pathological allocations. The resolver applies
RFC 6901 JSON pointers for JSON selectors, resolves
opencode `opencode-sqlite://?part_id=<id>&field=<field>` selectors by reading
`part.data`, and `opencode-sqlite://?input_id=<id>&field=prompt.text`
selectors by reading `session_input.prompt`, from the configured source
database in read-only mode. It then applies the hash-domain rules above. It
must not call the presenter preview resolver for hash computation.

Within one canonical extraction run, `file://...#L<n>` selectors build a
bounded line-offset index once per file, then read selected lines by offset
instead of re-scanning from the start of the file for every payload ref. Repeated
selectors for the same file and line are resolved once before JSON-pointer
projection and cached for reuse. This preserves exact hashing while avoiding
O(payload_refs * line_number) rescans when many canonical payload refs point at
JSONL records in the same session file. The cache key is the normalized absolute
file path, line anchor, and configured line-size cap; cached bytes are the exact
trimmed source line, not the projected JSON field.

For existing-DB live checks scoped to one or more source ids, canonical
`log_entries` extraction must use source-scoped indexed query branches: direct
source logs (`log_entries.source_id IN (...)`) and session-derived logs
(`sessions.source_id IN (...) AND log_entries.source_id IS NULL`). A scoped live
query must not join `sources` through `COALESCE(log_entries.source_id,
sessions.source_id)` in a way that scans unrelated log rows before filtering.

## Gate Modes

### One-Shot Runner Contract

`ai-viewer-ingest check-parity` is the executable entry point for the parity
engine.

```bash
ai-viewer-ingest check-parity \
  --source <adapter:path> \
  [--source <adapter:path> ...] \
  [--db <canonical-db>] \
  [--concurrency <n>] \
  [--timeout <duration>] \
  [--max-findings <n>] \
  [--json]
```

For each source, the command independently extracts source artifacts from the
native source path. It then builds the canonical side in one of two ways:

- Full-parity mode (`--sample 0`) streams source artifacts directly into the
  disk-backed diff index for every source extractor that exposes a writer API.
  Every adapter source extractor is required to expose a writer API for full
  existing-DB checks. File/snapshot extractors (`aiagent_v2`, `aiagent_v3`,
  `claude-code`, and `codex`) walk source files in deterministic path order,
  write each file's artifacts before moving to the next file, and must not build
  a whole-root source `[]Artifact`. The opencode extractor streams SQLite
  sessions in deterministic `session.id` order, writes each session's artifacts
  before advancing, loads only that session's relationship rows
  (`message`, `part`, `session_input`, and `session_message`) into scoped
  in-memory indexes, and resolves parent-root links by querying the parent chain
  on demand. It must not preload whole-source relationship tables before
  emitting artifacts.
  Source extractors must avoid repeated whole-record parsing while proving
  payload artifacts. When a record carries multiple nested artifacts, the
  extractor decodes the source record or payload document once for that record,
  discovers all relevant selectors from that decoded document, and resolves the
  selected values from the same decoded document before writing artifacts.
  Re-decoding the same JSON line once per emitted pointer is not acceptable for
  live full-parity gates over multi-million-artifact corpora.
- With no `--db`, the command scans the source through the real adapter into a
  temporary SQLite database, then streams canonical artifacts from that database
  into the same disk-backed diff index used for source artifacts. This is the
  fixture/CI mode and may write only to a temp directory outside the repository.
  In full-parity mode (`--sample 0`), it must not first build source or
  canonical whole-source `[]Artifact` manifests before diffing.
  For filesystem-backed adapters (`aiagent_v2`, `aiagent_v3`, `claude-code`,
  and `codex`), the no-DB runner first freezes the configured source into a
  temporary read-only source image. The independent source extractor and the
  real adapter/temp-canonical scan both read that same frozen image, while the
  result `source_id` and reported source location remain the configured original
  source. This proves parity for the captured image and prevents a live append
  from comparing source artifacts from one filesystem version against canonical
  artifacts from another. The freeze step opens source files read-only, copies
  only reachable regular files, records the original source fingerprint used by
  resume/changed-since cursors, and fails closed if a file cannot be copied or
  changes while it is being copied. Files created after the source image is
  frozen are outside that captured proof and are checked by the next run. The
  opencode adapter is not frozen this way because it uses SQLite read snapshots.
  The temp canonical scan is an ingestion run: adapter parse errors, ingester
  batch errors, resolver errors, read-model backfill errors, or temp DB write
  errors make the parity source result `INCOMPLETE`. A temp canonical scan must
  drain every submitted event batch, then run the same synchronous orphan/linkage
  resolver pass production uses before canonical extraction starts. This is
  required for source-visible parent/child and op/child relations such as
  Claude Code subagents: extracting canonical artifacts before resolver linkage
  produces a false `missing_canonical subagent_link` result from an incomplete
  temp database. A temp canonical scan must never log-and-continue after
  dropping events; a partial temp DB is not valid canonical proof.
  In diagnostic sample mode (`--sample > 0`) for file-backed adapters whose
  adapter cursor can skip unchanged files, the temp canonical scan must use a
  sampled scan cursor that marks unsampled files consumed when their stat
  identity is stable in the frozen image. At minimum this applies to Codex
  rollouts and aiagent_v2 root `.json.gz` snapshots. Sample mode still returns
  `SAMPLE ONLY`, never `PASS`, but it must not scan hundreds of thousands of
  unrelated unsampled files just to validate one sampled artifact.
- With `--db`, the command opens the existing canonical SQLite database
  read-only, filters canonical artifacts to the configured `source_id`, and
  diffs them against the source manifest. This detects stale, missing, extra, or
  unverifiable rows in an already-ingested database without mutating it. The
  canonical extractor must apply that `source_id` scope in SQL before resolving
  payload refs; it must not materialize unrelated sources and filter them in
  memory. A broken, huge, or private unrelated source in the same canonical DB
  cannot make a focused live check incomplete. In full-parity mode
  (`--sample 0`), canonical artifacts from an existing DB stream directly into
  the disk-backed diff index under the same pinned read-only snapshot; the
  runner must not first build a canonical `[]Artifact` for that source.
  Fixture-sized helper APIs may still materialize a canonical slice because they
  are not the executable live full-parity path.
- `--timeout <duration>` bounds the whole extraction/diff run. The default is
  `30m`, matching the first live performance target. `0s` is a valid immediate
  deadline used by deterministic timeout tests. A timeout during extraction or
  verification returns `INCOMPLETE` and exit code `1`, never a usage error and
  never `PASS`.
- JSON output includes per-source `stage_timings_ms` for live diagnostics. The
  map keys are `capture_source_snapshot`, `extract_source_manifest`,
  `extract_canonical_manifest`, and `diff_manifests`. Existing-DB checks and
  non-frozen source checks also include `verify_source_snapshot` for the
  post-extraction source verification stage. Frozen no-DB filesystem checks do
  not include `verify_source_snapshot`; their source-image copy is timed under
  `capture_source_snapshot`. Runs that build a temporary canonical DB also
  include `scan_temp_canonical_db` for the real-adapter scan/ingest step and
  `extract_canonical_artifacts` for streaming canonical artifacts from that temp
  DB into the diff index. `extract_canonical_manifest` remains the total outer
  timing for the whole canonical side, so live diagnostics can compare total
  canonical cost with its temp-scan and canonical-read sub-stages. Each completed
  or failed stage records elapsed wall-clock milliseconds before the result is
  emitted, so timeout cascades identify the stage that consumed the budget
  without exposing source content.
- `--max-findings <n>` caps detailed findings emitted per source and at the
  top level. The default is `200`; `0` emits only summary counts. The result
  always includes total finding counts and grouped counts by severity, code, and
  class, so limiting detail cannot hide the fact that parity failed.
  The diff engine applies this cap while accumulating findings: it may retain
  only the first `n` detailed finding records, but it must still count and group
  every finding it observes. It must not build an uncapped multi-million-finding
  slice merely to truncate it during output serialization.
  The executable runner's diff phase is disk-backed: artifacts are written to a
  temporary SQLite index under the parity work directory and compared from that
  index. It must not build source/canonical match maps whose memory grows with
  artifact count. Fixture tests may still pass slice-backed readers into that
  disk-backed engine, but the comparison logic itself must use the disk index.
  The diff database must optimize for large streaming writes: lookup indexes on
  artifact keys are created only after all source and canonical artifacts have
  been written and the insert transaction has committed. The comparison phase
  may rely on those indexes, but the streaming write phase must not maintain
  them row-by-row for multi-million-artifact runs.
- `--concurrency <n>` bounds parallel top-level source checks. The default is
  `1` to preserve deterministic single-source diagnostics; higher values run up
  to `n` configured sources at once while preserving the requested source order
  in output. The value must be positive. Source extractors that later gain
  session-level streaming may share the same budget, but this first control is
  source-level only.
- `--sample <n>` is a diagnostic mode only. `0` (the default) means full parity.
  A positive value streams source artifacts through a bounded sampler that keeps
  the first `n` non-corruption artifacts by the stable parity key. Any
  `availability=source_corrupt` artifact is retained in addition to those `n`
  artifacts and does not count against the sample limit, because a sampled
  diagnostic that reports corruption only in `errors[]` is not machine-countable.
  The runner then streams the sampled source artifacts plus retained corruption
  artifacts into the disk-backed diff index and restricts the canonical side to
  the same exact source keys plus class-mismatch candidates for those native ids.
  The canonical extractor must apply that sampled-key filter before resolving
  payload bytes, and must skip unrelated payload refs before mapping their
  adapter-facing kind into a parity class when the classless key is not sampled.
  A diagnostic sample therefore cannot read, classify, or fail on a broken
  payload row outside the sampled native ids, but source-corrupt artifacts that
  are discovered while extracting the sampled source manifest prefix must still
  be reported. For very large whole-file snapshot formats, aiagent_v2 sample
  mode may stop source extraction after the deterministic source-order prefix
  has retained `n` non-corruption artifacts. This keeps the diagnostic
  operationally usable on hundreds of thousands of snapshots; it is also why
  `SAMPLE ONLY` is never evidence of global source-corruption absence. For
  no-DB sampled runs, file-backed adapters may also pass a source-format resume
  cursor into the temporary canonical scan so the adapter ingests only source
  files that contain sampled artifacts. When the no-DB runner froze the source,
  this cursor is built against the frozen image paths because those are the
  paths both sides read for the captured proof. SQLite-backed adapters cannot
  always express a sampled subset as a monotonic cursor; for opencode, no-DB
  sample mode derives the sampled native session ids from the retained source
  artifacts and invokes an adapter-owned diagnostic session scan that loads and
  maps only those session trees through the same production mapper used by the
  full adapter scan. This diagnostic-only session scan must not run in
  `--sample 0` full-parity mode and must not silently fall back to a full
  opencode backfill when sampled session ids are present. Findings inside the
  sampled set and retained corruption artifacts are still reported, but a
  sampled run that completes without extraction errors returns state
  `SAMPLE ONLY` and exits non-zero even if the sampled diff has no findings.
  `SAMPLE ONLY` is never accepted as proof of full parity. Extraction errors,
  timeout, freeze-copy failure, or existing-DB snapshot mutation still return
  `INCOMPLETE` because the sample itself was not fully trustworthy.

When source or adapter extraction reports recoverable parse errors after
emitting some artifacts, the command still extracts and compares the other side
where possible. The source result remains `INCOMPLETE`, `errors[]` records every
extraction error, and `source_artifacts`, `canonical_artifacts`,
`total_findings`, summaries, and capped findings reflect the partial comparison.
This prevents one corrupt historical file from hiding unrelated missing,
extra, or mismatched artifacts in valid files. A partial comparison is never a
pass and never satisfies live full parity.

When source snapshot verification reports mutation in an existing-DB check, the
command follows the same partial-result rule: it reports whatever
source/canonical artifacts and capped findings were produced, but the source
state remains `INCOMPLETE`. A mutation is a deterministic-run failure, not proof
that ingestion is wrong and not proof that ingestion is correct. No-DB
filesystem-backed checks avoid this post-extraction ambiguity by comparing the
source extractor and temp canonical scan against the same frozen source image.

Exit codes:

- `0`: every requested source returns `PASS full parity`.
- `1`: at least one source returns `FAIL parity`, `INCOMPLETE`, or
  `SAMPLE ONLY`.
- `2`: invalid CLI usage, unknown adapter, missing source, or unreadable path.

Default output is a compact human summary with no raw private content. `--json`
emits the machine-readable result shape: top-level `state`, one result per
source, artifact counts, total finding counts, grouped finding summaries, and a
capped detailed finding list. It still excludes raw payload bodies.
The human summary must display the real `total_findings` count, not the number
of retained detailed findings after `--max-findings` capping, and must include a
compact grouped finding summary when findings exist. A run with
`--max-findings 0` therefore still visibly reports source-corruption and
mismatch counts instead of printing `findings=0`.
By default, both human and JSON output redact source ids, source locations,
native session ids, and native artifact ids with deterministic hash tokens so a
live run can be shared locally without exposing absolute roots or private native
identifiers. Redaction applies to structured finding fields and to source
extractor error strings that embed native row/session/part/session-input
identifiers. When redacting known raw tokens inside free-form errors, the CLI
replaces longer raw tokens before shorter ones so prefix-related ids cannot leave
suffix fragments in default output. Passing `--debug-ids` preserves the raw ids
for local debugging and must be an explicit operator action.

The first SOW-0097 CLI slice is allowed to hold fixture-sized manifests in
memory for helper APIs and tests. The full live gate remains bound by the
streaming, snapshot, resume, changed-since, timeout, and memory controls below
before the SOW can close.

Implementation boundary: `internal/parity` owns pure manifest structs, source
extractors, canonical extraction, and diffing. The executable runner that scans
sources into temporary stores or opens an existing canonical DB lives outside
that pure package (currently `internal/paritycheck`) so `internal/parity` does
not import the production ingester and create test/import cycles.

### CI Fixture Mode

`scripts/check-ingestion-parity.sh --fixtures` runs the deterministic
source-to-canonical parity fixture gate. It is a wrapper over the parity Go
tests, the `internal/parity` fuzz seed corpus, and CLI fixture tests so local
and CI invoke the same named gate instead of relying on scattered ad hoc package
commands. Fixture tests may construct sanitized source trees in temp directories
or read committed sanitized `testdata/` fixtures; they must never read private
live sources.

Requirements:

- All five adapters have one positive fixture for every artifact class the
  adapter matrix marks `available`, `source_empty`, `partial_source`, `redacted`,
  or `compacted_away`.
- Every artifact class has positive and negative coverage across the corpus.
- Every adapter has negative coverage for corrupted canonical data, corrupted
  source data, source-side missing artifacts, duplicate canonical artifacts,
  class mismatch, matrix mismatch, unverifiable selectors, and source-record
  accounting gaps.
- A clean fixture diff exits 0.
- Any P0/P1/P2 mismatch exits non-zero.
- Missing fixture, malformed manifest, source parse error, payload read error, or malformed canonical DB exits non-zero.
- Source extractors and the diff engine have deterministic seed-corpus fuzz
  tests in `internal/parity`; the fixture wrapper runs them with
  `go test -run='^Fuzz' ./internal/parity`.
- The wrapper exits non-zero when the underlying Go/CLI parity test command
  fails, when invoked without `--fixtures`, or when a required parity package or
  test surface is absent.

### Live Full Mode

`ai-viewer-ingest check-parity --db <db> --source <adapter:path> ...` runs against the operator's live local sources.

Result states:

- `PASS full parity`: every reachable source artifact was checked and no blocking findings exist.
- `FAIL parity`: blocking findings exist.
- `INCOMPLETE`: one or more sources could not be scanned or verified.
- `SAMPLE ONLY`: diagnostic subset; never a full pass.

Live output includes no raw payload bodies by default. It may include short redacted previews only behind an explicit local flag.

Live full mode uses snapshot semantics:

- File sources record path, size, mtime, and file hash before extraction and
  verify them after extraction for existing-DB checks. Any changed file makes
  the run `INCOMPLETE`.
- No-DB filesystem-backed checks instead freeze the source into a temp source
  image before extraction, then read that image for both source and temp
  canonical manifests. The original source fingerprint is still recorded for
  resume and changed-since cursors, but source mutations after the freeze do not
  invalidate the captured-image proof.
- Source SQLite databases are read from a read-only transaction/snapshot. For
  opencode, schema introspection, deterministic session enumeration, per-session
  relationship loads, parent-chain lookups, and source-side payload field
  projection all use the same pinned query surface for one parity source result.
  The extractor may read the small ordered session header list before streaming
  per-session artifacts, but it must not mix session headers from one SQLite
  version with `message`, `part`, `session_input`, or `session_message` rows from
  another version.
- Existing canonical SQLite is read from one read-only transaction per checked
  source. The runner forces the SQLite snapshot immediately after beginning the
  transaction, before any canonical artifact query is allowed to run, and all
  session, turn, op, payload-ref, and log-entry extraction queries for that
  source use that same transaction. A live DB writer may continue ingesting in
  parallel, but one parity source result must never combine canonical rows from
  multiple DB versions. The gate records a source progress cutoff before
  extraction and excludes newer canonical rows from the diff once row-level
  cutoff metadata is available. The current schema only persists source-level
  `source_progress`; it does not carry row-level ingestion/progress timestamps on
  `sessions`, `turns`, `ops`, `payload_refs`, or `log_entries`. Until a future
  schema adds such metadata, live full mode relies on the pinned SQLite snapshot,
  source snapshot verification, source-level `--changed-since` diagnostics, and
  source-level resume.
- The run is deterministic: the same unchanged source + DB produces byte-identical
  sorted NDJSON manifests and findings.

The snapshot check is part of the runner contract, not a best-effort diagnostic.
For existing-DB checks over filesystem-backed sources, `check-parity` records the
reachable regular files in scope before source extraction starts, including path,
size, mtime, and SHA-256 over file bytes. After source extraction and
canonical-side construction finish, it walks the same scope again. A file that is
added, removed, changes size, changes mtime, or changes hash makes the source
result `INCOMPLETE` and adds a mutation error. The runner may still return
partial artifact counts and diff summaries from the data it already collected,
but mutation detection can never be downgraded to `FAIL parity` or
`PASS full parity`. For no-DB filesystem-backed checks, the equivalent guarantee
is the frozen source image: both sides read the copied regular files and the
runner fails closed when the copy cannot be produced from stable source bytes.

Live mode streams manifests as NDJSON into the OS temp directory or an explicit
`--work-dir` outside the repository tree. The diff phase uses a temporary
SQLite-backed artifact index and does not hold source/canonical match maps in
memory. Existing-DB and temp-DB canonical extraction stream artifacts into that
disk index for full-parity runs, so the canonical side is not bounded by
artifact count. All adapter source extractors also write artifacts into that
disk index for full-parity runs, so no adapter is bounded by a whole-root source
artifact slice in that mode. Opencode source extraction additionally scopes
relationship indexes to one session at a time, so the full opencode path is not
bounded by whole-source relationship table size. Diagnostic sample mode keeps at
most `--sample` source artifacts in memory plus bounded sampled-key indexes, then
uses the same disk-backed diff and filtered canonical streaming path. Memory must
be bounded by configuration, not by artifact count.

Required live controls:

- `--concurrency <n>` for bounded source-level parallelism. Session-level
  extractor parallelism may reuse the same configured budget when streaming
  extractors are introduced.
- `--timeout <duration>` with a default of `30m`. Timeout expiry makes the run
  `INCOMPLETE`; it does not produce a partial pass.
- `--resume <cursor-file>` for interrupted full scans. The first implementation
  is a source-level checkpoint: after each non-diagnostic source result reaches a
  terminal `PASS full parity` or `FAIL parity`, the runner writes that source's
  unredacted result plus its source snapshot fingerprint to the cursor file with
  mode `0600`. A resumed run may reuse that result only when the requested
  source's format, source id, location, and source snapshot exactly match the
  cursor entry. Reused sources are reported as skipped rows with the stored
  terminal state and artifact/finding counts. Snapshot mismatch, missing entries,
  changed source config, previous `INCOMPLETE`, diagnostic `SAMPLE ONLY`, or a
  corrupt cursor file forces a fresh check; corrupt cursor files make the run
  `INCOMPLETE` rather than silently starting over. `--resume` is incompatible
  with diagnostic modes such as `--sample` and `--changed-since`; it is not a
  row-level or mid-source resume mechanism.
- `--changed-since <duration|@cursor-file>` for diagnostic incremental scans.
  This is never a replacement for full parity. The duration form is a
  source-level live DB filter: it requires `--db`, computes a cutoff from the
  runner wall clock, and checks only sources whose `source_progress.updated_at`
  is at or after the cutoff. Sources with no `source_progress` row are treated as
  changed and are checked, because absence of ingest progress is itself
  unverified. Sources older than the cutoff are reported as skipped diagnostic
  rows. The cursor form uses an explicit `@` prefix, e.g.
  `--changed-since @/tmp/parity-resume.json`, so invalid duration strings remain
  usage errors instead of silently becoming paths. Cursor mode loads the
  source-level resume cursor and checks only sources whose format, source id,
  location, or source snapshot differs from the cursor entry; missing entries
  are checked. Matching entries are reported as skipped diagnostic rows with
  zero artifact counts. Missing cursor files mean no entries are known and all
  requested sources are checked; corrupt cursor files make the run `INCOMPLETE`.
  A clean changed-since run returns `SAMPLE ONLY`, never `PASS full parity`,
  because unchanged sources and row-level records are outside the checked set.
- `--sample <n>` for diagnostics only; a completed sampled run returns
  `SAMPLE ONLY`, never `PASS full parity`.
- `--allow-repo-output` is the explicit local override for writing parity
  working files under the current repository working tree. Without this flag, a
  configured `--work-dir` that resolves inside the detected repository root is a
  usage/configuration failure before the runner creates any temporary
  subdirectory, frozen source image, manifest diff database, or temp canonical
  database. The default path uses the OS temp directory and needs no override.

Initial performance target: full parity over the current local corpus should
complete in 30 minutes or less on the operator workstation with memory below
1 GiB. If the first implementation cannot hit this, SOW-0097 remains open until
the gate is made resumable and operationally usable; sample mode is not accepted
as proof.

### Runtime Mode

Runtime health integration is not required for the first SOW-0097 deliverable. A later SOW may persist latest parity results and surface them in `/api/health`. The first deliverable is the deterministic gate and CLI.

## Adapter Availability Matrix

Every adapter spec must include a parity availability matrix with one row per
artifact class. The implementation also exposes the matrix in a
machine-readable form consumed by the gate. The documentation table and the
machine-readable matrix must be tested for drift.

Matrix availability is not identical to runtime artifact availability. Runtime
availability states describe concrete artifacts emitted by a source or canonical
manifest. Matrix availability also needs to describe classes that a source
format does not expose at all, and classes that SOW-0097 has not closed yet.

Matrix-only states:

- `not_source_visible`: the native source format has no record or field for this
  artifact class. A source extractor must not emit artifacts for the class unless
  the adapter spec is updated first.
- `unknown`: SOW-0097 has not yet proven the source-format contract for this
  class. This state is allowed while SOW-0097 remains open, but a completed
  parity SOW cannot leave any adapter/class row as `unknown`.

All other matrix states reuse runtime availability names:
`available`, `source_unavailable`, `source_empty`, `partial_source`,
`redacted`, and `compacted_away`.

The machine-readable matrix lives in `internal/parity` and is part of the gate
contract. Each row records:

- adapter format name, matching the configured source format
  (`aiagent_v2`, `aiagent_v3`, `claude-code`, `codex`, `opencode`);
- artifact class;
- one or more matrix availability states;
- one or more hash domains, unless all states are `not_source_visible`,
  `unknown`, or pure `source_unavailable`;
- canonical representation summary;
- selector or identity rule;
- source/spec evidence.

The gate validates every emitted source and canonical artifact against this
matrix. An artifact whose adapter/class is missing from the matrix, whose
runtime availability is not allowed by the row, or whose hash domain is outside
the row is a `matrix_mismatch` finding. Matrix validation never replaces source
record accounting: if a source record carries an artifact but the extractor
fails to emit it, the source extractor or fixture coverage must still catch that
gap.

| Class | Source availability | Hash domain | Canonical representation | Selector / identity rule | Evidence |
|---|---|---|---|---|---|
| `user_prompt` | `available` / `source_unavailable` / `source_empty` / `partial_source` / `redacted` / `compacted_away` | `semantic_text` / `canonical_json` / `raw_bytes` / `identity_json` | op/payload/log representation | native id + selector formula | Evidence from source spec/code. |

Rules:

- `source_unavailable` requires source-format evidence.
- `not_source_visible` requires source-format evidence that the class is absent,
  not merely absent from today's mapper.
- Canonical parity extraction must also honor `not_source_visible`: it must not
  invent canonical artifacts for adapter/classes the source format cannot prove.
- "Mapper does not read it" is a bug, not a matrix exception.
- Unknown availability is allowed only while SOW-0097 implementation is in progress; it cannot remain in a completed SOW.
- Every class marked source-visible must define a `native_artifact_id` formula.
- Every class marked `available` must define selector and hash-domain rules.
- Every class marked `synthetic` on the canonical side must define allowed
  `synthetic_reason` values and non-colliding id prefixes.
- The matrix must include source-record accounting rules for ignored record
  types. "Unknown but ignored" cannot remain in a completed SOW.
- The matrix drift tests fail if any live adapter/class row still allows
  `unknown` or retains the default placeholder canonical representation,
  selector rule, or evidence text from the in-progress SOW builder.

## Security And Privacy

- No source writes.
- No outbound network calls.
- No raw private content in specs, SOWs, logs, or default gate output.
- Committed manifests use sanitized fixtures only.
- Hashes, source ids, native session ids, native artifact ids, and selectors are
  local verification metadata; do not publish live manifests outside the
  workstation.
- Default live output redacts absolute roots and hashes native identifiers before
  printing, including native identifiers embedded in source-extractor error
  strings. Full raw ids require an explicit `--debug-ids` local flag.
- The initial CLI emits no payload previews. If a future SOW adds previews, they
  must be opt-in local output, default to zero characters, have a small maximum,
  and be rejected in CI mode.
- The gate refuses to write live manifest/output files, frozen source images, or
  temp canonical/diff SQLite files under the repository working tree unless
  `--allow-repo-output` is supplied for a sanitized fixture test.
- Local parity work directories matching `ai-viewer-parity-*` are gitignored as
  defense in depth; live or diagnostic frozen source images must never be
  committed.
- Payload resolving must retain existing source-root containment checks.

## Completion Criteria

SOW-0097 cannot be completed until:

- Specs define the parity contract and adapter availability matrices.
- CI fixture parity gate exists and fails closed.
- Live full parity CLI exists and clearly distinguishes pass/fail/incomplete/sample.
- Every adapter has source and canonical manifest coverage for all artifact classes.
- Known source-available missing artifacts are fixed or remain blocking findings
  in the live gate. SOW-0097 may create follow-up adapter SOWs, but it is not
  complete while P0/P1 source-available losses are silently allowed or waived.
- A first full live run has been executed or has ended `INCOMPLETE` for a
  documented environmental reason. Its findings are grouped by adapter, class,
  mismatch code, and root cause, without raw private content.
- External reviewers converge that the cross-checks are sufficient, or every
  remaining reviewer claim has been verified as false-positive/noise with
  evidence.
