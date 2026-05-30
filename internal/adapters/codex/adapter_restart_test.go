package codex

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the integration-level acceptance #6 tests for the codex
// adapter: they drive the PUBLIC Adapter API (New / Scan / ParseCursor /
// Cursor.String) and round-trip the cursor through its JSON serialization,
// proving a daemon restart resumes in place with zero duplicate and zero gap,
// and that a mid-stream truncation re-scans from offset 0 with a SourceError.
// The internal scanAll-level resume/truncation tests (scanner_test.go
// TestScan_ResumeNoDupNoGap, TestScan_TruncationRescans) are complementary; this
// file pins the same properties through the exact code path the ingester drives
// (cmd/ai-viewer-ingest/sources.go: ParseCursor → Scan → persist
// SourceProgress.Cursor → ParseCursor → Scan).

// twoTurnSession builds a deterministic two-turn rollout body and returns the
// per-turn line groups so a test can write turn 1, resume, then append turn 2.
// Each turn is the new (task_started/task_complete) format with a user input and
// an assistant message so it exercises the full op chain. The id is the session
// id (which must equal the filename UUID — the mapper derives nativeID from the
// filename).
func twoTurnSession(id string) (turn1, turn2 []string) {
	meta := metaLine(id, `"exec"`)
	turn1 = []string{
		meta,
		`{"timestamp":"2025-11-20T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5","sandbox_policy":{"type":"workspace-write"},"effort":"high","approval_policy":"never"}}`,
		`{"timestamp":"2025-11-20T16:59:10.100Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1763657950,"model_context_window":258400}}`,
		`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}`,
		`{"timestamp":"2025-11-20T16:59:13.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,
		`{"timestamp":"2025-11-20T16:59:13.500Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"last_token_usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"model_context_window":258400}}}`,
		`{"timestamp":"2025-11-20T16:59:14.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":1763657954,"duration_ms":4000}}`,
	}
	turn2 = []string{
		`{"timestamp":"2025-11-20T16:59:20.000Z","type":"turn_context","payload":{"turn_id":"t2","model":"gpt-5.5","sandbox_policy":{"type":"workspace-write"},"effort":"high","approval_policy":"never"}}`,
		`{"timestamp":"2025-11-20T16:59:20.100Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2","started_at":1763657960,"model_context_window":258400}}`,
		`{"timestamp":"2025-11-20T16:59:21.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second request"}]}}`,
		`{"timestamp":"2025-11-20T16:59:23.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}`,
		`{"timestamp":"2025-11-20T16:59:24.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","completed_at":1763657964,"duration_ms":4000}}`,
	}
	return turn1, turn2
}

// TestRestart_NoDupNoGap verifies acceptance #6: ingesting the first turn of a
// rollout through the PUBLIC adapter, persisting the cursor by serializing it
// through Cursor.String / ParseCursor (the ingester's exact round-trip), then
// resuming over the full file produces the same end state — no duplicate, no gap
// — as a single one-shot ingest.
//
// "Same end state" is compared on the canonical content the SQL layer keys on
// (event kind + session/turn/op identity + the load-bearing payload fields), NOT
// on SourceSeq — which is a byte-offset-derived observability counter that
// intentionally differs across a split vs one-shot pass (mirrors claude_code's
// content-key comparison; the ingester dedups on natural identity).
func TestRestart_NoDupNoGap(t *testing.T) {
	t.Parallel()

	id := uuid7(1)
	turn1, turn2 := twoTurnSession(id)
	full := append(append([]string{}, turn1...), turn2...)

	// One-shot reference run over the full file.
	oneShot := scanFullSession(t, id, full)

	// Split run: write turn 1, scan, persist cursor (JSON round-trip), append
	// turn 2, resume from the parsed cursor.
	root := t.TempDir()
	path := shardPath(root, id)
	writeFileBytes(t, path, []byte(strings.Join(turn1, "\n")+"\n"))
	setMtime(t, path, time.Minute) // fresh: turn 1 closed by task_complete, no stale finalize

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out1 := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	firstHalf := drainBuffered(out1)

	// Persist + reload the cursor exactly as the ingester does: take the last
	// SourceProgress cursor string and re-parse it through the public ParseCursor.
	cursorJSON := lastCursor(t, firstHalf)
	parsed, err := a.ParseCursor(cursorJSON)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}

	appendFileBytes(t, path, []byte(strings.Join(turn2, "\n")+"\n"))
	setMtime(t, path, time.Minute)

	out2 := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), parsed, out2); err != nil {
		t.Fatalf("Scan #2 (resume): %v", err)
	}
	secondHalf := drainBuffered(out2)

	combined := append(append([]canonical.Event{}, firstHalf...), secondHalf...)

	gotKeys := contentKeys(combined)
	wantKeys := contentKeys(oneShot)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("split produced %d content events, one-shot produced %d\nsplit:   %v\noneShot: %v",
			len(gotKeys), len(wantKeys), gotKeys, wantKeys)
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("content mismatch at %d:\n split:   %s\n oneShot: %s", i, gotKeys[i], wantKeys[i])
		}
	}

	// Belt-and-braces on the load-bearing invariant: SessionStarted exactly once
	// across the resume (no dup), both turns finalized exactly once (no gap).
	if got := countKind(combined, canonical.EvSessionStarted); got != 1 {
		t.Errorf("SessionStarted across resume = %d, want exactly 1 (no dup)", got)
	}
	if got := countKind(combined, canonical.EvTurnFinalized); got != 2 {
		t.Errorf("TurnFinalized across resume = %d, want 2 (no gap)", got)
	}
	if got := countKind(secondHalf, canonical.EvSessionStarted); got != 0 {
		t.Errorf("resume re-emitted SessionStarted %d times, want 0", got)
	}
}

