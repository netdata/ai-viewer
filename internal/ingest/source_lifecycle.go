package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxLifecycleErrorBytes = 1024

// SourceLifecycleState is the durable source supervisor state stored on
// source_progress.lifecycle_state.
type SourceLifecycleState string

// Source lifecycle states persisted in source_progress.lifecycle_state.
const (
	SourceLifecycleUnknown         SourceLifecycleState = "unknown"
	SourceLifecycleStarting        SourceLifecycleState = "starting"
	SourceLifecycleStartFailed     SourceLifecycleState = "start_failed"
	SourceLifecycleConstructFailed SourceLifecycleState = "construct_failed"
	SourceLifecycleScanning        SourceLifecycleState = "scanning"
	SourceLifecycleScanFailed      SourceLifecycleState = "scan_failed"
	SourceLifecycleScanComplete    SourceLifecycleState = "scan_complete"
	SourceLifecycleTailStarting    SourceLifecycleState = "tail_starting"
	SourceLifecycleTailing         SourceLifecycleState = "tailing"
	SourceLifecycleTailStale       SourceLifecycleState = "tail_stale"
	SourceLifecycleTailFailed      SourceLifecycleState = "tail_failed"
	SourceLifecycleTailRestarting  SourceLifecycleState = "tail_restarting"
	SourceLifecycleStopped         SourceLifecycleState = "stopped"
)

// ReadModelState is the durable FTS/rollup repair state stored on
// source_progress.read_model_state.
type ReadModelState string

// Read-model states persisted in source_progress.read_model_state.
const (
	ReadModelUnknown       ReadModelState = "unknown"
	ReadModelRepairPending ReadModelState = "repair_pending"
	ReadModelRepairing     ReadModelState = "repairing"
	ReadModelReady         ReadModelState = "ready"
	ReadModelRepairTimeout ReadModelState = "repair_timeout"
	ReadModelRepairFailed  ReadModelState = "repair_failed"
)

// SourceLifecycleUpdate is the additive lifecycle/read-model state delta.
// Nil timestamp pointers mean "leave the existing timestamp unchanged".
type SourceLifecycleUpdate struct {
	State                        SourceLifecycleState
	ExpectedLifecycleState       *SourceLifecycleState
	ReadModelState               ReadModelState
	AtUS                         int64
	ScanStartedAtUS              *int64
	ScanCompletedAtUS            *int64
	TailStartedAtUS              *int64
	TailHeartbeatUS              *int64
	TailFailedAtUS               *int64
	TailRestartDelta             int64
	ResetTailRestartCount        bool
	RepairStartedAtUS            *int64
	RepairCompletedAtUS          *int64
	RepairFailedAtUS             *int64
	RepairAttemptsDelta          int64
	ResetReadModelRepairAttempts bool
	Error                        string
	ClearLifecycleError          bool
	ReadModelError               string
	ClearReadModelError          bool
}

// SourceRegistration is one currently configured or discovered source at
// ingester startup.
type SourceRegistration struct {
	ID       string
	Format   string
	Location string
}

// RecordSourceLifecycle persists source lifecycle/read-model state and emits a
// source_status_changed notify row atomically with the state change.
func (i *Ingester) RecordSourceLifecycle(ctx context.Context, sourceID, format, location string, update SourceLifecycleUpdate) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if sourceID == "" {
		return errors.New("ingest: source lifecycle source id is empty")
	}
	at := update.AtUS
	if at == 0 {
		at = i.now()
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("source lifecycle begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := ensureSourceLifecycleRow(ctx, tx, sourceID, format, location, i.resolveFTS5IndexLogs(sourceID), i.resolveSourceMeta(sourceID)); err != nil {
		return err
	}
	if err := ensureSourceProgressLifecycleRow(ctx, tx, sourceID, at); err != nil {
		return err
	}

	changed := false
	if update.State != "" {
		stateChanged, err := updateLifecycleColumns(ctx, tx, sourceID, location, at, update)
		if err != nil {
			return err
		}
		changed = changed || stateChanged
	}
	if update.ReadModelState != "" {
		readModelChanged, err := updateReadModelColumns(ctx, tx, sourceID, location, at, update)
		if err != nil {
			return err
		}
		changed = changed || readModelChanged
	}
	if changed {
		if err := insertSourceStatusNotify(ctx, tx, sourceID, at); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("source lifecycle commit: %w", err)
	}
	return nil
}

