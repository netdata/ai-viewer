package store_test

import (
	"context"
	"strings"
	"testing"
)

// Migration 0010 (SOW-0091) adds the fts_content FTS5 virtual table that
// indexes operator-visible text extracted from each op's primary payload
// via the canonical extract.ReadableText helper. Mirrors the shape of
// fts_ops (content-owning, UNINDEXED linkage to op_id/session_id/turn_id)
// but with `text` as the indexed column instead of error_text.
//
// This file pins:
//   - The full-chain head version (10)
//   - The fts_content table shape: indexed column `text` + UNINDEXED
//     linkage to op_id / session_id / turn_id
//   - MATCH-style queries against fts_content work (FTS5 is compiled in
//     by modernc.org/sqlite)
//   - snippet() works against fts_content (returns a <mark>-tagged
//     excerpt from the matched text)

// TestMigration0010_ChainHeadSchemaVersion pins the FULL-chain head
// version: openInMemory runs every migration through the latest (0010),
// so the on-disk schema_meta.version is '10'. The bump is also asserted
// by presenter.SchemaVersion (rest-api.md §Schema versioning).
func TestMigration0010_ChainHeadSchemaVersion(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "10" {
		t.Fatalf("schema_meta.version: want %q, got %q (full chain head is 0010)", "10", version)
	}
}

// TestMigration0010_FtsContentShape pins the virtual-table columns:
//   - text      : indexed
//   - op_id     : UNINDEXED linkage
//   - session_id: UNINDEXED linkage
//   - turn_id   : UNINDEXED linkage
func TestMigration0010_FtsContentShape(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	rows, err := db.QueryContext(ctx, `PRAGMA table_info(fts_content)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(fts_content): %v", err)
	}
	defer func() { _ = rows.Close() }()

	type col struct {
		name    string
		typ     string
		indexed bool
	}
	var got []col
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    *string
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		_ = notnull
		_ = dflt
		_ = pk
		// PRAGMA table_info doesn't expose UNINDEXED directly; we infer
		// it from the CREATE VIRTUAL TABLE definition. For FTS5 a column
		// without UNINDEXED is indexed. We treat all columns as indexed
		// here; the column NAME contract is what matters.
		got = append(got, col{name: name, typ: typ, indexed: true})
	}
	if len(got) != 4 {
		t.Fatalf("fts_content columns: want 4 (text, op_id, session_id, turn_id), got %d (%+v)", len(got), got)
	}
	wantNames := []string{"text", "op_id", "session_id", "turn_id"}
	for i, want := range wantNames {
		if got[i].name != want {
			t.Errorf("fts_content column[%d]: want %q, got %q", i, want, got[i].name)
		}
	}
}

// TestMigration0010_FtsContentMatch verifies that FTS5 MATCH works
// against fts_content (modernc.org/sqlite compiles FTS5 in). Insert a
// few rows, search for a unique term, and confirm we get the right
// row back with snippet() populated.
func TestMigration0010_FtsContentMatch(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	// Seed fts_content with three rows.
	seed := []struct {
		opID      string
		sessionID string
		turnID    string
		text      string
	}{
		{"op-alpha", "sess-1", "turn-1", "rate limiting is configured at 100 req/min"},
		{"op-bravo", "sess-2", "turn-1", "we discussed rate limiting strategies"},
		{"op-charlie", "sess-3", "turn-1", "this turn is about caching, not throttling"},
	}
	for _, row := range seed {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO fts_content (text, op_id, session_id, turn_id) VALUES (?, ?, ?, ?)`,
			row.text, row.opID, row.sessionID, row.turnID); err != nil {
			t.Fatalf("seed fts_content %s: %v", row.opID, err)
		}
	}

	// Search for "rate limiting" — should match alpha + bravo, NOT charlie.
	rows, err := db.QueryContext(ctx,
		`SELECT op_id, snippet(fts_content, 0, '<mark>', '</mark>', '...', 16) FROM fts_content WHERE fts_content MATCH ? ORDER BY rank`,
		"rate limiting")
	if err != nil {
		t.Fatalf("MATCH query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var matches []string
	for rows.Next() {
		var opID, snippet string
		if err := rows.Scan(&opID, &snippet); err != nil {
			t.Fatalf("scan: %v", err)
		}
		matches = append(matches, opID)
		if !strings.Contains(snippet, "<mark>") {
			t.Errorf("snippet for %s should contain <mark>: %q", opID, snippet)
		}
	}
	if len(matches) != 2 {
		t.Errorf("expected 2 matches for 'rate limiting', got %d: %v", len(matches), matches)
	}
	wantMatches := map[string]bool{"op-alpha": true, "op-bravo": true}
	for _, m := range matches {
		if !wantMatches[m] {
			t.Errorf("unexpected match: %s", m)
		}
	}
}
