package parity

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestArtifactJSONShapeIsStable(t *testing.T) {
	t.Parallel()

	artifact := Artifact{
		SchemaVersion:    1,
		Adapter:          "codex",
		SourceID:         "codex:testdata",
		SourceFile:       "sessions/rollout.jsonl",
		NativeSessionID:  "session-1",
		NativeTurnID:     "turn-1",
		NativeArtifactID: "line:42:/msg/content/0/text",
		Class:            ClassAssistantMessage,
		Availability:     AvailabilityAvailable,
		HashDomain:       HashSemanticText,
		Selector: Selector{
			URI:         "file:///repo/testdata/codex/session.jsonl#L42",
			JSONPointer: "/msg/content/0/text",
		},
		Bytes:           13,
		Chars:           13,
		ComputedSHA256:  "315f5bdb76d078c43b8ac0064e4a0164612b1fce77c869345bfc94c75894edd3",
		ProducerSHA256:  "producer-hash",
		Synthetic:       false,
		SyntheticReason: "",
	}

	got, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}

	const want = `{
  "schema_version": 1,
  "adapter": "codex",
  "source_id": "codex:testdata",
  "source_file": "sessions/rollout.jsonl",
  "native_session_id": "session-1",
  "native_turn_id": "turn-1",
  "native_artifact_id": "line:42:/msg/content/0/text",
  "class": "assistant_message",
  "availability": "available",
  "hash_domain": "semantic_text",
  "selector": {
    "uri": "file:///repo/testdata/codex/session.jsonl#L42",
    "json_pointer": "/msg/content/0/text"
  },
  "bytes": 13,
  "chars": 13,
  "computed_sha256": "315f5bdb76d078c43b8ac0064e4a0164612b1fce77c869345bfc94c75894edd3",
  "producer_sha256": "producer-hash",
  "synthetic": false,
  "synthetic_reason": ""
}`
	if string(got) != want {
		t.Fatalf("artifact JSON changed\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func assertIntegrityFailures(t *testing.T, artifact Artifact, want []IntegrityFailure) {
	t.Helper()

	if !reflect.DeepEqual(artifact.IntegrityFailures, want) {
		t.Fatalf("integrity_failures = %+v, want %+v", artifact.IntegrityFailures, want)
	}
}

func TestEmptyTextArtifactUsesStableHash(t *testing.T) {
	t.Parallel()

	artifact := Artifact{
		SchemaVersion:    1,
		Adapter:          "codex",
		SourceID:         "codex:testdata",
		NativeSessionID:  "session-1",
		NativeArtifactID: "line:7:/msg/content/0/text",
		Class:            ClassAssistantMessage,
		Availability:     AvailabilitySourceEmpty,
		HashDomain:       HashSemanticText,
		Selector:         Selector{URI: "file:///repo/session.jsonl#L7", JSONPointer: "/msg/content/0/text"},
		Bytes:            0,
		Chars:            0,
		ComputedSHA256:   EmptySHA256,
	}

	if err := artifact.Validate(); err != nil {
		t.Fatalf("empty artifact should be valid: %v", err)
	}
}

func TestEmptyRawArtifactAllowsUnknownCharacterCount(t *testing.T) {
	t.Parallel()

	artifact := Artifact{
		SchemaVersion:    1,
		Adapter:          "aiagent_v3",
		SourceID:         "aiagent_v3:testdata",
		NativeSessionID:  "session-1",
		NativeArtifactID: "file:payloads/session/turn-0001/llm-response.sse.gz",
		Class:            ClassLLMResponse,
		Availability:     AvailabilitySourceEmpty,
		HashDomain:       HashRawBytes,
		Selector:         Selector{URI: "file:///repo/payloads/session/turn-0001/llm-response.sse.gz"},
		Bytes:            0,
		Chars:            -1,
		ComputedSHA256:   EmptySHA256,
	}

	if err := artifact.Validate(); err != nil {
		t.Fatalf("empty raw artifact should be valid: %v", err)
	}
}

func TestSourceCorruptArtifactRequiresIntegrityFailures(t *testing.T) {
	t.Parallel()

	artifact := Artifact{
		SchemaVersion:    1,
		Adapter:          "aiagent_v3",
		SourceID:         "aiagent_v3:testdata",
		SourceFile:       "payloads/session/turn-0001/llm-response.sse.gz",
		NativeSessionID:  "session-1",
		NativeTurnID:     "turn:1",
		NativeArtifactID: "file:payloads/session/turn-0001/llm-response.sse.gz",
		Class:            ClassLLMResponse,
		Availability:     AvailabilitySourceCorrupt,
		HashDomain:       HashRawBytes,
		Selector:         Selector{URI: "file:///repo/payloads/session/turn-0001/llm-response.sse.gz"},
		Bytes:            13,
		Chars:            -1,
		ComputedSHA256:   "315f5bdb76d078c43b8ac0064e4a0164612b1fce77c869345bfc94c75894edd3",
		ProducerSHA256:   "producer-hash",
	}
	if err := artifact.Validate(); err == nil {
		t.Fatal("source_corrupt artifact without integrity failures validated successfully")
	}

	artifact.IntegrityFailures = []IntegrityFailure{{
		Field:    "sha256",
		Expected: "producer-hash",
		Actual:   artifact.ComputedSHA256,
	}}
	if err := artifact.Validate(); err != nil {
		t.Fatalf("source_corrupt artifact with integrity failures should be valid: %v", err)
	}
}
