// Overview tab (ui-pages.md §/sessions/:id #1). A later chunk wires this to
// useSessionDetail(id) — whose session row carries the per-session aggregates
// (tokens_in/out, cost_usd, turn_count, op_count, failure_count) — and renders
// the header (agent/model/status) plus per-session StatCards. (Not useStats:
// /api/stats is cross-session only, see src/api/stats.ts.) Placeholder for
// Chunk 14; the id is passed in so the data hook drops in without prop changes.

export function OverviewTab({ sessionId }: { sessionId: string }) {
  return (
    <div>
      <p style={{ color: 'var(--text-secondary)' }}>
        Overview for session <code>{sessionId}</code> — header + per-session
        statistics land in a later chunk.
      </p>
    </div>
  );
}