// TestRestart_TruncationReScansWithSourceError verifies acceptance #6
// (truncation): after a clean ingest persists a cursor recording size N, an
// operator delete+recreate that leaves the file SHORTER than N must trigger a
// full re-scan from offset 0 with a SourceError surfaced (codex never truncates,
// so a shrunken file is the only way size < cursor.size happens). The re-scan
// re-emits the (now shorter) session; the SQL layer's idempotent upserts absorb
// the re-emitted rows.
func TestRestart_TruncationReScansWithSourceError(t *testing.T) {
	t.Parallel()

	id := uuid7(2)
	turn1, turn2 := twoTurnSession(id)
	full := append(append([]string{}, turn1...), turn2...)

	root := t.TempDir()
	path := shardPath(root, id)
	writeFileBytes(t, path, []byte(strings.Join(full, "\n")+"\n"))
	setMtime(t, path, time.Minute)

	var errs []string
	a, err := New(root, canonical.AdapterOptions{OnError: func(e error) { errs = append(errs, e.Error()) }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Full clean ingest → cursor records size == full file size.
	out1 := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	first := drainBuffered(out1)
	if len(errs) != 0 {
		t.Fatalf("phase1 unexpected errors: %v", errs)
	}
	if got := countKind(first, canonical.EvSessionStarted); got != 1 {
		t.Fatalf("phase1 SessionStarted = %d, want 1", got)
	}
	cursorJSON := lastCursor(t, first)
	parsed, err := a.ParseCursor(cursorJSON)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}

	// Operator delete+recreate leaving the file SHORTER (only turn 1). The new
	// size is below the cursor's recorded size → truncation defense fires.
	writeFileBytes(t, path, []byte(strings.Join(turn1, "\n")+"\n"))
	setMtime(t, path, time.Minute)
	errs = nil

	out2 := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), parsed, out2); err != nil {
		t.Fatalf("Scan #2 (after truncation): %v", err)
	}
	second := drainBuffered(out2)

	// A SourceError naming the shrink must surface (no silent failure).
	if !anyContains(errs, "shrank") {
		t.Fatalf("truncation did not surface a 'shrank' SourceError; errs=%v", errs)
	}
	// The re-scan from 0 re-emits the session (SQL dedup absorbs it downstream).
	if got := countKind(second, canonical.EvSessionStarted); got != 1 {
		t.Errorf("re-scan after truncation SessionStarted = %d, want 1 (full re-scan from 0)", got)
	}
	// Only turn 1 remains on disk, so exactly its TurnFinalized re-emits.
	if got := countKind(second, canonical.EvTurnFinalized); got != 1 {
		t.Errorf("re-scan TurnFinalized = %d, want 1 (only turn 1 remains)", got)
	}
}

