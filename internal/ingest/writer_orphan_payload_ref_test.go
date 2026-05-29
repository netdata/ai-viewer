package ingest

import (
	"context"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestWriter_PayloadRefOrphanOpSurfacesErrorAndSurvives pins the
// defense-in-depth guard in applyPayloadRef: a PayloadRefEvent whose
// (TurnSeq, OpSeq) names an op that has no ops row must NOT abort the batch
// (payload_refs.op_id is NOT NULL REFERENCES ops(id), so the insert would
// raise FK 787 and roll back everything). Instead the writer skips the
// insert, bumps sources.parse_errors, and writes a source-scoped ERR
// log_entries row so /api/health and the source-status panel surface the
// problem — never a silent drop. The batch commits with the unrelated
// session intact.
func TestWriter_PayloadRefOrphanOpSurfacesErrorAndSurvives(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	// A session exists, but NO OpStarted for (TurnSeq=1, OpSeq=1) — so the
	// derived op_id has no ops row.
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
	}); err != nil {
		t.Fatalf("apply SessionStarted: %v", err)
	}
	// The orphan payload ref. apply MUST return nil (batch survives).
	if err := w.apply(ctx, tx, canonical.PayloadRefEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1250},
		SessionNativeID: "sess-1", TurnSeq: 1, OpSeq: 1,
		PayloadKind: "llm_request", Format: "http",
		LocationURI: "file:///tmp/payloads/sess-1/turn-1/op-1-request.http",
	}); err != nil {
		t.Fatalf("apply orphan PayloadRef returned error (must survive): %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// No payload_refs row was inserted for the orphan ref.
	if v := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`); v != 0 {
		t.Errorf("payload_refs count = %d, want 0 (orphan ref must not insert)", v)
	}
	// The condition was surfaced: parse_errors incremented exactly once.
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 1 {
		t.Errorf("parse_errors = %d, want 1 (orphan ref must surface a parse error)", v)
	}
	// And a source-scoped ERR log row records it (not a silent drop).
	if v := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE source_id IS NOT NULL AND severity='ERR'`); v != 1 {
		t.Errorf("source-scoped ERR log count = %d, want 1", v)
	}
	// The unrelated session still committed.
	if v := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='sess-1'`); v != 1 {
		t.Errorf("session count = %d, want 1 (batch must survive the orphan ref)", v)
	}
}

// TestWriter_PayloadRefExistingOpInsertsNormally pins the happy path: when
// the parent op DOES exist (an OpStarted for the same turn/op ran first),
// the guard passes through and the payload_refs row is inserted as before,
// with no parse error surfaced.
func TestWriter_PayloadRefExistingOpInsertsNormally(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	_ = ensureSourceRowDirect(ctx, db, "aiagent_v3:/tmp", "aiagent_v3", "/tmp")
	w := newWriter("aiagent_v3:/tmp", "aiagent_v3", "/tmp", NopPricer{})

	tx, _ := db.BeginTx(ctx, nil)
	if err := w.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
	}); err != nil {
		t.Fatalf("apply SessionStarted: %v", err)
	}
	// OpStarted creates the ops row the payload ref will reference.
	if err := w.apply(ctx, tx, canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1200},
		SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
	}); err != nil {
		t.Fatalf("apply OpStarted: %v", err)
	}
	if err := w.apply(ctx, tx, canonical.PayloadRefEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1250},
		SessionNativeID: "sess-1", TurnSeq: 1, OpSeq: 1,
		PayloadKind: "llm_request", Format: "http", Compression: "gzip",
		LocationURI:   "file:///tmp/payloads/sess-1/turn-1/op-1-request.http.gz",
		OriginalBytes: 12345, StoredBytes: 4096, SHA256: "abc123",
	}); err != nil {
		t.Fatalf("apply PayloadRef: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The payload_refs row IS inserted.
	if v := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs`); v != 1 {
		t.Errorf("payload_refs count = %d, want 1 (existing op must insert)", v)
	}
	wantOpID := canonicalOpID(canonicalTurnID(canonicalSessionID("aiagent_v3:/tmp", "sess-1"), 1), 1)
	if v := scanInt(t, db, `SELECT COUNT(*) FROM payload_refs WHERE op_id = ?`, wantOpID); v != 1 {
		t.Errorf("payload_refs for op = %d, want 1", v)
	}
	// No parse error surfaced on the happy path.
	if v := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); v != 0 {
		t.Errorf("parse_errors = %d, want 0 (existing op must not surface an error)", v)
	}
}
