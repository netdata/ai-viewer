---
name: project-deployment
description: Install, run, and operate ai-viewer on the operator's workstation — the system install (/opt/ai-viewer, runs-as-operator, explicit --source flags, systemd units), the user install alternative, build/run/upgrade/uninstall commands, and the threat-model reasoning behind the run-as-operator decision. Use when installing or upgrading ai-viewer, debugging source-discovery / permission-denied errors, or answering "how do I run this / what port / where's the data".
---

# ai-viewer Deployment

This skill is the durable record of how ai-viewer gets installed and run on the operator's workstation. Load it for any install/upgrade/uninstall, source-discovery or permission issue, or "how do I use this" question. The contract lives in `.agents/sow/specs/deployment.md`; this file is the operational quick-reference.

## TL;DR

- **System install (recommended):** `scripts/install-system.sh` → binaries + data under `/opt/ai-viewer`, two **system** systemd units running **as the operator**, UI at **<http://127.0.0.1:7710/>**.
- **User install (no root):** `scripts/install-systemd-user.sh` → binaries in `~/.local/bin`, **user** systemd units, data in `~/.local/share/ai-viewer/`.
- Both are localhost-only (no auth in v1 — workstation-scoped).

## The system install (`scripts/install-system.sh`)

### What it does

1. Rebuilds from source (`scripts/build.sh` — frontend + both Go binaries).
2. Probes the **installing operator's** well-known agent-data paths and renders `--source` flags for each that exists:
   - `~/.ai-agent/sessions` → `aiagent_v3` + `aiagent_v2` (same dir backs both)
   - `~/.claude/projects` → `claude-code`
   - `~/.codex/sessions` → `codex`
   - `~/.local/share/opencode/opencode.db` → `opencode`
3. Lays out `/opt/ai-viewer/{bin,data,logs}`, atomically stages each rebuilt binary and renames it into place.
4. Renders the unit templates (`deploy/systemd-system/ai-viewer-{ingest,serve}.service`) — substituting `__OPERATOR_USER__`, `__OPERATOR_GROUP__`, and `__AI_VIEWER_SOURCES__` — into `/etc/systemd/system/`.
5. `systemctl daemon-reload`, enables both units, restarts `ai-viewer-ingest.service` and `ai-viewer-serve.service`, waits up to 20s for the server, prints the URL.

### Commands

```bash
scripts/install-system.sh             # install or upgrade (rebuilds) + start + verify + print URL
scripts/install-system.sh status      # systemctl status for both + the URL
scripts/install-system.sh uninstall   # stop + disable + remove units + /opt/ai-viewer (data is disposable)
```

### Layout

| Path | Purpose |
|---|---|
| `/opt/ai-viewer/bin/ai-viewer-{ingest,serve}` | binaries (rebuilt from source each install) |
| `/opt/ai-viewer/data/index.db` | SQLite index (owned by the operator) |
| `/opt/ai-viewer/logs/` | fallback log dir (journald is the primary sink) |
| `/etc/systemd/system/ai-viewer-{ingest,serve}.service` | rendered system units (committed templates: `deploy/systemd-system/`) |

### Two design decisions baked in (do NOT re-litigate without a new reason)

**1. Runs as the operator (`User=<operator>`), NOT a dedicated service user.**
The operator's agent-data dirs (`~/.ai-agent`, `~/.claude`, `~/.codex`, `~/.local/share/opencode`) are owner-only (`0700`), so a dedicated `ai-viewer` user **cannot read them** without invasive recursive ACLs (fragile — new files don't inherit unless default ACLs are set; surprises dotfile managers/backup tools). Running as the operator is correct and simple here:

