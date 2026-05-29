# ai-viewer

A read-only, real-time explorer for AI coding-agent session snapshots.

Watches your local session storage for `ai-agent`, `claude-code`, `codex`, and `opencode`, normalizes everything into a canonical model, and serves a modern web UI with tracing, topology, timeline, and statistics views.

## Status

**v0.1 — Phase 1 complete.** The single binary serves a live web UI (sessions
list, session detail with Overview + Logs, sources/health) for the ai-agent
v2/v3 adapters, with deep-linking, light/dark theme, and end-to-end +
accessibility tests in CI. Phase 2 (Trace/Topology/Timeline views, more
adapters, cross-session analytics) is next. See `.agents/sow/` for the SOWs.

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

New formats are added as Go adapters implementing one interface. See `.agents/sow/specs/adapter-contract.md`. **Phase 1 wires the ai-agent v2 + v3 adapters** (auto-discovered under `~/.ai-agent/sessions`); the claude-code, codex, and opencode adapters are planned for a later phase.

## Install

`ai-viewer-serve` is a single binary with the web UI embedded; `ai-viewer-ingest`
populates its database. Both are localhost-only and read-only on your source
files.

```bash
git clone https://github.com/netdata/ai-viewer ~/src/ai-viewer.git
cd ~/src/ai-viewer.git
./scripts/build.sh            # builds the UI + both binaries into bin/
bin/ai-viewer-ingest &        # ingest your sessions (auto-discovers sources)
bin/ai-viewer-serve           # serve the UI + API at http://127.0.0.1:7710
```

To run it persistently on login (systemd USER units, no root):

```bash
scripts/install-systemd-user.sh
systemctl --user enable --now ai-viewer-ingest.service ai-viewer-serve.service
```

See [docs/runbook.md](docs/runbook.md) for full operating instructions and
[SECURITY.md](SECURITY.md) for the security posture.

## Architecture

Two binaries:

- `ai-viewer-ingest` — daemon. Watches source directories, parses snapshots, writes canonical rows to SQLite.
- `ai-viewer-serve` — HTTP server. Serves embedded frontend + REST + SSE.

See `.agents/sow/specs/architecture.md` for the full mental model.

## License

MIT (see `LICENSE`).

## Contributing

This repository operates under a delegated-ownership contract documented in `AGENTS.md`. The project maintainer drives product; the assistant drives the technical implementation.
