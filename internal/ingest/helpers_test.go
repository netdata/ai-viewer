package ingest

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// silentLogger returns a discarding slog.Logger so tests do not paint
// the terminal with structured log output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openTestStore opens a fresh in-memory writer-side store with the v1
// schema applied. The store is closed on test cleanup.
func openTestStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	s, err := store.OpenWriter(context.Background(), ":memory:", silentLogger())
	if err != nil {
		t.Fatalf("store.OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s, s.DB()
}

// scanInt returns a single int64 from a one-row SELECT.
func scanInt(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	return v
}

// scanString returns a single string from a one-row SELECT.
func scanString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	return v.String
}

// waitFor polls cond every 10ms up to total. Returns false if the
// deadline expired without cond returning true. The helper keeps the
// many polling-loops across tests linter-friendly (QF1006) without
// hand-rolling each one.
func waitFor(total time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func waitForScan(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s scan did not finish within 10s", name)
	}
}
