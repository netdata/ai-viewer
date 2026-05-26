package aiagent_v3

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func mustRecord(t *testing.T, line string) record {
	t.Helper()
	rec, skip, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if skip {
		t.Fatalf("unexpected skip")
	}
	return rec
}

func TestMapRecord_SessionStartMapsHeadendToKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		headend string
		want    canonical.SessionKind
	}{
		{"cli", canonical.KindRoot},
		{"api", canonical.KindRoot},
		{"web", canonical.KindRoot},
		{"embed", canonical.KindRoot},
		{"slack", canonical.KindRoot},
		{"sub-agent", canonical.KindSubAgent},
		{"history_compaction", canonical.KindSubAgent},
		{"tool_output", canonical.KindToolInternal},
		{"future_thing", canonical.KindSubAgent},
	}
	for _, c := range cases {
		t.Run(c.headend, func(t *testing.T) {
			t.Parallel()
			if got := headendToKind(c.headend); got != c.want {
				t.Fatalf("headendToKind(%q) = %q, want %q", c.headend, got, c.want)
			}
		})
	}
}

func TestMapRecord_SessionStart(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"root","sessionId":"sess","parentSessionId":"root","agentId":"x","callPath":"cli:x","headendId":"sub-agent","capturePayloads":true,"attributes":{"ledgerPath":"session/sess.jsonl","custom":"v"}}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev, ok := events[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("unexpected type %T", events[0])
	}
	if ev.NativeID != "sess" || ev.RootNativeID != "root" || ev.ParentNativeID != "root" {
		t.Fatalf("bad ids: %+v", ev)
	}
	if ev.Kind != canonical.KindSubAgent {
		t.Fatalf("kind: %q", ev.Kind)
	}
	if ev.AgentName != "x" || ev.CallPath != "cli:x" {
		t.Fatalf("agent/callpath: %+v", ev)
	}
	if ev.Extras["headendId"] != "sub-agent" {
		t.Fatalf("missing headendId extra: %+v", ev.Extras)
	}
	if ev.Extras["attr.custom"] != "v" {
		t.Fatalf("missing attr extras: %+v", ev.Extras)
	}
	if ev.Extras["originId"] != "root" {
		t.Fatalf("missing originId extra: %+v", ev.Extras)
	}
	if ev.Ts == 0 {
		t.Fatalf("expected non-zero ts")
	}
	if ev.SourceSeq>>subEventBits != 1 {
		t.Fatalf("unexpected sourceSeq packing: %d", ev.SourceSeq)
	}
}

func TestMapRecord_TurnStart(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-05-26T10:00:00.000Z","originId":"root","sessionId":"sess","turn":3}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0].(canonical.TurnStartedEvent)
	if ev.SessionNativeID != "sess" || ev.Seq != 3 {
		t.Fatalf("bad TurnStarted: %+v", ev)
	}
}

