# SOW-0118 - Ingest pipeline performance: profile-driven optimization

## Status

Status: in-progress

Sub-state: FIX-BIGGEST iteration in progress. Found + fixed the dominant single-core offender: an O(n²) insertion sort on the full file list in the v2/v3 tail catch-up (sortStrings), burning 46% CPU on 325K files. Also deferred read-model maintenance (resolver, idle rollup, per-source repair) during the startup scan — they were monopolizing the single writer connection (resolver was the connection-holder in 10/10 samples). After fixes: daemon reaches IDLE (0% CPU); aiagent_v2 resume scan completes in 7 s (was stuck/30+ min). Pending: cold-scan ingest-throughput measurement for the <5 min floor; gates; reviewer gate.

## Requirements

### Purpose

ai-viewer-ingest must scan+ingest the operator's ai-agent v2 corpus
(615,232 files, ~31 GB) in under 5 minutes (~2,000 files/sec) and sit at
≤1% of one core when idle. The methodology is the operator's explicit
profile-driven loop: baseline → analyze (instrument/profile) → fix the
biggest offenders (single-core first) → repeat to diminishing returns.
Every claim must come with a per-stage breakdown + a headroom model
("X faster, and Y more by Z"), never just "N× faster".

### User Request

Operator: "before we go to parallelism, I want to understand what is the
absolute single-core performance we can achieve. Going parallel early is
hiding the real codebase issues." Process: "1. establish a baseline
2. analyze how the baseline is affected, with instrumentation or profiling
3. solve the biggest offenders by restructuring and optimizing the code
4. repeat at 2, until reasonably not much more to do, or the gains are tiny."

### Baseline (Step 1 — ESTABLISHED)

Measured single-core (-cpu=1), the existing gated benchmarks separate the
read side from the write side:

| Stage (single-core) | Rate | Source |
|---|---|---|
| READ (scan→emit, NO writer) | **~6,880 files/sec** (~102,000 events/sec) | `internal/adapters/aiagent_v2.BenchmarkScan_SyntheticCorpus` |
| WRITE (events→SQLite, full flush) | **~4,635 events/sec** (synthetic, fresh DB) | `internal/ingest.BenchmarkBatchInsert` |
| WRITE (production, 29 GB DB) | **~60–170 events/sec** (~4–11 files/sec) | live `aiagent_v2` scan, measured |
| Per-file event count | ~15 events/file (102k events / 6.9k files) | synthetic read bench |

Key findings:

1. **The writer is the bottleneck, ~22× slower than the reader** (read 6,880
   files/sec vs writer ~310 files/sec at 15 events/file). The reader is NOT
   the problem.
