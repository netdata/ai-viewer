# SOW-0008 - Release Polish + v0.1 Tag (Milestone 5)

## Status

Status: open

Sub-state: drafted 2026-05-26 for later operator approval. Prerequisite: SOWs 0003 (claude-code adapter), 0004 (codex adapter), 0005 (opencode adapter), 0006 (APM tracing UI), and 0007 (statistics & analytics) must ALL be in `.agents/sow/done/`. This is the terminal polish SOW for the v0.1 milestone; nothing about the v0.1 surface should still be in motion when this SOW starts.

## Requirements

### Purpose

Take the union of everything delivered by SOWs 0001-0007 from "engineering-complete" to "release-ready". Deliver operator-facing documentation, finalize the install path, freeze the performance baseline, complete one final external review on the whole product, and tag `v0.1.0`. After this SOW, ai-viewer has a real version number, real release notes, a real install story, and a known-good performance envelope.

### User Request

From `.agents/sow/specs/ui-pages.md` Phase Mapping:

> Phase 5: Polish, advanced filters, keyboard shortcuts modal, deep search.

And from `AGENTS.md` Production Scope:

> "ai-viewer is workstation-only initially. It binds 127.0.0.1 by default. There is no authentication; remote access is out of scope for v1."

This SOW closes out v1 — the workstation-local-only first release.

### Assistant Understanding

Facts:

- SOW-0001 Chunk 19 delivered `systemd` user units and an install script as part of the Phase 1 ingester/server bring-up. This SOW refines them, not creates them.
- `docs/runbook.md` and `docs/architecture-overview.md` were stubs at the end of SOW-0001; later SOWs (0003-0007) added sections opportunistically. This SOW audits them for completeness against the actual delivered surface.
- `bench/baseline.txt` is referenced by `.agents/sow/specs/quality-gates.md` as the file the Go bench gate compares against (≤ 20% regression). After v0.1, the file is frozen on the tag commit and becomes the floor for future regressions.
- The MIT license, the public GitHub repo at `netdata/ai-viewer`, and the no-authentication v1 stance are all locked in from SOW-0001.
- GitHub Releases supports attaching binaries; reproducible builds for Go are straightforward (`-trimpath`, `-buildvcs=false`, fixed module versions); for the frontend, `npm ci` from a committed `package-lock.json` is the reproducibility anchor.

Inferences:

- The "final external review" round on the whole product (not a specific changeset) is the right place to surface v0.1 blockers — coherence across SOWs, dead-code gaps, doc drift, security posture review.
- The install script needs to be tested on three reference distributions (Manjaro, Ubuntu LTS, Fedora) since those cover the operator's near-term workstation matrix and what most contributors are likely to be on.
- "v0.1" semantics: enough to be useful to the operator daily; not yet a stable API; breaking changes between v0.1 and v0.2 are allowed but must come with a migration note.

Unknowns:

- Whether any SOW between 0003 and 0007 introduced a sub-tab, route, or REST endpoint that this SOW must document but that the SOW assistants forgot to add to the docs. The audit in Chunk 2 surfaces this.
- Whether the reproducible-build target (identical hash for the same git commit on local vs CI) actually holds today — depends on Go toolchain version pin and frontend dependency tree determinism. Verified during Chunk 7.
- Whether any SQLite schema migration paths exist that need explicit documentation for operators upgrading from a hypothetical pre-v0.1 dev install. Almost certainly none (v0.1 is the first tagged release), but this SOW confirms.

### Acceptance Criteria

