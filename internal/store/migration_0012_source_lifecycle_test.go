package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// Migration 0012 (SOW-0114) adds durable per-source lifecycle and read-model
// repair state to source_progress. The ingester and presenter use these fields
// as the source lifecycle truth; sources.last_seen_at remains a legacy
// diagnostic and is not a freshness signal.

func TestMigration0012_ChainHeadSchemaVersion(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "12" {
		t.Fatalf("schema_meta.version: want %q, got %q (full chain head is 0012)", "12", version)
	}
}

func TestMigration0012_SourceProgressDefaults(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('src-life', 'codex', '/tmp/src-life', 1000)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO source_progress (source_id, updated_at) VALUES ('src-life', 1100)`); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	var got struct {
		lifecycleState         string
		lifecycleStateAt       int64
		tailRestartCount       int64
		readModelState         string
		readModelStateAt       int64
		readModelRepairAttempt int64
	}
	if err := db.QueryRowContext(ctx, `
SELECT lifecycle_state, lifecycle_state_at, tail_restart_count,
       read_model_state, read_model_state_at, read_model_repair_attempts
FROM source_progress
WHERE source_id = 'src-life'
`).Scan(
		&got.lifecycleState,
		&got.lifecycleStateAt,
		&got.tailRestartCount,
		&got.readModelState,
		&got.readModelStateAt,
		&got.readModelRepairAttempt,
	); err != nil {
		t.Fatalf("read source_progress defaults: %v", err)
	}
	if got.lifecycleState != "unknown" {
		t.Fatalf("lifecycle_state default = %q, want unknown", got.lifecycleState)
	}
	if got.lifecycleStateAt != 0 {
		t.Fatalf("lifecycle_state_at default = %d, want 0", got.lifecycleStateAt)
	}
	if got.tailRestartCount != 0 {
		t.Fatalf("tail_restart_count default = %d, want 0", got.tailRestartCount)
	}
	if got.readModelState != "unknown" {
		t.Fatalf("read_model_state default = %q, want unknown", got.readModelState)
	}
	if got.readModelStateAt != 0 {
		t.Fatalf("read_model_state_at default = %d, want 0", got.readModelStateAt)
	}
	if got.readModelRepairAttempt != 0 {
		t.Fatalf("read_model_repair_attempts default = %d, want 0", got.readModelRepairAttempt)
	}
}

func TestMigration0012_SourceProgressStateChecks(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('src-check', 'codex', '/tmp/src-check', 1000)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	tests := []struct {
		name  string
		query string
	}{
		{
			name: "rejects unknown lifecycle state value",
			query: `INSERT INTO source_progress
			        (source_id, updated_at, lifecycle_state)
			        VALUES ('src-check', 1000, 'almost_tailing')`,
		},
		{
			name: "rejects unknown read-model state value",
			query: `INSERT INTO source_progress
			        (source_id, updated_at, read_model_state)
			        VALUES ('src-check', 1000, 'sort_of_ready')`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := db.ExecContext(ctx, tc.query)
			if err == nil {
				t.Fatal("insert unexpectedly succeeded")
			}
			if !isCheckConstraintError(err) {
				t.Fatalf("error = %v, want CHECK constraint failure", err)
			}
		})
	}
}

func TestMigration0012_SourceProgressAcceptsEveryDocumentedState(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	lifecycleStates := []string{
		"unknown",
		"starting",
		"start_failed",
		"construct_failed",
		"scanning",
		"scan_failed",
		"scan_complete",
		"tail_starting",
		"tailing",
		"tail_stale",
		"tail_failed",
		"tail_restarting",
		"stopped",
	}
	for i, state := range lifecycleStates {
		sourceID := "src-life-" + state
		if _, err := db.ExecContext(ctx,
			`INSERT INTO sources (id, format, location, created_at) VALUES (?, 'codex', ?, ?)`,
			sourceID, "/tmp/"+sourceID, i+1); err != nil {
			t.Fatalf("seed source %s: %v", state, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO source_progress
			 (source_id, updated_at, lifecycle_state, lifecycle_state_at)
			 VALUES (?, ?, ?, ?)`,
			sourceID, 1000+i, state, 2000+i); err != nil {
			t.Fatalf("insert lifecycle_state %q: %v", state, err)
		}
	}

	readModelStates := []string{
		"unknown",
		"repair_pending",
		"repairing",
		"ready",
		"repair_timeout",
		"repair_failed",
	}
	for i, state := range readModelStates {
		sourceID := "src-read-model-" + state
		if _, err := db.ExecContext(ctx,
			`INSERT INTO sources (id, format, location, created_at) VALUES (?, 'codex', ?, ?)`,
			sourceID, "/tmp/"+sourceID, i+100); err != nil {
			t.Fatalf("seed source %s: %v", state, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO source_progress
			 (source_id, updated_at, read_model_state, read_model_state_at)
			 VALUES (?, ?, ?, ?)`,
			sourceID, 3000+i, state, 4000+i); err != nil {
			t.Fatalf("insert read_model_state %q: %v", state, err)
		}
	}
}

func isCheckConstraintError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "constraint")
}