// ReconcileSourceLifecycles repairs durable startup residue after discovery:
// configured sources restart from starting, unconfigured historical rows move to
// stopped, and sources.enabled is preserved as the operator/user-intent flag.
func (i *Ingester) ReconcileSourceLifecycles(ctx context.Context, configured []SourceRegistration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	configuredByID := make(map[string]SourceRegistration, len(configured))
	for _, src := range configured {
		if src.ID == "" {
			continue
		}
		configuredByID[src.ID] = src
	}

	rows, err := i.db.QueryContext(ctx, `
SELECT s.id, s.format, s.location, IFNULL(sp.lifecycle_state, 'unknown'), IFNULL(sp.read_model_state, 'unknown')
FROM sources s
LEFT JOIN source_progress sp ON sp.source_id = s.id
ORDER BY s.created_at, s.id
`)
	if err != nil {
		return fmt.Errorf("source lifecycle reconcile query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type existingSource struct {
		id             string
		format         string
		location       string
		state          SourceLifecycleState
		readModelState ReadModelState
	}
	var existing []existingSource
	seen := make(map[string]struct{})
	for rows.Next() {
		var src existingSource
		if err := rows.Scan(&src.id, &src.format, &src.location, &src.state, &src.readModelState); err != nil {
			return fmt.Errorf("source lifecycle reconcile scan: %w", err)
		}
		existing = append(existing, src)
		seen[src.id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("source lifecycle reconcile iterate: %w", err)
	}

	for _, src := range existing {
		if reg, ok := configuredByID[src.id]; ok {
			needsLifecycle := src.state != SourceLifecycleStarting
			needsReadModel := src.readModelState == ReadModelRepairing
			if !needsLifecycle && !needsReadModel {
				continue
			}
			update := SourceLifecycleUpdate{}
			if needsLifecycle {
				update.State = SourceLifecycleStarting
				update.ClearLifecycleError = true
			}
			if needsReadModel {
				update.ReadModelState = ReadModelRepairPending
				update.ClearReadModelError = true
			}
			if err := i.RecordSourceLifecycle(ctx, src.id, reg.Format, reg.Location, update); err != nil {
				return err
			}
			continue
		}
		if src.state == SourceLifecycleStopped {
			continue
		}
		if err := i.RecordSourceLifecycle(ctx, src.id, src.format, src.location, SourceLifecycleUpdate{
			State: SourceLifecycleStopped,
			Error: "source is no longer configured",
		}); err != nil {
			return err
		}
	}
	for _, reg := range configured {
		if _, ok := seen[reg.ID]; ok || reg.ID == "" {
			continue
		}
		if err := i.RecordSourceLifecycle(ctx, reg.ID, reg.Format, reg.Location, SourceLifecycleUpdate{
			State: SourceLifecycleStarting,
		}); err != nil {
			return err
		}
	}
	return nil
}

func ensureSourceLifecycleRow(ctx context.Context, tx *sql.Tx, sourceID, format, location string, fts5IndexLogs bool, metaJSON string) error {
	if err := ensureSourceRow(ctx, tx, sourceID, format, location, fts5IndexLogs, metaJSON); err != nil {
		return fmt.Errorf("source lifecycle ensure source row: %w", err)
	}
	return nil
}

func ensureSourceProgressLifecycleRow(ctx context.Context, tx *sql.Tx, sourceID string, tsUS int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO source_progress (source_id, updated_at)
VALUES (?, ?)
ON CONFLICT (source_id) DO NOTHING
`, sourceID, tsUS); err != nil {
		return fmt.Errorf("source lifecycle ensure progress row: %w", err)
	}
	return nil
}

func updateLifecycleColumns(ctx context.Context, tx *sql.Tx, sourceID, location string, tsUS int64, update SourceLifecycleUpdate) (bool, error) {
	query := `
UPDATE source_progress
SET lifecycle_state = ?,
    lifecycle_state_at = ?,
    scan_started_at = COALESCE(?, scan_started_at),
    scan_completed_at = COALESCE(?, scan_completed_at),
    tail_started_at = COALESCE(?, tail_started_at),
    tail_heartbeat_at = COALESCE(?, tail_heartbeat_at),
    tail_failed_at = COALESCE(?, tail_failed_at),
    tail_restart_count = CASE WHEN ? THEN 0 ELSE tail_restart_count + ? END,
    lifecycle_error = CASE WHEN ? THEN NULL ELSE COALESCE(?, lifecycle_error) END
WHERE source_id = ?`
	args := []any{
		string(update.State),
		tsUS,
		nullInt64(update.ScanStartedAtUS),
		nullInt64(update.ScanCompletedAtUS),
		nullInt64(update.TailStartedAtUS),
		nullInt64(update.TailHeartbeatUS),
		nullInt64(update.TailFailedAtUS),
		update.ResetTailRestartCount,
		update.TailRestartDelta,
		update.ClearLifecycleError,
		nullString(sanitizeLifecycleError(update.Error, location)),
		sourceID,
	}
	if update.ExpectedLifecycleState != nil {
		query += ` AND lifecycle_state = ?`
		args = append(args, string(*update.ExpectedLifecycleState))
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("source lifecycle update: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("source lifecycle rows affected: %w", err)
	}
	return changed > 0, nil
}

func updateReadModelColumns(ctx context.Context, tx *sql.Tx, sourceID, location string, tsUS int64, update SourceLifecycleUpdate) (bool, error) {
	res, err := tx.ExecContext(ctx, `
UPDATE source_progress
SET read_model_state = ?,
    read_model_state_at = ?,
    read_model_repair_started_at = COALESCE(?, read_model_repair_started_at),
    read_model_repair_completed_at = COALESCE(?, read_model_repair_completed_at),
    read_model_repair_failed_at = COALESCE(?, read_model_repair_failed_at),
    read_model_repair_attempts = CASE WHEN ? THEN 0 ELSE read_model_repair_attempts + ? END,
    read_model_error = CASE WHEN ? THEN NULL ELSE COALESCE(?, read_model_error) END
WHERE source_id = ?
`,
		string(update.ReadModelState),
		tsUS,
		nullInt64(update.RepairStartedAtUS),
		nullInt64(update.RepairCompletedAtUS),
		nullInt64(update.RepairFailedAtUS),
		update.ResetReadModelRepairAttempts,
		update.RepairAttemptsDelta,
		update.ClearReadModelError,
		nullString(sanitizeLifecycleError(update.ReadModelError, location)),
		sourceID,
	)
	if err != nil {
		return false, fmt.Errorf("read model lifecycle update: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read model lifecycle rows affected: %w", err)
	}
	return changed > 0, nil
}

func insertSourceStatusNotify(ctx context.Context, tx *sql.Tx, sourceID string, tsUS int64) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO notify (ts_us, kind, source_id) VALUES (?, 'source_status_changed', ?)`,
		tsUS, sourceID); err != nil {
		return fmt.Errorf("source lifecycle notify: %w", err)
	}
	return nil
}

func nullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func sanitizeLifecycleError(v, sourceLocation string) string {
	out := strings.ToValidUTF8(v, "")
	replacements := []struct {
		from string
		to   string
	}{
		{from: sourceLocation, to: "[source]"},
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		replacements = append(replacements, struct {
			from string
			to   string
		}{from: home, to: "$HOME"})
	}
	for _, repl := range replacements {
		if repl.from == "" {
			continue
		}
		out = strings.ReplaceAll(out, repl.from, repl.to)
	}

	var b strings.Builder
	b.Grow(len(out))
	lastSpace := false
	for _, r := range out {
		if unicode.IsControl(r) {
			continue
		}
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	out = strings.TrimSpace(b.String())
	if len(out) <= maxLifecycleErrorBytes {
		return out
	}
	end := maxLifecycleErrorBytes
	for end > 0 && !utf8.ValidString(out[:end]) {
		end--
	}
	return strings.TrimSpace(out[:end])
}
