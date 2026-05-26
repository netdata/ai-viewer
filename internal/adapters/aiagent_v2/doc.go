// Package aiagent_v2 implements the canonical.Adapter for the ai-agent
// v2 single-file gzipped-JSON snapshot format.
//
// In v2 each session is persisted as a single `<originId>.json.gz` file
// at the root of the configured sessions directory. The producer
// (`ai-agent.git/src/persistence.ts`) gzips a `{version, reason, opTree}`
// envelope into a `.tmp-<pid>-<ts>` companion and atomically renames it
// into place; every snapshot writes the full session tree, so the file
// is rewritten on every checkpoint. All descendants of one root share
// the same filename (children inherit the parent's originTxnId), so a
// single file may carry the parent session plus an arbitrary nested tree
// of embedded sub-agent sessions.
//
// The adapter walks the directory non-recursively for `*.json.gz`
// (ignoring v3 subdirectories and `*.tmp-*` orphans), decompresses each
// snapshot, parses the opTree into canonical events, recursively
// descends into `op.childSession` to surface embedded sub-agents, and
// emits events into the canonical channel. Large snapshots (compressed
// over a configurable threshold) are routed through a streaming decoder
// that walks the JSON token-by-token to bound peak memory.
//
// The cursor records `(content_hash, mtime_ns, size)` per file. Because
// v2 rewrites the whole file on every snapshot a byte-offset cursor is
// meaningless; content hashing lets the adapter skip re-emission when a
// filesystem touch changes mtime but not bytes. The ingester's SourceSeq
// HWM absorbs duplicates whenever a re-scan re-emits unchanged content.
//
// See `.agents/sow/specs/adapter-aiagent-v2.md` for the format
// reference (snapshot shape, edge cases, sub-agent embedding semantics)
// and `.agents/sow/specs/adapter-contract.md` for the universal adapter
// rules.
package aiagent_v2
