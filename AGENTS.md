# ai-viewer

A read-only, real-time explorer for AI coding-agent session snapshots. Multi-format: ingests `ai-agent` (v2 and v3), `claude-code`, `codex`, and `opencode` session storage formats, normalizes them into a canonical model, and presents them through a modern dark/light web UI with span-based tracing, topology, timeline, and statistics views.

## Goals

- **Primary purpose**: give the operator a fast, beautiful, low-friction way to see what their AI coding agents have been doing — across time, across formats, across sub-agents.
- **Single source of truth**: read source-system snapshot files directly; never call live agent systems.
- **Real-time**: file-watch the source directories; push updates to the browser without polling.
- **Multi-format and extensible**: an adapter is one Go package implementing one interface; new formats are additive, never schema-breaking.
- **Mental model first**: anyone (operator or contributor) should be able to read the specs and know exactly what the system does and why.
- **Tested, working, production-quality code**: no half-built features, no silent failures, no untested code paths.

## Ownership Model

This repository operates under a **delegated-ownership contract**:

### user owns

- Product direction, mission, prioritization.
- UX feedback and visual judgment.
- Bug reports and missing-feature requests.
- Production-deployment approval if/when the tool ever runs outside the workstation.
- Risk acceptance and destructive-operation approval.

### the assistant owns (autonomously, after SOW sign-off)

- All technical decisions: language, framework, library, schema, architecture, refactoring, dependency management, security updates.
- All code, tests, CI, releases, documentation.
- Bug triage, fixes, performance tuning, profiling.
- Build, install, run scripts.
- Quality enforcement: zero lint warnings, zero test failures, before any merge.

### Sign-off boundary (the only checkpoint user owes the assistant)

Per the assistant's user-level rules, every non-trivial change of architecture or design must be visible to user **before** code is written. This is enforced by the SOW system:

- The assistant writes a SOW (with Pre-Implementation Gate) in `.agents/sow/pending/`.
- user approves the SOW.
- The assistant moves it to `.agents/sow/current/` and works autonomously until delivered.

After SOW sign-off, the assistant does not ask permission for technical choices within the agreed scope. If during implementation a finding materially changes the SOW, the assistant pauses, writes an addendum, and asks. Otherwise: work proceeds; user receives a working system to use.

## Tech Stack (decided)

| Layer | Choice | Rationale |
|---|---|---|
| Backend language | **Go (current stable)** | smallest reasonable footprint, fsnotify is mature, goroutine-per-adapter model, single static binary, native gzip/JSON/SQLite, precedent in user's other repos |
| Backend HTTP | **stdlib `net/http`** | enough for SSE + REST; no framework needed; trivial to debug |
| Realtime transport | **SSE (server-sent events) + REST** | trivially debuggable with `curl -N`; browser EventSource built-in; reconnect is automatic; bidirectional needs handled by REST endpoints |
| Storage | **SQLite (modernc.org/sqlite, CGO-free)** | indexed queries over millions of canonical rows; WAL mode for concurrent read; single-file deploy |
| File watching | **fsnotify** | de-facto inotify wrapper for Go |
| Frontend build | **Vite (current stable) + React (current stable) + TypeScript (current stable)** | fast dev loop, strong ecosystem, embedded into Go binary via `go:embed` for single-binary deploy |
| Charts / topology | **D3 (current stable)** for force-directed topology and timeline; native HTML/CSS where possible | proven, headless-friendly |
| Frontend tests | **Vitest + React Testing Library + Playwright (current stable) for E2E** | matches Vite-native tooling |

**Library version policy**: always pin to the **latest stable release** available at the time of work. Dependabot or equivalent is enabled. Major-version upgrades require a brief SOW; minor/patch upgrades may be applied autonomously and committed together.

**Binary topology**:

- `ai-viewer-ingest`: daemon. Watches source directories, parses snapshots, writes canonical rows to SQLite. Knows nothing about HTTP.
- `ai-viewer-serve`: HTTP server. Serves embedded frontend + REST + SSE. Reads SQLite. Knows nothing about adapters.
- Coupling between the two: the SQLite file + a small notify channel (Unix socket or SQLite-WAL polling). This separation is enforced so the two can run as separate processes (different hosts in the future).

## Repo Layout

