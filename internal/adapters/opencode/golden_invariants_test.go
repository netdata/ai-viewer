package opencode

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file makes the committed goldens NOT self-justifying. TestGolden pins the
// EXACT emitted bytes, but a future `-update-golden` could silently launder a
// regression past review. These tests re-scan each fixture and assert the
// load-bearing INVARIANT each scenario exists to prove (AC#3/#4/#5/#7), keyed on
// canonical-event fields rather than golden text — so a regression that changed
// the math/linkage/degrade would fail HERE even after a golden refresh.

// scenarioEvents builds the named scenario's fixture DB and returns its scanned,
// SourceProgress-filtered event stream (the same content TestGolden pins).
func scenarioEvents(t *testing.T, scenario string) []canonical.Event {
	t.Helper()
	dbPath := buildFixtureDB(t, fixtureSQLPath(scenario))
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	out := scanScenario(t, abs)
	filtered := make([]canonical.Event, 0, len(out))
	for _, ev := range out {
		if _, ok := ev.(canonical.SourceProgressEvent); ok {
			continue
		}
		filtered = append(filtered, ev)
	}
	return filtered
}

// sessionStarts returns every SessionStartedEvent in the stream.
func sessionStarts(events []canonical.Event) []canonical.SessionStartedEvent {
	var out []canonical.SessionStartedEvent
	for _, ev := range events {
		if s, ok := ev.(canonical.SessionStartedEvent); ok {
			out = append(out, s)
		}
	}
	return out
}

// sessionStartByID finds the SessionStartedEvent for a native id (fatal if absent).
func sessionStartByID(t *testing.T, events []canonical.Event, id string) canonical.SessionStartedEvent {
	t.Helper()
	for _, s := range sessionStarts(events) {
		if s.NativeID == id {
			return s
		}
	}
	t.Fatalf("no SessionStartedEvent for %q", id)
	return canonical.SessionStartedEvent{}
}

// TestGoldenInvariant_AHappy pins the baseline tree shape: exactly one root
// session, one turn, one LLM op, one reasoning op, one tool op, and NO
// SessionFinalized (a running session with neither archive nor error stays
// running — adapter-opencode.md "Canonical Model Gaps" #5).
func TestGoldenInvariant_AHappy(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "a_happy")

	if got := countKind(ev, canonical.EvSessionStarted); got != 1 {
		t.Fatalf("SessionStarted = %d, want 1", got)
	}
	ss := firstStarted(t, ev)
	if ss.Kind != canonical.KindRoot {
		t.Errorf("Kind = %q, want root", ss.Kind)
	}
	if ss.ParentNativeID != "" {
		t.Errorf("root ParentNativeID = %q, want empty", ss.ParentNativeID)
	}
	if ss.Model != "claude-x" {
		t.Errorf("Model = %q, want claude-x", ss.Model)
	}
	if got := countKind(ev, canonical.EvTurnStarted); got != 1 {
		t.Errorf("TurnStarted = %d, want 1", got)
	}
	if got := len(llmOps(ev)); got != 1 {
		t.Errorf("llm ops = %d, want 1", got)
	}
	if got := countKindOpKind(ev, canonical.OpReasoning); got != 1 {
		t.Errorf("reasoning ops = %d, want 1", got)
	}
	if got := len(toolOps(ev)); got != 1 {
		t.Errorf("tool ops = %d, want 1", got)
	}
	if got := countKind(ev, canonical.EvSessionFinalized); got != 0 {
		t.Errorf("SessionFinalized = %d, want 0 (running session)", got)
	}
}

