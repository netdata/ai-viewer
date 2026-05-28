# Deployment

## TL;DR

Single user, single workstation, two long-running processes. systemd user units optional but recommended. Single static binary per process, no runtime dependencies beyond glibc.

## Default Paths

| Purpose | Default | Configurable |
|---|---|---|
| SQLite index | `~/.local/share/ai-viewer/index.db` | `--db` |
| State dir | `~/.local/share/ai-viewer/` | `--state-dir` |
| Logs (when run by systemd) | journald via stdout | systemd handles it |
| Config file (optional) | `~/.config/ai-viewer/config.yaml` | `--config` |
| Bind address | `127.0.0.1:7710` | `--bind` |

## Install (target after Phase 1)

```bash
git clone https://github.com/<owner>/ai-viewer ~/src/ai-viewer.git
cd ~/src/ai-viewer.git
./scripts/build.sh                            # builds bin/ai-viewer-ingest and bin/ai-viewer-serve
sudo install -m 0755 bin/ai-viewer-* /usr/local/bin/
mkdir -p ~/.local/share/ai-viewer
ai-viewer-ingest &                             # start ingester
ai-viewer-serve &                              # start server
xdg-open http://127.0.0.1:7710                # open UI
```

Manjaro-friendly install script (matches the user's environment) provided at `scripts/install-systemd-user.sh`:

- Installs binaries to `~/.local/bin/`.
- Drops user systemd units at `~/.config/systemd/user/ai-viewer-{ingest,serve}.service`.
- `systemctl --user enable --now ai-viewer-{ingest,serve}.service`.
- No root needed.

## Source Auto-Discovery

If `ai-viewer-ingest` is started with no `--source` flags, it probes
the default locations of every adapter the binary was compiled
against. Each existing location becomes a source; missing locations
are silently skipped.

Phase 1 (Chunk 11 onward) ships only the `aiagent_v3` and `aiagent_v2`
adapters, so only the first two rows of the table below are wired into
the binary. The remaining rows are reserved for future Phase 2 SOWs
that introduce the matching adapter packages.

| Format | Probe | Status |
|---|---|---|
| aiagent_v3 | `~/.ai-agent/sessions/session/` exists | live (Chunk 11) |
| aiagent_v2 | `~/.ai-agent/sessions/` exists | live (Chunk 11) |
| claude_code | `~/.claude/projects/` exists | adapter pending (Phase 2 SOW) |
| codex | `~/.codex/sessions/` exists | adapter pending (Phase 2 SOW) |
| opencode | `~/.local/share/opencode/opencode.db` exists | adapter pending (Phase 2 SOW) |

The Chunk 11 v2 probe checks for the parent `sessions/` directory
rather than the glob `*.json.gz` documented earlier: a freshly-bootstrapped
ai-agent install creates the directory before any session has been
written, so the parent-directory probe captures the intent ("the
operator has used ai-agent v2 on this machine") without false negatives
on a brand-new install.

Each existing location becomes a source. Logged at startup.

`--source` flags, if any, **replace** auto-discovery (no merge).
Explicit > implicit.

## Configuration File

Optional. CLI flags take precedence.

```yaml
# ~/.config/ai-viewer/config.yaml
db: ~/.local/share/ai-viewer/index.db
state_dir: ~/.local/share/ai-viewer
bind: 127.0.0.1:7710
log_level: info
sources:
  - format: aiagent_v3
    location: ~/.ai-agent/sessions
    workers: 4
  - format: claude_code
    location: ~/.claude/projects
    workers: 2
```

## systemd User Units

`ai-viewer-ingest.service`:

```ini
[Unit]
Description=ai-viewer ingester
After=default.target

[Service]
ExecStart=%h/.local/bin/ai-viewer-ingest
Restart=on-failure
RestartSec=3s

[Install]
WantedBy=default.target
```

`ai-viewer-serve.service`: analogous; `After=ai-viewer-ingest.service`.

## Updates

```bash
cd ~/src/ai-viewer.git
git pull
./scripts/build.sh
sudo install -m 0755 bin/ai-viewer-* /usr/local/bin/
systemctl --user restart ai-viewer-{ingest,serve}.service
```

The SQLite schema migrates automatically on ingester startup. If migration fails: ingester exits non-zero with a loud log; user must intervene (rollback binary or run `ai-viewer-ingest migrate --dry-run` to inspect).

## Backup / Reset

- SQLite is **derived data**. Deleting `index.db` causes a full re-ingest from sources — slow but harmless.
- `ai-viewer-ingest reset` (Phase 2 CLI subcommand) wipes index.db and re-discovers sources.
- No backup needed; sources are the truth.

## Production Deployment (out of scope for v1)

If/when ai-viewer ever runs on a remote host (e.g. `nova`, `agent-events`):

- Bind would need to expose beyond localhost — auth design required first.
- TLS termination via the user's existing reverse proxy.
- Network access to the source storage (NFS / rsync sync of `~/.ai-agent/sessions`).
- Separate SOW with explicit security review.

None of this is in scope for v1. v1 is workstation-only.

## Port Allocation

- `7710` — `ai-viewer-serve` HTTP+SSE (default).
- `7711` — `ai-viewer-serve` `/metrics` (when enabled).
- `7712` — reserved for future ingester admin endpoint.

To confirm during Phase 1 implementation that these ports do not conflict with anything the user already runs (Netdata, vLLM scripts, etc.).
