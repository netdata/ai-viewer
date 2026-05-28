// Package ingest wires canonical events emitted by adapters into the
// SQLite store. One Ingester instance owns the writer-side *sql.DB and
// is the only writer in the process (see internal/store doc.go for the
// single-writer invariant).
//
// Architecture
//
//	adapter.Scan/Tail → chan canonical.Event → worker → batched tx →
//	    SQLite (idempotent upserts) + aggregates + catalog + parent resolver
//
// One worker goroutine per source drains its channel into a per-source
// in-memory accumulator. Each accumulator flushes when it reaches the
// batch size (default 1000 events) or the batch interval (default
// 500 ms), whichever trips first. Flushing opens a single
// database/sql.Tx, applies per-event UPSERTs in arrival order,
// re-computes aggregates over the dirty session/turn set, persists
// source_progress, and commits.
//
// # Dedup and idempotency
//
// There is no per-source scalar high-water-mark event-drop: one sourceID
// aggregates many independently-sequenced files, so a scalar watermark
// silently drops valid events and orphans FK children (SOW-0015).
// Instead, resume-skipping is the adapter cursor's job (per-file offsets
// loaded from source_progress.cursor), and event-level idempotency is a
// SQL-layer guarantee — every writer table uses idempotent upserts
// (ON CONFLICT) keyed on a natural identity, so re-emitted events never
// duplicate rows regardless of ordering. source_progress.last_seq is
// retained only as an observability counter (max SourceSeq seen),
// surfaced via /api/health. See .agents/sow/specs/ingester.md
// §Dedup and Idempotency.
//
// # Parent linkage
//
// SessionStartedEvents carrying a ParentNativeID are linked when the
// parent is already present. When the parent has not yet been ingested
// the child row is inserted with parent_session_id = NULL and the
// background resolver (5 s ticks) retries the link as new parents land.
// The child's parent_native_id is persisted into extras_json so the
// resolver can re-run against durable state on restart.
//
// # Cost computation
//
// The Pricer interface is the seam for cost computation. The default
// NopPricer returns 0 so adapter-supplied costs flow through unchanged.
// The production binary (Chunk 11) plugs in *pricing.Pricer from
// internal/pricing, which loads internal/pricing/pricing.json at process
// startup and selects per-op price tiers by the op's start_ts (the
// value persisted in ops.start_ts, in UNIX-microseconds UTC) —
// historical sessions priced with the tier that was in effect when the
// op STARTED, not when it finalized. An op that straddles a
// price-change date is always charged at the rate in effect at start
// time, which matches every vendor's billing model.
//
// # Catalog updates
//
// Inline upserts populate catalog_providers, catalog_models,
// catalog_tools, catalog_agents, and catalog_cwds per event. The
// time-bucketed rollups described in data-model.md §Aggregation are
// deferred to SOW-0007.
//
// See .agents/sow/specs/ingester.md for the authoritative spec.
package ingest