// TestGoldenInvariant_BSubagentTask is the AC#4 dual-edge proof. The single
// tool='task' part must yield BOTH a session Op (kind=session,
// ChildSessionNativeID=ses_child01) AND a tool Op (kind=tool, name=task), and the
// child session row must map to Kind=sub_agent with ParentNativeID=ses_parent01.
func TestGoldenInvariant_BSubagentTask(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "b_subagent_task")

	// Edge 1: session Op naming the child as topology parent.
	var sessionOp *canonical.OpStartedEvent
	for i, s := range opStarts(ev) {
		if s.Kind == canonical.OpSession {
			cp := opStarts(ev)[i]
			sessionOp = &cp
			break
		}
	}
	if sessionOp == nil {
		t.Fatal("no session Op emitted for tool='task' (AC#4 edge 1 missing)")
	}
	if sessionOp.ChildSessionNativeID != "ses_child01" {
		t.Errorf("session Op ChildSessionNativeID = %q, want ses_child01", sessionOp.ChildSessionNativeID)
	}

	// Edge 2: tool Op for the task tool, in the SAME turn as the session Op.
	var taskTool *canonical.OpStartedEvent
	for _, s := range toolOps(ev) {
		if s.Name == "task" {
			cp := s
			taskTool = &cp
			break
		}
	}
	if taskTool == nil {
		t.Fatal("no tool Op name=task emitted (AC#4 edge 2 missing)")
	}
	if taskTool.TurnSeq != sessionOp.TurnSeq {
		t.Errorf("task tool TurnSeq %d != session Op TurnSeq %d (must be same turn)", taskTool.TurnSeq, sessionOp.TurnSeq)
	}
	if taskTool.Seq == sessionOp.Seq {
		t.Errorf("task tool Seq %d must differ from session Op Seq %d (two distinct ops)", taskTool.Seq, sessionOp.Seq)
	}

	// Edge 3 (parent_id): the child session maps to sub_agent linked to the parent.
	child := sessionStartByID(t, ev, "ses_child01")
	if child.Kind != canonical.KindSubAgent {
		t.Errorf("child Kind = %q, want sub_agent", child.Kind)
	}
	if child.ParentNativeID != "ses_parent01" {
		t.Errorf("child ParentNativeID = %q, want ses_parent01", child.ParentNativeID)
	}
	if child.RootNativeID != "ses_parent01" {
		t.Errorf("child RootNativeID = %q, want ses_parent01", child.RootNativeID)
	}
}

// TestGoldenInvariant_CMultiProvider is the AC#7 multi-provider proof: the two
// turns' LLM ops carry distinct ProviderAlias verbatim (anthropic, openai) plus a
// canonical Provider, so two catalog providers seed downstream. It ALSO pins the
// two-level token model: per-op tokens reset per message (turn2 op = 300/80) while
// per-turn tokens are the session-level delta (turn2 turn = 200/50).
func TestGoldenInvariant_CMultiProvider(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "c_multi_provider")

	aliases := map[string]bool{}
	for _, s := range llmOps(ev) {
		if s.ProviderAlias == "" {
			t.Errorf("LLM op (turn %d) has empty ProviderAlias", s.TurnSeq)
		}
		if s.Provider != s.ProviderAlias {
			t.Errorf("turn %d Provider %q != alias %q (both in knownProviderAliases here)", s.TurnSeq, s.Provider, s.ProviderAlias)
		}
		aliases[s.ProviderAlias] = true
	}
	for _, want := range []string{"anthropic", "openai"} {
		if !aliases[want] {
			t.Errorf("provider alias %q absent (want both anthropic+openai)", want)
		}
	}

	// Per-op (per-message-reset) vs per-turn (session-delta) tokens for turn 2.
	op2 := opFinalForTurnSeq(t, ev, 2)
	if op2.TokensIn != 300 || op2.TokensOut != 80 {
		t.Errorf("turn2 LLM op tokens = %d/%d, want 300/80 (per-message cumulative)", op2.TokensIn, op2.TokensOut)
	}
	tf2 := turnFinalForSeq(t, ev, 2)
	if tf2.TokensIn != 200 || tf2.TokensOut != 50 {
		t.Errorf("turn2 turn tokens = %d/%d, want 200/50 (session-level delta)", tf2.TokensIn, tf2.TokensOut)
	}
}

