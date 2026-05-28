-- Per-source ingest bookkeeping: an observability sequence counter and
-- last-seen cursor JSON. Lives separate from sources so the ingester can
-- update the counter/cursor on every batch commit without contending
-- with operator metadata in `sources`.
--
-- Source of truth: .agents/sow/specs/data-model.md §source_progress and
-- .agents/sow/specs/ingester.md §Dedup and Idempotency.
--
-- `last_seq` is a per-source observability sequence counter (the max
-- SourceSeq seen, advanced atomically with the batch that wrote it),
-- surfaced via /api/health. It is NOT a dedup gate: resume-skipping is
-- the adapter cursor's job and event-level idempotency is a SQL-layer
-- guarantee (SOW-0015). `cursor` is the opaque JSON cursor produced by
-- the adapter's most recent SourceProgressEvent so a restart resumes
-- from the right offset. Both fields advance atomically with the batch
-- they describe.

CREATE TABLE source_progress (
    source_id  TEXT PRIMARY KEY NOT NULL REFERENCES sources(id),
    last_seq   INTEGER NOT NULL DEFAULT 0,
    last_ts_us INTEGER NOT NULL DEFAULT 0,
    cursor     TEXT,
    updated_at INTEGER NOT NULL
);