// TestRestart_EOFFinalizeNotReFiredOnUnchangedRescan pins H2: an EOF-finalize
// (the OLD-format completed close, or the stale NEW-format failed/incomplete close
// + SessionFinalized) must fire EXACTLY ONCE for a given file size. The mapper's
// own eofFinalized guard is per-instance and the scanner replays from offset 0 on
// every scan (rebuilding a fresh mapper), so without a DURABLE cursor marker an
// unchanged rescan/restart re-fires the synthetic finalize. The marker
// (FileCursor.EOFFinalizedSize) is round-tripped through Cursor.String/ParseCursor
// exactly as the ingester persists it, so the resume sees ZERO duplicate
// TurnFinalized / SessionFinalized.
func TestRestart_EOFFinalizeNotReFiredOnUnchangedRescan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		body        func(id string) []byte
		age         time.Duration
		wantTurnFin int  // TurnFinalized expected on the FIRST scan
		wantSessFin int  // SessionFinalized expected on the FIRST scan
		oldFormat   bool // documents which EOF path fires
	}{
		{
			// OLD-format (turn_context only, no task_started): closes COMPLETED at
			// EOF regardless of staleness (spec edge #3) — the 38%-of-corpus case.
			name:        "old_format_clean_close",
			body:        oldFormatOpenTurnSession,
			age:         time.Minute, // fresh: old-format still closes at EOF
			wantTurnFin: 1,
			wantSessFin: 0, // codex has no per-session terminal signal (SOW C#3)
			oldFormat:   true,
		},
		{
			// NEW-format hanging turn aged stale ≥ 1 h: closes failed/incomplete AND
			// emits SessionFinalized(failed,incomplete) — the only SessionFinalized
			// codex emits (rule #23). This is the case the P2 explicitly flagged for
			// a duplicate SessionFinalized on rescan.
			name:        "new_format_stale_crash",
			body:        hangingSession,
			age:         2 * time.Hour, // stale ≥ 1 h
			wantTurnFin: 1,
			wantSessFin: 1,
			oldFormat:   false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := uuid7(40 + len(tc.name))
			root := t.TempDir()
			path := shardPath(root, id)
			writeFileBytes(t, path, tc.body(id))
			setMtime(t, path, tc.age)

			a, err := New(root, canonical.AdapterOptions{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// First scan: the EOF-finalize fires exactly once.
			out1 := make(chan canonical.Event, 512)
			if err := a.Scan(context.Background(), nil, out1); err != nil {
				t.Fatalf("Scan #1: %v", err)
			}
			first := drainBuffered(out1)
			if got := countKind(first, canonical.EvTurnFinalized); got != tc.wantTurnFin {
				t.Fatalf("first scan TurnFinalized = %d, want %d", got, tc.wantTurnFin)
			}
			if got := countKind(first, canonical.EvSessionFinalized); got != tc.wantSessFin {
				t.Fatalf("first scan SessionFinalized = %d, want %d", got, tc.wantSessFin)
			}

			// Persist + reload the cursor exactly as the ingester does (JSON
			// round-trip through the public ParseCursor). The EOFFinalizedSize marker
			// must survive this round-trip.
			cursorJSON := lastCursor(t, first)
			parsed, err := a.ParseCursor(cursorJSON)
			if err != nil {
				t.Fatalf("ParseCursor: %v", err)
			}

			// Rescan with NO new bytes (same size, same mtime). The durable marker
			// must suppress the EOF-finalize entirely.
			out2 := make(chan canonical.Event, 512)
			if err := a.Scan(context.Background(), parsed, out2); err != nil {
				t.Fatalf("Scan #2 (unchanged rescan): %v", err)
			}
			second := drainBuffered(out2)
			if got := countKind(second, canonical.EvTurnFinalized); got != 0 {
				t.Errorf("unchanged rescan re-fired TurnFinalized %d times, want 0 (H2)", got)
			}
			if got := countKind(second, canonical.EvSessionFinalized); got != 0 {
				t.Errorf("unchanged rescan re-fired SessionFinalized %d times, want 0 (H2)", got)
			}

			// A genuine append (size grows) re-opens normally: appending a fresh turn
			// and closing it must produce a new TurnFinalized (the marker no longer
			// matches the grown size). This guards against the marker over-suppressing.
			appendFileBytes(t, path, []byte(appendedClosedTurn()))
			setMtime(t, path, time.Minute)
			out3 := make(chan canonical.Event, 512)
			if err := a.Scan(context.Background(), parsed, out3); err != nil {
				t.Fatalf("Scan #3 (after append): %v", err)
			}
			third := drainBuffered(out3)
			if got := countKind(third, canonical.EvTurnFinalized); got < 1 {
				t.Errorf("append after EOF-finalize produced %d TurnFinalized, want >=1 (marker must not over-suppress a real append)", got)
			}
		})
	}
}

