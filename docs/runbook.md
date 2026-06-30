# ai-viewer Operator Runbook (Phase 1)

A practical guide to building, running, and operating ai-viewer on your
workstation. ai-viewer is **localhost-only** and **read-only** on your agent
session files (see [SECURITY.md](../SECURITY.md)).

## What it does

Two binaries share one SQLite database:

- `ai-viewer-ingest` — watches your AI-agent session directories, parses each
  snapshot, and writes normalized rows into the database.
- `ai-viewer-serve` — serves the web UI plus a REST + SSE API at
  `http://127.0.0.1:7710`, reading from that same database.

## Build

```bash
git clone https://github.com/netdata/ai-viewer ~/src/ai-viewer.git
cd ~/src/ai-viewer.git
./scripts/build.sh
```

`scripts/build.sh` builds the React UI, embeds it into `ai-viewer-serve`, and
produces both binaries in `bin/`. The single `ai-viewer-serve` binary serves the
UI itself — there is no separate web server or Node runtime at run time.

## Run (manual)

```bash
# 1. Ingest your sessions into the database (Ctrl-C to stop; it tails for new ones).
bin/ai-viewer-ingest

# 2. In another terminal, serve the UI + API.
bin/ai-viewer-serve

# 3. Open the UI.
xdg-open http://127.0.0.1:7710
```

By default both use `~/.local/share/ai-viewer/index.db`. The ingester
auto-discovers ai-agent v2/v3 (`~/.ai-agent/sessions`), claude-code, codex, and
opencode sources when their default directories exist. To ingest a specific
source explicitly, pass `--source format:location` (e.g.
`--source aiagent_v3:~/.ai-agent/sessions`) — any explicit `--source` flag
replaces auto-discovery. Override the DB path with `--db`, the state dir with
`--state-dir`, and (serve only) the bind with `--bind 127.0.0.1:<port>`. Run
either binary with `--help` for all flags.

> If you start `ai-viewer-serve` before the ingester has created the database,
> it exits with a schema error — start the ingester first (or use the systemd
> units below, which retry automatically).

## Run (systemd, run-on-login)

```bash
scripts/install-systemd-user.sh            # build + install binaries and USER units
systemctl --user enable --now ai-viewer-ingest.service ai-viewer-serve.service
```

`install` rebuilds from source, copies the binaries to `~/.local/bin/` and the
USER units to `~/.config/systemd/user/`, reloads systemd, and prints the
`enable --now` command for you to run (it does not start services itself). No
root is needed. `ai-viewer-serve.service` is ordered `After=` the ingester and
restarts on failure, so on first run it retries until the database schema
exists.

```bash
scripts/install-systemd-user.sh status     # systemctl --user status for both
scripts/install-systemd-user.sh uninstall  # stop + remove the units (keeps binaries + data)
```

## What you can do in the UI

- **`/`** — the sessions list (root sessions: agent, model, start, duration,
  status, turns, ops, tokens, cost, failures). A child-count cell links to a
  session whose Overview lists its sub-sessions. "Load more" pages older
  sessions. The list refreshes live as new sessions are ingested.
- **`/sessions/:id`** — session detail. **Overview** (per-session aggregates +
  a tools-used summary + a child-sessions table), **Trace** (APM views of the
  op tree: waterfall / flame-graph / event-list, with a shared span-detail
  drawer), **Topology** (per-session actor graph of the agents and tools that
  participated, node size/color encoding a selectable metric), **Timeline**
  (video-editor lanes — one per session, spans drawn on a shared time axis),
  and **Logs** (severity-filter + paging). Deep-linking / reloading this URL
  works.
- **`/topology`** — cross-session topology: the actor graph aggregated across
  all sessions in the current timeframe + filters.
- **`/sources`** — per-source lifecycle/read-model status, Tail heartbeat or
  failure evidence, parse-error counts, progress timestamp, and an overall
  health badge.
- **Theme** — Auto (follows your OS) / Dark / Light, in the header; your choice
  persists.

## Updating

```bash
cd ~/src/ai-viewer.git
git pull
scripts/install-systemd-user.sh install     # rebuilds + reinstalls to ~/.local/bin
systemctl --user restart ai-viewer-ingest.service ai-viewer-serve.service
```

The schema migrates automatically on ingester startup. (For a manual install,
re-run `./scripts/build.sh` and restart the binaries.)

## Where things live

| Path | What |
|---|---|
| `~/.local/share/ai-viewer/index.db` | the SQLite database (delete to reset; it rebuilds on next ingest) |
| `~/.local/bin/ai-viewer-{ingest,serve}` | installed binaries (systemd install) |
| `~/.config/systemd/user/ai-viewer-*.service` | the USER units |
| source dirs (`~/.ai-agent`, `~/.claude`, …) | your agents' session files — **read only**, never modified |

## Troubleshooting

- **`/sources` shows parse errors or a "degraded"/"down" health badge** — a
  source file failed to parse, a source is still scanning too long, Tail failed
  or became stale, read-model repair is pending/failed, or the DB/schema is
  unavailable. Check `/api/health`, then the ingester logs
  (`scripts/install-systemd-user.sh status`, or the manual ingester's output).
  Parse errors, lifecycle failures, and read-model repair failures are surfaced
  per source, never hidden.
- **No sources configured/discovered** — `/api/health` returns
  `status="degraded"` with `status_detail="no_sources_configured"`. Start the
  ingester with explicit `--source format:path` flags or restore the expected
  default source directories.
- **The UI says "UI not built"** — you ran `ai-viewer-serve` without building
  the frontend. Run `./scripts/build.sh` and restart.
- **`ai-viewer-serve` exits with a schema error** — the log line is
  `ai-viewer-serve: schema version mismatch`, usually with
  `schema_meta.version row missing` or `schema_meta.version is <got>, want 14`.
  Stop serve, check `journalctl --user -u ai-viewer-ingest.service` for the
  migration failure, start/restart `ai-viewer-ingest` so it applies migration
  0014, then start/restart serve. The systemd units handle this ordering with
  auto-restart. Migration 0014 clears the derived `fts_ops` / `fts_logs` search
  indexes when upgrading old stores; source repair or `rollups-backfill`
  repopulates them from primary rows.
- **Do not run SQLite `VACUUM` on the ingest database while ai-viewer is
  running.** `fts_ops` uses `ops.rowid` as an internal maintenance docid, and
  SQLite can rewrite implicit rowids during `VACUUM`. If external maintenance
  has vacuumed or rebuilt the DB, stop the service and run
  `ai-viewer-ingest rollups-backfill --db <index.db>` (or let daemon startup
  complete its retained full read-model reconciliation) before trusting search.
- **Reset everything** — stop the services, `rm -rf ~/.local/share/ai-viewer`,
  and re-run the ingester; it re-reads your sources from scratch.

## Boundaries

ai-viewer never modifies your source files, never makes a network call, and
binds only localhost. See [SECURITY.md](../SECURITY.md) and
[docs/architecture-overview.md](architecture-overview.md).
