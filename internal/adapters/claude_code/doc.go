// Package claude_code implements the canonical.Adapter for Anthropic's
// Claude Code CLI session-transcript format.
//
// Claude Code stores one session as an append-only JSONL transcript at
// ~/.claude/projects/<sanitized-cwd>/<sessionId>.jsonl. Sub-agents spawned
// by the Agent tool live in sibling files under
// <sanitized-cwd>/<sessionId>/subagents/agent-<agentId>.jsonl with a
// .meta.json sidecar. The adapter walks the configured root (the projects
// dir), parses each JSONL line into a canonical event stream, and follows
// changes via fsnotify.
//
// Unlike ai-agent's ledger format, claude-code has no native turn or op
// boundaries, no cost data, and no session-end signal. The adapter
// synthesizes turns from the message chain, computes cost downstream from
// the pricing catalog, and never emits SessionFinalizedEvent — claude-code
// sessions remain status='running' (resumable) indefinitely.
//
// See .agents/sow/specs/adapter-claude-code.md for the full format
// reference (path naming, record schema, sub-agent linkage, compaction,
// cursor design, edge cases) and .agents/sow/specs/adapter-contract.md for
// the universal adapter rules.
package claude_code