func TestMapRecord_TurnEndEmitsOpsAndPayloadRefs(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:30.000Z","originId":"root","sessionId":"sess","turn":1,"status":"ok","ops":[{"opId":"op1","opIndex":1,"kind":"llm","status":"ok","startedAt":"2026-05-26T10:00:02.000Z","endedAt":"2026-05-26T10:00:25.000Z","model":"claude","provider":"anthropic","payloadRefs":[{"kind":"llm_request","opId":"op1","turn":1,"opIndex":1,"format":"http","compression":"gzip","path":"payloads/sess/turn-0001/llm-0001-request.http.gz","originalBytes":100,"compressedBytes":40,"sha256":"abc","captured":true,"truncated":false,"redacted":false}],"accounting":{"tokensIn":10,"tokensOut":2,"tokensCacheRead":0,"tokensCacheWrite":0,"costUsd":0.001}}],"accounting":{"tokensIn":10,"tokensOut":2,"tokensCacheRead":0,"tokensCacheWrite":0,"costUsd":0.001},"warnings":["slow"],"errors":["oops"]}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	// Expected: TurnFinalized + OpStarted + OpFinalized + PayloadRef + LogEntry(WRN) + LogEntry(ERR) = 6
	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d: %#v", len(events), events)
	}
	if _, ok := events[0].(canonical.TurnFinalizedEvent); !ok {
		t.Fatalf("events[0] not TurnFinalized: %T", events[0])
	}
	if _, ok := events[1].(canonical.OpStartedEvent); !ok {
		t.Fatalf("events[1] not OpStarted: %T", events[1])
	}
	opFin, ok := events[2].(canonical.OpFinalizedEvent)
	if !ok {
		t.Fatalf("events[2] not OpFinalized: %T", events[2])
	}
	if opFin.Status != "completed" {
		t.Fatalf("op status: %q", opFin.Status)
	}
	if opFin.BytesIn != 100 {
		t.Fatalf("BytesIn aggregation wrong: %d", opFin.BytesIn)
	}
	if opFin.CostUSD != 0.001 {
		t.Fatalf("cost: %v", opFin.CostUSD)
	}
	pl, ok := events[3].(canonical.PayloadRefEvent)
	if !ok {
		t.Fatalf("events[3] not PayloadRef: %T", events[3])
	}
	if pl.OriginalBytes != 100 || pl.SHA256 != "abc" {
		t.Fatalf("bad payload: %+v", pl)
	}
	if pl.LocationURI == "" {
		t.Fatalf("expected LocationURI for captured payload")
	}
	if log4 := events[4].(canonical.LogEntryEvent); log4.Severity != "WRN" || log4.Message != "slow" {
		t.Fatalf("warn log wrong: %+v", log4)
	}
	if log5 := events[5].(canonical.LogEntryEvent); log5.Severity != "ERR" || log5.Message != "oops" {
		t.Fatalf("err log wrong: %+v", log5)
	}
}

func TestMapRecord_TurnEndUncapturedPayload(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:30.000Z","originId":"root","sessionId":"sess","turn":1,"status":"ok","ops":[{"opId":"op1","opIndex":1,"kind":"llm","status":"ok","startedAt":"2026-05-26T10:00:02.000Z","endedAt":"2026-05-26T10:00:25.000Z","payloadRefs":[{"kind":"sdk_request","opId":"op1","turn":1,"opIndex":1,"format":"json","captured":false,"truncated":false,"redacted":false}]}],"warnings":[],"errors":[]}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	var pl canonical.PayloadRefEvent
	for _, ev := range events {
		if p, ok := ev.(canonical.PayloadRefEvent); ok {
			pl = p
			break
		}
	}
	if pl.PayloadKind != "sdk_request" {
		t.Fatalf("missing uncaptured payload event")
	}
	if pl.LocationURI != "" || pl.Compression != "" || pl.StoredBytes != 0 {
		t.Fatalf("uncaptured payload should have empty location/compression/stored: %+v", pl)
	}
	if pl.OriginalBytes != -1 {
		t.Fatalf("unknown OriginalBytes should be -1, got %d", pl.OriginalBytes)
	}
}

func TestMapRecord_TurnEndSessionOpEmitsChildLink(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:30.000Z","originId":"root","sessionId":"sess","turn":1,"status":"ok","ops":[{"opId":"op1","opIndex":1,"kind":"session","name":"web-fetch","status":"ok","startedAt":"2026-05-26T10:00:02.000Z","endedAt":"2026-05-26T10:00:25.000Z","provider":"sub-agent","childSessions":[{"sessionId":"child","originId":"root","parentSessionId":"sess","parentOpId":"op1","ledgerPath":"session/child.jsonl","status":"ok"}]}],"warnings":[],"errors":[]}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	opStart := events[1].(canonical.OpStartedEvent)
	if opStart.ChildSessionNativeID != "child" {
		t.Fatalf("child link missing: %+v", opStart)
	}
	if opStart.Kind != canonical.OpSession {
		t.Fatalf("kind: %q", opStart.Kind)
	}
}