// TestRestart_MetadataAppendDoesNotReFinalizeOldFormatEOF pins SOW-0004 I2: after
// an OLD-format turn is closed COMPLETED at EOF (the 38%-of-corpus case), codex may
// append a bare metadata-only session_meta record (real: recorder.rs:1615). That
// append GROWS the file past the EOF-finalize marker but carries NO new turn
// content. The adapter must NOT re-fire the turn's TurnFinalized, and the original
// close-ts (the turn's last CONTENT-activity ts, not the metadata append's later
// ts) must stay unchanged. A genuine new turn would re-open and close normally; a
// metadata append must be inert.
func TestRestart_MetadataAppendDoesNotReFinalizeOldFormatEOF(t *testing.T) {
	t.Parallel()

	id := uuid7(7)
	root := t.TempDir()
	path := shardPath(root, id)
	writeFileBytes(t, path, oldFormatOpenTurnSession(id))
	setMtime(t, path, time.Minute) // fresh: old-format still closes COMPLETED at EOF

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// First scan: the OLD-format turn closes COMPLETED exactly once at EOF.
	out1 := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	first := drainBuffered(out1)
	tf1 := turnFinals(first)
	if len(tf1) != 1 {
		t.Fatalf("first scan TurnFinalized count = %d, want 1", len(tf1))
	}
	// The close-ts is the turn's last CONTENT-activity ts (tsDone, the assistant
	// message), NOT the file mtime / wall-clock (G6) and — after the append below —
	// NOT the later metadata ts (I2).
	wantEndTs, perr := parseTsToMicros(tsDone)
	if perr != nil {
		t.Fatalf("parse tsDone: %v", perr)
	}
	if tf1[0].EndTs != wantEndTs {
		t.Fatalf("first scan TurnFinalized EndTs = %d, want %d (last content ts, tsDone)", tf1[0].EndTs, wantEndTs)
	}

	// Persist + reload the cursor through the exact ingester round-trip. The
	// EOFFinalizedSize marker must survive.
	cursorJSON := lastCursor(t, first)
	parsed, err := a.ParseCursor(cursorJSON)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}

	// Append ONLY a metadata-only session_meta with a LATER timestamp. This grows the
	// file past the marker but carries no turn content; a naive size-only suppression
	// would re-fire the close and re-date it to this later ts.
	metaAppend := `{"timestamp":"2025-11-20T18:30:00.000Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","source":"exec"}}`
	appendFileBytes(t, path, []byte(metaAppend+"\n"))
	setMtime(t, path, time.Minute)

	out2 := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), parsed, out2); err != nil {
		t.Fatalf("Scan #2 (metadata append): %v", err)
	}
	second := drainBuffered(out2)

	if got := countKind(second, canonical.EvTurnFinalized); got != 0 {
		t.Errorf("metadata-only append re-fired TurnFinalized %d times, want 0 (I2)", got)
	}
	if got := countKind(second, canonical.EvSessionFinalized); got != 0 {
		t.Errorf("metadata-only append emitted SessionFinalized %d times, want 0 (I2)", got)
	}

	// Belt-and-braces: a THIRD unchanged rescan after the suppressed append (no new
	// bytes) must also be inert — the marker advanced to the post-append size, so the
	// file no longer looks "grown".
	cursorJSON2 := lastCursor(t, second)
	parsed2, err := a.ParseCursor(cursorJSON2)
	if err != nil {
		t.Fatalf("ParseCursor #2: %v", err)
	}
	out3 := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), parsed2, out3); err != nil {
		t.Fatalf("Scan #3 (unchanged after append): %v", err)
	}
	third := drainBuffered(out3)
	if got := countKind(third, canonical.EvTurnFinalized); got != 0 {
		t.Errorf("unchanged rescan after metadata append re-fired TurnFinalized %d times, want 0 (I2 marker advance)", got)
	}
}

