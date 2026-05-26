// Package aiagent_v3 implements the canonical.Adapter for the ai-agent
// v3 split-storage format.
//
// In v3 each session is a directory tree on disk: a manifest JSON, an
// append-only JSONL log of records, and a payloads/ subtree of compressed
// per-record blobs. The adapter walks the configured root, parses
// manifests and JSONL records, and emits canonical events ordered by the
// JSONL byte offset that doubles as the per-session source sequence.
//
// This package currently ships only the skeleton landed in SOW-0001
// Chunk 4: Name, Format, the package init that registers the factory
// with internal/adapters, and Scan/Tail/ParseCursor stubs that return a
// sentinel "not implemented" error. The real parser, fixtures, and
// golden tests land in SOW-0001 Chunk 6.
//
// See .agents/sow/specs/adapter-aiagent-v3.md for the format reference
// (file layout, record schema, cursor design, edge cases) and
// .agents/sow/specs/adapter-contract.md for the universal adapter rules.
package aiagent_v3
