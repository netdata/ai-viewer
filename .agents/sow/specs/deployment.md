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

Manjaro-friendly install script (matches the user's environment) provided at `scripts/install-systemd-user.sh` (no root needed; localhost-only):

```bash
scripts/install-systemd-user.sh            # (default: install)
scripts/install-systemd-user.sh uninstall  # remove units (keeps binaries + data)
scripts/install-systemd-user.sh status     # systemctl --user status for both
```

- `install`: ALWAYS rebuilds from current source via `scripts/build.sh` (so `git pull && install` never reinstalls stale binaries), copies the binaries to `~/.local/bin/` and the unit templates from `deploy/systemd/` to `${XDG_CONFIG_HOME:-~/.config}/systemd/user/`, runs `systemctl --user daemon-reload`, then **prints** (does not run) the enable command — enabling a run-on-login service is the operator's explicit step:
  ```bash
  systemctl --user enable --now ai-viewer-ingest.service ai-viewer-serve.service
  ```
- `uninstall`: disables+stops the units if present (idempotent), removes them, reloads; leaves the binaries and the data dir (`~/.local/share/ai-viewer`) intact.
- The two units are version-controlled templates under `deploy/systemd/`; `scripts/test/systemd-units-test.sh` statically lints them (`systemd-analyze verify` + required-directive checks).

## Build Pipeline

ai-viewer ships as a **single binary that serves the UI**: `ai-viewer-serve`
embeds the built React SPA via `go:embed` and serves it same-origin alongside
the `/api` + SSE routes. No separate web server, no Node at runtime.

### `scripts/build.sh`

The release build. From the repo root, in order:

1. Resolve the repo root from the script's own directory (repo-relative; no
   absolute machine paths).
2. `cd frontend && npm ci && npm run build` (`npm ci` because
   `frontend/package-lock.json` is committed; falls back to `npm install` only
   if the lock file is absent). Output lands in `frontend/dist/`.
3. Sync the build output into the embed directory: clear everything under
   `cmd/ai-viewer-serve/frontend_dist/` **except** the tracked `.gitkeep`
   sentinel, then copy the **whole** `frontend/dist/` tree in (`index.html`,
   `assets/`, AND the root public files Vite copies from `frontend/public/`
   such as `favicon.svg` — all of which the built `index.html` references at
   the site root). The copied files are git-ignored (see `architecture.md`
   §"Embed-dir git policy"), so the tree stays clean.
4. `go build -o bin/ai-viewer-serve ./cmd/ai-viewer-serve` and
   `go build -o bin/ai-viewer-ingest ./cmd/ai-viewer-ingest`.
5. Print the resulting `bin/` paths.

Outputs: `bin/ai-viewer-serve` and `bin/ai-viewer-ingest` (both git-ignored;
the release binaries are build artifacts, never committed). Running
`bin/ai-viewer-serve` then serves the real UI at `http://127.0.0.1:7710/`.

### `scripts/dev.sh`

The development loop. Builds `ai-viewer-serve` to a temp binary and runs that
directly (so the tracked PID is the real server, not a `go run` wrapper whose
child would survive a kill), alongside the Vite dev server in `frontend/`.
Vite proxies `/api` to `127.0.0.1:7710` (per `frontend/vite.config.ts`) so the
same relative fetch URLs work in dev and in the single-binary build. Both
child processes are tracked by PID and killed on exit (only those PIDs; never
`pkill`/`killall`); the temp build dir is removed on exit too. In this mode the
Go binary usually has no built UI embedded; it serves the not-built notice at
`/` while the live UI is the Vite dev server (see `architecture.md`
§"Not-built degrade").

### `scripts/e2e-serve.sh`

Boots the PRE-BUILT single binary with deterministically seeded data for the
Playwright E2E suite (`frontend/playwright.config.ts` §`webServer`). It does NOT
build — it requires `bin/ai-viewer-serve` + `bin/ai-viewer-ingest` to already
exist (run `scripts/build.sh` first; CI builds before the E2E step). It ingests
a fixed set of committed fixtures (`testdata/aiagent_v3/{happy_single_turn,
multi_turn,sub_agent}/INPUT`) into a `mktemp` temp DB, waits until all three
sources log "adapter scan complete; tail starting" (so every fixture has emitted
before shutdown), then SIGTERMs the ingester and waits for a clean exit (which
flushes the final batch). It then enforces the EXACT deterministic seed via a
read-only `sqlite3` open — exactly 4 sessions, 1 child session, 3 sources (a
shortfall = partial seed; a surplus = fixture drift; both fail loudly) — then
`exec`s
`ai-viewer-serve --bind 127.0.0.1:7710` in the foreground so Playwright owns and
tears down the process. Kills only the ingester PID it starts (never
`pkill`/`killall`). The temp DB is built from already-sanitized fixtures, never
the operator's real state dir. In CI the `frontend` job runs `scripts/build.sh`
(which sets up Go) BEFORE `npm run e2e`, so the binaries exist when the
`webServer` launches this script (SOW-0001 Chunk-18 D1/D2).

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

The two USER units are shipped as version-controlled templates under
`deploy/systemd/` and installed by `scripts/install-systemd-user.sh` (above).
They are USER units (no `User=`, no privilege) and the server binds localhost.

`deploy/systemd/ai-viewer-ingest.service`:

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

`deploy/systemd/ai-viewer-serve.service`: analogous (`Description=ai-viewer
server`), with `After=ai-viewer-ingest.service`.

**Start-order note.** `After=` orders START only, not readiness. On a fresh
machine the server may start before the ingester has created+migrated the SQLite
schema; the server's `CheckSchema` then exits non-zero and `Restart=on-failure`
(`RestartSec=3s`) retries until the schema exists. This is intentional — no
serve-side "wait for schema" is needed in v1.

## Updates

```bash
cd ~/src/ai-viewer.git
git pull
scripts/install-systemd-user.sh install     # rebuilds + reinstalls bin -> ~/.local/bin and units
systemctl --user restart ai-viewer-ingest.service ai-viewer-serve.service
```

`install` copies the freshly-built binaries to `~/.local/bin/` — the SAME path
the user units' `ExecStart=%h/.local/bin/ai-viewer-*` runs — so the restart
picks up the new binaries. (Do NOT `sudo install` to `/usr/local/bin`: the user
units would keep running the stale `~/.local/bin` copies.)

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
