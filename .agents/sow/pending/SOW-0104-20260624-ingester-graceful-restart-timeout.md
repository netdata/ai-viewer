# SOW-0104 - Ingester Graceful Restart Timeout

## Status

Status: open

Sub-state: Pending approval. Created from live install evidence during SOW-0097 close-out.

## Requirements

### Purpose

Ensure ai-viewer upgrades and restarts are reliable on the operator workstation: a normal `scripts/install-system.sh` upgrade must stop and restart the ingester without systemd timing out and sending `SIGKILL`, while preserving accepted source events.

### User Request

No direct user request yet. This is a follow-up SOW created because SOW-0097 installation exposed a restart-time operational defect.

### Assistant Understanding

Facts:

- `scripts/install-system.sh` now restarts `ai-viewer-ingest.service` and `ai-viewer-serve.service` during upgrades.
- During the SOW-0097 install validation on 2026-06-24, the previous ingester process did not exit before systemd's stop timeout.
- The journal showed repeated shutdown flush retry warnings with `context deadline exceeded`, then systemd reported the ingester stop timed out and killed the old process with `SIGKILL`.
- The new ingester instance started cleanly afterward and resumed all configured operator sources.

Inferences:

- The bounded shutdown-drain path can still exceed the systemd stop timeout or retry until the process is force-killed under real local source/database load.
- Force-kill during shutdown may be harmless for already committed data, but it violates the intended graceful shutdown contract and can hide event-loss or cursor-state edge cases.

Unknowns:

- Whether the root cause is SQLite write contention, shutdown-drain retry behavior, context selection, oversized batches, read-model refresh work during shutdown, systemd timeout sizing, or interaction between those factors.

### Acceptance Criteria

- A controlled automated test or integration harness reproduces the current shutdown timeout/failure mode or proves the relevant root cause without relying on private workstation data.
- `ai-viewer-ingest` stops cleanly under a heavy in-flight batch/restart scenario without systemd `SIGKILL`.
- Shutdown logs distinguish bounded drain expiry from data-loss conditions and do not spam duplicate retry warnings.
- Specs and deployment/operator docs state the intended restart/shutdown behavior and any timeout assumptions.
- Local gates pass, including focused ingest shutdown tests and the system install self-test.

## Analysis

Sources checked:

- `deploy/systemd-system/ai-viewer-ingest.service`
- `.agents/sow/specs/ingester.md`
- `internal/ingest/worker_runtime.go`
- `internal/ingest/worker_test.go`
- Live `journalctl` output for `ai-viewer-ingest.service` / `ai-viewer-serve.service` during SOW-0097 install validation, with paths and operator identity redacted.

Current state:

- `deploy/systemd-system/ai-viewer-ingest.service` contains a systemd watchdog/memory cap but no explicit shutdown timeout contract beyond systemd defaults.
- `.agents/sow/specs/ingester.md` specifies bounded shutdown-drain behavior and says accepted events must not be dropped merely because lifecycle cancellation arrives.
- Existing worker tests cover many shutdown-drain cases, but the live restart still hit systemd's stop timeout under real source/database load.

Risks:

- A forced kill during upgrade can leave operator trust low even if SQLite rollback/WAL safety prevents corruption.
- If the old process is killed while holding source progress or in-memory batches, source replay may be correct but must be proven, not assumed.
- Simply increasing `TimeoutStopSec` may mask an underlying retry/context bug and lengthen every upgrade.

## Pre-Implementation Gate

Status: blocked

Problem / root-cause model:

- A real upgrade restart exposed that ingester shutdown can outlive systemd's stop timeout. The observed warnings were repeated shutdown flush retries with `context deadline exceeded`, followed by systemd `SIGKILL`.
- The likely fault domain is the interaction between worker shutdown-drain retry behavior, SQLite transaction start under heavy load, and the service stop timeout.

Evidence reviewed:

- Live install validation on 2026-06-24.
- `internal/ingest/worker_runtime.go` shutdown-drain context and retry code.
- `.agents/sow/specs/ingester.md` shutdown guarantees.
- `deploy/systemd-system/ai-viewer-ingest.service` systemd unit.

Affected contracts and surfaces:

- `ai-viewer-ingest` shutdown behavior.
- `scripts/install-system.sh` upgrade experience.
- `deploy/systemd-system/ai-viewer-ingest.service` stop timeout assumptions.
- Ingest data durability, source progress, and operator confidence during upgrades.

Existing patterns to reuse:

- Worker shutdown-drain tests in `internal/ingest/worker_test.go`.
- System install validation in `scripts/test/install-system-test.sh`.
- Structured warning/error logging patterns in `internal/ingest`.

Risk and blast radius:

- Medium. The code path is upgrade/shutdown only, but it touches ingest durability and source progress.
- The fix must not trade forced-kill avoidance for silent event loss.

Sensitive data handling plan:

- Do not commit live source paths, hostnames, operator names, raw payloads, or private journal snippets. Use `$HOME` and redacted summaries only.

Implementation plan:

1. Build a deterministic reproduction for shutdown with in-flight worker flush/retry pressure.
2. Identify whether retry/context behavior or systemd timeout assumptions are wrong.
3. Fix the smallest correct layer: worker shutdown-drain, retry suppression on terminal drain, read-model refresh during shutdown, or explicit unit timeout.
4. Add tests proving accepted events are either committed or safely replayed after restart.
5. Validate with `scripts/install-system.sh` on the workstation and inspect only ai-viewer service logs.

Validation plan:

- Focused Go tests under `internal/ingest`.
- `bash scripts/test/install-system-test.sh`.
- `scripts/install-system.sh` live validation: both units active, no systemd stop timeout, no `SIGKILL` for ai-viewer units.
- `curl http://127.0.0.1:7710/api/health` confirms configured sources register after restart.

Artifact impact plan:

- AGENTS.md: likely unaffected unless a new deployment rule emerges.
- Runtime project skills: update `project-deployment` if restart validation changes.
- Specs: update `.agents/sow/specs/ingester.md` and possibly `.agents/sow/specs/deployment.md`.
- End-user/operator docs: update only if operator-facing restart behavior changes.
- End-user/operator skills: likely unaffected.
- SOW lifecycle: pending until explicitly selected.

Open-source reference evidence:

- Not checked yet. This SOW is only a captured live defect; implementation should check local/reference projects if systemd shutdown patterns or SQLite graceful stop handling are material.

Open decisions:

- None for the user yet. This is a technical reliability defect and should be handled autonomously when scheduled.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
