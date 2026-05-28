# SOW-0017 - Scrub AI-reviewer attribution comments + add a scan gate

## Status

Status: open

Sub-state: awaits the operator moving it to current/ for execution. Operator-directed ("remove these comments") during SOW-0001 Chunk 17. Filed as its own SOW/PR so the Chunk-17 build-pipeline PR stays focused; the two Chunk-17-touched files (`cmd/ai-viewer-serve/main.go`, `cmd/ai-viewer-serve/main_test.go`) are already cleaned in the Chunk-17 commit.

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

(Filled during execution.)
