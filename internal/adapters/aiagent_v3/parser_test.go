package aiagent_v3

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLine_SkipsBlank(t *testing.T) {
	t.Parallel()

	rec, skip, err := parseLine([]byte("   \n"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !skip {
		t.Fatalf("expected skip for whitespace-only line")
	}
	if rec.Common.SessionID != "" {
		t.Fatalf("expected zero record for skip")
	}
}

func TestParseLine_SessionStart(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","agentId":"x","headendId":"cli","capturePayloads":true}`)
	rec, skip, err := parseLine(line)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if skip {
		t.Fatalf("unexpected skip")
	}
	if rec.SessionStart == nil {
		t.Fatalf("expected SessionStart body")
	}
	if rec.SessionStart.AgentID != "x" || rec.SessionStart.HeadendID != "cli" {
		t.Fatalf("bad body: %+v", rec.SessionStart)
	}
	if rec.Common.Seq != 1 {
		t.Fatalf("seq: %d", rec.Common.Seq)
	}
}

func TestParseLine_TurnStart(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","turn":1}`)
	rec, _, err := parseLine(line)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rec.TurnStart == nil || rec.TurnStart.Turn != 1 {
		t.Fatalf("bad body: %+v", rec.TurnStart)
	}
}

func TestParseLine_TurnStartRejectsZeroTurn(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","turn":0}`)
	if _, _, err := parseLine(line); err == nil {
		t.Fatalf("expected error for turn=0")
	}
}

func TestParseLine_TurnEnd(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","turn":1,"status":"ok","ops":[]}`)
	rec, _, err := parseLine(line)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rec.TurnEnd == nil || rec.TurnEnd.Status != "ok" {
		t.Fatalf("bad body: %+v", rec.TurnEnd)
	}
}

func TestParseLine_TurnEndRejectsMissingStatus(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","turn":1,"ops":[]}`)
	if _, _, err := parseLine(line); err == nil {
		t.Fatalf("expected error for missing status")
	}
}

func TestParseLine_SessionSummary(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","status":"ok","finalReport":{"format":"json","captured":true}}`)
	rec, _, err := parseLine(line)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rec.SessionSummary == nil || rec.SessionSummary.Status != "ok" {
		t.Fatalf("bad body: %+v", rec.SessionSummary)
	}
}

func TestParseLine_SessionSummaryRejectsMissingStatus(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc"}`)
	if _, _, err := parseLine(line); err == nil {
		t.Fatalf("expected error for missing status")
	}
}

func TestParseLine_SessionError(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"session_error","seq":5,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","error":"oops"}`)
	rec, _, err := parseLine(line)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rec.SessionError == nil || rec.SessionError.Error != "oops" {
		t.Fatalf("bad body: %+v", rec.SessionError)
	}
}

func TestParseLine_UnknownRecordType(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":3,"recordType":"snazzy_new_thing","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc"}`)
	_, _, err := parseLine(line)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, errUnknownRecordType) {
		t.Fatalf("expected errUnknownRecordType, got %v", err)
	}
}

func TestParseLine_RejectsBadJSON(t *testing.T) {
	t.Parallel()

	if _, _, err := parseLine([]byte(`not json`)); err == nil {
		t.Fatalf("expected error for bad JSON")
	}
}

func TestParseLine_RejectsWrongVersion(t *testing.T) {
	t.Parallel()

	line := []byte(`{"version":2,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","capturePayloads":true}`)
	if _, _, err := parseLine(line); err == nil {
		t.Fatalf("expected error for wrong version")
	}
}

func TestParseLine_RejectsMissingRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
	}{
		{"missing version", `{"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","capturePayloads":true}`},
		{"missing recordType", `{"version":3,"seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc"}`},
		{"missing ts", `{"version":3,"recordType":"session_start","seq":1,"originId":"abc","sessionId":"abc","capturePayloads":true}`},
		{"missing originId", `{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","sessionId":"abc","capturePayloads":true}`},
		{"missing sessionId", `{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","capturePayloads":true}`},
		{"seq zero", `{"version":3,"recordType":"session_start","seq":0,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","capturePayloads":true}`},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := parseLine([]byte(tc.line)); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestParseLine_TurnEndRejectsBadJSONBody(t *testing.T) {
	t.Parallel()

	// Valid envelope; turn_end body has wrong type for ops (string instead of array).
	line := []byte(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:00.000Z","originId":"abc","sessionId":"abc","turn":1,"status":"ok","ops":"oops"}`)
	if _, _, err := parseLine(line); err == nil {
		t.Fatalf("expected error for bad ops type")
	}
}

func TestParseLine_BadEnvelopeStatusContainsMessage(t *testing.T) {
	t.Parallel()

	if _, _, err := parseLine([]byte(`{"version":3,"recordType":""}`)); err == nil {
		t.Fatalf("expected error")
	} else if !strings.Contains(err.Error(), "recordType") {
		t.Fatalf("expected recordType in error, got %v", err)
	}
}
