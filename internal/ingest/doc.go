// Package ingest wires canonical events emitted by adapters into the
// SQLite store. One Ingester instance owns the writer-side *sql.DB and
// is the only writer in the process (see internal/store doc.go for the
// single-writer invariant).
//
// Architecture
//
//	adapter.Scan/Tail → chan canonical.Event → worker → batched tx →
//	    SQLite + dedup HWM + aggregates + catalog + parent resolver
//
// One worker goroutine per source drains its channel into a per-source
// in-memory accumulator. Each accumulator flushes when it reaches the
// batch size (default 1000 events) or the batch interval (default
// 500 ms), whichever trips first. Flushing opens a single
// database/sql.Tx, applies per-event UPSERTs in arrival order,
// re-computes aggregates over the dirty session/turn set, persists
// source_progress, and commits.
//
// # Dedup
//
// The ingester maintains a per-source high-water-mark loaded at Start
// from source_progress.last_seq. Events with SourceSeq <= hwm are
// dropped before any SQL is issued. The HWM advances atomically with
// the batch commit so a kill-9 between batches resumes at the right
// offset on restart.
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
// Chunk 10 plugs in the real pricer backed by internal/pricing/pricing.json.
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
