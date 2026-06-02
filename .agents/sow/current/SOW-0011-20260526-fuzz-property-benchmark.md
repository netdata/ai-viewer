# SOW-0011 - Fuzz, Property-Based, and Benchmark Infrastructure

## Status

Status: in progress

Sub-state: drafted 2026-05-26 alongside SOW-0009 and SOW-0010. **Activated 2026-06-02** under the operator's standing blanket mandate, re-scoped to the measured live delta (see "Re-scope decisions" + "Enumeration" below). Prerequisite SOW-0001 (Go module + `internal/canonical/` + the adapters) is long merged; the packages exist. Branch: `sow-0011-fuzz-property-bench`.

### Re-scope decisions (CTO, 2026-06-02) — proposed activation, governs over the original draft where they differ

Drafted 2026-05-26 assuming **zero** fuzz/property/benchmark coverage; the measured live delta (see "Pre-measurement — 2026-06-02" below) supersedes that premise. Decisions:

1. **AC#1 (adapter fuzz) — ALREADY SATISFIED**: 10 `FuzzXxx` targets exist across all 5 adapters. Scope narrows to verifying them + adding committed seed corpora (`testdata/fuzz/`) where absent.
2. **AC#2 (canonical fuzz) — IN SCOPE**: enumerate the `internal/canonical` decoders at impl start; add ≥1 `FuzzXxx` per decoder.
3. **AC#4 (property tests, `rapid`, 5 invariants) — IN SCOPE**: add `pgregory.net/rapid` + `internal/canonical/property_test.go` with the five named invariants.
4. **AC#5/#6 (benchmarks + baseline) — IN SCOPE (partial today)**: 1 of 6 benchmarks exists (`aiagent_v2` Scan). Add the 5 missing (adapter `Tail`, canonical encode/decode, SQLite batch insert, REST query, SSE fanout); add the implementing-commit-SHA header to `bench/baseline.txt`.
5. **AC#7 (`scripts/check-bench.sh` + benchstat regression gate) — IN SCOPE**: build it with self-tests (a synthetic-regression must fail it); ≤ 20% threshold.
6. **AC#3 (CI fuzz wiring) — IN SCOPE for the per-push 30s + nightly 5min runs that FAIL the job on a crash. Auto-file-GitHub-issue-on-crash — DEFERRED** to a follow-up: a failed nightly fuzz job is already visible (red CI + GitHub workflow-failure notifications); auto-issue-with-crashing-input adds non-trivial CI machinery (issue create + artifact attach + dedup) for marginal value over job-failure visibility. Mirrors SOW-0010's deferral of reporting niceties; the crash-fails-the-gate behaviour is the value.
7. **AC#10 (bench sticky PR comment) — DEFERRED** (reporting nicety; artifact upload suffices; follow-up or fold into SOW-0013 CI wiring), mirroring SOW-0010.
8. **Doc-drift reconcile — IN SCOPE**: `quality-gates.md`, `project-testing`, `project-quality-gates` currently over-claim canonical fuzz / property tests / fuzz-CI / the bench regression gate as present; bring them to reality in lockstep as each lands (the severe drift cluster from the pre-measurement).

**Enumeration (2026-06-02, read-only sweep — resolves the open items + two scope-reducing facts):**

