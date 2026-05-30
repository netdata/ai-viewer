package opencode

import (
	"database/sql"
	"math"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the SOW-0005 ROUND-4 external-review fixes:
//   - P2-1: the DELTA-ROW scanners surface a corrupt OPTIONAL numeric cell via a
//     WARN and degrade to 0 (parity with the non-delta loadSession path), while a
//     corrupt REQUIRED watermark cell (id/time_updated) ERRORS the page so the
//     cursor never advances to a poisoned watermark.
//   - P2-2: a part/op/tool emitter fed a crafted huge timestamp clamps to
//     math.MaxInt64 AND surfaces a WARN (no silent saturation on an emitted Ts).
//
// (P1 lives in review_round3_test.go; the file-part LogEntry P2-3 in mapper_test.go
// / mapper_branch_test.go; the DSN P3-2 in conn_dsn_test.go; the ProbeStatus P3-1
// in cmd/ai-viewer-ingest/sources_test.go.)

// --- P2-1: corrupt delta-row cells warn (optional) / error (required) ---------

// insertSessionCorruptCol inserts a session row whose ONE named numeric column
// carries a non-numeric TEXT literal (a corrupt cell), exercising the delta
// scanner's parse-failure path. SQLite's flexible typing stores an unconvertible
// string verbatim even in an INTEGER/REAL-affinity column, so parseInt64Checked /
// parseFloat64Checked fail on it — the same shape a corrupt opencode DB would have.
// The base required columns are always set to valid values; `col` is set to the
// corrupt text. col must NOT be one of the base columns below (so it is never
// duplicated). For corrupting a base required column (time_updated), the base value
// for that column is overridden via the corrupt argument list instead.
func insertSessionCorruptCol(t *testing.T, path, id, col, badText string) {
	t.Helper()
	rw, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() { _ = rw.Close() }()
	// Base columns whose VALUES we control per-column so `col` is never duplicated.
	cols := []string{"id", "project_id", "slug", "directory", "title", "version", "time_created", "time_updated"}
	vals := []any{id, "prj", "slug", "/d", "T", "9", int64(100), int64(100)}
	// If the corrupt column is one of the base columns, overwrite its value in place;
	// otherwise append it.
	replaced := false
	for i, c := range cols {
		if c == col {
			vals[i] = badText
			replaced = true
			break
		}
	}
	if !replaced {
		cols = append(cols, col)
		vals = append(vals, badText)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	stmt := `INSERT INTO session (` + strings.Join(cols, ", ") + `) VALUES (` + placeholders + `)`
	if _, err := rw.Exec(stmt, vals...); err != nil {
		t.Fatalf("insert corrupt %s: %v", col, err)
	}
}

// TestP2_1_CorruptOptionalCellWarnsDegradesToZero pins P2-1's optional-column path:
// a delta row whose OPTIONAL numeric cell (cost) is corrupt surfaces a WARN naming
// the table/column AND still resolves the row's session (degrades to 0, does not
// abort the page) — parity with the non-delta loadSession path.
func TestP2_1_CorruptOptionalCellWarnsDegradesToZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	insertSessionCorruptCol(t, path, "ses_cost", "cost", "not-a-number")
	db, schema := introspect(t, path)

	var warns []error
	affected := newAffectedSet()
	// The WARN is buffered in sink during the page tx and flushed via the
	// scanTableDelta onError AFTER the tx closes (round-5 P2-1).
	sink := &warnSink{}
	onRow := deltaRowHandler(ctxBG(), db, "session", schema["session"], affected, map[string]string{}, sink.collect)
	if _, err := scanTableDelta(ctxBG(), db, schema["session"], TableWatermark{}, onRow, sink, func(e error) { warns = append(warns, e) }); err != nil {
		t.Fatalf("scanTableDelta: corrupt OPTIONAL cell must NOT abort the page, got %v", err)
	}
	// Session still derived (row processed, cost degraded to 0).
	if ids := affected.ids(); len(ids) != 1 || ids[0] != "ses_cost" {
		t.Fatalf("affected = %v, want [ses_cost] (corrupt optional cell degrades, not skips)", affected.ids())
	}
	// Exactly the corrupt-cost WARN surfaced.
	foundCost := false
	for _, w := range warns {
		if strings.Contains(w.Error(), "corrupt numeric cell") && strings.Contains(w.Error(), "column=cost") {
			foundCost = true
		}
	}
	if !foundCost {
		t.Errorf("corrupt cost cell did not WARN; got %v", warns)
	}
}

// TestP2_1_CorruptRequiredCellErrorsNoCursorAdvance pins P2-1's required-column
// path: a delta row whose REQUIRED watermark cell (time_updated) is corrupt ERRORS
// the page rather than coercing to 0 — so the cursor cannot advance to a poisoned
// (0) watermark. The error is surfaced; the watermark stays at the input.
func TestP2_1_CorruptRequiredCellErrorsNoCursorAdvance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	// A session row whose time_updated is a non-numeric text literal (corrupt).
	insertSessionCorruptCol(t, path, "ses_bad_tuid", "time_updated", "garbage")
	db, schema := introspect(t, path)

	affected := newAffectedSet()
	sink := &warnSink{}
	onRow := deltaRowHandler(ctxBG(), db, "session", schema["session"], affected, map[string]string{}, sink.collect)
	from := TableWatermark{MaxTimeUpdatedMs: 50, MaxTimeUpdatedID: "aaa", MaxIDSeen: "aaa"}
	delta, err := scanTableDelta(ctxBG(), db, schema["session"], from, onRow, sink, func(error) {})
	if err == nil {
		t.Fatal("corrupt REQUIRED time_updated cell must ERROR the page (no poisoned-0 watermark)")
	}
	if !strings.Contains(err.Error(), "poisoned watermark") {
		t.Errorf("error = %v, want a poisoned-watermark refusal", err)
	}
	// The watermark must NOT have advanced to a 0/garbage value — it stays at the
	// input position (the page aborted before recording any row).
	if delta.watermark.MaxTimeUpdatedMs != from.MaxTimeUpdatedMs || delta.watermark.MaxTimeUpdatedID != from.MaxTimeUpdatedID {
		t.Errorf("watermark advanced past the corrupt row: got (%d,%q), want input (%d,%q)",
			delta.watermark.MaxTimeUpdatedMs, delta.watermark.MaxTimeUpdatedID, from.MaxTimeUpdatedMs, from.MaxTimeUpdatedID)
	}
}