func TestMapRecord_SessionSummaryCompleted(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-05-26T10:00:31.000Z","originId":"root","sessionId":"sess","status":"ok","finalReport":{"format":"json","captured":true}}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events (Finalized + Updated for finalReport), got %d", len(events))
	}
	fin := events[0].(canonical.SessionFinalizedEvent)
	if fin.Status != canonical.StatusCompleted {
		t.Fatalf("status: %q", fin.Status)
	}
	up := events[1].(canonical.SessionUpdatedEvent)
	if up.Extras["finalReport.format"] != "json" || up.Extras["finalReport.captured"] != true {
		t.Fatalf("finalReport extras missing: %+v", up.Extras)
	}
}

func TestMapRecord_SessionSummaryFailedAddsLog(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-05-26T10:00:31.000Z","originId":"root","sessionId":"sess","status":"failed","error":"boom","finalReport":{"format":"sub-agent","captured":false}}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	// Expected: SessionFinalized + SessionUpdated(finalReport) + LogEntry(ERR)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	fin := events[0].(canonical.SessionFinalizedEvent)
	if fin.Status != canonical.StatusFailed {
		t.Fatalf("status: %q", fin.Status)
	}
	if fin.ErrorMessage != "boom" {
		t.Fatalf("missing error message")
	}
	log := events[2].(canonical.LogEntryEvent)
	if log.Severity != "ERR" || log.Message != "boom" {
		t.Fatalf("log wrong: %+v", log)
	}
}

func TestMapRecord_SessionError(t *testing.T) {
	t.Parallel()

	rec := mustRecord(t, `{"version":3,"recordType":"session_error","seq":2,"ts":"2026-05-26T15:00:00.500Z","originId":"sess","sessionId":"sess","error":"crashed"}`)
	events, err := mapRecord(rec, "src", "/tmp/root")
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	fin := events[0].(canonical.SessionFinalizedEvent)
	if fin.Status != canonical.StatusFailed || fin.ErrorClass != "session_error" {
		t.Fatalf("wrong fin: %+v", fin)
	}
	log := events[1].(canonical.LogEntryEvent)
	if log.Severity != "ERR" || log.Message != "crashed" {
		t.Fatalf("log: %+v", log)
	}
}

func TestPackSeq_MonotonicWithinRecord(t *testing.T) {
	t.Parallel()

	prev := packSeq(5, 0)
	for i := uint64(1); i < 8; i++ {
		next := packSeq(5, i)
		if next <= prev {
			t.Fatalf("packSeq not monotonic at sub %d: prev=%d next=%d", i, prev, next)
		}
		prev = next
	}
	// Different ledger seq yields strictly larger.
	if packSeq(6, 0) <= packSeq(5, maxSubEventsPerRecord-1) {
		t.Fatalf("packSeq does not order across records")
	}
}

func TestParseTsToMicros(t *testing.T) {
	t.Parallel()

	got, err := parseTsToMicros("2026-05-26T10:00:00.000Z")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 2026-05-26T10:00:00.000Z corresponds to 1779789600 seconds.
	want := int64(1779789600) * 1_000_000
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

func TestParseTsToMicros_BadTs(t *testing.T) {
	t.Parallel()

	if _, err := parseTsToMicros("not a date"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestMapRecord_TurnEndSubEventOverflow(t *testing.T) {
	t.Parallel()

	// Build a turn_end with maxSubEventsPerRecord ops (way past the cap).
	rec := record{
		Common: commonFields{Version: formatVersion, RecordType: recTurnEnd, Seq: 1, Ts: "2026-05-26T10:00:00.000Z", OriginID: "r", SessionID: "s"},
		TurnEnd: &turnEndBody{
			Turn:   1,
			Status: "ok",
			Ops:    make([]opSummary, maxSubEventsPerRecord),
		},
	}
	if _, err := mapRecord(rec, "src", "/tmp/r"); err == nil {
		t.Fatalf("expected overflow error")
	}
}
