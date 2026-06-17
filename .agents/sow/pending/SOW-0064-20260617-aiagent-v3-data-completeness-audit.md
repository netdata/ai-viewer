# SOW-0064 — ai-agent v3 adapter data-completeness audit

## Status

Status: open (BLOCKER for SOW-0065)

## Requirements

### Purpose

Verify that the ai-agent v3 adapter captures 100% of the information in source `.jsonl` ledger files. Missing or misinterpreted fields void the application for its primary purpose: understanding ai-agent failure conditions, model quality, and agent interaction patterns. The operator plans to use ai-viewer as the main tool for improving ai-agent.

### Scope

ai-agent v3 ONLY. Claude-code, codex, and opencode are secondary ("not that important" per operator). v2 is legacy.

### Method

1. Read the ai-agent v3 source format end-to-end (the producer code in `anomalyco/ai-agent`, the `EvidenceLedgerWriter`, every record type it emits).
2. Produce a field-by-field inventory: every record type, every field in each record, what it means semantically.
3. Compare that inventory against what the adapter (`internal/adapters/aiagent_v3/`) actually maps into the canonical model.
4. Identify gaps: dropped fields, lossy mappings, failure-mode under-representation, sub-agent interaction flattening.
5. Fix every gap — no partial mapping. Use extras_json for fields without a canonical column.
6. Validate against 20-30 diverse real sessions (happy path, failures, deep nesting, compaction, retries, multi-model).

### Acceptance Criteria

1. A field-by-field audit document exists comparing source format vs adapter mapping, with every gap identified. **Verification**: the document is in the SOW.
2. Every source field is either mapped to a canonical column, mapped to extras_json, or explicitly documented as intentionally dropped (with a reason). **Verification**: grep the adapter for every source field name.
3. Error/failure information (error_class, error_message, retry attempts, timeouts, compaction triggers, warnings) is fully captured — not just "status=failed". **Verification**: a real failed session shows all error details in the DB.
4. Sub-agent interaction details (call paths, parent/child op linkage, delegation patterns, tool I/O) are fully captured. **Verification**: a multi-level delegation session shows the complete tree in /api/sessions/:id/topology.
5. 20-30 real sessions validated field-by-field (source file → DB row). **Verification**: test or audit log.

## Pre-Implementation Gate

(To be filled on pickup.)

## Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
