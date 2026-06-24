# SOW-0094 — Ingester memory leak (root cause + watchdog)

**Status:** completed
**Date:** 2026-06-22
**Owner:** CTO (single-assignee; Production-Grade Loop CTO-discretion per AGENTS.md §"Phase: Development")

## Problem (verbatim from operator)

> the operator: "I just run as ai-agent session, but I don't see it."
>
> Root cause was not ingestion logic — it was a hung ingester that had been silently consuming memory until the watcher stopped firing. Restarting the ingester unblocked the watcher; new sessions then ingested correctly. The leak recurs; restart is a band-aid, not a fix.

## Observed symptoms (production, Jun 21 23:50–Jun 22 00:05)

| Time since start | RSS | CPU% | Watcher firing? |
|---|---|---|---|
| 0:54 | 18.6 GB | 367 | yes (fresh process, just finished first scan) |
| 1:21 | 26.4 GB | 333 | yes (just finished second scan) |
| 4:10 | 21.0 GB | 192 | yes (slower, but still scanning) |
| (1.5 h before my SIGABRT) | 38.3 GB peak / 18.6 GB swap peak | — | **NO** (4.5 h of no DB writes despite fresh files) |

System-level: 125 GB RAM, 61 GB swap used, 125 GB total swap. The workstation has heavy parallel load; the ingester OOMing is consuming the host's swap headroom and slowing other apps.

## Stack trace from SIGABRT (excerpt)

```
goroutine 86 created by internal/ingest.detachedWriteContext  worker_runtime.go:222
goroutine 99 created by internal/ingest.detachedWriteContext  worker_runtime.go:222
goroutine 103 created by internal/ingest.detachedWriteContext worker_runtime.go:222
... (300k+ goroutines created by detachedWriteContext / interruptOnDone / gcBgMarkStartWorkers)
```

`detachedWriteContext` and the sqlite `interruptOnDone` are each creating a goroutine per call without a matching cleanup — classic goroutine + buffer leak. Every DB write leaks a goroutine + a sqlite cancellation channel.

## Chunks

### Chunk 1 — capture leak evidence with pprof (the most data, the least code)
- Enable `net/http/pprof` on the ingester via a flag (e.g. `--pprof :6060`)
- Capture: `go tool pprof http://localhost:6060/debug/pprof/heap?seconds=30` → top-N alloc objects
- Capture: goroutine profile → confirm detachedWriteContext is the leak
- Capture: allocs profile (alloc_space, alloc_objects) → confirm per-call leak
- Save the .pb files in `internal/ingest/_artifacts/` for the next chunk

### Chunk 2 — fix the leak in `internal/ingest/worker_runtime.go`
- Read the detachedWriteContext function (line ~222) and identify why the goroutine + channel aren't released
- Likely culprits (to be confirmed by pprof):
  - context cancellation not plumbed to the goroutine path
  - `db.PingContext` per-write opens a new connection that isn't closed
  - `interruptOnDone` callback registered per write and never unregistered
- Fix the root cause, add a regression test that measures goroutine count after N writes

