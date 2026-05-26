# Specifications Index

This directory contains the **living specifications** of ai-viewer: what it does, how it does it, what contracts it honors. Specs are the durable memory of the project so the operator does not have to repeat decisions.

**Rule**: every code change that affects runtime behavior, schemas, defaults, or interfaces must update the relevant spec in the same commit. Drift between code and specs is a regression.

## Layout

### Foundations

- [architecture.md](architecture.md) — the mental model: ingester + presenter binaries, adapter pattern, SQLite store, SSE+REST transport, frontend.
- [data-model.md](data-model.md) — the canonical SQLite schema, indexes, retention rules.
- [canonical-events.md](canonical-events.md) — the normalized event types adapters emit into the ingest pipeline.

### Adapters

- [adapter-contract.md](adapter-contract.md) — the Go interface every adapter implements, lifecycle, error handling.
- [adapter-aiagent-v3.md](adapter-aiagent-v3.md) — ai-agent split format (JSONL ledger + gz payloads).
- [adapter-aiagent-v2.md](adapter-aiagent-v2.md) — ai-agent legacy format (single `.json.gz` per session).
- [adapter-claude-code.md](adapter-claude-code.md) — claude-code transcript format.
- [adapter-codex.md](adapter-codex.md) — codex rollout format.
- [adapter-opencode.md](adapter-opencode.md) — opencode SQLite format.

### Backend

- [ingester.md](ingester.md) — the ingest daemon: workers, ordering, dedup, failure handling.
- [presenter.md](presenter.md) — the HTTP server: routing, embedded frontend, query layer.
- [sse-protocol.md](sse-protocol.md) — server-sent events envelope, subscription model, reconnect.
- [rest-api.md](rest-api.md) — REST endpoint inventory.
- [observability.md](observability.md) — structured logs, `/health` metrics, adapter status surface.

### Frontend

- [frontend-architecture.md](frontend-architecture.md) — React app shape, state management, routing.
- [ui-pages.md](ui-pages.md) — page inventory: filters, list, detail, topology, timeline, statistics, analytics.

### Cross-cutting

- [testing-strategy.md](testing-strategy.md) — test pyramid, fixture management, CI gates.
- [deployment.md](deployment.md) — install, run, configure; SQLite location; ports.
- [security.md](security.md) — bind address, read-only on sources, no auth in v1.
- [second-opinions.md](second-opinions.md) — when and how to consult external LLMs for review.

## Spec Authoring Rules

- Specs describe **current behavior**, not aspirational future behavior. Aspirational work lives in SOWs, not specs.
- When a SOW lands, the spec is updated **in the same commit**.
- Specs cite file paths and (where stable) line numbers as evidence.
- Specs do not duplicate code; they explain WHY a contract exists, the invariants, the edge cases that motivated the design.
- Specs MUST NOT contain raw sensitive data. See AGENTS.md for the redaction rules.
