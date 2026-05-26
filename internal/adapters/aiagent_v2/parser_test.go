package aiagent_v2

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestParseSnapshot_HappyV2(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "abc")
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := parseSnapshot(body)
	if err != nil {
		t.Fatalf("parseSnapshot: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("Version: %d", got.Version)
	}
	if got.OpTree.TraceID != "abc" {
		t.Fatalf("TraceID: %q", got.OpTree.TraceID)
	}
	if len(got.OpTree.Turns) != 1 {
		t.Fatalf("Turns: %d", len(got.OpTree.Turns))
	}
}

func TestParseSnapshot_LegacyV1(t *testing.T) {
	t.Parallel()
	snap := snapshot{Version: 1, Reason: "final", OpTree: opTree{TraceID: "v1"}}
	body, _ := json.Marshal(snap)
	got, err := parseSnapshot(body)
	if err != nil {
		t.Fatalf("v1 should parse: %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("Version: %d", got.Version)
	}
}

func TestParseSnapshot_RejectsMissingVersion(t *testing.T) {
	t.Parallel()
	_, err := parseSnapshot([]byte(`{"opTree":{"traceId":"x"}}`))
	if err == nil || !errors.Is(err, errMalformedSnapshot) {
		t.Fatalf("expected malformed snapshot error, got %v", err)
	}
}

func TestParseSnapshot_RejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	_, err := parseSnapshot([]byte(`{"version":99,"opTree":{"traceId":"x"}}`))
	if err == nil || !errors.Is(err, errMalformedSnapshot) {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestParseSnapshot_RejectsMissingTraceID(t *testing.T) {
	t.Parallel()
	_, err := parseSnapshot([]byte(`{"version":2,"opTree":{}}`))
	if err == nil || !errors.Is(err, errMalformedSnapshot) {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestParseSnapshot_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	_, err := parseSnapshot([]byte(`{not json`))
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestParseSnapshot_ToleratesUnknownFields(t *testing.T) {
	t.Parallel()
	body := []byte(`{"version":2,"reason":"final","opTree":{"traceId":"x","futureField":42,"turns":[]}}`)
	got, err := parseSnapshot(body)
	if err != nil {
		t.Fatalf("unknown fields should be tolerated: %v", err)
	}
	if got.OpTree.TraceID != "x" {
		t.Fatalf("TraceID lost: %q", got.OpTree.TraceID)
	}
}

func TestParseSnapshot_StepsOnlyOK(t *testing.T) {
	t.Parallel()
	snap := snapshot{
		Version: 2,
		Reason:  "final",
		OpTree: opTree{
			TraceID: "x",
			Steps: []stepNode{
				{ID: "s1", Index: 0, Kind: "internal"},
			},
		},
	}
	body, _ := json.Marshal(snap)
	got, err := parseSnapshot(body)
	if err != nil {
		t.Fatalf("steps-only: %v", err)
	}
	if len(got.OpTree.Steps) != 1 {
		t.Fatalf("Steps: %d", len(got.OpTree.Steps))
	}
}

func TestParseSnapshotStream_HappyPath(t *testing.T) {
	t.Parallel()
	snap := simpleSnapshot(2, "stream")
	body, _ := json.Marshal(snap)
	got, err := parseSnapshotStream(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parseSnapshotStream: %v", err)
	}
	if got.OpTree.TraceID != "stream" {
		t.Fatalf("TraceID: %q", got.OpTree.TraceID)
	}
}

func TestParseSnapshotStream_RejectsBadJSON(t *testing.T) {
	t.Parallel()
	if _, err := parseSnapshotStream(bytes.NewReader([]byte(`{not json`))); err == nil {
		t.Fatalf("expected error")
	}
}

func TestParseSnapshotStream_ValidatesEnvelope(t *testing.T) {
	t.Parallel()
	if _, err := parseSnapshotStream(bytes.NewReader([]byte(`{"version":2,"opTree":{}}`))); err == nil {
		t.Fatalf("expected error")
	}
}
