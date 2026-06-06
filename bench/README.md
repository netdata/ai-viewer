# ai-viewer benchmarks

Two complementary benchmarks live here:

1. **Synthetic Go benchmarks** — hermetic. The current suite is 11
   benchmarks across 7 packages: ai-agent v2 `Scan` + `Tail`, claude-code
   `Scan` + `Tail`, Codex `Scan` + `Tail`, Opencode `Scan` + `Tail`,
   SQLite batch insert, REST query, and SSE fanout. They are gated
   **locally** by `scripts/check-bench.sh` (benchstat vs
   `bench/baseline.txt`; the workstation baseline is not comparable to
   GitHub-runner hardware), so CI runs only the compile-smoke + the gate
   self-test. The original ai-agent v2 `Scan` bench is documented below;
   see `quality-gates.md` §Go — Benchmarks for the full set and the
   local-gate rationale.
2. **Real-data harness** — operator-runnable, measures the v2
   adapter against the live `~/.ai-agent/sessions/` directory and
   feeds the SOW-0001 §Chunk 9 60-minute backfill gate.

Both are read-only. The real-data harness never writes, renames, or
deletes anything under its `--root`.

## Synthetic Go bench

```bash
go test -bench=BenchmarkScan_SyntheticCorpus -benchmem -count=3 \
    ./internal/adapters/aiagent_v2
```

The bench builds the synthetic corpus inside `b.TempDir()` and tears
it down when the test exits. Custom metrics reported alongside the
standard `ns/op` and `B/s`:

| metric | meaning |
|---|---|
| `files/sec`             | files processed per second (wall) |
| `events/sec`            | canonical events emitted per second |
| `peak_heap_mb`          | peak `runtime.MemStats.HeapInuse` observed during the run |
| `corpus_compressed_mb`  | total compressed input the bench wrote |

The corpus stays **below** the 50 MiB compressed streamer threshold:
it measures the small/medium-file fast path that dominates real
throughput (>99.9% of operator files). Streamer correctness is
exercised separately by `streamer_test.go` (`TestStreamer_*`), and
streamer throughput end-to-end by the real-data harness.

## Real-data harness

```bash
./scripts/bench-v2-backfill.sh                            # defaults
./scripts/bench-v2-backfill.sh "$HOME/.ai-agent/sessions" 16
```

Defaults: `root=$HOME/.ai-agent/sessions`, `workers=$(nproc)`. The
harness fans out file-level parsing across N goroutines, each
calling `aiagent_v2.ScanFile` (exported helper that wraps the
adapter's per-file processing). Workers share a metrics struct and
drain emitted events into counters; the v2 adapter API is unchanged.

Per-5-second progress lines go to stderr; the final summary block
goes to stdout in a format suitable for capture into
`bench/baseline.txt`.

### Exit codes

- `0` — wall time < 60 minutes; the SOW-0001 §Chunk 9 gate passes.
- non-zero — wall time ≥ 60 minutes; SOW pauses for redesign per
  §Chunk 9 plan. The summary still prints; redirect stdout to a
  file before checking `$?` if you want both signals.

### Warm vs cold page cache

A cold-cache run measures disk bandwidth as much as the adapter.
For repeatable adapter-only numbers, warm the cache once (this is
still read-only):

```bash
find "$HOME/.ai-agent/sessions" -name '*.json.gz' -print0 \
    | xargs -0 -P "$(nproc)" cat > /dev/null
```

Then run the harness. Report the **warmed** number in
`bench/baseline.txt`. The cold-cache number is useful supporting
evidence in the SOW Chunk 9 entry but is not the SOW gate metric.

## `bench/baseline.txt`

Frozen baseline consumed by `benchstat` via `scripts/check-bench.sh`. It
is the raw `go test -run=^$ -bench=. -benchmem -count=6` output for the 11
synthetic benchmarks across 7 packages, prefixed by a comment header
carrying benchmark-code provenance and the `goos/goarch/pkg/cpu` config
lines (benchstat groups by config, so the baseline and the current run
must share it). `check-bench.sh` compares a fresh run against it and fails
on a statistically-significant > 20% **sec/op** regression for any
benchmark (the `geomean` aggregate and the custom metrics are excluded).
`count=6` is benchstat's minimum for a 0.95 confidence interval; baseline
refresh requires an explicit SOW.

Benchmark fixtures should not add helper-goroutine scheduler noise to a serial
hot-path measurement. If a benchmark needs to keep a queue/channel on the fast
path, prefer deterministic buffer sizing or pre-seeding over background
drainers unless the helper concurrency is itself the behavior being measured.

The real-data harness digest is NOT in `baseline.txt` — it is saved
separately (e.g. `bench/v2-backfill-2026-05-27.txt`) as a dated,
human-readable "did Chunk 9 still pass" record, not consumed by benchstat.

## The 60-minute gate

`SOW-0001` §Chunk 9 sets a hard upper bound: a full read+parse pass
over the operator's ~294K-file corpus must complete in under 60
minutes wall time. The synthetic Go benchmarks are the **local**
regression signal (`scripts/check-bench.sh` on the workstation; CI runs
the compile-smoke, not the regression comparison); the real-data harness is
the absolute test (production-shaped data, production-shaped
parallelism).

If the real-data harness exceeds 60 minutes:

1. Capture the full output (the harness prints a bottleneck-shaped
   summary: bytes/sec compressed, files/sec, peak RSS, streamed
   files, parse errors).
2. Do NOT add parallelism inside the v2 adapter unilaterally. The
   harness already drives N workers; if N workers is not enough,
   the bottleneck is somewhere else (disk, gzip, JSON parse, syscall
   overhead).
3. Pause the SOW and write up the analysis in the active
   `.agents/sow/current/SOW-0001-phase-1-foundation.md` Chunk 9
   entry for review.

The repo never silently passes a 60-minute gate failure. The
harness exits non-zero so any wrapper script (CI, ad hoc) catches
it.
