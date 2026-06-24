package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestE2E_AIAgentLineageGolden_RealUpstreamFixtures is the SOW-0039
// end-to-end lineage golden. It ingests the REAL ai-agent upstream
// fixtures (ai-agent@8a0078bc src/tests/fixtures/v3-evidence/
// sub-agent-with-parent-id/session/{root,child}-session.jsonl, adopted
// byte-identically under internal/adapters/aiagent_v3/testdata/) through
// the real v3 adapter Scan -> ingester -> in-memory SQLite store, then
// asserts the persisted session/op rows the presenter serves.
//
// Harness: ingest->store, mirroring TestE2E_SubAgentFixture and
// resolver_op_child_test.go. Store-layer SQL assertions are deliberate:
// the persisted sessions/ops rows ARE the rows the presenter's
// loadSession (session_detail.go: root_session_id, parent_session_id)
// reads, so asserting them is asserting the end-to-end lineage. Crossing
// into the presenter's separate reader-side store would add HTTP/Options
// plumbing without strengthening the lineage assertion.
//
// The four assertions reference the EXACT real-fixture ids
// (root-session / child-session / parent-op-1 / chatcmpl-fixture) so a
// wrong mapping is caught. This test proves the INTEGRATED end-state with
// BOTH ledgers present (the steady-state UI sees this); it is the overall
// lineage golden.
//
// Mutation-sensitivity is split across three tests on purpose. With BOTH
// ledgers present the integrated end-state is order-dependent: the parent-side
// synthesizer (mapper.go:229-260) can re-supply parent_session_id /
// parent_op_id / root_session_id on UPSERT, so this combined test is NOT a
// reliable standalone detector of a wrong child-side mapping (whether a given
// child-side mutation is masked depends on event-arrival order). Each path is
// therefore isolated in a test where it is UNCONDITIONALLY mutation-sensitive,
// independent of ingest order:
//   - child's OWN ledger (i)+(iv): TestE2E_AIAgentLineageGolden_ChildSideOwnLedger
//     (no parent -> no synthesizer can mask the child-side mapping).
//   - parent's childSessions[] alone (ii)+(iii):
//     TestE2E_AIAgentLineageGolden_ParentSideSynthesizer (no child ledger).
//
// Each split test FAILS under its corresponding single-field mutation (verified
// during authoring: ParentNativeID flip + ParentOpKey drop turn
// _ChildSideOwnLedger red; synthesizer child.OriginID flip turns
// _ParentSideSynthesizer red; the llmRequestId drop turns the relevant test
// red). This combined test is the integration sanity check, not the mutation
// proof.
func TestE2E_AIAgentLineageGolden_RealUpstreamFixtures(t *testing.T) {
	t.Parallel()

	// The adapter walks <root>/session/*.jsonl; point it at the fixture
	// scenario dir that contains the session/ subtree.
	fixtureDir, err := filepath.Abs(filepath.Join(
		"..", "adapters", "aiagent_v3", "testdata", "sub-agent-with-parent-id"))
	if err != nil {
		t.Fatalf("abs fixture dir: %v", err)
	}
	src := "aiagent_v3:" + fixtureDir

	db := ingestV3Dir(t, src, fixtureDir, 2)

	// Identities the writer derives from (sourceID, nativeID).
	rootID := canonicalSessionID(src, "root-session")
	childID := canonicalSessionID(src, "child-session")

	// --- (preconditions) both real sessions present ---
	// NOTE on kind: the real upstream fixture's root session_start carries
	// NO headendId, so headendToKind("") -> sub_agent (mapper.go:78-89) for
	// BOTH rows. That is faithful adapter behavior — the fixture exercises
	// LINEAGE, not headend classification — so we do not assert kind='root'
	// here. The meaningful root-ness signal is structural (asserted below):
	// the root has no parent and is its own root.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id IN ('root-session','child-session')`); got != 2 {
		t.Fatalf("expected both root+child sessions present, got %d", got)
	}
	// The root session has NO parent (it is the origin of the tree).
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE native_id='root-session'`); got != "" {
		t.Errorf("root parent_session_id = %q, want empty (root has no parent)", got)
	}

	// --- (i) child resolves parent_session_id + parent_op_id from its OWN ledger ---
	// child-session.jsonl session_start carries parentSessionId=root-session,
	// parentOpId=parent-op-1 (mapper.go:118-119). parent_session_id is the
	// resolved FK to the root row; parent_op_id (ParentOpKey) is persisted by
	// the writer into sessions.extras_json.aiViewer.parentOpKey (writer.go:486).
	gotParent := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE native_id='child-session'`)
	if gotParent != rootID {
		t.Errorf("(i) child parent_session_id = %q, want %q (root row)", gotParent, rootID)
	}
	if gotParent == childID {
		t.Errorf("(i) child parent_session_id points at itself (%q) — mapper mapped the wrong field", childID)
	}
	gotParentOp := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.parentOpKey'),'') FROM sessions WHERE native_id='child-session'`)
	if gotParentOp != "parent-op-1" {
		t.Errorf("(i) child parentOpKey = %q, want %q", gotParentOp, "parent-op-1")
	}

	// --- (iii) root_session_id / origin chains to root-session ---
	gotRoot := scanString(t, db, `SELECT IFNULL(root_session_id,'') FROM sessions WHERE native_id='child-session'`)
	if gotRoot != rootID {
		t.Errorf("(iii) child root_session_id = %q, want %q (root row)", gotRoot, rootID)
	}
	if gotRoot == childID {
		t.Errorf("(iii) child root_session_id points at itself (%q) — origin mapped wrong", childID)
	}
	// The root session is its own root (self-referential per data-model.md).
	if got := scanString(t, db, `SELECT IFNULL(root_session_id,'') FROM sessions WHERE native_id='root-session'`); got != rootID {
		t.Errorf("(iii) root root_session_id = %q, want self %q", got, rootID)
	}

	// --- (iv) child's LLM op carries attr.llmRequestId == chatcmpl-fixture ---
	// child-session.jsonl turn_end op child-llm-1 (kind=llm, opIndex=1) has
	// attributes.llmRequestId; the adapter copies it to extras["attr.llmRequestId"]
	// (ops.go:78-80) and the writer persists op extras into ops.extras_json.
	turnID := canonicalTurnID(childID, 1)
	opID := canonicalOpID(turnID, 1)
	gotReqID := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$."attr.llmRequestId"'),'') FROM ops WHERE id=?`, opID)
	if gotReqID != "chatcmpl-fixture" {
		t.Errorf("(iv) op attr.llmRequestId = %q, want %q", gotReqID, "chatcmpl-fixture")
	}
	// Pin that we are reading the LLM op, not some other op.
	if got := scanString(t, db, `SELECT kind FROM ops WHERE id=?`, opID); got != "llm" {
		t.Errorf("(iv) op kind = %q, want llm", got)
	}
}

