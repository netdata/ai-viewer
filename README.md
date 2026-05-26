# ai-viewer

A read-only, real-time explorer for AI coding-agent session snapshots.

Watches your local session storage for `ai-agent`, `claude-code`, `codex`, and `opencode`, normalizes everything into a canonical model, and serves a modern web UI with tracing, topology, timeline, and statistics views.

## Status

**Pre-alpha.** Phase 1 (ingester foundation + minimal UI) is in active scoping. See `.agents/sow/pending/` for the current SOW.

## Why

When you run AI coding agents, every session leaves a trail on disk — turn boundaries, LLM requests, tool calls, sub-agent invocations, payloads, costs. The trail is rich but format-specific and unreadable by hand. `ai-viewer` reads those trails in real time, presents them as APM-style spans, and lets you understand what your agents are doing — across vendors, across time, across sub-agents.

## Supported Source Formats

| Source | Default location | Mechanism |
|---|---|---|
| ai-agent v3 (split) | `~/.ai-agent/sessions/{session,payloads}/` | JSONL ledger + gz payload artifacts |
| ai-agent v2 (legacy) | `~/.ai-agent/sessions/<originId>.json.gz` | full gzipped opTree per snapshot |
| claude-code | `~/.claude/projects/<encoded-cwd>/<sessionId>.jsonl` | JSONL per session, sharded by cwd |
| codex | `~/.codex/sessions/YYYY/MM/DD/rollout-*.json` | date-sharded rollout JSON |
| opencode | `~/.local/share/opencode/opencode.db` | SQLite |

New formats are added as Go adapters implementing one interface. See `.agents/sow/specs/adapter-contract.md`.

## Install

(Available after Phase 1. Will be a single static binary `ai-viewer` with embedded frontend.)

## Architecture

Two binaries:

- `ai-viewer-ingest` — daemon. Watches source directories, parses snapshots, writes canonical rows to SQLite.
- `ai-viewer-serve` — HTTP server. Serves embedded frontend + REST + SSE.

See `.agents/sow/specs/architecture.md` for the full mental model.

## License

MIT (see `LICENSE`).

## Contributing

This repository operates under a delegated-ownership contract documented in `AGENTS.md`. The project maintainer drives product; the assistant drives the technical implementation.