// TestGoldenInvariant_DSchemaDrift is the AC#5 graceful-degrade proof. Against the
// pre-session_usage schema (session lacks agent/model/cost/tokens_*) the adapter
// must NOT reject (it emits a full tree), session-level Model/AgentName must be
// empty (their columns are gone), Extras must lack providerID/variant (both come
// from session.model), yet the op/turn token+provider values must survive because
// they come from message.data (untouched by the column drift).
func TestGoldenInvariant_DSchemaDrift(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "d_schema_drift")

	if got := countKind(ev, canonical.EvSessionStarted); got != 1 {
		t.Fatalf("SessionStarted = %d, want 1 (old schema must still ingest, not be rejected)", got)
	}
	ss := firstStarted(t, ev)
	if ss.Model != "" {
		t.Errorf("Model = %q, want empty (session.model column absent on old schema)", ss.Model)
	}
	if ss.AgentName != "" {
		t.Errorf("AgentName = %q, want empty (session.agent column absent)", ss.AgentName)
	}
	if _, ok := ss.Extras["providerID"]; ok {
		t.Errorf("Extras has providerID on old schema; want absent (derives from session.model)")
	}
	if _, ok := ss.Extras["variant"]; ok {
		t.Errorf("Extras has variant on old schema; want absent")
	}
	// Always-present columns still populate Extras.
	for _, k := range []string{"directory", "project_id", "slug", "title", "version"} {
		if _, ok := ss.Extras[k]; !ok {
			t.Errorf("Extras missing always-present key %q", k)
		}
	}
	// Op-level provider/model + token values come from message.data, unaffected.
	ops := llmOps(ev)
	if len(ops) != 1 {
		t.Fatalf("llm ops = %d, want 1", len(ops))
	}
	if ops[0].Provider != "anthropic" || ops[0].Model != "claude-x" {
		t.Errorf("LLM op provider/model = %q/%q, want anthropic/claude-x (from message.data)", ops[0].Provider, ops[0].Model)
	}
	tf := turnFinals(ev)
	if len(tf) != 1 || tf[0].TokensIn != 60 || tf[0].TokensOut != 15 {
		t.Errorf("turn tokens = %v, want one turn 60/15 (from message.data)", tf)
	}
}

