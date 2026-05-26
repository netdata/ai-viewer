-- Per-source ingest bookkeeping: high-water-mark sequence and last-seen
-- cursor JSON. Lives separate from sources so the ingester can update
-- HWM/cursor on every batch commit without contending with operator
-- metadata in `sources`.
--
-- Source of truth: .agents/sow/specs/data-model.md §source_progress and
-- .agents/sow/specs/ingester.md §Dedup.
--
-- `last_seq` is the per-source monotonic-watermark used by the ingester
-- to discard re-emitted events on resume. `cursor` is the opaque JSON
-- cursor produced by the adapter's most recent SourceProgressEvent so a
-- restart resumes from the right offset. Both fields advance atomically
-- with the batch they describe.

CREATE TABLE source_progress (
    source_id  TEXT PRIMARY KEY NOT NULL REFERENCES sources(id),
    last_seq   INTEGER NOT NULL DEFAULT 0,
    last_ts_us INTEGER NOT NULL DEFAULT 0,
    cursor     TEXT,
    updated_at INTEGER NOT NULL
);
