// Logs tab (ui-pages.md §/sessions/:id #5). Chunk 16 wires this to GET
// /api/sessions/:id/logs (severity filter + keyset pagination). Placeholder for
// Chunk 14; sessionId is threaded through so the query hook drops in cleanly.

export function LogsTab({ sessionId }: { sessionId: string }) {
  return (
    <div>
      <p style={{ color: 'var(--text-secondary)' }}>
        Logs for session <code>{sessionId}</code> — severity-filtered entries
        land in a later chunk.
      </p>
    </div>
  );
}