// TestGoldenInvariant_DSchemaDrift_MissingColumnsLoggedINF is the AC#5 INF-logging
// proof. Against the pre-`20260510033149` schema (session lacks the optional
// columns the dynamic SELECT omits — including time_compacting, SOW-0005 round-2
// P2-E), a Scan through the public adapter must emit
// exactly one INFO record per missing optional column, each carrying the matching
// `table`+`column` attributes. The set of logged (table, column) pairs must equal
// the set of columns introspection reports Missing — no more, no less. (Scan and
// Tail each emit the set once; this test exercises Scan, so one record per column.)
func TestGoldenInvariant_DSchemaDrift_MissingColumnsLoggedINF(t *testing.T) {
	t.Parallel()
	dbPath := buildFixtureDB(t, fixtureSQLPath("d_schema_drift"))
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// Ground truth: the columns introspection reports Missing on this schema.
	set, err := introspectAll(ctxBG(), openRO(t, dbPath))
	if err != nil {
		t.Fatalf("introspectAll must accept the old schema (graceful degrade), got: %v", err)
	}
	want := map[[2]string]bool{}
	for _, table := range trackedTables {
		for _, col := range set[table].Missing {
			want[[2]string{table, col}] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("d_schema_drift fixture has no missing optional columns; the INF assertion is vacuous")
	}

	// Scan through the public adapter with a record-capturing logger.
	rec := &captureHandler{}
	a, err := New(abs, canonical.AdapterOptions{Logger: slog.New(rec)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 8192)
	if err := a.Scan(ctxBG(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Collect the (table, column) pairs from the missing-column INFO records.
	const wantMsg = "opencode: optional column absent on this database schema; omitted from projection (old opencode version)"
	got := map[[2]string]int{}
	for _, r := range rec.records() {
		if r.level != slog.LevelInfo || r.message != wantMsg {
			continue
		}
		got[[2]string{r.attrs["table"], r.attrs["column"]}]++
	}

	// Every Missing column logged exactly once; nothing extra logged.
	for key := range want {
		if got[key] != 1 {
			t.Errorf("missing-column INF for %v logged %d times, want exactly 1", key, got[key])
		}
	}
	for key, n := range got {
		if !want[key] {
			t.Errorf("unexpected missing-column INF for %v (logged %d times)", key, n)
		}
	}
}

// TestGoldenInvariant_GNestedSubagent is the SOW-0005 P2.4 proof: in a 3-level
// session tree (root → child → grandchild) every session's RootNativeID is the
// TRUE tree root (ses_groot), NOT its direct parent. The grandchild is the
// load-bearing case: the pre-P2.4 code set its RootNativeID to its direct parent
// (ses_gchild); the chain-walk resolver must set it to ses_groot. ParentNativeID
// still points at the DIRECT parent (the immediate link), so the two differ for
// the grandchild — exactly what pins the fix.
func TestGoldenInvariant_GNestedSubagent(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "g_nested_subagent")

	root := sessionStartByID(t, ev, "ses_groot")
	if root.Kind != canonical.KindRoot {
		t.Errorf("root Kind = %q, want root", root.Kind)
	}
	if root.RootNativeID != "ses_groot" {
		t.Errorf("root RootNativeID = %q, want ses_groot (its own id)", root.RootNativeID)
	}

	child := sessionStartByID(t, ev, "ses_gchild")
	if child.ParentNativeID != "ses_groot" || child.RootNativeID != "ses_groot" {
		t.Errorf("child parent/root = %q/%q, want ses_groot/ses_groot", child.ParentNativeID, child.RootNativeID)
	}

	grand := sessionStartByID(t, ev, "ses_ggrand")
	if grand.Kind != canonical.KindSubAgent {
		t.Errorf("grandchild Kind = %q, want sub_agent", grand.Kind)
	}
	// The DIRECT parent is the child; the TRUE ROOT is the topmost ancestor.
	if grand.ParentNativeID != "ses_gchild" {
		t.Errorf("grandchild ParentNativeID = %q, want ses_gchild (direct parent)", grand.ParentNativeID)
	}
	if grand.RootNativeID != "ses_groot" {
		t.Errorf("grandchild RootNativeID = %q, want ses_groot (tree root, NOT the direct parent ses_gchild)", grand.RootNativeID)
	}
}

// countKindOpKind counts OpStartedEvents of a given OpKind.
func countKindOpKind(events []canonical.Event, kind canonical.OpKind) int {
	n := 0
	for _, s := range opStarts(events) {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

// opFinalForTurnSeq returns the (single) LLM op_finalized in the given turn.
// c_multi_provider has one LLM op per turn, so this is unambiguous.
func opFinalForTurnSeq(t *testing.T, events []canonical.Event, turnSeq int) canonical.OpFinalizedEvent {
	t.Helper()
	for _, f := range opFinals(events) {
		if f.TurnSeq == turnSeq {
			return f
		}
	}
	t.Fatalf("no op_finalized in turn %d", turnSeq)
	return canonical.OpFinalizedEvent{}
}

// turnFinalForSeq returns the TurnFinalizedEvent for a turn seq (fatal if absent).
func turnFinalForSeq(t *testing.T, events []canonical.Event, seq int) canonical.TurnFinalizedEvent {
	t.Helper()
	for _, f := range turnFinals(events) {
		if f.Seq == seq {
			return f
		}
	}
	t.Fatalf("no turn_finalized for seq %d", seq)
	return canonical.TurnFinalizedEvent{}
}

// TestGoldenInvariant_IFailedAssistant pins SOW-0005 round-5 P3-1: a session whose
// LAST assistant message carries data.error finalizes as SessionFinalized
// Status=failed with BOTH ErrorClass (data.error.name) AND ErrorMessage
// (data.error.data.message). The failed turn carries ErrorClass too
// (TurnFinalizedEvent has no ErrorMessage field, so the message rides only on the
// session terminal). Keyed on canonical-event fields so a regression that dropped
// ErrorMessage fails HERE even after a -update-golden refresh.
func TestGoldenInvariant_IFailedAssistant(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "i_failed_assistant")

	if got := countKind(ev, canonical.EvSessionFinalized); got != 1 {
		t.Fatalf("SessionFinalized = %d, want 1 (failed assistant message)", got)
	}
	fin := sessionFinal(ev)
	if fin == nil {
		t.Fatal("no SessionFinalizedEvent emitted for a failed assistant message")
	}
	if fin.Status != canonical.StatusFailed {
		t.Errorf("Status = %q, want failed", fin.Status)
	}
	if fin.ErrorClass != "MessageAbortedError" {
		t.Errorf("ErrorClass = %q, want MessageAbortedError (data.error.name)", fin.ErrorClass)
	}
	if fin.ErrorMessage != "request was aborted by the user" {
		t.Errorf("ErrorMessage = %q, want the data.error.data.message string (P3-1)", fin.ErrorMessage)
	}
	// The failed turn carries the same ErrorClass (the canonical TurnFinalizedEvent
	// has no ErrorMessage field — the detail enriches the session terminal only).
	tf := turnFinalForSeq(t, ev, 1)
	if tf.Status != "failed" {
		t.Errorf("turn1 Status = %q, want failed", tf.Status)
	}
	if tf.ErrorClass != "MessageAbortedError" {
		t.Errorf("turn1 ErrorClass = %q, want MessageAbortedError", tf.ErrorClass)
	}
}

// TestGoldenInvariant_JFileAttachment pins SOW-0005 round-4 P2-3 + round-6 P3-3
// end-to-end: a file part flows through the full load→map pipeline as an INF
// LogEntry carrying {filename,url,mime} in extras, and the stream emits NO
// PayloadRefEvent at all (a file attachment has no canonical PayloadKind — the
// removed "user_attachment" kind was a contract violation). Keyed on canonical
// fields so a future -update-golden cannot launder a regression that re-introduced
// a non-canonical PayloadRef for a file part.
func TestGoldenInvariant_JFileAttachment(t *testing.T) {
	t.Parallel()
	ev := scenarioEvents(t, "j_file_attachment")

	// NO PayloadRef anywhere — the file part must not emit one (and nothing else in
	// this scenario does either: text/tool parts are absent).
	if got := countKind(ev, canonical.EvPayloadRef); got != 0 {
		t.Fatalf("PayloadRef count = %d, want 0 (a file part is a LogEntry, not a payload ref; round-6 P3-3)", got)
	}
	// Defence in depth: any PayloadRef that DID slip through must at least carry a
	// canonical kind (this also guards the assertion above against a kind rename).
	for _, e := range ev {
		if p, ok := e.(canonical.PayloadRefEvent); ok && !canonicalPayloadKinds[p.PayloadKind] {
			t.Fatalf("non-canonical PayloadRef kind=%q emitted for the file-attachment scenario", p.PayloadKind)
		}
	}

	// Exactly one INF LogEntry "file attachment" with the three extras, scoped to the
	// turn and the open LLM op.
	var found int
	for _, e := range ev {
		l, ok := e.(canonical.LogEntryEvent)
		if !ok || l.Message != "file attachment" {
			continue
		}
		found++
		if l.Severity != "INF" {
			t.Errorf("file-attachment LogEntry severity = %q, want INF", l.Severity)
		}
		if l.Source != Format {
			t.Errorf("file-attachment LogEntry source = %q, want %q", l.Source, Format)
		}
		if l.Extras["filename"] != "diagram.png" {
			t.Errorf("extras.filename = %v, want diagram.png", l.Extras["filename"])
		}
		if l.Extras["mime"] != "image/png" {
			t.Errorf("extras.mime = %v, want image/png", l.Extras["mime"])
		}
		if l.Extras["url"] != "https://cdn.example.invalid/diagram.png" {
			t.Errorf("extras.url = %v, want the verbatim data.url", l.Extras["url"])
		}
		if l.TurnSeq != 1 || l.OpSeq != 1 {
			t.Errorf("file-attachment LogEntry scope = (turn %d, op %d), want (1, 1)", l.TurnSeq, l.OpSeq)
		}
	}
	if found != 1 {
		t.Fatalf("file-attachment INF LogEntry count = %d, want 1", found)
	}
}