// oldFormatOpenTurnSession returns a modern rollout with an OLD-format turn
// (turn_context only — no task_started, no task_complete) that stays open until
// EOF. finalizeAtEOF closes it COMPLETED regardless of staleness (spec edge #3).
func oldFormatOpenTurnSession(id string) []byte {
	lines := []string{
		metaLine(id, `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
		`{"timestamp":"` + tsDone + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// appendedClosedTurn returns a NEW-format turn (task_started + task_complete) to
// append after an EOF-finalize, proving a genuine append re-opens the file and
// produces its own TurnFinalized rather than being suppressed by the EOF marker.
func appendedClosedTurn() string {
	lines := []string{
		`{"timestamp":"2025-11-20T17:10:00.000Z","type":"turn_context","payload":{"turn_id":"t2","model":"gpt-5.5"}}`,
		`{"timestamp":"2025-11-20T17:10:00.100Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2","started_at":1763658600}}`,
		`{"timestamp":"2025-11-20T17:10:05.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","completed_at":1763658605,"duration_ms":5000}}`,
	}
	return strings.Join(lines, "\n") + "\n"
}

// scanFullSession runs the public Scan once over a freshly-written rollout and
// returns the event stream (a one-shot reference run for the resume comparison).
func scanFullSession(t *testing.T, id string, lines []string) []canonical.Event {
	t.Helper()
	root := t.TempDir()
	path := shardPath(root, id)
	writeFileBytes(t, path, []byte(strings.Join(lines, "\n")+"\n"))
	setMtime(t, path, time.Minute)
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 512)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return drainBuffered(out)
}

// lastCursor returns the cursor JSON from the last SourceProgressEvent in the
// stream (the checkpoint the ingester persists into sources.cursor).
func lastCursor(t *testing.T, events []canonical.Event) string {
	t.Helper()
	cur := ""
	for _, ev := range events {
		if sp, ok := ev.(canonical.SourceProgressEvent); ok {
			cur = sp.Cursor
		}
	}
	if cur == "" {
		t.Fatal("no SourceProgressEvent in events (cannot persist cursor)")
	}
	return cur
}

// contentKeys reduces a stream to a sorted slice of content identity keys,
// ignoring SourceProgress and SourceSeq. Two streams with identical content keys
// represent the same end state after SQL-layer dedup (mirrors claude_code).
func contentKeys(events []canonical.Event) []string {
	keys := make([]string, 0, len(events))
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); ok {
			continue
		}
		keys = append(keys, contentKey(ev))
	}
	sort.Strings(keys)
	return keys
}

// contentKey builds a stable identity string for an event from the fields the
// SQL layer keys on (kind + session/turn/op identity + the load-bearing payload
// discriminators), excluding SourceSeq. Mirrors claude_code's contentKey,
// extended with the codex op discriminators (kind, name, namespace,
// reasoning_kind) and the payload-ref kind so a resume that changed any of them
// would be caught.
func contentKey(ev canonical.Event) string {
	switch e := ev.(type) {
	case canonical.SessionStartedEvent:
		return "ss|" + e.NativeID + "|" + string(e.Kind) + "|" + e.ParentNativeID
	case canonical.SessionUpdatedEvent:
		return "su|" + e.NativeID + "|" + e.Model + "|" + e.AgentName
	case canonical.SessionFinalizedEvent:
		return "sf|" + e.NativeID + "|" + string(e.Status) + "|" + e.ErrorClass
	case canonical.TurnStartedEvent:
		return "ts|" + e.SessionNativeID + "|" + itoa(e.Seq)
	case canonical.TurnFinalizedEvent:
		return "tf|" + e.SessionNativeID + "|" + itoa(e.Seq) + "|" + e.Status + "|" + itoa64(e.TokensIn) + "|" + itoa64(e.TokensOut)
	case canonical.OpStartedEvent:
		return "os|" + e.SessionNativeID + "|" + itoa(e.TurnSeq) + "|" + itoa(e.Seq) + "|" + string(e.Kind) + "|" + e.Name + "|" + e.ToolNamespace + "|" + e.ReasoningKind
	case canonical.OpFinalizedEvent:
		return "of|" + e.SessionNativeID + "|" + itoa(e.TurnSeq) + "|" + itoa(e.Seq) + "|" + e.Status + "|" + itoa64(e.CtxUsed)
	case canonical.PayloadRefEvent:
		return "pr|" + e.SessionNativeID + "|" + itoa(e.TurnSeq) + "|" + itoa(e.OpSeq) + "|" + e.PayloadKind + "|" + e.Format
	case canonical.LogEntryEvent:
		return "log|" + e.SessionNativeID + "|" + itoa(e.TurnSeq) + "|" + e.Message
	default:
		return "other"
	}
}

// itoa / itoa64 are tiny strconv wrappers used by contentKey.
func itoa(i int) string     { return strconv.Itoa(i) }
func itoa64(i int64) string { return strconv.FormatInt(i, 10) }

// anyContains reports whether any string in s contains sub.
func anyContains(s []string, sub string) bool {
	for _, v := range s {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}