// TestE2E_AIAgentLineageGolden_ChildSideOwnLedger isolates AC #2 (i) and
// (iv) on the CHILD's OWN ledger. We ingest ONLY child-session.jsonl (the
// root ledger is absent), so the parent-side synthesizer (mapper.go:229-260)
// CANNOT fire — every lineage signal here originates from the child's own
// session_start / turn_end. This is what makes the child-side mapping
// UNCONDITIONALLY mutation-sensitive here: with no parent ledger nothing can
// re-supply the child-side fields, so a wrong mapping cannot be masked (unlike
// the combined test, where the synthesizer may re-supply them on UPSERT
// depending on arrival order). Confirmed during authoring: emptying
// mapper.go:119 ParentOpKey FAILS the parentOpKey assertion here; pointing
// mapper.go:118 ParentNativeID at the child's own id FAILS the parentNativeId
// assertion here.
//
// Parent/root rows are absent, so parent_session_id / root_session_id FK
// columns cannot resolve (writer leaves them at NULL / self and stashes the
// native ids in extras_json.aiViewer for the resolver). We therefore assert
// the stashed native ids — which come purely from the child ledger — plus
// the op attribute.
func TestE2E_AIAgentLineageGolden_ChildSideOwnLedger(t *testing.T) {
	t.Parallel()

	childOnly := t.TempDir()
	sessDir := filepath.Join(childOnly, "session")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	srcChild, err := filepath.Abs(filepath.Join(
		"..", "adapters", "aiagent_v3", "testdata", "sub-agent-with-parent-id",
		"session", "child-session.jsonl"))
	if err != nil {
		t.Fatalf("abs child fixture: %v", err)
	}
	childBytes, err := os.ReadFile(srcChild)
	if err != nil {
		t.Fatalf("read child fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "child-session.jsonl"), childBytes, 0o644); err != nil {
		t.Fatalf("write child-only ledger: %v", err)
	}

	src := "aiagent_v3:" + childOnly
	// Only the child session exists on disk (no synthesized sibling).
	db := ingestV3Dir(t, src, childOnly, 1)

	childID := canonicalSessionID(src, "child-session")

	// This row came from the child's OWN session_start, not synthesis.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.synthesizedFromParent'),'') FROM sessions WHERE native_id='child-session'`); got != "" {
		t.Errorf("child unexpectedly flagged synthesizedFromParent (%q) — should be its own session_start", got)
	}

	// (i) parent linkage from the child's OWN ledger. The parent row is
	// absent, so parent_session_id is NULL and the native parent id is
	// stashed for the resolver — that stash (and parentOpKey) is the
	// child-side mapping under test (mapper.go:118-119 -> writer.go:485-486).
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE native_id='child-session'`); got != "" {
		t.Errorf("(i) child parent_session_id = %q, want empty (parent row absent — must stash, not invent)", got)
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.parentNativeId'),'') FROM sessions WHERE native_id='child-session'`); got != "root-session" {
		t.Errorf("(i) child parentNativeId stash = %q, want %q (from child's own session_start)", got, "root-session")
	}
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.parentOpKey'),'') FROM sessions WHERE native_id='child-session'`); got != "parent-op-1" {
		t.Errorf("(i) child parentOpKey = %q, want %q (from child's own session_start)", got, "parent-op-1")
	}
	// (iii) origin captured from the child's own session_start (rootNativeId
	// stash == originId), even though the absent root row means the
	// root_session_id FK cannot point elsewhere yet.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.rootNativeId'),'') FROM sessions WHERE native_id='child-session'`); got != "root-session" {
		t.Errorf("(iii) child rootNativeId stash = %q, want %q (origin from child's own ledger)", got, "root-session")
	}

	// (iv) child's LLM op carries attr.llmRequestId == chatcmpl-fixture,
	// read straight from the child ledger's turn_end (ops.go:78-80).
	opID := canonicalOpID(canonicalTurnID(childID, 1), 1)
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$."attr.llmRequestId"'),'') FROM ops WHERE id=?`, opID); got != "chatcmpl-fixture" {
		t.Errorf("(iv) op attr.llmRequestId = %q, want %q", got, "chatcmpl-fixture")
	}
	if got := scanString(t, db, `SELECT kind FROM ops WHERE id=?`, opID); got != "llm" {
		t.Errorf("(iv) op kind = %q, want llm", got)
	}
}

