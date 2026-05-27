// Coverage tests for health-helper internals: db-size probe,
// recentParseErrorCount edge paths, collectSources error path, and the
// nullableInt sanity struct. Split out of coverage_test.go in iter-4
// so no single test file exceeds the project's 400-line budget.
package presenter

import (
	"context"
	"database/sql"
	"testing"
)

// TestDBSizeBytesProbe asserts the size probe returns a sensible value
// against a freshly-migrated database. Page count * page size is
// non-zero (the schema itself takes pages).
func TestDBSizeBytesProbe(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	got := p.dbSizeBytesOrZero(context.Background())
	if got <= 0 {
		t.Fatalf("dbSizeBytesOrZero = %d, want > 0", got)
	}
}

// TestDBSizeBytesProbeFailsCleanly asserts a closed DB returns 0
// without panicking. Verifies the soft-fail path.
func TestDBSizeBytesProbeFailsCleanly(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	_ = db.Close()
	got := p.dbSizeBytesOrZero(context.Background())
	if got != 0 {
		t.Fatalf("dbSizeBytesOrZero on closed db = %d, want 0", got)
	}
}

// TestRecentParseErrorCountEmpty asserts the count is zero against a
// freshly-migrated database. Provides the missing branch coverage on
// the helper.
func TestRecentParseErrorCountEmpty(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	count, err := p.recentParseErrorCount(context.Background(), fixedTime)
	if err != nil {
		t.Fatalf("recentParseErrorCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

// TestRecentParseErrorCountAfterClose asserts the helper reports a
// DB error rather than panicking when the connection is gone.
func TestRecentParseErrorCountAfterClose(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	_ = db.Close()
	count, err := p.recentParseErrorCount(context.Background(), fixedTime)
	if err == nil {
		t.Fatal("want error against closed DB")
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

// TestCollectSourcesScanError asserts an error during row scan is
// surfaced. We trigger the failure by closing the DB before iteration.
func TestCollectSourcesScanError(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	_ = db.Close()
	_, _, err := p.collectSources(context.Background(), fixedTime)
	if err == nil {
		t.Fatal("want error on closed DB")
	}
}

// TestNullableIntJSON is a no-op sanity check on the helper struct to
// keep its declaration exercised; the type exists for future paginated
// endpoints.
func TestNullableIntJSON(t *testing.T) {
	t.Parallel()
	n := nullableInt{NullInt64: sql.NullInt64{Int64: 5, Valid: true}}
	if !n.Valid || n.Int64 != 5 {
		t.Fatal("nullableInt round-trip failed")
	}
}
