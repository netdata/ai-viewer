package ingest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// SOW-0055 characterization pins for resolver.go linkOrphans transactional behaviour.

// TestResolver_CharacterizationLinkageFailureRollsBackWithoutNotifyRows pins
// the resolver.go linkOrphans contract: when a downstream UPDATE/notify INSERT
// fails inside the resolver's single transaction, neither the linkage UPDATEs
// nor the notify rows are visible after the failure. A helper-runner refactor
// that committed in pieces would break the SSE contract.
func TestResolver_CharacterizationLinkageFailureRollsBackWithoutNotifyRows(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	db, childID, parentID := seedOrphanChildThenParent(t, src)
	ctx := context.Background()

	assertOrphanPrecondition(t, db, childID)
	sow55InstallNotifyAbortTrigger(t, db)
	defer sow55DropNotifyAbortTrigger(t, db)

	r := newResolver(db, silentLogger(), time.Minute)
	err := r.linkOrphans(ctx)
	if err == nil {
		t.Fatal("linkOrphans succeeded despite forced notify abort")
	}
	if !strings.Contains(err.Error(), "sow55 forced notify abort") {
		t.Fatalf("error does not contain the forced abort marker: %v", err)
	}

	assertResolverRollbackLinkage(t, db, childID, parentID)
	assertResolverRollbackNotify(t, db)
}

// TestResolver_CharacterizationLinkOrphansEmitsAllPassesInOneTx pins the
// complementary linkOrphans property: every link pass AND the notify emission
// run inside the SAME transaction. The happy path is exercised so the contract
// — pass results visible at commit AND notify rows landed in the same commit —
// is pinned.
func TestResolver_CharacterizationLinkOrphansEmitsAllPassesInOneTx(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	db, childID, parentID := seedOrphanChildThenParent(t, src)

	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(context.Background()); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

	assertResolverHappyPathLinkage(t, db, childID, parentID)
	assertResolverHappyPathNotify(t, db)
}

// ----- resolver helpers -----

func assertOrphanPrecondition(t *testing.T, db *sql.DB, childID string) {
	t.Helper()
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != "" {
		t.Fatalf("child unexpectedly linked before resolver: %q", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify`); got != 0 {
		t.Fatalf("notify table non-empty before test: %d rows", got)
	}
}

func sow55InstallNotifyAbortTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
CREATE TRIGGER abort_notify BEFORE INSERT ON notify
BEGIN
    SELECT RAISE(ABORT, 'sow55 forced notify abort');
END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
}

func sow55DropNotifyAbortTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS abort_notify`); err != nil {
		t.Logf("drop trigger: %v", err)
	}
}

func assertResolverRollbackLinkage(t *testing.T, db *sql.DB, childID, parentID string) {
	t.Helper()
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != "" {
		t.Errorf("child parent_session_id = %q, want empty (linkage must roll back on notify failure)", got)
	}
	if got := scanString(t, db, `SELECT IFNULL(root_session_id,'') FROM sessions WHERE id=?`, childID); got == parentID {
		t.Errorf("child root_session_id = %q (= parent), want still self (linkage rolled back)", got)
	}
}

func assertResolverRollbackNotify(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify`); got != 0 {
		t.Errorf("notify rows = %d, want 0 (whole tx must roll back together)", got)
	}
}

func assertResolverHappyPathLinkage(t *testing.T, db *sql.DB, childID, parentID string) {
	t.Helper()
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != parentID {
		t.Errorf("child parent_session_id = %q, want %q (linkage must commit)", got, parentID)
	}
}

func assertResolverHappyPathNotify(t *testing.T, db *sql.DB) {
	t.Helper()
	rows := readNotify(t, db)
	if len(rows) < 2 {
		t.Errorf("notify rows = %d, want ≥ 2 (resolver must emit linkage notify rows)", len(rows))
	}
	hasStats := false
	for _, r := range rows {
		if r.kind == "stats_invalidated" {
			hasStats = true
		}
	}
	if !hasStats {
		t.Error("missing stats_invalidated notify row from resolver pass")
	}
}
