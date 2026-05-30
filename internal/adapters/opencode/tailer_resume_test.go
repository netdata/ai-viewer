package opencode

import (
	"database/sql"
	"sort"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the resume invariant (AC#6 durability): a scanLoop over the
// first half of a fixture, its cursor persisted+reparsed, then a scanLoop over
// the rest from that cursor, yields the SAME set of canonical content events as
// a single cold scanLoop over the whole fixture — zero duplicates, zero gaps
// (modulo SourceProgress checkpoints, which are not content).

// eventFingerprint renders a content event into a stable string for set
// comparison. SourceProgress is excluded by the caller (it is a checkpoint, not
// content). The fingerprint captures the kind + the identifying fields so a
// duplicate or a missing event changes the multiset.
func eventFingerprint(ev canonical.Event) string {
	switch e := ev.(type) {
	case canonical.SessionStartedEvent:
		return "session_started|" + e.NativeID + "|" + string(e.Kind)
	case canonical.SessionFinalizedEvent:
		return "session_finalized|" + e.NativeID + "|" + string(e.Status)
	case canonical.TurnStartedEvent:
		return fp("turn_started", e.SessionNativeID, e.Seq)
	case canonical.TurnFinalizedEvent:
		return fp("turn_finalized", e.SessionNativeID, e.Seq)
	case canonical.OpStartedEvent:
		return fp("op_started", e.SessionNativeID, e.TurnSeq) + "|" + string(e.Kind) + "|" + itoa(e.Seq)
	case canonical.OpFinalizedEvent:
		return fp("op_finalized", e.SessionNativeID, e.TurnSeq) + "|" + itoa(e.Seq)
	case canonical.PayloadRefEvent:
		return fp("payload_ref", e.SessionNativeID, e.TurnSeq) + "|" + itoa(e.OpSeq) + "|" + e.LocationURI
	case canonical.LogEntryEvent:
		return "log|" + e.SessionNativeID + "|" + e.Severity + "|" + e.Message
	default:
		return string(ev.EventKind())
	}
}

func fp(kind, sid string, seq int) string { return kind + "|" + sid + "|" + itoa(seq) }

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// contentFingerprints returns the sorted content-event fingerprints (excluding
// SourceProgress) of an event slice — the multiset to compare across runs.
func contentFingerprints(evs []canonical.Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.EventKind() == canonical.EvSourceProgress {
			continue
		}
		out = append(out, eventFingerprint(ev))
	}
	sort.Strings(out)
	return out
}

// TestScanLoop_ResumeZeroDupesZeroGaps is the resume proof. It builds a fixture,
// scans the WHOLE thing cold (baseline), then on a FRESH copy scans the first
// half (by inserting half, scanning, persisting the cursor), inserts the rest,
// reparses the cursor, and scans again — asserting the union of content events
// from the two-part run equals the cold-run baseline.
func TestScanLoop_ResumeZeroDupesZeroGaps(t *testing.T) {
	t.Parallel()

	// Baseline: one DB with all 6 sessions, scanned cold.
	baselinePath := seedBackfillDB(t, t.TempDir(), 6)
	baseOut := make(chan canonical.Event, 8192)
	var ce0 collectErrs
	if _, err := scanLoop(ctxBG(), baselinePath, "opencode:x", newCursor(), baseOut, silentLogger(), ce0.onError); err != nil {
		t.Fatalf("baseline scanLoop: %v", err)
	}
	baseline := contentFingerprints(drainAll(baseOut))

	// Two-part run: build a SEPARATE DB, seed half, scan, persist+reparse cursor,
	// seed the rest, scan from the reparsed cursor.
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	seedSessionsInto(t, rw, 1, 3) // sessions 1..3
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw (first half): %v", err)
	}

	out1 := make(chan canonical.Event, 8192)
	var ce1 collectErrs
	cur1, err := scanLoop(ctxBG(), path, "opencode:x", newCursor(), out1, silentLogger(), ce1.onError)
	if err != nil {
		t.Fatalf("first-half scanLoop: %v", err)
	}
	part1 := contentFingerprints(drainAll(out1))

	// Persist + reparse the cursor (the durable round-trip).
	stored := cur1.String()
	reparsed, err := ParseCursor(stored)
	if err != nil {
		t.Fatalf("ParseCursor(%q): %v", stored, err)
	}

	// Seed the rest (sessions 4..6) into the SAME DB.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw (second half): %v", err)
	}
	seedSessionsInto(t, rw2, 4, 6)
	if err := rw2.Close(); err != nil {
		t.Fatalf("close rw (second half): %v", err)
	}

	out2 := make(chan canonical.Event, 8192)
	var ce2 collectErrs
	if _, err := scanLoop(ctxBG(), path, "opencode:x", reparsed, out2, silentLogger(), ce2.onError); err != nil {
		t.Fatalf("second-half scanLoop: %v", err)
	}
	part2 := contentFingerprints(drainAll(out2))

	// Union of the two parts must equal the cold baseline: zero gaps (every
	// baseline event present) and zero dupes (no event present more than the
	// baseline multiplicity).
	union := append(append([]string{}, part1...), part2...)
	sort.Strings(union)

	if diff := multisetDiff(baseline, union); diff != "" {
		t.Fatalf("resume content events differ from cold baseline:\n%s", diff)
	}
}

// seedSessionsInto inserts sessions [lo, hi] (1-based, inclusive) into rw with
// the SAME per-session structure and ids seedBackfillDB uses (ses_<i>/msg_<i>,
// tokens 10*i/5*i, one step-start/step-finish/text triple), so the content
// fingerprints match the cold baseline regardless of absolute timestamps. Times
// are derived from the index so they are globally monotonic.
func seedSessionsInto(t *testing.T, rw *sql.DB, lo, hi int) {
	t.Helper()
	for i := lo; i <= hi; i++ {
		sid := fmtID("ses", i)
		mid := fmtID("msg", i)
		ts := int64(1000 + i*10)
		insertSession(t, rw, sid, "", ts, ts, 0)
		insertAssistantMessage(t, rw, mid, sid, ts+1, ts+1, int64(10*i), int64(5*i))
		insertPart(t, rw, fmtID("prt_ss", i), mid, sid, ts+2, ts+2, stepStartBody())
		insertPart(t, rw, fmtID("prt_sf", i), mid, sid, ts+3, ts+3, stepFinishBody(int64(10*i), int64(5*i), 0.01))
		insertPart(t, rw, fmtID("prt_tx", i), mid, sid, ts+4, ts+4, textBody("answer"))
	}
}

// multisetDiff returns "" when sorted multisets a and b are equal, else a
// human-readable description of the first difference.
func multisetDiff(a, b []string) string {
	if len(a) != len(b) {
		var sb strings.Builder
		sb.WriteString("length: baseline=")
		sb.WriteString(itoa(len(a)))
		sb.WriteString(" resume=")
		sb.WriteString(itoa(len(b)))
		sb.WriteString("\n")
		sb.WriteString(firstMismatch(a, b))
		return sb.String()
	}
	for i := range a {
		if a[i] != b[i] {
			return "at " + itoa(i) + ": baseline=" + a[i] + " resume=" + b[i]
		}
	}
	return ""
}

// firstMismatch reports the first element present in one slice but not at the
// same position in the other (both are sorted).
func firstMismatch(a, b []string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return "first diff @ " + itoa(i) + ": baseline=" + a[i] + " resume=" + b[i]
		}
	}
	if len(a) > n {
		return "baseline has extra: " + a[n]
	}
	if len(b) > n {
		return "resume has extra: " + b[n]
	}
	return ""
}
