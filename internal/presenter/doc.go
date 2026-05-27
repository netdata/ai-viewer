// Package presenter is the HTTP+SSE serving layer for ai-viewer-serve.
//
// It reads the canonical SQLite store via a read-only database handle
// and exposes JSON over HTTP plus an SSE event channel. It knows nothing
// about adapters or the ingester process; the only coupling is the
// SQLite file on disk plus an optional notify socket (landing in a
// later chunk).
//
// Source of truth: .agents/sow/specs/presenter.md and
// .agents/sow/specs/rest-api.md. Observability contract:
// .agents/sow/specs/observability.md. Security stance:
// .agents/sow/specs/security.md.
//
// Chunk 11 of SOW-0001 ships only /api/health and /api/sources. Every
// other route declared in presenter.md is wired in subsequent chunks
// (12 — sessions / logs / stats; 13 — SSE hub).
//
// The package must not import internal/ingest: the ingester owns the
// SQLite writer and lives in a separate process. Coupling the two
// packages would defeat the two-binary topology that AGENTS.md mandates.
package presenter