- The app's purpose is "the operator's viewer for the operator's agent data"; localhost-only; read-only on sources by design (`os.O_RDONLY`).
- It's the same privilege level the operator already grants every other agent CLI they run (opencode/codex/claude — all run as the operator).
- A dedicated user for *this one tool* is security theater relative to the rest of the operator's stack and was actively breaking source access (the 0-sources bug during initial install — see History).
- The systemd hardening stays (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome=read-only`, `PrivateTmp`, `ReadWritePaths` scoped to the data dir for the ingester).

**2. Explicit `--source` flags in the unit, NOT auto-discovery.**
Auto-discovery probes `$HOME`-relative paths. The explicit list is (a) auditable — the unit file shows exactly what the service reads, and (b) independent of `$HOME` resolution (irrelevant now that we run as the operator, but it survived because it's the cleaner contract). The committed template carries `__AI_VIEWER_SOURCES__`; the install script renders it.

### Verifying an install

```bash
systemctl is-active ai-viewer-ingest.service ai-viewer-serve.service   # both → active
curl -s http://127.0.0.1:7710/api/health | python3 -m json.tool         # status + sources[] (want len ≥ 1)
journalctl -u ai-viewer-ingest -u ai-viewer-serve -f                    # live tail
scripts/install-system.sh status                                       # the bundled status command
```

- `/api/health.status` is `degraded` on a fresh install until sources catch up (parse errors from real-data edge cases + historical staleness) — that's **correct** (errors are surfaced, not hidden). It is NOT an install failure.
- `/api/health.sources` is empty for the first few seconds (sources register on the first batch flush, after the adapters scan) — wait, don't panic.

## Operating

- **Logs:** `journalctl -u ai-viewer-ingest -u ai-viewer-serve -f` (journald via stdout; the primary sink).
- **Restart after a code change:** `scripts/install-system.sh` (uninstall is NOT needed — install is idempotent; it rebuilds + atomically replaces binaries + re-renders + `daemon-reload` + restarts).
- **Data is disposable.** `/opt/ai-viewer/data/index.db` is derived. Deleting it (or `uninstall`) triggers a full re-ingest on next start; the source files are the source of truth. Never back up the index.
- **Read-only on sources.** Neither binary writes to `~/.ai-agent`/`~/.claude`/etc. The ingester opens source files `os.O_RDONLY`; the server never touches them.
- **Port:** `7710` (the spec default; `127.0.0.1:7710`). Changing it requires editing the rendered serve unit + the frontend's Vite proxy target (dev only).

## Common issues

- **`sources: 0` after install.** Either the units run as the wrong user (permission denied on the `0700` home dirs — re-run install so it renders `User=<operator>`), or you're checking too early (sources register on first batch flush — wait ~5s).
- **`permission denied` on source paths in the ingest log.** The unit's `User=` doesn't own the data. Confirm `/etc/systemd/system/ai-viewer-ingest.service` has `User=<operator>` (the install script renders this from `SUDO_USER`/`$USER`).
- **Server not responding after 20s.** `systemctl status ai-viewer-serve` + `journalctl -u ai-viewer-serve` — the usual cause is the schema-not-ready start-order race (the serve unit's `Restart=on-failure` cycles until the ingester migrates); give it another few seconds.
- **`bad substitution` / sed errors from the installer.** The script renders the unit via `sed` (no `-o`); if you edit it, don't reintroduce `sed -o`.

## The user install (alternative, no root)

`scripts/install-systemd-user.sh` — binaries to `~/.local/bin`, **user** systemd units (`systemctl --user`), data in `~/.local/share/ai-viewer/`. Uses auto-discovery (works because it runs as the operator under the user manager). Same ports, same localhost-only. Use this when root is unavailable; the system install is otherwise preferred (runs at boot without a login session).

## History (why these decisions)

- **Initial system install used a dedicated `ai-viewer` user + auto-discovery** → discovered 0 sources. Root cause #1: auto-discovery probes `$HOME`-relative paths, but the service user's `$HOME` was `/home/ai-viewer`. Fixed by making sources explicit.
- **Re-rendered with explicit sources, still 0 sources** → `permission denied` on every source path. Root cause #2: the operator's `~/.ai-agent` etc. are `0700`, so even with correct paths the dedicated user couldn't traverse them. Tried ACLs; rejected as fragile/invasive.
- **Final: run as the operator + explicit sources.** Correct, simple, matches the mental model, keeps the hardening. Recorded here so the dedicated-user idea is not retried without a genuinely new reason (e.g. the app gains a network-exposed surface — then least-privilege outweighs simplicity and this decision is revisited in its own SOW).
- The deployment spec (`.agents/sow/specs/deployment.md` §"System Install") is the authoritative contract; this skill is the quick-reference + the reasoning.
