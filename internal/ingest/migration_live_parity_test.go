package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins SOW-0026 mitigation R2: the historical backfill in migration
// 0005 must reconstruct EXACTLY the catalog total_duration_us values the live
// incremental writer path produces. If the two ever diverge (e.g. a future
// edit changes one recompute's grouping but not the other), a migrated store
// would disagree with a freshly-ingested one and the duration columns would be
// silently wrong for historical data. The test drives BOTH paths over the same
// logical set of ops and asserts equality.
//
//   - Path A (live): a fresh writer ingests LLM + tool ops with correct
//     start/end timestamps, so onOpFinalized books total_duration_us live.
//   - Path B (migration): a fresh store is seeded with the pre-fix buggy state
//     (ops.duration_us = 0, catalog rows.total_duration_us = 0), then ONLY the
//     shipped 0005 recompute SQL is executed against it.
//
// The two captured total_duration_us values must be identical, per catalog row.

// op0005Fixture is one op present in BOTH the live and migration DBs, so the
// two paths operate over an identical logical set.
type op0005Fixture struct {
	seq           int
	kind          canonical.OpKind
	name          string
	provider      string // LLM only
	model         string // LLM only
	toolNamespace string // tool only ("" folds to builtin)
	startTs       int64
	endTs         int64
}

// op0005Fixtures is the shared op set: two LLM ops under one (provider, model),
// one tool op with an explicit namespace, and one tool op with an empty
// namespace (which both paths must fold to 'builtin').
var op0005Fixtures = []op0005Fixture{
	{seq: 1, kind: canonical.OpLLM, name: "message", provider: "openai", model: "gpt-5.5", startTs: 1000, endTs: 1300}, // 300
	{seq: 2, kind: canonical.OpLLM, name: "message", provider: "openai", model: "gpt-5.5", startTs: 2000, endTs: 2700}, // 700
	{seq: 3, kind: canonical.OpTool, name: "shell", toolNamespace: "shell", startTs: 3000, endTs: 3500},                // 500
	{seq: 4, kind: canonical.OpTool, name: "read", toolNamespace: "", startTs: 4000, endTs: 4200},                      // 200 → builtin
}

// parityCatalogTotals is the comparable shape captured from each path: every catalog
// model/tool row's total_duration_us, keyed by its natural identity.
type parityCatalogTotals struct {
	models map[string]int64 // "provider|name" → total_duration_us
	tools  map[string]int64 // "namespace|name" → total_duration_us
}

// TestMigration0005RecomputeMatchesLiveWriter proves the 0005 backfill recompute
// reconstructs the same catalog total_duration_us the live writer books, so a
// migrated historical store agrees with a freshly-ingested one (SOW-0026 R2).
func TestMigration0005RecomputeMatchesLiveWriter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	live := captureLiveCatalogTotals(t, ctx)
	migrated := captureMigratedCatalogTotals(t, ctx)

	// The live path must actually book non-zero totals, otherwise the test
	// would pass trivially against an all-zero migration result.
	if live.models["openai|gpt-5.5"] != 1000 {
		t.Fatalf("live catalog_models[openai|gpt-5.5] = %d, want 1000 (300+700) — live premise broken",
			live.models["openai|gpt-5.5"])
	}
	if live.tools["shell|shell"] != 500 || live.tools["builtin|read"] != 200 {
		t.Fatalf("live catalog_tools unexpected: shell=%d builtin/read=%d — live premise broken",
			live.tools["shell|shell"], live.tools["builtin|read"])
	}

	for key, want := range live.models {
		if got := migrated.models[key]; got != want {
			t.Errorf("catalog_models[%s].total_duration_us: live=%d migration=%d (must match)", key, want, got)
		}
	}
	for key, want := range live.tools {
		if got := migrated.tools[key]; got != want {
			t.Errorf("catalog_tools[%s].total_duration_us: live=%d migration=%d (must match)", key, want, got)
		}
	}
	// Symmetric check: the migration must not invent rows the live path lacks.
	if len(migrated.models) != len(live.models) {
		t.Errorf("catalog_models row count: live=%d migration=%d", len(live.models), len(migrated.models))
	}
	if len(migrated.tools) != len(live.tools) {
		t.Errorf("catalog_tools row count: live=%d migration=%d", len(live.tools), len(migrated.tools))
	}
}

// captureLiveCatalogTotals ingests op0005Fixtures through the real writer (which
// books catalog totals incrementally in onOpFinalized) and returns the totals.
func captureLiveCatalogTotals(t *testing.T, ctx context.Context) parityCatalogTotals {
	t.Helper()
	const src = "codex:/tmp"
	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, src, "codex", "/tmp"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	w := newWriter(src, "codex", "/tmp", NopPricer{})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	apply := func(ev canonical.Event) {
		if aErr := w.apply(ctx, tx, ev); aErr != nil {
			t.Fatalf("apply %T: %v", ev, aErr)
		}
	}
	apply(canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	})
	var seq uint64 = 2
	for _, f := range op0005Fixtures {
		apply(canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: seq, Ts: f.startTs},
			SessionNativeID: "s", TurnSeq: 1, Seq: f.seq, ParentOpSeq: -1,
			Kind: f.kind, Name: f.name, Provider: f.provider, Model: f.model, ToolNamespace: f.toolNamespace,
		})
		seq++
		// Spec-conformant: Finalized.Ts == EndTs (the op END). Duration derives
		// from the persisted start_ts regardless.
		apply(canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: seq, Ts: f.endTs},
			SessionNativeID: "s", TurnSeq: 1, Seq: f.seq, Status: "completed", EndTs: f.endTs,
		})
		seq++
	}
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit live: %v", cErr)
	}
	return readParityCatalogTotals(t, ctx, db)
}