1. **Operator-facing docs are complete and current.** `docs/install.md`, `docs/runbook.md`, `docs/architecture-overview.md`, `docs/adding-an-adapter.md`, and `SECURITY.md` exist, are evidence-checked against the delivered surface, and every documented command/endpoint/path has been verified working at the tag commit. **Verification**: doc-drift audit script run; each doc has a "Last reviewed: <commit>" footer; spec-drift gate green.
2. **`README.md` rewritten for v0.1.** Status moves from "Pre-alpha" to "v0.1". Sections: what it is, what it ingests, install in one paragraph, screenshots (Trace + Topology + Stats), a "what's not in v0.1" list (auth, multi-host, retention policies). **Verification**: visual review by the operator; markdown lint clean; relative links resolve.
3. **Install script runs cleanly on Manjaro / Ubuntu LTS / Fedora.** Single command (`./scripts/install.sh` or equivalent) installs binaries, sets up systemd user units, configures defaults, prints a "next steps" message. Idempotent. **Verification**: tested on three reference VMs (Manjaro, Ubuntu 24.04, Fedora 41) using the operator's `install-vm` workflow; install transcript captured in `docs/install.md` test evidence.
4. **`bench/baseline.txt` committed.** Performance baseline captured for the ingester, the rollup refresh, the FTS5 search, the REST aggregate, the topology render, the timeline render — at the tag commit. Future regressions measured against this file. **Verification**: file present; numbers documented in `docs/runbook.md`; `quality-gates.md` references it as the regression floor.
5. **All quality gates green on the tag commit.** Per `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-quality-gates/SKILL.md`: gofmt, vet, golangci-lint, gosec, govulncheck, staticcheck, errcheck, ineffassign, unused, race test, coverage thresholds, fuzz, bench, frontend lint, types, unit, E2E, a11y, bundle, secrets scan, spec drift — ALL green at `v0.1.0`. **Verification**: CI run on the tag commit is green; `./scripts/gates.sh` exits 0 locally on the tag commit.
6. **External review converged on the FINAL state.** One round of codex + gemini + glm + qwen run in parallel per `.agents/skills/project-second-opinions/SKILL.md`, scoped to the whole repo at the pre-tag commit. Findings addressed; re-review until no actionable findings remain. **Verification**: review transcripts recorded in this SOW's `## Reviews` section.
7. **GitHub release tagged and published.** Tag `v0.1.0` on `main`. Release notes derived from the SOW done/ directory (one bullet per SOW). Reproducibly-built `ai-viewer-ingest` and `ai-viewer-serve` binaries attached for `linux-amd64` and `linux-arm64`. **Verification**: release URL renders; binaries downloadable; SHA256SUMS attached; reproducible-build check (build twice, identical SHA256) green.
8. **Schema migration documentation.** Even though v0.1 is the first tagged release with no prior migrations to document, `docs/architecture-overview.md` includes a "Schema versioning" section describing the migration model (`internal/store/migrations/NNNN_*.sql`, idempotent, schema_meta table) for future upgrade paths. **Verification**: section present; matches `.agents/sow/specs/data-model.md`.

## Analysis

Sources checked (at SOW drafting):