// TestP2_1_RequiredAccessorsGuard pins the required-watermark accessors directly:
// i64Required/strRequired (the guard the delta AND boundary scanners share via
// requiredWatermark) return an error on a NULL/absent/corrupt required cell rather
// than a coerced 0/"" — the single chokepoint that prevents a poisoned watermark
// on EITHER scan path.
func TestP2_1_RequiredAccessorsGuard(t *testing.T) {
	t.Parallel()
	idx := columnIndex{"id": 0, "time_updated": 1}

	// Corrupt (non-numeric) required time_updated → error.
	dBad := &scanDest{holders: []sql.NullString{{String: "x1", Valid: true}, {String: "not-num", Valid: true}}, table: "session"}
	if _, err := dBad.i64Required(idx, "time_updated"); err == nil {
		t.Error("i64Required on a corrupt cell returned nil error (must refuse the poisoned watermark)")
	}
	// NULL required time_updated → error (never silently 0).
	dNull := &scanDest{holders: []sql.NullString{{String: "x1", Valid: true}, {Valid: false}}, table: "session"}
	if _, err := dNull.i64Required(idx, "time_updated"); err == nil {
		t.Error("i64Required on a NULL required cell returned nil error")
	}
	// Empty required id → error.
	dEmptyID := &scanDest{holders: []sql.NullString{{String: "", Valid: true}, {String: "100", Valid: true}}, table: "session"}
	if _, err := dEmptyID.strRequired(idx, "id"); err == nil {
		t.Error("strRequired on an empty id returned nil error")
	}
	// Valid cells → no error.
	dOK := &scanDest{holders: []sql.NullString{{String: "ses_1", Valid: true}, {String: "12345", Valid: true}}, table: "session"}
	id, err := dOK.strRequired(idx, "id")
	if err != nil || id != "ses_1" {
		t.Errorf("strRequired(valid) = (%q,%v), want (ses_1,nil)", id, err)
	}
	v, err := dOK.i64Required(idx, "time_updated")
	if err != nil || v != 12345 {
		t.Errorf("i64Required(valid) = (%d,%v), want (12345,nil)", v, err)
	}
}