```
ai-viewer.git/
├── AGENTS.md                    canonical contract (this file)
├── CLAUDE.md                    symlink → AGENTS.md
├── GEMINI.md                    symlink → AGENTS.md
├── README.md                    end-user docs
├── LICENSE                      MIT
├── .gitignore
├── .claude/
│   └── skills                   symlink → ../.agents/skills
├── .agents/
│   ├── skills/                  project skills (runtime guidance for assistants)
│   │   ├── project-coding/SKILL.md
│   │   ├── project-go-backend/SKILL.md
│   │   ├── project-frontend/SKILL.md
│   │   ├── project-adapters/SKILL.md
│   │   ├── project-testing/SKILL.md
│   │   ├── project-second-opinions/SKILL.md
│   │   └── project-specs-sync/SKILL.md
│   └── sow/
│       ├── SOW.template.md      template for new SOWs
│       ├── audit.sh             local audit script
│       ├── pending/             SOWs proposed but not yet started
│       ├── current/             active SOWs
│       ├── done/                completed SOWs (audit trail)
│       ├── todo-backup/         archived planning notes
│       └── specs/               living specifications of WHAT the project does
├── cmd/
│   ├── ai-viewer-ingest/        ingester binary entry point
│   └── ai-viewer-serve/         server binary entry point
├── internal/
│   ├── adapters/                one sub-package per source format
│   │   ├── aiagent_v3/
│   │   ├── aiagent_v2/
│   │   ├── claude_code/
│   │   ├── codex/
│   │   └── opencode/
│   ├── canonical/               canonical event types + interfaces
│   ├── ingest/                  ingest pipeline, SQLite writer, dedup, sequencing
│   ├── store/                   SQLite schema, migrations, query helpers
│   ├── presenter/               HTTP handlers, SSE hub, REST endpoints
│   ├── notify/                  ingest↔serve notification channel
│   └── obs/                     structured logging, health metrics
├── frontend/
│   ├── package.json
│   ├── vite.config.ts
│   ├── src/                     React app source
│   └── public/                  static assets
├── testdata/                    sanitized real fixture files per adapter
│   ├── aiagent_v3/
│   ├── aiagent_v2/
│   ├── claude_code/
│   ├── codex/
│   └── opencode/
├── docs/                        end-user documentation (architecture overview, install, configure, adding adapters)
├── scripts/
│   ├── build.sh                 builds frontend, embeds, builds Go binaries
│   ├── dev.sh                   dev workflow (vite dev + go run)
│   └── lint.sh                  golangci-lint + frontend lint, zero warnings
└── .github/
    └── workflows/               CI: lint, test, build on every push
```

## Required First Checks Before Non-Trivial Work

1. Read `.agents/sow/pending/` and `.agents/sow/current/` for overlap, contradictions, existing decisions.
2. Read the relevant specs under `.agents/sow/specs/`. Start from `specs/index.md`.
3. Inspect `.agents/skills/project-*/SKILL.md` and load every runtime project skill whose trigger matches the work.
4. Inspect source code, tests, fixtures as ground truth.
5. Ask user only for irreducible product/design/risk decisions.

## SOW System

This project uses a local Statement of Work system. The SOW system is self-contained in this repository. Normal SOW work does not depend on `~/.agents`, `~/.AGENTS.md`, global skills, global templates, or global scripts.

### SOW Lifecycle

- New SOWs are created from `.agents/sow/SOW.template.md` into `.agents/sow/pending/` for user approval.
- Approved SOWs move to `.agents/sow/current/` and may begin implementation only after the Pre-Implementation Gate is filled.
- Completed SOWs move to `.agents/sow/done/` with `Status: completed` (`done` is the directory, not a status; never write `Status: done` or `Status: complete`).
- The SOW status change and the move must be in the same commit as the implementation, unless user explicitly asks for a different commit split.

### Pre-Implementation Gate

Implementation must not begin until the active SOW contains a concrete `## Pre-Implementation Gate` section. The gate must record: problem/root-cause model, evidence reviewed, affected contracts and surfaces, existing patterns to reuse, risk and blast radius, sensitive data handling plan, implementation plan, validation plan, artifact impact plan, and open decisions. Generic placeholders such as `TBD`, `N/A`, or "to be checked later" are invalid unless the SOW explains why the item truly does not apply.

### Regressions

When a SOW was considered completed and later testing or use finds broken behavior: reopen the original SOW and append a dated `## Regression - YYYY-MM-DD` section at the end of the file. Never prepend regression content above the original SOW narrative.

### When a SOW Is Required

Non-trivial work needs a SOW: features, behavioral bug fixes, refactors, migrations, content with product impact, process changes, regressions, spec hygiene, project-skill changes, anything with unclear risk. Trivial work does not: typo fixes, formatting-only changes, mechanical renames with no behavior change.