- `AGENTS.md` — production scope, quality bar, ownership model, git discipline (no AI mentions in commits/releases).
- `.agents/sow/specs/quality-gates.md` — the gate catalog `./scripts/gates.sh` runs.
- `.agents/sow/specs/data-model.md` — schema versioning section, migration pattern.
- `.agents/sow/specs/deployment.md` — install/systemd story (referenced; will be re-read at SOW start).
- `.agents/sow/specs/security.md` — workstation-local-only stance (referenced; will be re-read at SOW start).
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` Chunk 19-20 — initial install script + runbook delivered.
- `.agents/skills/project-second-opinions/SKILL.md` — review protocol.

Current state (at SOW drafting):

- SOWs 0001 in `current/`, 0002 in `done/`, 0003-0007 not yet drafted. This SOW assumes the chain completes before this one starts.
- No git tag exists; the repo has only the bootstrap commit + SOW-0001 commits.
- `bench/baseline.txt` does not exist yet; baselines are seeded by each SOW that adds a benchmark and frozen here.
- `README.md` Status: "Pre-alpha".

Risks:

- **R1 — Final external review surfaces a v0.1 blocker.** A reviewer may flag a security, correctness, or UX issue that requires real code work, not docs. Mitigation: this SOW explicitly budgets for fix-then-re-review iterations; if a fix needs new behavior, it forks into its own follow-up SOW and v0.1 ships once that lands.
- **R2 — Reproducible-build divergence between local and CI.** Go binaries can differ on `-buildvcs` metadata, embedded paths, or toolchain version drift. Mitigation: `go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=v0.1.0"`; toolchain version pinned in `go.mod`'s `toolchain` directive; CI uses the same `toolchain` line; SHA256 check in CI compares two builds.
- **R3 — Documentation drift.** Specs are kept current by `project-specs-sync`, but operator-facing `docs/` are not gated by `scripts/spec-drift.sh`. Mitigation: this SOW adds a `docs/`-drift check (every command in `docs/install.md` and `docs/runbook.md` must be executable on a fresh checkout and produce non-error output) to the CI gate set.
- **R4 — Install script breaks on a tested distro.** Each distro has different systemd-user idioms (Fedora's SELinux, Ubuntu's older systemd, Manjaro's rolling toolchain). Mitigation: the install script tests itself before exiting; CI runs it in a Docker container matching each distro's base image.
- **R5 — Release-notes drift from `done/` SOWs.** Auto-generating notes from SOW titles risks missing nuance (a SOW labelled "polish" may contain a major user-visible feature). Mitigation: notes are hand-written from `done/` outcomes, cross-checked against the changelog of git tags and `pending/` follow-ups.
- **R6 — Performance baseline captured on a flaky CI runner.** GitHub Actions runners have variable performance; baseline snapshots can drift between CI runs. Mitigation: the baseline file records `goos`, `goarch`, `cpu`, `cpu_count`, and runner type; the regression gate compares per-runner-class only. If on the operator's workstation, that's the canonical baseline (recorded once, frozen).
- **R7 — A docs gap surfaces a missing feature.** Documenting how something works can reveal it doesn't quite work. Mitigation: every doc claim has a "verified at <commit>" anchor; a verification failure during docs writing reopens the source SOW for a regression patch, not a v0.1 workaround.

## Pre-Implementation Gate

Status: blocked (pending operator sign-off + SOWs 0003-0007 in `done/`)

Problem / root-cause model:

- ai-viewer at the end of SOW-0007 is engineering-complete for v0.1 but not release-ready: docs likely lag, the install path isn't tested across distros, no performance floor is locked in, and no third party has reviewed the whole product as a single artifact. Shipping `v0.1.0` without those is shipping a development snapshot, not a release. This SOW closes that gap by treating "release" as its own engineering task.

Evidence reviewed:

- All specs cited above.
- SOW-0001 Chunks 19-20 to confirm what install/docs already exist.
- The quality-gates catalog to confirm what's automated vs what needs manual review here.
- Operator's `install-vm` skill (`~/.agents/skills/install-vm/`) for the VM-testing pattern.
- Reproducible-build practice from upstream Go release notes (Go 1.20+ `-trimpath` is the standard anchor).

Affected contracts and surfaces:

- Docs: `README.md`, `docs/install.md`, `docs/runbook.md`, `docs/architecture-overview.md`, `docs/adding-an-adapter.md`, `SECURITY.md`. All operator-facing.
- Scripts: `scripts/install.sh` refined; `scripts/release.sh` added (build reproducibly, sign, attach to release); `scripts/gates.sh` may grow a docs-drift check.
- Specs: minor updates only — `quality-gates.md` for any new gate added in Chunk 6.
- Repo: a `v0.1.0` git tag and a GitHub Release.
- No application code changes expected unless an external review surfaces a blocker.

Existing patterns to reuse:

- The SOW done/ directory IS the changelog source; one bullet per SOW outcome is the release-note pattern.
- `install-vm` workflow for the three-distro install test.
- `project-second-opinions` skill for the final review round.
- `mirrored-repos` skill to compare release-tagging practice against upstream observability projects (Grafana, Prometheus, Jaeger, OpenTelemetry-Collector).

Risk and blast radius:

- Docs-only and ops-only change for most of the SOW. Worst case: a review finding triggers a code fix; that fix lives in its own follow-up SOW.
- The git tag and GitHub Release are external-visible but easily revoked if needed (`git tag -d v0.1.0` + delete release; not encouraged but reversible).

Sensitive data handling plan:

- Release notes and docs MUST NOT include the operator's personal name (per AGENTS.md sensitive-data rule); refer to `the operator` / `user`.
- No AI tool, vendor, or product mentioned in commits, tags, releases, or release notes (per AGENTS.md git discipline).
- Bench baseline file contains performance numbers + CPU model; safe to commit.
- Install script does not embed any secrets; documented in `SECURITY.md`.

Implementation plan (ordered chunks):

1. **Inventory pass**: list every route, every REST endpoint, every config flag, every CLI flag, every systemd unit, every doc file, every spec file, every gate. Cross-check against the docs to surface gaps.
2. **Docs audit + rewrite**:
   - `docs/install.md` — single-command install, prerequisites, post-install verification.
   - `docs/runbook.md` — daily-ops procedures (start/stop/restart, log locations, troubleshooting, reset, perf baseline reading).
   - `docs/architecture-overview.md` — operator-readable architecture summary; mirrors `.agents/sow/specs/architecture.md` but for humans; includes schema versioning section.
   - `docs/adding-an-adapter.md` — contributor doc; mirrors `.agents/skills/project-adapters/SKILL.md` for humans.
   - `SECURITY.md` — workstation-local-only stance, no auth, what to do if exposed, vulnerability reporting.
3. **`README.md` rewrite**: status → v0.1; screenshots; install one-liner; "not in v0.1" list.
4. **Install script polish**: idempotency, prerequisite checks, "next steps" message; self-test.
5. **Three-distro install test**: spin up VMs via `install-vm` skill (Manjaro, Ubuntu 24.04, Fedora 41); run installer; capture transcripts into `docs/install.md` test-evidence section.
6. **Bench baseline freeze**: run the full bench suite on the operator's workstation at the pre-tag commit; capture into `bench/baseline.txt`; document numbers in `docs/runbook.md`; reference from `quality-gates.md`.
7. **Reproducible-build verification**: `scripts/release.sh` builds twice; SHA256 must match; CI runs the same check on the tag commit.
8. **All-gates green check**: `./scripts/gates.sh` locally and in CI on the pre-tag commit. Any failure blocks tagging.
9. **External review on the final state**: codex + gemini + glm + qwen in parallel per `project-second-opinions`; prompt scopes the WHOLE repo at pre-tag commit, not a diff.
10. **Address review findings**: in-scope fixes here, out-of-scope fixes deferred to follow-up SOWs in `pending/`.
11. **Re-review** until convergence.
12. **Tag + release**: `git tag -s v0.1.0` (or unsigned per operator preference recorded in this SOW), push tag, `gh release create v0.1.0` with notes + binaries + SHA256SUMS. No AI mentions in tag message or release notes.
13. **Mark SOW completed**, move to `done/`. Status update + move land in the same commit per AGENTS.md.

Validation plan:

- Doc-drift check (every documented command runs successfully).
- Three-distro install transcripts (captured in docs).
- All quality gates green on the tag commit (verified in CI).
- Reproducible-build check (two builds produce identical SHA256).
- External review converged.
- Release URL renders; binaries downloadable; SHA256SUMS valid.

Artifact impact plan:

- `AGENTS.md`: no expected change.
- Runtime project skills: minor — `project-quality-gates` may grow a "docs-drift" gate description.
- Specs: minimal — `quality-gates.md` for any new gate.
- End-user/operator docs: full rewrite of all `docs/` files; new `SECURITY.md` (if not already present from SOW-0001).
- End-user/operator skills: none expected; ai-viewer is read-only and has no operator-facing skill artifacts to update.
- SOW lifecycle: standard — completed + moved to `done/` in the final commit, alongside the tag.

Open-source reference evidence:

- Release-tagging practice: `grafana/grafana` release notes structure, `prometheus/prometheus` reproducible-build pattern, `jaegertracing/jaeger` install docs — cited per `mirrored-repos` skill during Chunk 1 inventory if relevant.
- Reproducible Go builds: official Go reference (Go 1.20+ release notes on `-trimpath`); upstream `goreleaser` config patterns for cross-arch builds.

Open decisions:

- **Tag signing.** Default: signed if a GPG key is configured on the operator's workstation, unsigned otherwise. Operator may override at SOW start.
- **Linux-arm64 binaries.** Default: yes, cross-compiled from amd64 via Go toolchain. Operator may drop arm64 from v0.1 if cross-compile testing is impractical.

## Implications And Decisions

(Filled when operator answers the open decisions above and any others surfaced during review.)

## Plan

(Mirror of Implementation Plan above; expanded with commit refs as chunks land.)

## Execution Log

(Filled per chunk during implementation.)

## Validation

(Filled at end. Doc-audit summary, install-test transcripts, bench numbers, review summary, release URL.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