- **AC#2 (canonical fuzz) → satisfied by ZERO.** `internal/canonical` has **no decoder/parser functions** — it is pure event-type definitions + the `Event` interface (`events.go`); all untrusted-bytes parsing lives in the adapters (covered by AC#1's 10 existing fuzz targets). Nothing to add. **Spec delta**: `quality-gates.md` "Go — Fuzzing" + the skills say "every canonical decoder exposes a FuzzXxx" → correct to "adapters expose the fuzz targets; canonical has no decoders to fuzz."
- **AC#5 → 5 benchmarks, not 6 (4 new).** Canonical events are **constructed directly by adapters, never serialized/deserialized**, so the named "canonical event encode/decode" benchmark is **moot**. Real set: adapter `Scan` (exists — `internal/adapters/aiagent_v2/bench_test.go`) + 4 new: adapter `Tail` (`aiagent_v2/adapter.go`), SQLite batch insert (`internal/ingest` `worker.flush`, `worker.go:221`), REST query (`internal/presenter` `handleSessionsList`, `sessions_list.go:44`), SSE fanout (`internal/notify` `Hub.Deliver`, `hub.go:233`). Each `-count=5`. **Spec delta**: correct the 6→5 path list in `quality-gates.md` "Go — Benchmarks".
- **AC#4 (property tests)** → `internal/canonical/property_test.go` + `pgregory.net/rapid` (go.mod is `go 1.26`; add the pinned dep). Five invariants → targets: (a) round-trip equality — **adapter-mediated** (canonical has no decode), (b) monotone `SourceSeq` ordering, (c) idempotent re-ingest (`internal/ingest`), (d) schema completeness over the 11 event types, (e) timestamp µs precision across the ISO-8601→int64 boundary.
- **AC#6/#7** → add the implementing-commit-SHA header to `bench/baseline.txt` (absent today); `scripts/check-bench.sh` mirrors `scripts/bench-v2-backfill.sh` conventions + `benchstat` (fail > 20%), with synthetic-regression self-tests.
- **AC#3** → per-push `go test -fuzz=Fuzz -fuzztime=30s ./internal/adapters/...` step in `ci.yml`; a new `fuzz-nightly.yml` (`-fuzztime=5m`, cron staggered from the 06:00 race-stress / 07:00 govulncheck nightlies), mirroring `race-stress-nightly.yml`. Auto-issue-on-crash DEFERRED (decision 6 above). Seed corpora under `testdata/fuzz/` where absent.

**Net**: AC#1 ✅ done; AC#2 ✅ zero-needed (architectural); AC#5 reduced 6→5 (4 new); AC#3/#4/#6/#7 in scope; AC#10 + auto-issue deferred; doc-drift reconciled in lockstep. These two reductions are measured-reality re-scopes (CTO), surfaced to the operator for visibility — not requiring approval, mirroring SOW-0010's activation.

## Requirements

### Purpose

Land the highest-signal quality gates in the catalog: fuzz testing on every adapter parser and canonical decoder, property-based tests on canonical mapping invariants, and a benchmark suite with regression-gating against a committed baseline. After this SOW, every adapter parse path is fuzzed for at least 30 seconds per push and 5 minutes nightly; canonical encoders and decoders satisfy five named invariants under property generation; performance-critical paths (adapter Scan, adapter Tail, canonical encode/decode, SQLite batch insert, REST query, SSE fanout) cannot regress by more than 20% on any metric without an explicit baseline-refresh SOW.

### User Request

`AGENTS.md` Quality Gates table commits to lines 111-112: "Go fuzz → zero crashes per CI run" and "Go bench → ≤ 20% regression vs baseline". `.agents/sow/specs/quality-gates.md` lines 56-77 spec the fuzz cadences, the property-based test requirement, and the benchstat-based regression check. `.agents/skills/project-testing/SKILL.md` lines 49-83 list every mandatory test kind including the fuzz and benchmark requirements. None of these are enforced today; this SOW operationalizes them.

### Assistant Understanding

Facts:

- `.agents/sow/specs/quality-gates.md` lines 56-73 spec fuzz cadence (30s push, 5min nightly), property-based tests for canonical mapping invariants, and benchmark thresholds (≤ 20% regression vs `bench/baseline.txt`).
- `.agents/skills/project-testing/SKILL.md` lines 52-58 enumerate the test pyramid layers including Go fuzz, property-based, and performance-benchmark layers.
- Go's built-in fuzzing (`go test -fuzz=Fuzz...`) is stable since Go 1.18 and is the canonical choice.
- `pgregory.net/rapid` is the dominant Go property-based testing library, maintained, and is what `.agents/skills/project-testing/SKILL.md` line 54 implies ("property-based").
- `benchstat` (from `golang.org/x/perf/cmd/benchstat`) is the canonical regression-comparison tool for Go benchmarks.
- The repo currently has zero fuzz tests, zero property tests, zero benchmarks, zero baseline file.

Inferences:

