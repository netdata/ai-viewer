// Package aiagent_v2 implements the canonical.Adapter for the ai-agent
// v2 single-file gzipped-JSON snapshot format.
//
// In v2 each session is stored as a single gzipped JSON document keyed by
// originId. The adapter watches the configured root for new or updated
// .json.gz files, parses the snapshot in full, and emits canonical events
// derived from its fields. v2 records cumulative token and cost counters;
// the adapter converts them to deltas before emitting so the canonical
// stream remains delta-only.
//
// This package currently ships only the skeleton landed in SOW-0001
// Chunk 4: Name, Format, the package init that registers the factory
// with internal/adapters, and Scan/Tail/ParseCursor stubs that return a
// sentinel "not implemented" error. The real parser, fixtures, and
// golden tests land in SOW-0001 Chunk 8.
//
// See .agents/sow/specs/adapter-aiagent-v2.md for the format reference
// and .agents/sow/specs/adapter-contract.md for the universal adapter
// rules.
package aiagent_v2