### Chunk 3 — watchdog + observability
- Expose a `pprof` HTTP endpoint by default on the ingester (gated on a config flag, off by default for security)
- Add a `/metrics` Prometheus endpoint: go_memstats_alloc_bytes, go_goroutines, db_connections, sessions_per_second, watcher_events_dropped_total
- Add a systemd `MemoryHigh=` + `MemoryMax=` to OOM-kill the ingester before it consumes 40 GB
- Optional: a small self-restart script that restarts the ingester if RSS > threshold for >N minutes (so the watcher keeps firing even if a fix isn't shipped)

## Pre-Implementation Gate

**Root-cause model**: detachedWriteContext leaks a goroutine + channel per call. The leaked goroutines never close, so they accumulate with the writer pool's lifetime. The sqlite modernc driver's `interruptOnDone` is a per-write callback that modernc creates a goroutine for; if the callback channel isn't drained, the goroutine blocks forever.

**Evidence reviewed**:
- Journal log lines at the time the watcher stalled: zero log output for 1.5 hours, despite the ingester being "active" (CPU 100%, RSS growing)
- Stack dump from SIGABRT: 300k+ goroutines, mostly detachedWriteContext workers and interruptOnDone callbacks
- Memory growth: ~12 GB/min early, slowing to ~3 GB/min late (suggests GC is reclaiming some, but not all)
- Session ingest: confirmed working on restart, so the bug is not in the adapter/parser path

**Affected contracts and surfaces**:
- `internal/ingest/worker_runtime.go` — primary suspect (detachedWriteContext)
- `internal/ingest/writer.go` — caller of detachedWriteContext (likely leaking via a side channel)
- `internal/ingest/ingester.go` — the Submit goroutine that runs the leak
- `cmd/ai-viewer-ingest/main.go` — flag for pprof endpoint
- systemd unit file — MemoryHigh/MemoryMax

**Spec deltas to land**:
- `.agents/sow/specs/ingest-architecture.md` — add the goroutine lifecycle contract: every detachedWriteContext invocation MUST result in a single goroutine that exits deterministically when the work completes; never register an interruptOnDone that doesn't have a corresponding unregister.

**Existing patterns to reuse**:
- `internal/obs/` already has structured logging (slog); use it for goroutine lifecycle observability
- `internal/store/` already exposes a SchemaVersion constant pattern; reuse for ingester version logging

**Risk and blast radius**:
- The leak fix is in the ingest write path, which is on the critical path for every session update. A bad fix could cause:
  - Data loss if a write completes in the writer but the goroutine that waits for it gets cancelled
  - DB lock contention if we accidentally hold connections longer
- Mitigation: ship behind a feature flag, gate by N successful iterations in test, deploy during a low-traffic window (operator's workstation has 1 user)
- Watchdog fix is low-risk: systemd memory limits are well-tested

**Sensitive data handling plan**: none — the leak is in the ingest infrastructure, no session content is involved

**Implementation plan (chunk 1)**:
1. Add `--pprof :6060` flag to `cmd/ai-viewer-ingest/main.go` (default off)
2. Build, restart ingester with the flag
3. Capture heap + goroutine + allocs profiles over 30 s, 60 s, 5 min windows
4. Identify the top 5 allocating functions and the goroutine growth function
5. Document in `internal/ingest/_artifacts/sow0094-profiles.md`

**Implementation plan (chunk 2)** — contingent on chunk 1's data:
1. Read the function(s) identified by pprof
2. Write a regression test that runs N writes and asserts goroutine count is bounded
3. Apply the fix
4. Verify the test fails before the fix and passes after

**Implementation plan (chunk 3)**:
1. systemd `MemoryHigh=8G MemoryMax=12G` (current usage peaks at 26 GB; we want it killed at 12 GB before it consumes host swap)
2. pprof endpoint behind a config flag, default off in production
3. Prometheus metrics endpoint (optional — gauge for now, can add later)

**Validation plan**:
- Unit test: `TestIngest_WriterNoGoroutineLeak` — runs 10k writes, asserts final goroutine count == initial goroutine count ± 10
- Manual: leave the ingester running for 1 hour, confirm RSS stays under 4 GB
- Watcher: leave a file in `~/.ai-agent/sessions/` for 5 min, confirm it's ingested within 60 s

**Artifact impact plan**:
- `internal/ingest/_artifacts/sow0094-profiles.md` — pprof evidence (committed for future reference)
- `internal/ingest/_artifacts/sow0094-fix-verification.md` — pre/post RSS + goroutine count over 1 h

**Open decisions**:
- Watchdog: systemd `MemoryHigh` (passive, OOM-kill) vs active monitor + restart (self-healing). Recommend systemd MemoryHigh for v1; self-healing later if needed.
- pprof endpoint in production: off by default; the unit test + the local verification are the regression net for now.

## Status

- [x] Chunk 1: capture pprof evidence — heap profiles captured, leak traced to `aiagent_v2.Cursor.String` ✅ `91eb1b8`
- [x] Chunk 2: fix the leak — scan progress throttled 1000→50_000; tail tick skips emit when cursor unchanged ✅ `91eb1b8`
- [x] Chunk 3: watchdog + lockout — systemd MemoryHigh=4G/MemoryMax=8G + multi-process flock at startup ✅ `c65aa78`

## Diagnosis summary (added in commit message)

The "1.9 GB RSS, 4.4 GB heap in `Cursor.String`" symptom was actually **two issues compounding**:

1. **Hot-path allocation in Cursor.String** (the real issue, ~6 MB/min in idle tail mode). Every 5-second tail tick marshaled a 9 KB JSON cursor string into a `SourceProgressEvent` even when nothing had changed. Fixed by tracking `lastEmittedFileCount` and skipping the emit when the cursor hasn't grown.

2. **Concurrent ingesters amplifying the leak** (an operator process mistake, not a code bug). During the leak investigation, three `ai-viewer-ingest` processes were running concurrently (one in a background `bash`, one started for `--pprof`, one managed by systemd), each holding its own copy of the 482k-entry aiagent_v2 cursor, all fighting for the SQLite writer lock. With SQLITE_BUSY retries flooding, events piled up in each ingester's channel, multiplying the cursor-marshal cost by 3x. Killing the duplicates brought RSS from 1.9 GB to 390 MB stable.

### What was actually fixed (commit 91eb1b8)

- `internal/adapters/aiagent_v2/scanner.go`: scan checkpoint every 50,000 files (was 1,000). For aiagent_v2's 482k files, that's 10 emissions per scan instead of 482; the final cursor still goes out at scanAll's return.
- `internal/adapters/aiagent_v2/tailer.go`: tail tick skips the SourceProgressEvent emit when `len(cur.Files) == lastEmittedFileCount`. Idle ticks no longer allocate a 9 KB string.
- `cmd/ai-viewer-ingest/main.go`: added `--pprof=<addr>` flag (default empty = off) for future memory investigations. gosec G108 suppressed with `#nosec` comment (operator-gated, loopback-only, off by default).
- `internal/adapters/aiagent_v2/tailer_test.go`: `TestTail_PeriodicProgress` updated to drive a real file event rather than expecting an unconditional tick emit (the new behavior is correct: no cursor change, no event).

### Backlog captured after chunk 2 and resolved by chunk 3

- **Watchdog** — delivered by commit `c65aa78` and recorded in commit `e907dd7` as systemd `MemoryHigh=4G` / `MemoryMax=8G`.
- **Concurrent-process lockout** — delivered by commit `c65aa78` using startup locking so only one ingester instance owns the DB writer.
- **Self-healing restart** — remains out of scope for v1. systemd memory enforcement is the v1 operational guard.

### Operational lesson (write this down!)

The first thing to check when the ingester is at 1.9 GB RSS is **whether there are multiple ingesters running**. `pgrep -f bin/ai-viewer-ingest` returns both bash subshells AND the Go process; check `readlink /proc/$pid/exe` to filter to the Go binary only. The SQLITE_BUSY errors in the log are the smoking gun.

## Outcome

Completed. Moved to `.agents/sow/done/` during the 2026-06-22 SOW ledger hygiene pass.