- Each adapter (aiagent_v3, aiagent_v2, claude_code, codex, opencode) needs at least one `FuzzXxx` per `quality-gates.md` line 56-58. Phase 1 (SOW-0001) ships aiagent_v3 and aiagent_v2 first; this SOW lands their fuzz targets immediately and stubs the future ones with TODOs that themselves block when claude_code/codex/opencode adapters ship in later milestones.
- Initial fuzz corpus seeds come from the sanitized fixtures under `testdata/<adapter>/` — fuzz with empty corpus discovers shallow crashes only.
- Properties for canonical mapping that the implementer must encode (five named per the SOW spec below): (1) **decode-then-encode round-trip equality** (canonical events round-trip without field loss); (2) **monotone ordering** under stable sort key (sequence numbers never decrease within a session); (3) **idempotent ingestion** (re-ingesting the same source file produces zero new rows); (4) **schema completeness** (every emitted event has all required canonical fields populated per `specs/canonical-events.md`); (5) **timestamp boundary** (microsecond precision preserved across the ISO-8601 → int64 boundary, no value drift > 0 µs).
- Benchmark variance on shared-CPU GitHub runners is the well-known reason for `-count=5` plus `benchstat`'s p-value filtering; this SOW pins both.
- Bench results posted as PR comments need the same sticky-comment pattern as SOW-0010 coverage; if SOW-0010 lands first, this SOW reuses the same comment library.

Unknowns:

- The exact list of canonical-decoder fuzz targets — depends on what `internal/canonical/` looks like after SOW-0001 Chunk 3. The implementer enumerates at start of this SOW and updates the SOW plan if it differs from "one fuzz target per decoder".
- Whether benchmark variance on GitHub-hosted runners is small enough for a 20% threshold to be stable. Mitigation: measure during Chunk 5; if false-positive regressions appear, the implementer either raises `-count` to 10, switches to self-hosted runner for benchmarks (operator decision if needed), or files a follow-up SOW to refine the threshold per metric. No silent threshold lowering.

### Acceptance Criteria