// captureMigratedCatalogTotals seeds the pre-fix buggy state (ops.duration_us=0,
// catalog rows.total_duration_us=0) then runs ONLY the shipped 0005 recompute
// SQL, returning the totals it reconstructs.
func captureMigratedCatalogTotals(t *testing.T, ctx context.Context) parityCatalogTotals {
	t.Helper()
	_, db := openTestStore(t)
	seedBuggyHistoricalState(t, ctx, db)

	// Execute the EXACT shipped 0005 migration body (read from disk so the test
	// runs what actually ships, not a re-typed copy). go test runs with the
	// package directory as CWD, so the store migrations live one level up.
	sqlPath := filepath.Join("..", "store", "migrations", "0005_op_duration_backfill.sql")
	body, err := os.ReadFile(sqlPath) // #nosec G304 -- compile-time-constant test path, no external input
	if err != nil {
		t.Fatalf("read migration 0005 (%s): %v", sqlPath, err)
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("apply migration 0005 recompute: %v", err)
	}
	return readParityCatalogTotals(t, ctx, db)
}

// seedBuggyHistoricalState inserts the source/session/turn parents, the
// op0005Fixtures ops with the buggy duration_us=0, and catalog rows with
// total_duration_us=0 — exactly the state the pre-fix writer produced.
func seedBuggyHistoricalState(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	mustExecParity(t, ctx, db, `INSERT INTO sources (id, format, location, enabled, parse_errors, created_at)
	                            VALUES ('src','codex','/tmp',1,0,1000)`)
	mustExecParity(t, ctx, db, `INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
	                            VALUES ('sess','src','s','sess','root','completed',1000,5000)`)
	mustExecParity(t, ctx, db, `INSERT INTO turns (id, session_id, seq, start_ts, status)
	                            VALUES ('turn','sess',1,1000,'completed')`)

	for _, f := range op0005Fixtures {
		// Op id keys on the unique seq (two LLM fixtures share the name "message",
		// so a name-based id would collide on the ops.id PK).
		opID := fmt.Sprintf("op-%d", f.seq)
		mustExecParity(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, tool_namespace, model, provider,
                 start_ts, end_ts, duration_us, status)
VALUES (?, 'turn', 'sess', ?, ?, ?, ?, ?, ?, ?, ?, 0, 'completed')`,
			opID, f.seq, string(f.kind), f.name,
			nullIfEmpty(f.toolNamespace), nullIfEmpty(f.model), nullIfEmpty(f.provider),
			f.startTs, f.endTs)
	}

	// Catalog rows in the buggy total_duration_us=0 state. Namespaces mirror the
	// live writer's fold: empty tool namespace → 'builtin'.
	mustExecParity(t, ctx, db, `INSERT INTO catalog_models (provider, name, first_seen, last_seen, call_count, total_duration_us)
	                            VALUES ('openai','gpt-5.5',1000,2700,2,0)`)
	mustExecParity(t, ctx, db, `INSERT INTO catalog_tools (namespace, name, first_seen, last_seen, call_count, total_duration_us)
	                            VALUES ('shell','shell',3000,3500,1,0)`)
	mustExecParity(t, ctx, db, `INSERT INTO catalog_tools (namespace, name, first_seen, last_seen, call_count, total_duration_us)
	                            VALUES ('builtin','read',4000,4200,1,0)`)
}

// readParityCatalogTotals snapshots every catalog model/tool row's total_duration_us
// keyed by natural identity, so the two paths compare directly.
func readParityCatalogTotals(t *testing.T, ctx context.Context, db *sql.DB) parityCatalogTotals {
	t.Helper()
	out := parityCatalogTotals{models: map[string]int64{}, tools: map[string]int64{}}

	rows, err := db.QueryContext(ctx, `SELECT provider, name, total_duration_us FROM catalog_models`)
	if err != nil {
		t.Fatalf("query catalog_models: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var provider, name string
		var total int64
		if err := rows.Scan(&provider, &name, &total); err != nil {
			t.Fatalf("scan catalog_models: %v", err)
		}
		out.models[provider+"|"+name] = total
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iter catalog_models: %v", err)
	}

	trows, err := db.QueryContext(ctx, `SELECT namespace, name, total_duration_us FROM catalog_tools`)
	if err != nil {
		t.Fatalf("query catalog_tools: %v", err)
	}
	defer func() { _ = trows.Close() }()
	for trows.Next() {
		var namespace, name string
		var total int64
		if err := trows.Scan(&namespace, &name, &total); err != nil {
			t.Fatalf("scan catalog_tools: %v", err)
		}
		out.tools[namespace+"|"+name] = total
	}
	if err := trows.Err(); err != nil {
		t.Fatalf("iter catalog_tools: %v", err)
	}
	return out
}

func mustExecParity(t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}
