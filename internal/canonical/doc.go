// Package canonical defines the format-agnostic event model and adapter
// contract that decouple ai-viewer's source-format parsers from its
// downstream storage and presentation layers.
//
// Adapters in internal/adapters/<name> read one specific session-snapshot
// format (ai-agent v2/v3, claude-code, codex, opencode, ...) and emit a
// stream of typed Event values onto a channel. The ingester is the only
// writer to SQLite; it consumes that stream, enforces dedup and ordering
// by (SourceID, SourceSeq), and persists canonical rows according to the
// schema in .agents/sow/specs/data-model.md.
//
// The event model is deliberately wider than any single source format so
// that the same downstream schema covers every adapter. Per-format quirks
// (cumulative-to-delta token conversion, sub-agent linkage, turn
// synthesis, snapshot-vs-append cursor semantics) are the adapter's
// responsibility; the ingester sees only the clean canonical stream.
//
// See:
//   - .agents/sow/specs/canonical-events.md for the authoritative event
//     contract (every field documented here mirrors the spec).
//   - .agents/sow/specs/adapter-contract.md for the Adapter interface
//     contract this package exposes.
//   - .agents/sow/specs/data-model.md for the SQLite schema each event
//     family maps to.
package canonical
