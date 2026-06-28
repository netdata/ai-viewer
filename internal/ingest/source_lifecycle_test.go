package ingest

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecordSourceLifecycle_InitialInsertSetsUpdatedAt(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := i.RecordSourceLifecycle(ctx, "src-initial", "codex", "/tmp/src-initial", SourceLifecycleUpdate{
		State: SourceLifecycleStarting,
		AtUS:  1100,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle initial: %v", err)
	}

	var got struct {
		sourceCount      int64
		updatedAt        int64
		lifecycleState   string
		lifecycleStateAt int64
	}
	if err := db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM sources WHERE id='src-initial'),
  updated_at,
  lifecycle_state,
  lifecycle_state_at
FROM source_progress
WHERE source_id='src-initial'
`).Scan(&got.sourceCount, &got.updatedAt, &got.lifecycleState, &got.lifecycleStateAt); err != nil {
		t.Fatalf("read initial lifecycle row: %v", err)
	}
	if got.sourceCount != 1 {
		t.Fatalf("sources row count = %d, want 1", got.sourceCount)
	}
	if got.updatedAt != 1100 {
		t.Fatalf("updated_at = %d, want initial AtUS 1100", got.updatedAt)
	}
	if got.lifecycleState != string(SourceLifecycleStarting) {
		t.Fatalf("lifecycle_state = %q, want %q", got.lifecycleState, SourceLifecycleStarting)
	}
	if got.lifecycleStateAt != 1100 {
		t.Fatalf("lifecycle_state_at = %d, want 1100", got.lifecycleStateAt)
	}

	if err := i.RecordSourceLifecycle(ctx, "src-initial", "codex", "/tmp/src-initial", SourceLifecycleUpdate{
		State: SourceLifecycleScanning,
		AtUS:  2200,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle follow-up: %v", err)
	}
	if got := scanInt(t, db, `SELECT updated_at FROM source_progress WHERE source_id='src-initial'`); got != 1100 {
		t.Fatalf("updated_at after lifecycle-only update = %d, want preserved 1100", got)
	}
}

func TestRecordSourceLifecycle_UpsertsStateAndPreservesCursor(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, "src-life", "codex", "/tmp/src-life"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := upsertSourceProgress(ctx, tx, "src-life", 42, 1200, `{"offset":42}`, true); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed source_progress: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	initialUpdatedAt := scanInt(t, db, `SELECT updated_at FROM source_progress WHERE source_id='src-life'`)

	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	update := SourceLifecycleUpdate{
		State:            SourceLifecycleTailing,
		AtUS:             2000,
		TailStartedAtUS:  ptrInt64Ingest(1900),
		TailHeartbeatUS:  ptrInt64Ingest(2000),
		TailRestartDelta: 1,
	}
	if err := i.RecordSourceLifecycle(ctx, "src-life", "codex", "/tmp/src-life", update); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}

	var got struct {
		lastSeq          int64
		cursor           string
		updatedAt        int64
		lifecycleState   string
		lifecycleStateAt int64
		tailStartedAt    sql.NullInt64
		tailHeartbeatAt  sql.NullInt64
		tailRestartCount int64
	}
	if err := db.QueryRowContext(ctx, `
SELECT last_seq, IFNULL(cursor, ''), updated_at, lifecycle_state, lifecycle_state_at,
       tail_started_at, tail_heartbeat_at, tail_restart_count
FROM source_progress
WHERE source_id = 'src-life'
`).Scan(
		&got.lastSeq,
		&got.cursor,
		&got.updatedAt,
		&got.lifecycleState,
		&got.lifecycleStateAt,
		&got.tailStartedAt,
		&got.tailHeartbeatAt,
		&got.tailRestartCount,
	); err != nil {
		t.Fatalf("read source_progress: %v", err)
	}
	if got.lastSeq != 42 {
		t.Fatalf("last_seq = %d, want preserved 42", got.lastSeq)
	}
	if got.cursor != `{"offset":42}` {
		t.Fatalf("cursor = %q, want preserved cursor", got.cursor)
	}
	if got.updatedAt != initialUpdatedAt {
		t.Fatalf("updated_at = %d, want preserved %d", got.updatedAt, initialUpdatedAt)
	}
	if got.lifecycleState != string(SourceLifecycleTailing) {
		t.Fatalf("lifecycle_state = %q, want %q", got.lifecycleState, SourceLifecycleTailing)
	}
	if got.lifecycleStateAt != 2000 {
		t.Fatalf("lifecycle_state_at = %d, want 2000", got.lifecycleStateAt)
	}
	if !got.tailStartedAt.Valid || got.tailStartedAt.Int64 != 1900 {
		t.Fatalf("tail_started_at = %+v, want 1900", got.tailStartedAt)
	}
	if !got.tailHeartbeatAt.Valid || got.tailHeartbeatAt.Int64 != 2000 {
		t.Fatalf("tail_heartbeat_at = %+v, want 2000", got.tailHeartbeatAt)
	}
	if got.tailRestartCount != 1 {
		t.Fatalf("tail_restart_count = %d, want 1", got.tailRestartCount)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='source_status_changed' AND source_id='src-life'`); got != 1 {
		t.Fatalf("source_status_changed notify rows = %d, want 1", got)
	}
}

