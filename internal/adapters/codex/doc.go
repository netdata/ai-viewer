// Package codex implements the canonical.Adapter for the OpenAI Codex CLI
// rollout format.
//
// Codex stores one conversation as an append-only JSONL "rollout" file under
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-YYYY-MM-DDTHH-MM-SS-<ThreadId>.jsonl
// (default $CODEX_HOME is ~/.codex). The directory shards use local time; the
// per-line timestamp field is UTC and is the canonical time source. Each line
// is one RolloutItem envelope {timestamp, type, payload}; the top-level type is
// one of session_meta, turn_context, response_item, event_msg, compacted, and
// the nested payload carries its own type discriminator. Files are written
// pure-append (no rename, no fsync), so a byte-offset tail with last-newline
// seek-back is the correct watch strategy.
//
// A pre-mid-2025 legacy flat layout (rollout-YYYY-MM-DD-<uuid>.json directly
// under sessions/) is recognized but not ingested by default: the adapter emits
// one informational SourceError per legacy file and suppresses it thereafter
// via the cursor's LegacyJSON map.
//
// Codex rollouts carry no native turn/op rollups, no cost data, and no
// per-session terminal signal, so the adapter is a state machine over the line
// stream (later chunks): it synthesizes turn boundaries from turn_context /
// task_started / task_complete, computes cost downstream from the pricing
// catalog, and never emits a clean SessionFinalizedEvent (codex sessions stay
// status='running' and are resumable, like claude-code).
//
// See .agents/sow/specs/adapter-codex.md for the full format reference
// (filesystem layout, wire format, record schema, sub-agent/fork linkage,
// compaction, cursor design, edge cases) and
// .agents/sow/specs/adapter-contract.md for the universal adapter rules.
package codex