1. Every adapter package that exists at SOW execution time (aiagent_v3 and aiagent_v2 from SOW-0001 minimum) exposes at least one `FuzzXxx` function in `internal/adapters/<name>/fuzz_test.go`, with seed corpus drawn from sanitized fixtures. **Verification**: `grep -l "^func Fuzz" internal/adapters/*/fuzz_test.go` lists every adapter package; `go test -fuzz=Fuzz -fuzztime=30s ./internal/adapters/<each>` exits 0.
2. `internal/canonical/fuzz_test.go` exposes at least one `FuzzXxx` per decoder function in `internal/canonical/`. **Verification**: as above for the canonical package.
3. CI runs each fuzz target for `-fuzztime=30s` per push and `-fuzztime=5m` nightly. Nightly crashes auto-file a GitHub issue with the crashing input attached. **Verification**: workflow file shows both schedules; auto-issue logic exercised once via a deliberate seed crash that is then reverted (proof recorded in execution log).
4. `internal/canonical/property_test.go` exists and uses `pgregory.net/rapid` to encode at least 5 named invariants: (a) decode-then-encode round-trip equality; (b) monotone ordering under sequence key; (c) idempotent ingestion of the same source file; (d) schema completeness per `specs/canonical-events.md`; (e) timestamp microsecond precision preservation. Each invariant is a separate `func TestPropertyXxx(t *testing.T) { rapid.Check(t, ...) }`. **Verification**: `go test -run=TestProperty ./internal/canonical -count=1` exits 0; `go doc` on each test names the invariant.
5. Benchmark suite (`*_test.go` files with `BenchmarkXxx` functions) exists for the six paths spec'd in `quality-gates.md` "Go — Benchmarks": adapter `Scan`, adapter `Tail`, canonical event encode/decode, SQLite batch insert, REST query path, SSE fanout. Each benchmark runs `-count=5`. **Verification**: `go test -run=^$ -bench=. -benchmem -count=5 ./... > /tmp/bench.txt` exits 0 and includes all six named benchmarks.
6. `bench/baseline.txt` is committed at first SOW completion. The contents are the output of `go test -run=^$ -bench=. -benchmem -count=5 ./...` on the implementing commit. **Verification**: file exists; head shows the implementing commit SHA in a comment line at the top.
7. `scripts/check-bench.sh` runs the bench suite, invokes `benchstat bench/baseline.txt bench-current.txt`, and exits non-zero if any metric regresses > 20%. Output is actionable (names the regressed benchmark and the delta). **Verification**: synthetic regression injected into a benchmark causes the script to fail with the regressed name and percentage; reverting the regression makes the script pass.
8. Baseline refresh requires an explicit SOW. `scripts/check-bench.sh` produces no auto-update mode. **Verification**: code-review the script for any auto-overwrite path; none present.
9. CI integrates fuzz + bench steps into the workflow. Per-push budget: fuzz at 30s × N targets + bench at `-count=5` × M benchmarks must fit within the < 5-min total-gate budget from `quality-gates.md` "Performance Target". **Verification**: timed CI run logged; if budget exceeded, split bench to a separate job that runs in parallel with the static analysis job.
10. Bench results posted as sticky PR comment showing per-benchmark delta vs baseline (if SOW-0010's sticky-comment infrastructure has landed; otherwise an artifact upload suffices and a follow-up SOW for the comment is filed). **Verification**: PR comment visible on a sample PR.

## Analysis

Sources checked:

- `.agents/sow/specs/quality-gates.md` (authoritative fuzz/property/bench gates).
- `.agents/skills/project-testing/SKILL.md` (test pyramid layers and mandatory test kinds).
- `.agents/skills/project-quality-gates/SKILL.md` (runtime catalog with commands).
- `.agents/sow/specs/canonical-events.md` (the schema the canonical fuzz targets and property tests must honor).
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` (Chunks 3, 6, 8 land the canonical package and the first two adapters this SOW attaches tests to).
- `pgregory.net/rapid` upstream README and examples for property-test idioms (cite exact commit in execution log when adopted).
- `golang.org/x/perf/cmd/benchstat` upstream for benchstat invocation patterns.

Current state:

- Zero fuzz, zero property tests, zero benchmarks, no baseline file. Phase 1 ships without any of these (correctly — they need this dedicated SOW).
- Once SOW-0001 Chunk 3 lands `internal/canonical/`, the canonical fuzz + property tests have a target. Once Chunk 6 and 8 land aiagent_v3 and aiagent_v2 adapters, their fuzz + bench targets have a home.

Risks:

- **R1 — fuzz corpus emptiness**: starting fuzz with empty corpus discovers shallow crashes only. Mitigation: seed each fuzz target from the sanitized fixtures under `testdata/<adapter>/` (canonical also gets seeds from the round-trip output of the adapter fuzzes).
- **R2 — fuzz target API drift**: Go fuzz is stable since 1.18, low concern. The actual risk is upstream `pgregory.net/rapid` behavioral drift; mitigation: pin the dependency in `go.mod` and consult upstream release notes on upgrade.
- **R3 — rapid library learning curve**: the small team (assistant + future reviewers) needs to recognize rapid idioms in code review. Mitigation: this SOW also updates `.agents/skills/project-testing/SKILL.md` with a short "Property Testing With rapid" subsection plus an in-tree example pointer.
- **R4 — benchmark variance on shared-CPU GitHub runners**: known issue. Mitigation: `-count=5` plus `benchstat`'s built-in p-value filtering. If still noisy in practice, the implementer raises to `-count=10` for the worst benchmarks (documented per benchmark) before considering threshold changes. Switching to a self-hosted runner for benchmarks is an operator-decision option recorded in Open Decisions.
- **R5 — baseline staleness**: a fresh baseline taken on a slow CI day will be too forgiving; one taken on a fast day will trigger spurious regressions. Mitigation: the SOW that refreshes the baseline (this one, on first commit, and any future refresh SOW) takes the baseline from a 3-run-average on the same CI runner type used by the gate.
- **R6 — CI runtime budget**: 30s fuzz × ~10 targets + 5-run bench × ~6 benchmarks can exceed the < 5-min total budget. Mitigation: parallel jobs; if still over, split fuzz/bench from the per-push pipeline into a dedicated "heavy gates" job that still blocks merge but runs concurrently with the static-analysis job.

## Pre-Implementation Gate

Status: ready (activated 2026-06-02 under the blanket mandate; the "Re-scope decisions" + "Enumeration" above govern where they differ from this original draft — AC#1 ✅ done, AC#2 ✅ zero-needed (canonical has no decoders, verified), AC#5 reduced 6→5 paths / 4 new, AC#3/#4/#6/#7 in scope, AC#10 + auto-issue-on-crash deferred). Spec deltas to land before tests: `quality-gates.md` "Go — Fuzzing" (canonical-decoder → adapters-only) + "Go — Benchmarks" (6→5 paths); `project-testing` + `project-quality-gates` fuzz/property/bench rows reconciled to reality. Validation plan (named files): `internal/canonical/property_test.go` (5 `TestPropertyXxx`), 4 new `BenchmarkXxx` (aiagent_v2 `Tail`, ingest `worker.flush`, presenter `handleSessionsList`, notify `Hub.Deliver`), `scripts/check-bench.sh` + its self-test, `.github/workflows/fuzz-nightly.yml` + a per-push fuzz step in `ci.yml`, `bench/baseline.txt` SHA header.)

Problem / root-cause model:

- The repo commits to fuzz, property, and benchmark gates in spec but has none of them. Adapters and canonical decoders ingest untrusted JSON from disk — exactly the class of input where fuzz catches the panics, OOMs, and stack overflows that static analysis cannot. Property tests are the only practical way to encode mapping invariants (idempotency, ordering, round-trip) that cannot be enumerated as line-coverage cases. Benchmarks without a regression gate are decorative; the gate is what makes them useful.

Evidence reviewed:

- `.agents/sow/specs/quality-gates.md` lines 56-77 (fuzz, property, bench thresholds).
- `.agents/skills/project-testing/SKILL.md` lines 49-83 (mandatory test kinds).
- `.agents/skills/project-quality-gates/SKILL.md` lines 76-104 (runtime catalog with commands).
- `.agents/sow/specs/canonical-events.md` (schema for property test (d) "schema completeness").
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` Chunks 3/6/8 (prerequisite source surfaces).
- Go fuzz docs and `pgregory.net/rapid` upstream (consulted at SOW drafting; cite exact commits in execution log when adopted).

Affected contracts and surfaces:

- New: `internal/adapters/<name>/fuzz_test.go` per adapter; `internal/canonical/fuzz_test.go`; `internal/canonical/property_test.go`; `internal/adapters/<name>/bench_test.go` per adapter; `internal/store/bench_test.go`; `internal/presenter/bench_test.go`; `bench/baseline.txt`; `scripts/check-bench.sh`; CI workflow extensions.
- Modified: existing CI workflow from SOW-0001 Chunk 2 (extensions for fuzz + bench jobs); `scripts/gates.sh` (add bench check); `.agents/skills/project-testing/SKILL.md` (add "Property Testing With rapid" subsection); `.agents/sow/specs/quality-gates.md` only if implementation surfaces a needed correction.
- Unaffected: production Go source behavior, fixtures (used read-only for seed corpus), frontend.

Existing patterns to reuse:

- `AGENTS.md` "Transparency in scripts" pattern for `scripts/check-bench.sh`.
- The sticky PR comment infrastructure from SOW-0010 if it has landed (graceful degradation if not).
- `testdata/<adapter>/` fixture layout from SOW-0001 for fuzz seed corpus.

Risk and blast radius:

- Local-only impact (workstation tool + CI workflow).
- A crashing fuzz target reveals a real adapter defect; the fix is in the adapter, not the fuzz target.
- A spuriously-failing bench gate can block legitimate PRs; mitigation per R4/R5/R6 above.
- Property-test failures pin real canonical-mapping bugs; the fix is in `internal/canonical/`.

Sensitive data handling plan:

- Fuzz seed corpus comes from `testdata/<adapter>/`, which is mandatorily sanitized before commit per `AGENTS.md` "Sensitive Data In Durable Artifacts". The fuzz machinery never reads from `~/.ai-agent/sessions/` or any unsanitized source.
- Fuzz crashes auto-filed as GitHub issues attach the crashing input. The implementer adds a sanitization filter to the auto-issue logic: any seed-derived input goes raw (already sanitized); any mutator-generated input is bytes-only with no plausible secret pattern (random bytes do not produce real customer data, but the implementer adds a defensive entropy check before posting, and on hit, the issue is filed without the input and the input is uploaded as an artifact retained for the assistant to inspect privately).
- Property-test generators (rapid) produce random structured data; same defensive posture as above.
- Bench output contains only timing/allocation numbers; no sensitive data exposure.

Implementation plan:

1. **Spec read-back**: re-read `.agents/sow/specs/quality-gates.md`, `.agents/sow/specs/canonical-events.md`, and `.agents/skills/project-testing/SKILL.md` at start; confirm no drift since SOW approval.
2. **Property invariants finalized**: confirm the five named invariants in Acceptance Criterion 4 are still the right five; adjust based on what `internal/canonical/` looks like after SOW-0001 Chunk 3. Any change is documented in the execution log.
3. **Adapter fuzz targets**: one `FuzzXxx` per adapter package that exists (aiagent_v3, aiagent_v2 at SOW start; placeholder TODOs for the others if not yet landed). Seed corpus from `testdata/<adapter>/`.
4. **Canonical fuzz targets**: one `FuzzXxx` per decoder in `internal/canonical/`.
5. **Property tests**: `internal/canonical/property_test.go` with the five invariants. Each invariant is a named `TestPropertyXxx` calling `rapid.Check`.
6. **Benchmark suite**: `BenchmarkXxx` functions for the six spec'd paths. Each benchmark uses representative input (a fixture session) to keep variance low.
7. **Baseline capture**: run the bench suite three times on a fresh CI runner; take the median; commit as `bench/baseline.txt` with a header comment naming the commit SHA and runner type.
8. **`scripts/check-bench.sh`**: author the regression-check script with synthetic-fixture self-tests.
9. **CI fuzz job**: extend the workflow with a per-push fuzz step (30s per target) and a nightly fuzz step (5min per target). Wire the auto-issue logic for nightly crashes (use `actions/github-script` or a small Go helper; pin exact version in execution log).
10. **CI bench job**: extend the workflow with a per-push bench step running `scripts/check-bench.sh` and posting the diff as a sticky PR comment (or artifact if SOW-0010's comment infra is not yet landed).
11. **`scripts/gates.sh` integration**: ensure `scripts/check-bench.sh` runs as part of the canonical local pre-commit gate.
12. **`.agents/skills/project-testing/SKILL.md`**: add a short "Property Testing With rapid" subsection with an in-tree example pointer.
13. **Local exercise**: run every new gate locally; address any finding (adapter or canonical bugs surfaced by fuzz or property tests are real bugs, fixed in their own subagent task, not silenced).
14. **CI exercise**: open a sample PR; verify all jobs run, timing fits the < 5-min budget, comments/artifacts land.
15. **Spec / skill sync**: update `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-quality-gates/SKILL.md` if any threshold or command differs from what landed.
16. **External review** (per `project-second-opinions`): at least three reviewers in parallel on the diff.
17. **Address findings**, re-review, mark SOW completed.

Validation plan:

- Acceptance Criteria 1-10 each have a named verification method.
- Synthetic regression test for `scripts/check-bench.sh` (the gate itself must be correct).
- One deliberate seed crash to exercise the auto-issue logic, then reverted (proof in execution log).
- External reviewers confirm: every adapter and canonical decoder has a real fuzz target (not a stub), five property invariants are real assertions (not tautologies), baseline is honest (not taken on a fast outlier run), and the 20% threshold is not silently weakened anywhere.

Artifact impact plan:

- `AGENTS.md`: no change expected.
- Specs: `.agents/sow/specs/quality-gates.md` updated only if commands/thresholds drift from what landed.
- Runtime project skills: `.agents/skills/project-testing/SKILL.md` updated with the rapid subsection; `.agents/skills/project-quality-gates/SKILL.md` reviewed for command accuracy.
- End-user docs: none affected.
- SOW lifecycle: on success, this SOW moves to `.agents/sow/done/` with `Status: completed` in the same commit as the final tests, scripts, and CI updates. Any adapter or canonical bug surfaced by fuzz/property runs is fixed in its own subagent task and either folded into this SOW (if trivial) or filed as a follow-up SOW (if non-trivial); the choice is recorded in the execution log per the tech-debt-is-paid-not-deferred rule.

Open-source reference evidence:

- `pgregory.net/rapid @ <commit>` cited in execution log when adopted.
- `golang.org/x/perf @ <commit>` (for benchstat) cited in execution log when adopted.
- Any sticky-comment action reused from SOW-0010 inherits its pinned tag.
- No workstation absolute paths to external OSS recorded here.

Open decisions:

- Whether benchmark variance on shared-CPU GitHub runners is low enough for the 20% threshold to be stable, or whether bench should move to a self-hosted runner. This is an operator decision **only if** the variance proves unacceptable during Chunk 5 measurement. Default path: stay on GitHub-hosted runners with `-count=5` and `benchstat` p-value filtering; raise only if data forces it.

## Implications And Decisions

No operator decisions required at SOW approval time. The single Open Decision above only escalates to the operator if Chunk 5 measurement proves the default insufficient; otherwise it stays a CTO call within the SOW.

## Plan

1. Property invariants finalized against the actual canonical package (low effort, gates Chunk 5).
2. Adapter fuzz targets + canonical fuzz targets (medium effort; seed corpus assembly).
3. Property tests with rapid (medium effort; five named invariants).
4. Benchmark suite (medium effort; six paths, representative input).
5. Baseline capture + `scripts/check-bench.sh` with self-tests (medium risk; the gate must be correct).
6. CI integration for fuzz + bench (medium risk; runtime budget).
7. Skill update for rapid idioms (low effort).
8. Exercise + tuning iteration (potentially high effort if real bugs surface — those are fixed, not silenced).
9. External review + convergence.

## Pre-measurement — 2026-06-02 (re-scope input, not yet activated)

This SOW was drafted 2026-05-26 assuming **zero** fuzz/property/benchmark coverage. Measured live on 2026-06-02 (read-only Explore sweep); the premise is stale and there is a **doc-drift cluster** to reconcile. The Pre-Implementation Gate (filled at activation, after SOW-0010 merges) must re-scope to this measured delta.

**Measured reality (re-verify with file:line in the activation gate):**
- **Fuzz targets: 10 exist across all 5 adapters** (codex ×2, aiagent_v3 ×2, opencode ×2, claude_code ×2, aiagent_v2 ×2). → **AC#1 already satisfied.**
- **Canonical fuzz: 0** (`internal/canonical/fuzz_test.go` absent). → AC#2 open.
- **Property tests: 0**; `pgregory.net/rapid` not in `go.mod`. → AC#4 open.
- **Benchmarks: 1 of 6** — only `aiagent_v2/bench_test.go BenchmarkScan_SyntheticCorpus`. Missing: adapter `Tail`, canonical encode/decode, SQLite batch insert, REST query, SSE fanout. → AC#5 partial.
- **Baseline:** `bench/baseline.txt` exists but lacks the implementing-commit-SHA header AC#6 requires. → AC#6 partial.
- **CI fuzz wiring: none** (no `-fuzz`/`-fuzztime`/nightly/auto-issue in `.github/workflows/`). → AC#3 open.
- **`scripts/check-bench.sh` + benchstat regression gate: absent.** CI bench step runs `-count=1` smoke, artifact-only, no benchstat diff. → AC#7 open; AC#5/#9/#10 partial.

**Doc-drift to reconcile in this SOW (claims that do NOT match reality):**
- `.agents/sow/specs/quality-gates.md` (Go — Fuzzing / Property / Benchmarks): canonical fuzz, fuzz CI schedule, auto-issue, `property_test.go`, `-count=5`, benchstat gate — claimed present, none exist.
- `.agents/skills/project-testing/SKILL.md` (pyramid + mandatory-kinds): canonical fuzz file, fuzz cadence, property file, the 5 missing benchmarks, `-count=5`, regression gate.
- `.agents/skills/project-quality-gates/SKILL.md` (Go — Fuzzing / Benchmarks): canonical-fuzz "MUST", fuzz CI, auto-issue, 5 missing benchmarks, regression gate.

**Re-scope direction (CTO; finalize in the activation gate):** real delta = canonical fuzz targets + property tests (`rapid`, 5 invariants) + the 5 missing benchmarks + CI fuzz wiring + `check-bench.sh` regression gate + baseline SHA header + reconcile the 3 drift artifacts to reality. **Open decision for activation:** whether auto-file-GitHub-issue-on-fuzz-crash stays in-scope or is deferred to a follow-up — it adds non-trivial CI complexity, and nightly job-failure visibility may suffice; decide with evidence at activation, do not carry the stale AC blindly.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