func TestRecordSourceLifecycle_ResetsTailRestartCount(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, "src-restart-reset", "codex", "/tmp/src-restart-reset"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}

	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.RecordSourceLifecycle(ctx, "src-restart-reset", "codex", "/tmp/src-restart-reset", SourceLifecycleUpdate{
		State:            SourceLifecycleTailRestarting,
		AtUS:             1000,
		TailRestartDelta: 3,
		Error:            "tail restart failed",
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle increment: %v", err)
	}
	if got := scanInt(t, db, `SELECT tail_restart_count FROM source_progress WHERE source_id='src-restart-reset'`); got != 3 {
		t.Fatalf("tail_restart_count after increment = %d, want 3", got)
	}

	if err := i.RecordSourceLifecycle(ctx, "src-restart-reset", "codex", "/tmp/src-restart-reset", SourceLifecycleUpdate{
		State:                 SourceLifecycleTailing,
		AtUS:                  2000,
		TailStartedAtUS:       ptrInt64Ingest(2000),
		TailHeartbeatUS:       ptrInt64Ingest(2000),
		ResetTailRestartCount: true,
		ClearLifecycleError:   true,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle reset: %v", err)
	}
	if got := scanInt(t, db, `SELECT tail_restart_count FROM source_progress WHERE source_id='src-restart-reset'`); got != 0 {
		t.Fatalf("tail_restart_count after successful tailing reset = %d, want 0", got)
	}
	if got := scanString(t, db, `SELECT IFNULL(lifecycle_error, '') FROM source_progress WHERE source_id='src-restart-reset'`); got != "" {
		t.Fatalf("lifecycle_error after successful tailing reset = %q, want empty", got)
	}
}

func TestRecordSourceLifecycle_SanitizesErrorAndDoesNotDisableSource(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	sourceLocation := "/tmp/aiviewer-source-secret"
	if err := ensureSourceRowDirect(ctx, db, "src-error", "codex", sourceLocation); err != nil {
		t.Fatalf("ensure source: %v", err)
	}

	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	home, _ := os.UserHomeDir()
	errText := "tail\x00 failed\nopen " + sourceLocation + "/session.jsonl"
	if home != "" {
		errText += " and " + home + "/.codex/sessions/private.jsonl"
	}
	errText += " " + strings.Repeat("payload-\u20ac", 200)
	if err := i.RecordSourceLifecycle(ctx, "src-error", "codex", sourceLocation, SourceLifecycleUpdate{
		State:          SourceLifecycleTailFailed,
		AtUS:           3000,
		TailFailedAtUS: ptrInt64Ingest(3000),
		Error:          errText,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}

	gotErr := scanString(t, db, `SELECT lifecycle_error FROM source_progress WHERE source_id='src-error'`)
	if gotErr == "" {
		t.Fatal("lifecycle_error is empty, want sanitized error text")
	}
	if len(gotErr) > 1024 {
		t.Fatalf("lifecycle_error length = %d, want <= 1024", len(gotErr))
	}
	if !utf8.ValidString(gotErr) {
		t.Fatalf("lifecycle_error is not valid UTF-8: %q", gotErr)
	}
	if strings.ContainsAny(gotErr, "\x00\n") {
		t.Fatalf("lifecycle_error contains control characters: %q", gotErr)
	}
	if strings.Contains(gotErr, sourceLocation) {
		t.Fatalf("lifecycle_error contains configured source location: %q", gotErr)
	}
	if home != "" && strings.Contains(gotErr, home) {
		t.Fatalf("lifecycle_error contains home prefix: %q", gotErr)
	}
	if got := scanInt(t, db, `SELECT enabled FROM sources WHERE id='src-error'`); got != 1 {
		t.Fatalf("sources.enabled = %d, want 1 (lifecycle stopped/failed must not disable source)", got)
	}
}

func TestRecordSourceLifecycle_GuardedTransitionNoopsOnLostRace(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, "src-guard", "codex", "/tmp/src-guard"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.RecordSourceLifecycle(ctx, "src-guard", "codex", "/tmp/src-guard", SourceLifecycleUpdate{
		State: SourceLifecycleTailFailed,
		AtUS:  1000,
		Error: "tail failed",
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle failed state: %v", err)
	}

	expected := SourceLifecycleTailing
	if err := i.RecordSourceLifecycle(ctx, "src-guard", "codex", "/tmp/src-guard", SourceLifecycleUpdate{
		State:                  SourceLifecycleTailStale,
		ExpectedLifecycleState: &expected,
		AtUS:                   2000,
		Error:                  "late watchdog",
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle guarded late state: %v", err)
	}

	state, lifecycleErr := readSourceLifecycleRow(t, db, "src-guard")
	if state != string(SourceLifecycleTailFailed) {
		t.Fatalf("lifecycle_state = %q, want preserved %q", state, SourceLifecycleTailFailed)
	}
	if lifecycleErr != "tail failed" {
		t.Fatalf("lifecycle_error = %q, want original tail failure", lifecycleErr)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='source_status_changed' AND source_id='src-guard'`); got != 1 {
		t.Fatalf("source_status_changed rows = %d, want only initial transition", got)
	}
}

func TestRecordReadModelState_TracksRepairAttemptsAndPreservesLifecycle(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, "src-read", "codex", "/tmp/src-read"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}

	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.RecordSourceLifecycle(ctx, "src-read", "codex", "/tmp/src-read", SourceLifecycleUpdate{
		State: SourceLifecycleScanning,
		AtUS:  1000,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}
	if err := i.RecordSourceLifecycle(ctx, "src-read", "codex", "/tmp/src-read", SourceLifecycleUpdate{
		ReadModelState:      ReadModelRepairing,
		AtUS:                2000,
		RepairStartedAtUS:   ptrInt64Ingest(2000),
		RepairAttemptsDelta: 1,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle repairing: %v", err)
	}
	if err := i.RecordSourceLifecycle(ctx, "src-read", "codex", "/tmp/src-read", SourceLifecycleUpdate{
		ReadModelState:               ReadModelReady,
		AtUS:                         3000,
		RepairCompletedAtUS:          ptrInt64Ingest(3000),
		ResetReadModelRepairAttempts: true,
		ClearReadModelError:          true,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle ready: %v", err)
	}

	var got struct {
		lifecycleState             string
		readModelState             string
		readModelStateAt           int64
		readModelRepairStartedAt   sql.NullInt64
		readModelRepairCompletedAt sql.NullInt64
		readModelRepairAttempts    int64
	}
	if err := db.QueryRowContext(ctx, `
SELECT lifecycle_state, read_model_state, read_model_state_at,
       read_model_repair_started_at, read_model_repair_completed_at,
       read_model_repair_attempts
FROM source_progress
WHERE source_id = 'src-read'
`).Scan(
		&got.lifecycleState,
		&got.readModelState,
		&got.readModelStateAt,
		&got.readModelRepairStartedAt,
		&got.readModelRepairCompletedAt,
		&got.readModelRepairAttempts,
	); err != nil {
		t.Fatalf("read source_progress: %v", err)
	}
	if got.lifecycleState != string(SourceLifecycleScanning) {
		t.Fatalf("lifecycle_state = %q, want preserved scanning", got.lifecycleState)
	}
	if got.readModelState != string(ReadModelReady) {
		t.Fatalf("read_model_state = %q, want ready", got.readModelState)
	}
	if got.readModelStateAt != 3000 {
		t.Fatalf("read_model_state_at = %d, want 3000", got.readModelStateAt)
	}
	if !got.readModelRepairStartedAt.Valid || got.readModelRepairStartedAt.Int64 != 2000 {
		t.Fatalf("read_model_repair_started_at = %+v, want 2000", got.readModelRepairStartedAt)
	}
	if !got.readModelRepairCompletedAt.Valid || got.readModelRepairCompletedAt.Int64 != 3000 {
		t.Fatalf("read_model_repair_completed_at = %+v, want 3000", got.readModelRepairCompletedAt)
	}
	if got.readModelRepairAttempts != 0 {
		t.Fatalf("read_model_repair_attempts = %d, want reset to 0", got.readModelRepairAttempts)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='source_status_changed' AND source_id='src-read'`); got != 3 {
		t.Fatalf("source_status_changed notify rows = %d, want 3", got)
	}
}

func TestRecordSourceLifecycle_SanitizesReadModelError(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	if err := ensureSourceRowDirect(ctx, db, "src-read-error", "codex", "/tmp/src-read-error"); err != nil {
		t.Fatalf("ensure source: %v", err)
	}

	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	errText := strings.Repeat("read model repair failed with local payload\n", 20)
	if err := i.RecordSourceLifecycle(ctx, "src-read-error", "codex", "/tmp/src-read-error", SourceLifecycleUpdate{
		ReadModelState: ReadModelRepairFailed,
		AtUS:           4000,
		ReadModelError: errText,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}

	gotErr := scanString(t, db, `SELECT read_model_error FROM source_progress WHERE source_id='src-read-error'`)
	if gotErr == "" {
		t.Fatal("read_model_error is empty, want sanitized error text")
	}
	if len(gotErr) > 1024 {
		t.Fatalf("read_model_error length = %d, want <= 1024", len(gotErr))
	}
	if strings.Contains(gotErr, "\n") {
		t.Fatalf("read_model_error contains newline: %q", gotErr)
	}
}

func TestReconcileSourceLifecycles_StartupTransitions(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	rows := []struct {
		id, format, location, lifecycle string
	}{
		{"src-unknown", "codex", "/tmp/unknown", "unknown"},
		{"src-crash", "codex", "/tmp/crash", "tailing"},
		{"src-repairing", "codex", "/tmp/repairing", "tailing"},
		{"src-unconfigured", "codex", "/tmp/unconfigured", "tail_restarting"},
		{"src-reconfigured", "codex", "/tmp/reconfigured", "stopped"},
	}
	for _, row := range rows {
		if err := ensureSourceRowDirect(ctx, db, row.id, row.format, row.location); err != nil {
			t.Fatalf("ensure source %s: %v", row.id, err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO source_progress (source_id, updated_at, lifecycle_state, lifecycle_state_at)
VALUES (?, ?, ?, ?)`,
			row.id, 1000, row.lifecycle, 1000); err != nil {
			t.Fatalf("seed source_progress %s: %v", row.id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
UPDATE source_progress
SET read_model_state='repairing', read_model_state_at=1000
WHERE source_id='src-repairing'`); err != nil {
		t.Fatalf("seed repairing read-model state: %v", err)
	}

	i, err := New(db, WithLogger(silentLogger()), WithNow(func() int64 { return 5000 }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	configured := []SourceRegistration{
		{ID: "src-unknown", Format: "codex", Location: "/tmp/unknown"},
		{ID: "src-crash", Format: "codex", Location: "/tmp/crash"},
		{ID: "src-repairing", Format: "codex", Location: "/tmp/repairing"},
		{ID: "src-reconfigured", Format: "codex", Location: "/tmp/reconfigured"},
	}
	if err := i.ReconcileSourceLifecycles(ctx, configured); err != nil {
		t.Fatalf("ReconcileSourceLifecycles: %v", err)
	}

	wantStates := map[string]string{
		"src-unknown":      string(SourceLifecycleStarting),
		"src-crash":        string(SourceLifecycleStarting),
		"src-repairing":    string(SourceLifecycleStarting),
		"src-unconfigured": string(SourceLifecycleStopped),
		"src-reconfigured": string(SourceLifecycleStarting),
	}
	for sourceID, want := range wantStates {
		got := scanString(t, db, `SELECT lifecycle_state FROM source_progress WHERE source_id=?`, sourceID)
		if got != want {
			t.Fatalf("%s lifecycle_state = %q, want %q", sourceID, got, want)
		}
		gotAt := scanInt(t, db, `SELECT lifecycle_state_at FROM source_progress WHERE source_id=?`, sourceID)
		if gotAt != 5000 {
			t.Fatalf("%s lifecycle_state_at = %d, want 5000", sourceID, gotAt)
		}
	}
	if got := scanInt(t, db, `SELECT enabled FROM sources WHERE id='src-unconfigured'`); got != 1 {
		t.Fatalf("unconfigured sources.enabled = %d, want preserved 1", got)
	}
	if got := scanString(t, db, `SELECT read_model_state FROM source_progress WHERE source_id='src-repairing'`); got != string(ReadModelRepairPending) {
		t.Fatalf("src-repairing read_model_state = %q, want %q", got, ReadModelRepairPending)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='source_status_changed'`); got != 5 {
		t.Fatalf("source_status_changed notify rows = %d, want 5", got)
	}
}

func ptrInt64Ingest(v int64) *int64 {
	return &v
}

func readSourceLifecycleRow(t *testing.T, db *sql.DB, sourceID string) (string, string) {
	t.Helper()
	var state string
	var lifecycleErr sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT lifecycle_state, lifecycle_error FROM source_progress WHERE source_id=?`,
		sourceID,
	).Scan(&state, &lifecycleErr); err != nil {
		t.Fatalf("read source lifecycle row: %v", err)
	}
	return state, lifecycleErr.String
}
