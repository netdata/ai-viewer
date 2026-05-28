// Session tree helpers — used by the hierarchical sessions list (expand to show
// child sub-agent sessions). The list endpoint already returns
// child_session_count per root row, so the expander can render without this in
// Chunk 14; the grouping/flattening helpers land with the live list (Chunk 15).
// Intentionally empty for Chunk 14 to keep the bundle lean.
export {};