// --- P2-2: emitter timestamps clamp AND warn ----------------------------------

// hugeMs is an opencode-ms value whose *1000 overflows int64, forcing the ms→µs
// conversion to saturate. msToMicrosWarn must surface a WARN at every EMITTED
// event's Ts (round-4 P2-2), not just session start/finalize.
const hugeMs = math.MaxInt64/1000 + 1

// TestP2_2_StepStartTimestampClampsAndWarns pins that a step-start (LLM op) part
// whose time_created is the crafted hugeMs clamps the emitted OpStarted Ts to
// MaxInt64 AND surfaces a WARN through the mapper's warn channel.
func TestP2_2_StepStartTimestampClampsAndWarns(t *testing.T) {
	s := rootSession("ses_x", 0)
	var warns []error
	step := stepStart("prt_1")
	step.TimeCreatedMs = hugeMs
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""), step),
	}
	evs, err := mapSession(testSourceID, s, msgs, WithOnWarn(func(e error) { warns = append(warns, e) }))
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	var llmStartTs int64 = -1
	for _, ev := range evs {
		if op, ok := ev.(canonical.OpStartedEvent); ok && op.Kind == canonical.OpLLM {
			llmStartTs = op.Ts
		}
	}
	if llmStartTs != math.MaxInt64 {
		t.Fatalf("LLM OpStarted Ts = %d, want clamp to MaxInt64", llmStartTs)
	}
	if !anyWarnContains(warns, "overflow") || !anyWarnContains(warns, "step-start") {
		t.Errorf("step-start huge timestamp did not WARN with overflow+field context; got %v", warns)
	}
}

// TestP2_2_ToolEndTimestampClampsAndWarns pins the same no-silent-clamp contract on
// a TOOL op's end timestamp (state.time.end), which closeLLMOp/emitToolOp convert
// via the warning-capable helper. The emitted OpFinalized EndTs clamps AND warns.
func TestP2_2_ToolEndTimestampClampsAndWarns(t *testing.T) {
	s := rootSession("ses_x", 0)
	var warns []error
	endMs := int64(hugeMs)
	tool := toolPart("prt_2", "read", "completed", 200, &endMs, nil)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"), tool),
	}
	evs, err := mapSession(testSourceID, s, msgs, WithOnWarn(func(e error) { warns = append(warns, e) }))
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	var toolEnd int64 = -1
	for _, ev := range evs {
		if op, ok := ev.(canonical.OpFinalizedEvent); ok {
			toolEnd = op.EndTs
		}
	}
	if toolEnd != math.MaxInt64 {
		t.Fatalf("tool OpFinalized EndTs = %d, want clamp to MaxInt64", toolEnd)
	}
	if !anyWarnContains(warns, "overflow") || !anyWarnContains(warns, "tool") {
		t.Errorf("tool huge end timestamp did not WARN with overflow+field context; got %v", warns)
	}
}

// anyWarnContains reports whether any error in warns contains substr.
func anyWarnContains(warns []error, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w.Error(), substr) {
			return true
		}
	}
	return false
}
