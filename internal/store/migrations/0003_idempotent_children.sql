-- ai-viewer schema migration 0003: idempotent child-row writes.
-- Source of truth: .agents/sow/specs/data-model.md (payload_refs,
-- log_entries) and .agents/sow/specs/ingester.md §Dedup and Idempotency.
--
-- SOW-0015 removes the per-source scalar high-water-mark that dropped
-- valid events under an aggregating sourceID (one sourceID covers many
-- independently-sequenced files; SourceSeq is per-file, not per-source).
-- With the event-drop gone, every event flows to the writer, so the two
-- FK-bearing child tables (payload_refs, log_entries) — previously the
-- only writer tables using a plain INSERT — gain a natural-identity
-- UNIQUE index and switch to ON CONFLICT DO NOTHING. This makes ingest
-- idempotent at the SQL layer: re-emission (Tail re-read, file re-scan)
-- never duplicates rows, regardless of event ordering or batch
-- boundaries.
--
-- payload_refs: an op has at most one payload per (kind, location). Both
-- adapters emit exactly one request and one response payload per op,
-- each with a distinct kind, so (op_id, kind, location_uri) is a true
-- natural key — replaying the same payload collides, distinct payloads
-- do not.
CREATE UNIQUE INDEX idx_payload_refs_identity
    ON payload_refs(op_id, kind, location_uri);

-- log_entries: COALESCE maps NULL owner columns to a '' sentinel so
-- re-emitted source-level (session_id NULL) and session-level
-- (source_id NULL) rows collide. Raw SQL NULLs are distinct in a UNIQUE
-- index, which would otherwise let duplicate parse-error / pricing-miss
-- rows through. turn_id is part of the key: v3 emits turn-scoped
-- warnings/errors with turn_id set but op_id NULL, so two genuinely
-- distinct logs in the same session under different turns (same
-- ts/severity/source/message, op_id NULL) must NOT collide — omitting
-- turn_id would silently drop the second as a false duplicate.
-- extras_json is the LAST keyed column: it is the only persisted
-- content column outside the owner/time/severity/source/message tuple,
-- so including it makes the key cover EVERY persisted column. A log row
-- is then a duplicate iff it is byte-identical; two logs that match on
-- everything but extras (e.g. v2 stores the source `path` in extras —
-- see aiagent_v2/mapper.go) are kept distinct, closing the same
-- false-dedup data-loss class as the turn_id fix (SOW-0015 iter-2/4).
-- message and extras_json are indexed directly (not hashed): log rows
-- here are short structured lines (parse errors, pricing-miss warnings)
-- — payload bodies live in payload_refs, never log_entries — so the
-- b-tree stays small and a hash column would add complexity for no
-- benefit.
CREATE UNIQUE INDEX idx_log_entries_identity ON log_entries(
    COALESCE(session_id, ''),
    COALESCE(source_id, ''),
    COALESCE(op_id, ''),
    COALESCE(turn_id, ''),
    ts, severity, source, message,
    COALESCE(extras_json, '')
);

-- Bump the operator-facing schema marker in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '3');
