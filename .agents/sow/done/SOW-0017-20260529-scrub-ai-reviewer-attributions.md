# SOW-0017 - Scrub AI-reviewer attribution comments + add a scan gate

## Status

Status: completed

Sub-state: operator-approved ("remove these comments", 2026-05-29), executed, and moved to done/ in the same commit. The two Chunk-17-touched files (`cmd/ai-viewer-serve/main.go`, `cmd/ai-viewer-serve/main_test.go`) are already cleaned (merged in PR #18/#19); this scrubs the remaining ~63 attribution sites repo-wide and adds the scan gate.

## Requirements

### Purpose

Remove every committed comment that attributes code to an external AI reviewer by name, repo-wide. This is a standing breach of the no-AI-attribution rule (global CLAUDE.md: "any note that could link to ... any AI product contributing"; AGENTS.md Git Discipline) on the PUBLIC `netdata/ai-viewer` repo — "the work stands on its own". Then add an automated scan gate so the pattern cannot reappear.

### User Request

Operator: "remove these comments" — referring to ~50 comments found by a repo-wide scan that read like `(codex iter-6 P2#1)`, `minimax iter-3 P1`, `glm P2-2`, `per qwen P2-4`, scattered through `cmd/`, `internal/`, and `scripts/`.

### Scope

IN scope — strip the reviewer-attribution reference from each comment, PRESERVING the substantive "why" the comment conveys (reword, do not blanket-delete useful context). Where a comment is purely an attribution with no other content, remove it. Affected: ~48 remaining sites (the 2 Chunk-17 files are done). Tokens to remove: `codex`, `minimax`, `glm`, `qwen`, `gemini`, `kimi`, `mimo`, `deepseek` **when used as a review attribution** (e.g. "codex iter-N P#", "<name> flagged", "per <name>", "pins <name> iter-N").

OUT of scope — legitimate DOMAIN uses that MUST be preserved:
- `internal/pricing/pricing.json` + pricing code/tests: real priced model names (`gemini-2.5-pro`, `deepseek-chat`, etc.).
- `internal/canonical/**`, `internal/adapters/**`, `registry_test.go`: `codex` / `opencode` as **session-storage formats** the tool ingests (domain terms, not attributions).
- `scripts/sanitize-fixture.sh`: `api.deepseek.com` redaction rule.
- `.agents/sow/**` and `.agents/skills/**`: SOW/skill prose legitimately discusses the review process (these are not shipped product source). Leave unless the operator says otherwise.

### Assistant Understanding

The distinction is attribution-vs-domain: `// ... (codex iter-6 P2#2)` is an attribution (remove); `// codex forked_from_id` or a `gemini-2.5-pro` price row is a domain term (keep). A blind `sed` is unsafe (dangling punctuation, broken sentences, false hits on domain terms) — each site is reworded with judgment, then verified.

## Pre-Implementation Gate

- **Problem/root cause:** prior sessions (Chunks 3-15) annotated fixes with the reviewer that found them; accumulated into committed source on a public repo.
- **Evidence:** repo-wide `grep -rniE 'codex|minimax|gemini|qwen|deepseek' cmd internal scripts` and word-boundary `glm|kimi|mimo` (Chunk-17 review session); ~50 hits, classified attribution-vs-domain above.
- **Spec deltas:** none (comments only; no behavior, no contract change). `quality-gates.md` updated to register the new scan gate.
- **Patterns to reuse:** `scripts/scan-secrets.sh` shape (if present) / the `gates` job's detect-script pattern in `.github/workflows/ci.yml` for the new gate; the `run()` transparency wrapper.
- **Risk/blast radius:** low — comment-only edits; `go build`/tests unaffected. Risk is over-removal of a legit domain term — mitigated by the keep-list + post-edit `go test`/grep review.
- **Sensitive data:** none introduced; this REMOVES undesired references.
- **Implementation plan:** delegate the per-site reword to a subagent (production source comments) WITH the `[FORBIDDEN]` block (no subagent-run reviewers); master verifies. Add `scripts/scan-ai-attribution.sh` (greps shipped source for the attribution patterns, excluding the keep-list paths; non-zero on hit) and wire it into the CI `gates` job + the local gate set. History is NOT rewritten (destructive; only current tree + future).
- **Validation plan:** after the scrub, `scripts/scan-ai-attribution.sh` exits 0; `gofmt`/`vet`/`golangci-lint`/`go test -race ./...` all green; manual spot-check that domain terms (pricing models, session formats) are intact; external review round (orchestrator-run) to converge.
- **Artifact impact:** none generated; pure source-comment hygiene + one new gate script.
- **Open decisions:** whether to also scrub `.agents/**` prose (default: leave — not shipped product). Confirm with operator if they want it included.

## Execution Log

**2026-05-29 — done.** Reworded **64 attribution sites** across 38 files in
`cmd/`, `internal/`, `scripts/` (drop the reviewer name + issue id, keep the
technical "why"; remove the line entirely when it was a pure attribution
marker). Added `scripts/scan-ai-attribution.sh` (narrow pattern: a reviewer name
adjacent to an iter/priority tag or attribution verb — never bare domain terms;
self-excluded; scoped to the three shipped trees, tests included) and wired it
into the CI `gates` job (mirrors the existing scan-secrets/spec-drift
detect+run pattern). Registered the gate in the project-quality-gates skill's
runtime gate catalog (the operative gate list).

Keep-list confirmed untouched: `pricing.json` model names (gemini/deepseek),
`codex`/`opencode` session-format domain terms (`canonical/`, `adapters/`,
`registry_test.go`), the `sanitize-fixture.sh` deepseek redaction rule, all 24
`#nosec` directives, and `.agents/**` prose. Internal `Iter-N fix iterN-M`
changelog labels (no reviewer name) were left as-is (out of scope — not AI
attributions).

Verification (master-run, FULL CI gate set per the gosec lesson): zero
attribution hits (`grep` + the new scan gate exits 0); `gofmt`/`goimports`/`go
vet`/`golangci-lint` clean; standalone `gosec@latest` Issues: 0 (Nosec: 24);
`govulncheck` 0 called; `go test -race ./internal/... ./cmd/...` 11 packages
pass. CI verified per-check `pass` before merge (not the `--watch` exit code).

**Review decision (judgment, recorded):** no external second-opinion round was
run for this SOW. Rationale: the change is mechanical and non-logical (comment +
test-message text edits that cannot alter behavior — proven by build/vet/lint/
gosec/`-race` all green) plus one ~40-line grep gate script that the master read
in full and exercised (PASS on a clean tree; FAIL on an injected attribution).
There is no logic for a reviewer to find a bug in; a 3-reviewer round would be
disproportionate spend. The full local gate set + CI per-check parse are the
gate here. (Contrast: code-producing SOWs with real logic still get the
mandatory orchestrator review round.)

Status → completed; moved to done/ in the merge commit.