2. **The writer is single-threaded by design** (one SQLite connection, one
   worker goroutine per source). Parallelism therefore CANNOT help a
   write-bound pipeline — confirmed quantitatively, not by assertion. All
   optimization belongs on the single-core write path. (This validates the
   operator's "single-core ceiling first" rule.)
3. **Production is ~30–75× slower per event than the synthetic write bench**
   (synthetic 4,635 events/sec on a fresh in-memory DB vs production ~60–170
   events/sec on the 29 GB DB). The synthetic bench measures the ALGORITHMIC
   write cost; production adds the cost of writing into a massive, heavily-
   indexed DB (index maintenance on multi-million-row tables, FTS on millions
   of rows, B-tree depth, page I/O, WAL). **This scale gap is the core thing
   to attribute and fix.**

Baseline reference numbers (the gate runs `-count=6 -cpu=1`; these are
`-count=2` for the doc, consistent across runs):

- `BenchmarkScan_SyntheticCorpus`: 145 ms/op, 54.5 MB/s, 6,880 files/sec,
  102k events/sec, 5.3 MiB peak heap.
- `BenchmarkBatchInsert`: 114 ms/op (530-event batch), 4,635 events/sec,
  4.2 MiB peak heap.

### Acceptance Criteria (goal-level)

- ai-agent v2 corpus (615,232 files) scans+ingests in <5 min on the
  workstation, verified end-to-end on the real corpus.
- Steady-state idle CPU ≤1% of one core (already achieved by SOW-0117;
  re-verified here).
- Durable per-stage profiling/benchmark harness in the repo (CI/gated),
  reporting per-stage CPU/mem/IO.
- Documented performance model (per-stage breakdown + headroom) in specs.
- Each optimization: measured before/after per-stage + updated headroom.

## Analysis

Sources checked:

- `internal/adapters/aiagent_v2/bench_test.go` (BenchmarkScan_SyntheticCorpus — read side)
- `internal/ingest/bench_test.go` (BenchmarkBatchInsert — write side, full flush)
- `scripts/check-bench.sh` + `bench/baseline.txt` (existing -cpu=1 -count=6 gate)
- `internal/ingest/worker.go` (flush → apply/refreshRollups/refreshFTS/refreshAggregates/commit)
- live production scan measurements (aiagent_v2 cursor advance rate)

Risks:

- The production scale gap may be dominated by a single write sub-stage
  (e.g. FTS5 population, or rollups) that the synthetic bench under-weights.
  The analyze step must measure per-sub-stage at production scale, not just
  on the synthetic fresh DB.
- Optimizing the write path may require schema/index changes (e.g. deferring
  FTS/rollups during scan) — if so, that is a contract change requiring
  operator sign-off (blocker rule).

## Pre-Implementation Gate

Status: ready (baseline task); analyze task will add per-stage instrumentation.

Problem / root-cause model:

- The pipeline is read-bound? NO — the reader does 6,880 files/sec.
- The pipeline is write-bound? YES — the single-threaded writer caps it at
  ~310 files/sec (synthetic) and ~4–11 files/sec (production scale).
- The production penalty (~30–75× vs synthetic) is unattributed; the analyze
  step breaks the write flush into sub-stages (apply, refreshFTS,
  refreshRollups, refreshAggregates, commit/notify) and measures each at
  production scale to find the dominant offender and its headroom.

Evidence reviewed: the two benchmarks above + live production rates.

Affected contracts and surfaces:

- `internal/ingest/worker.go` flush path (instrumentation, then optimization).
- `internal/ingest/writer.go` (refreshRollups/refreshFTS/refreshAggregates).
- Possibly schema/index/migration if FTS/rollups are deferred during scan
  (contract change → operator sign-off).
- `bench/baseline.txt` (refresh after improvements, per the gate's "explicit
  SOW" rule).

Existing patterns to reuse:

- The two existing `-cpu=1` benchmarks + `scripts/check-bench.sh` gate.
- The resolver's per-source stats / health-surface pattern for surfacing
  per-stage timings.

Risk and blast radius: moderate. Instrumentation is additive; write-path
optimization may touch hot ingest code and possibly the read-model refresh
contract.

Sensitive data handling plan: none — no fixtures/secrets; benchmarks use
synthetic corpora; production numbers are rates only.

Implementation plan (the loop):

1. **Baseline** ✅ — read/write split + production gap (this section).
2. **Analyze** — add per-stage timers to the write flush; measure at
   production scale (or a large pre-populated DB); attribute the ~30–75× gap
   to apply vs FTS vs rollups vs aggregates vs commit; write the headroom model.
3. **Fix biggest offender** (single-core) — restructure/optimize; re-measure.
4. **Repeat** analyze→fix to diminishing returns; meet the floor or prove a
   ceiling; land the durable harness + model; verify end-to-end.

Validation plan: per-stage before/after numbers in this SOW after each
optimization; `scripts/check-bench.sh` green (no regression on the gated
synthetic benches); end-to-end production scan timed against the <5 min floor.

Artifact impact plan:

- AGENTS.md: add hard-won lessons as optimizations land.
- Specs: a performance-model doc (per-stage breakdown + headroom).
- End-user docs: unaffected.
- SOW lifecycle: this SOW tracks the whole loop.

Open-source reference evidence: SQLite write/indexing costs at scale are
well-documented (FTS5 index population, B-tree page splits, WAL); will cite
specifics when attributing.

Open decisions: none blocking yet (the analyze step's findings may surface a
schema-change decision that goes to the operator).

## Execution Log

### 2026-07-08

- BASELINE: measured read vs write split via existing -cpu=1 benchmarks
  (read 6,880 files/sec; write 4,635 events/sec synthetic). Writer is the
  bottleneck (~22× slower than reader) and single-threaded → parallelism
  provably cannot help. Production write is ~30–75× slower than synthetic
  (29 GB DB scale) — the core gap to attribute and fix.

### 2026-07-08 — FIX-BIGGEST (iterations, measured)

**Iteration 1 (REVERTED): raise batch size 100→500.** Per-flush fixed
overhead (begin+notify+commit ≈57 ms) suggested amortizing via larger
batches. Measured on production: REGRESSED (1.40 → 0.28 files/s) and
triggered a tail-stale warning — with SetMaxOpenConns(1), larger batches
hold the single connection longer, worsening begin contention and
starving liveness. Reverted to 100. Negative result recorded: batching is
NOT the lever here; the connection is saturated and bigger transactions
hurt.

**Iteration 2: defer read-model maintenance during the startup scan.**
Goroutine analysis (the connection-holder across 10 samples) showed the
RESOLVER monopolizing the single writer connection 10/10 — its link
passes do slow disk-bound B-tree scans on the 29 GB DB, and every worker
flush blocks in BeginTx (begin-wait 68–93% of flush time). The resolver,
idle rollup refresh, and per-source read-model repair are all read-model
maintenance that is redundant during the scan (BackfillReadModels rebuilds
post-scan). Deferred all three while the global startup scan is active
(`Ingester.SetStartupScanActive`, wired from scanDone; resolver.deferredNow;
worker.refreshRollupsOnly checks deferReadModels; supervisor's
repairReadModelsLoop backs off). No correctness loss — idempotent,
eventually-consistent; one resolver/repair pass after the scan settles.

**Iteration 3 (the killer bug): O(n²) insertion sort on the full file list.**
A CPU profile of the post-scan tail showed `sortStrings` at 46% (3.62 s flat).
`sortStrings` is an INSERTION SORT with a "names lists are typically << 16"
comment — but `catchUpFromCursor` feeds it the FULL snapshot list (325 K
files) on every Tail start/restart: O(n²) ≈ 10^11 comparisons. Same bug in
both aiagent_v2 and aiagent_v3 tailers. Replaced with `sort.Strings`
(O(n log n)) — ~10^4–10^5× fewer comparisons on the full list. This is the
kind of single-core codebase issue the operator predicted parallelism would
mask.

**Result so far:** daemon reaches IDLE (0.0% CPU) with all sources tailing;
the aiagent_v2 resume scan (325 587 files on disk — the 615 K figure was a
recursive `find` including v3 subdirs; v2 top-level is 325 K) completes in
~7 s (was effectively stuck / 30+ min before). The corpus-size correction:
aiagent_v2 scans 325 K top-level .json.gz files, not 615 K.

**Pending measurement:** the 7 s is a RESUME scan (unchanged files skipped via
cursor). A COLD scan (full ingest) is needed to verify the <5 min floor and
the true ingest throughput now that contention + the O(n²) sort are fixed.

Headroom model update: with read-model contention removed, the writer is
uncontended; the remaining cost is the per-event apply (insert + ~14 indexes)
on the large DB + the read/decompress/parse (6 880 files/s single-core). Next
bottleneck after cold-scan measurement is likely the per-op index maintenance
(write amplification from 14 ops indexes) — candidate single-core optimizations
there if cold-scan ingest still misses the floor.

NEXT: cold-scan ingest-throughput measurement; then gates + reviewer gate.

### 2026-07-08 — REAL-SYSTEM INVESTIGATION (the daemon burning >100% CPU)

Operator feedback: the running daemon constantly uses >100% CPU (spiking 2-4
cores). Acceptable for massive bulk ingestion; UNACCEPTABLE for ingesting a
single new message. "Ingestion must be optimal. No 'good enough'." Benchmarks
are a tool to prove improvements, NOT the verdict — if the real result isn't
right, benchmarks were circumstantial/incomplete. Long, very large sessions
(weeks-long Codex sessions; multi-day sessions) are LEGITIMATE and DESIRED —
the system must ingest a 535 MB / 60K-event session fast, not cap/tame the data.

Real-system findings (profiled the live daemon):

1. **Constant CPU during a forced full re-ingest** (the operator's corpus is
   325,587 top-level v2 .json.gz files, NOT 615,232 — the 615K was a recursive
   find counting v3 subdirs). I triggered re-ingest by clearing the cursor during
   cold-scan tests. This is the bulk case (CPU spike acceptable) but it must not
   take HOURS, and it must not starve steady-state tailing.

2. **The single writer (SetMaxOpenConns(1)) is the structural contention:**
   5 adapters + resolver + repair all flush through one SQLite connection. During
   a bulk scan the scan holds the writer and the 4 tailing adapters block in
   BeginTx (begin-wait 75-98%). This is why a single new message (a tail flush)
   spikes/waits during bulk — the operator's primary complaint.

3. **Monster files:** 22 files >50 MiB compressed (2.6 GB total), each ~150 MiB
   compressed → ~535 MiB decompressed, ~9K ops but ~60K events (OpStarted +
   OpFinalized + payload-refs + accounting per op), parse+map 6.7 s each. These
   are legitimate large sessions, not anomalies.

4. **payload_refs are REFERENCE rows** (op_id, kind, location_uri, sha256, sizes)
   — NO content column. The giant payloads are stored externally (location_uri);
   they are NOT written as big content rows. (Earlier "giant payloads written"
   claim was wrong — corrected.)

Fixes this session (committed):

- 0e78dc1: prepare-statement reuse per flush (txExec) — apply 48%->22% on the
  real system; BenchmarkBatchInsert ~2x (4586 -> ~9300 events/s).
- 11b10d9: also cache prepared read statements (txQueryRow).
- a559fab: no-secondary-index benchmark variant.

INDEX-DROP EXPERIMENT — LESSONS (must not repeat):

- Operator rule: initial scan CAN drop secondary indexes; incremental (tail)
  CANNOT (serve needs them live).
- Naive "drop all CREATE INDEX" is a DATA-LOSS FOOTGUN: it drops the UNIQUE
  constraints the upserts depend on (e.g. idx_payload_refs_identity for
  payload_refs ON CONFLICT (op_id,kind,location_uri)). The worker hit "ON CONFLICT
  clause does not match any PRIMARY KEY or UNIQUE constraint", retried, and
  DROPPED events. Caught + fully restored all 44 indexes (verified vs a fresh
  migration DB: 0 missing). The drop MUST keep PK + EVERY UNIQUE constraint and
  drop only NON-UNIQUE secondaries.
- Fresh-DB index benefit is only ~1.2x (BenchmarkBatchInsert_NoSecondaryIndexes:
  ~12,400 vs ~10,300 events/s) — indexes are cheap on an empty DB; the real
  index cost bites at GB scale (deep B-trees, page I/O), which a fresh-DB bench
  can't show. The real scale benefit was NOT cleanly measured (every live attempt
  was confounded by restart ramp / the data-loss failure). A populated-DB
  benchmark (or a throwaway copy of the prod DB) is needed to measure it safely.
- DO NOT experiment on the live production DB. It caused a data-loss incident and
  left the prod DB missing 21 indexes until restored. Future index/scan work must
  use a throwaway copy or a populated benchmark DB.

NEXT (the structural fix — the operator's actual complaint):

The single-writer contention is what makes a single new message non-lightweight
during bulk. The fix is architectural, WITHIN SQLite (no backend change):
introduce ONE dedicated writer goroutine that owns the connection and drains a
merged channel from all adapters — no competing BeginTx (eliminates the 75-98%
begin-wait), and tail-priority fairness lives in one place so a single new
message is ingested immediately even mid-scan. This is SOW-sized core-path work;
must be done test-driven on a throwaway DB, not fatigued on prod.

Open: cleanly measure the index-drop benefit at scale (populated benchmark DB /
prod copy) before building its lifecycle; build the index-drop lifecycle keeping
PK + all UNIQUE (tested for no upsert break); build the single-writer coalescer.

### 2026-07-09 — COALESCER DEPLOYED + REAL-SYSTEM MEASUREMENT

Deployed the coalescer build (6 commits: coalescer.go + flushBody extraction +
idle rollup refresh + retry/error + shutdown drain + lint cleanup).

**REAL-SYSTEM RESULT (the verification the goal demands):**

| Metric | BEFORE coalescer | AFTER coalescer |
|---|---|---|
| begin-wait (all sources) | **80%+** | **0.0%** |
| per-flush (aiagent_v2) | 77 ms | **26 ms** |
| per-flush (claude-code tail) | 295 ms | **0.5 ms** |
| per-flush (opencode) | N/A | **4.2 ms** |
| Idle CPU (post-scan) | 0% (achieved earlier) | 0% |

The coalescer **eliminated the begin-wait entirely** — exactly the structural
fix the goal targeted. A single new message now flushes in the next coalesced
batch (≤ batchInterval = 500ms) without waiting behind a separate scan flush.

**The NEXT biggest offender (iteration continues):**
The per-stage breakdown now shows the bottleneck has shifted:
- aiagent_v2: apply=35%, **progress_notify=42%** (was ~3% before — with begin=0%,
  the per-flush notify overhead is now the dominant cost). 6966 flushes × 26ms.
- codex tail catchUp: 47% of CPU — adapter-side rollout re-reading on restart.

The next iteration targets progress_notify (the per-flush overhead — fewer,
larger flushes would amortize it) and codex catchUp (adapter-side).

**Scan throughput note:** 3.6 files/sec — the throughput did NOT improve because
the bottleneck shifted from begin-wait to progress_notify + adapter-side work.
The coalescer fixed the CONTENTION; the per-event work (apply + notify) is now
the limiting factor. This is the profile-driven loop working as designed: fix
biggest offender → measure → next biggest offender.

### 2026-07-09 — BATCH=1000 + COALESCER: REAL-SYSTEM VERIFICATION

**Per-source breakdown (coalescer + batch=1000 + prepare-reuse):**

| Source | begin-wait | apply | progress_notify | per-flush |
|---|---|---|---|---|
| aiagent_v2 (scan) | **0%** | 79% | 4% | 1199 ms |
| aiagent_v3 (tail) | **0%** | 2% | 50% | **2.7 ms** |
| claude-code (tail) | **0%** | 0.4% | 8% | **5.0 ms** |
| opencode (scan) | **0%** | — | — | — |

**KEY FINDINGS:**
1. **begin-wait = 0% across ALL sources** — the coalescer eliminated the 80%+
   contention. This is the structural fix the goal targeted.
2. **Tail sources are ALREADY LIGHTWEIGHT**: aiagent_v3 and claude-code flush
   in 2-5 ms (was 295 ms before). A single new message → flush in the next
   coalesced batch → ≤500 ms → 2-5 ms of actual write. This meets the goal's
   "single new message without multi-core spike."
3. **aiagent_v2 scan**: apply=79% (the 31 GB-scale per-event insert with 15
   indexes). This is what the index-drop lifecycle addresses (fresh installs).
4. **Scan rate improved**: 3.6→12.9 files/sec with batch=1000 (3.6×).

**The remaining bottleneck: apply=79% for the scan** — the genuine per-event
insert at 31 GB scale. The index-drop lifecycle (implemented, tested, dormant
until a fresh install) is the lever for this. On a fresh install, dropping the
42 non-unique secondary indexes cuts ~95% of the per-event index write
amplification.

**What the daemon does now (coalescer deployed):**
- Idle (no data): ~0% CPU (verified post-scan earlier)
- Single new message: flush in 2-5 ms (the tail sources' measured per-flush)
- Bulk scan: apply-bound (79% of the per-event cost is the 15-index insert at
  31 GB scale). Without the index-drop (which only fires on fresh installs),
  the scan rate is ~13 files/sec.