// TestE2E_AIAgentLineageGolden_ParentSideSynthesizer pins AC #2 (ii): the
// SAME parent linkage must be resolvable from the PARENT's childSessions[]
// alone. We ingest ONLY the root ledger (no child-session.jsonl on disk),
// so the child row exists solely because mapper.go's
// synthesizedChildSessionStarted (mapper.go:229-260) fabricated a
// SessionStartedEvent from root's turn_end.ops[].childSessions[] /
// session_summary.childSessions[]. The child's own ledger is absent, so a
// passing assertion proves the parent-side path — not the child-side fast
// path — produced the linkage.
func TestE2E_AIAgentLineageGolden_ParentSideSynthesizer(t *testing.T) {
	t.Parallel()

	// Build a scenario dir containing ONLY the root ledger.
	rootOnly := t.TempDir()
	sessDir := filepath.Join(rootOnly, "session")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	srcRoot, err := filepath.Abs(filepath.Join(
		"..", "adapters", "aiagent_v3", "testdata", "sub-agent-with-parent-id",
		"session", "root-session.jsonl"))
	if err != nil {
		t.Fatalf("abs root fixture: %v", err)
	}
	rootBytes, err := os.ReadFile(srcRoot)
	if err != nil {
		t.Fatalf("read root fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "root-session.jsonl"), rootBytes, 0o644); err != nil {
		t.Fatalf("write root-only ledger: %v", err)
	}

	src := "aiagent_v3:" + rootOnly
	// Two sessions expected: the real root + the synthesized child.
	db := ingestV3Dir(t, src, rootOnly, 2)

	rootID := canonicalSessionID(src, "root-session")
	childID := canonicalSessionID(src, "child-session")

	// The child exists only via synthesis from the parent.
	if got := scanInt(t, db,
		`SELECT COUNT(*) FROM sessions WHERE native_id='child-session'`); got != 1 {
		t.Fatalf("synthesized child session not present (count=%d) — parent-side path did not fire", got)
	}
	// It must be marked synthesized-from-parent (mapper.go sets this on the
	// parent-side path only; the child-side fast path never sets it). This is
	// the discriminator proving the linkage came from childSessions[], not the
	// child's own (absent) session_start.
	if got := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.synthesizedFromParent'),'') FROM sessions WHERE native_id='child-session'`); got != "1" {
		t.Errorf("child not flagged synthesizedFromParent (got %q) — linkage did not come from the parent-side path", got)
	}

	// (ii) The SAME parent linkage resolves from childSessions[] alone:
	// parent_session_id -> root row, parentOpKey == parent-op-1.
	gotParent := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE native_id='child-session'`)
	if gotParent != rootID {
		t.Errorf("(ii) synthesized child parent_session_id = %q, want %q (root row)", gotParent, rootID)
	}
	if gotParent == childID {
		t.Errorf("(ii) synthesized child parent_session_id points at itself (%q)", childID)
	}
	gotParentOp := scanString(t, db,
		`SELECT IFNULL(json_extract(extras_json,'$.aiViewer.parentOpKey'),'') FROM sessions WHERE native_id='child-session'`)
	if gotParentOp != "parent-op-1" {
		t.Errorf("(ii) synthesized child parentOpKey = %q, want %q", gotParentOp, "parent-op-1")
	}
	// (iii) origin chains to root-session even on the synthesized child
	// (mapper.go:230-233: child.OriginID == "root-session").
	gotRoot := scanString(t, db, `SELECT IFNULL(root_session_id,'') FROM sessions WHERE native_id='child-session'`)
	if gotRoot != rootID {
		t.Errorf("(ii/iii) synthesized child root_session_id = %q, want %q (root row)", gotRoot, rootID)
	}
}

// ingestV3Dir runs the real aiagent_v3 adapter against scanRoot, drains
// Scan through a fresh in-memory store-backed ingester, waits for
// wantSessions rows, runs one ResolveOrphans pass, and returns the DB for
// assertions. Factored from TestE2E_SubAgentFixture so both lineage
// sub-tests share one harness.
func ingestV3Dir(t *testing.T, src, scanRoot string, wantSessions int64) *sql.DB {
	t.Helper()

	a, err := aiagent_v3.New(scanRoot, canonical.AdapterOptions{Logger: silentLogger()})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}
	_, db := openTestStore(t)
	ing, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2000),
		WithBatchInterval(50*time.Millisecond),
		WithSourceFormat(src, "aiagent_v3"),
		WithLocation(src, scanRoot),
	)
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := make(chan canonical.Event, 256)
	scanDone := make(chan struct{})
	go func() {
		defer close(events)
		if err := a.Scan(ctx, nil, events); err != nil {
			t.Errorf("Scan: %v", err)
		}
		close(scanDone)
	}()
	if err := ing.Submit(src, events); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForScan(t, scanDone, "aiagent_v3 lineage")
	if err := ing.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions`); got != wantSessions {
		t.Fatalf("session count after Stop = %d, want %d", got, wantSessions)
	}
	// Backfill any out-of-order parent/root/op-child linkage now that all
	// rows are present.
	if err := ing.ResolveOrphans(ctx); err != nil {
		t.Fatalf("ResolveOrphans: %v", err)
	}
	return db
}