## Specifications (Living)

Specs live under `.agents/sow/specs/`. They describe WHAT the project does — current behavior, contracts, schemas, defaults, edge cases. They are the durable memory so user does not have to repeat decisions.

**Every code change that affects runtime behavior, schemas, defaults, or interfaces MUST update specs in the same commit.** This is non-negotiable. Specs out of sync with code is a regression by definition.

Index of specs: `.agents/sow/specs/index.md`.

## Quality Bar

- **Zero lint warnings, zero lint errors.** `golangci-lint run` and frontend lint must be clean before any commit.
- **All tests pass.** Unit + integration + E2E run in CI on every commit.
- **No silent failures.** Every error is logged with structured context. Adapter parse errors surface in `/health` and the UI's adapter status panel.
- **No half-built features.** A feature is either fully delivered (code + tests + docs + spec) or it is not in the codebase.
- **Mental model clarity.** Every new package has a `doc.go` (Go) or `README.md` (frontend dir) explaining its purpose in 1-3 paragraphs.

## Sensitive Data In Durable Artifacts

SOWs, specs, documentation, project skills, agent instructions, code comments, and test fixtures are commit-ready artifacts. Treat them as public unless a repository-specific policy explicitly says otherwise.

CRITICAL: Never write raw sensitive data to durable artifacts. This includes passwords, API keys, bearer tokens, session cookies, customer names, customer identifiers, personal data, non-private IP addresses that can identify customers, private endpoints, account IDs, and proprietary content from real sessions.

For fixture files (real snapshot samples committed under `testdata/`):

- Strip or pseudonymize all `originId`, `sessionId`, user message contents, and tool I/O that contains private data.
- Replace API URLs with `https://api.example.invalid/...`.
- Replace model API keys (if any in headers) with `[REDACTED_SECRET]`.
- Keep schema shape, timing, and token counts intact — that's what tests verify.

If sensitive data is required to test something, ask user for a secure handling path.

## Open-Source Reference Evidence

When SOW evidence comes from local mirrored or cloned open-source repositories (e.g. `/opt/baddisk/monitoring/repos/`), cite the upstream repository identity and checked commit, not the workstation mirror path:

```text
owner/repo @ commit
relative/path/inside/repo:line
```

Never write workstation absolute paths for external open-source evidence into SOW evidence.

## Second Opinions (External LLMs)

The assistant may consult external LLMs for second opinions, code reviews, SOW reviews, and design validation. This is **encouraged for non-trivial work** — every major SOW should include at least one round of external review before being marked completed.

Available reviewers and the exact invocation patterns are documented in `.agents/skills/project-second-opinions/SKILL.md`. Always run external reviewers in parallel when reviewing the same artifact. Always show the user the prompts before running them.

**Critical safety rule**: if the assistant has itself been spawned as a reviewer (e.g. by a parent assistant), it MUST NOT invoke external reviewers — that causes infinite recursion. The skill documents how to detect this.

## Git Worktrees

The assistant must not create git worktrees on their own. Create a worktree only when user explicitly asks for it or approves it.

## Git Discipline

- Never use `git add -A` or `git add .` — always add specific files by name.
- Never delete files outside the SOW scope without user consent.
- Never reset the repo or run `git checkout FILE` without user approval.
- Never mention any AI tool, AI assistant, AI vendor, or AI product in commit messages, PR descriptions, or any commit metadata. The work stands on its own.
- Always create new commits rather than amending, unless user explicitly requests amend.
- Pre-commit hooks: fix the underlying issue, never use `--no-verify`.

## Build, Test, Run

(These commands will exist after Phase 1 is delivered. See `.agents/sow/pending/SOW-0001-phase-1-bootstrap.md`.)

```bash
./scripts/build.sh          # build frontend + Go binaries
./scripts/dev.sh            # dev workflow with hot reload
./scripts/lint.sh           # all lints, zero warnings
go test ./...               # all Go tests
cd frontend && npm test     # frontend tests
```

## Production Scope

ai-viewer is **workstation-only** initially. It binds `127.0.0.1` by default. There is no authentication; remote access is out of scope for v1. If/when user authorizes production deployment, that decision lands in its own SOW with an explicit security and auth design.

## Long-Term Memory

This `AGENTS.md`, the specs under `.agents/sow/specs/`, and the project skills under `.agents/skills/project-*/` are the assistant's long-term memory for this repository. When user gives feedback about how the assistant operates, the assistant must update these artifacts so the lesson is not lost.
