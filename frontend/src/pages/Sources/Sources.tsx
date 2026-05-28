// Sources admin / status panel (ui-pages.md §/sources). Chunk 15/16 wires this
// to useSources() + useHealth() and renders the per-source table (format,
// location, last_seen, lag, parse_errors, enabled) plus the /api/health
// summary. Placeholder for Chunk 14; the page owns the content region so the
// query hooks drop in without structural change.

export function Sources() {
  return (
    <section aria-labelledby="sources-title">
      <h1 id="sources-title">Sources</h1>
      <p style={{ color: 'var(--text-secondary)' }}>
        Per-source status and health diagnostics land in a later chunk.
      </p>
    </section>
  );
}
