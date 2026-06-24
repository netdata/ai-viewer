package parity

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const parityFuzzMaxInputBytes = 64 * 1024

func FuzzDiffManifests(f *testing.F) {
	matching, err := json.Marshal([]Artifact{parityFuzzArtifact()})
	if err != nil {
		f.Fatalf("marshal matching seed: %v", err)
	}
	sourceCorruptWithoutFailure, err := json.Marshal([]Artifact{parityFuzzSourceCorruptWithoutFailure()})
	if err != nil {
		f.Fatalf("marshal corrupt seed: %v", err)
	}

	f.Add(matching, matching)
	f.Add([]byte(`[]`), []byte(`[]`))
	f.Add([]byte(`[{}]`), []byte(`[]`))
	f.Add(sourceCorruptWithoutFailure, []byte(`[]`))
	f.Add([]byte(`not-json`), []byte(`[]`))

	f.Fuzz(func(t *testing.T, sourceRaw []byte, canonicalRaw []byte) {
		if len(sourceRaw) > parityFuzzMaxInputBytes || len(canonicalRaw) > parityFuzzMaxInputBytes {
			return
		}
		source, sourceOK := decodeParityFuzzManifest(sourceRaw)
		canonical, canonicalOK := decodeParityFuzzManifest(canonicalRaw)
		if !sourceOK || !canonicalOK {
			return
		}

		hasInvalidArtifact := parityFuzzManifestHasInvalidArtifact(source) || parityFuzzManifestHasInvalidArtifact(canonical)
		result, err := DiffContextCapped(context.Background(), source, canonical, 8)
		if err != nil {
			t.Fatalf("DiffContextCapped: %v", err)
		}
		if hasInvalidArtifact && result.State == StatePass {
			t.Fatalf("diff passed with invalid artifact: source=%+v canonical=%+v", source, canonical)
		}
	})
}

func FuzzExtractAIAgentV2Source(f *testing.F) {
	f.Add([]byte(`{"version":2,"opTree":{"traceId":"root-session","startedAt":1700000000000}}`))
	f.Add([]byte(`{"version":2,"opTree":{"traceId":"root-session","startedAt":1700000000000,"turns":[]}}`))
	f.Add([]byte(`{not-json`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > parityFuzzMaxInputBytes {
			return
		}
		root := t.TempDir()
		writeParityFuzzGzipFile(t, filepath.Join(root, "root-session.json.gz"), raw)

		artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
			Root:     root,
			SourceID: "aiagent_v2:fuzz",
		})
		assertParityFuzzExtraction(t, artifacts, err)
	})
}

func FuzzExtractAIAgentV3Source(f *testing.F) {
	f.Add([]byte(joinJSONLLines([]string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	})))
	f.Add([]byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session"}` + "\n"))
	f.Add([]byte(`{not-json`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > parityFuzzMaxInputBytes {
			return
		}
		root := t.TempDir()
		ledger := filepath.Join(root, "session", "root-session.jsonl")
		writeParityFuzzFile(t, ledger, raw)

		artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
			Root:     root,
			SourceID: "aiagent_v3:fuzz",
		})
		assertParityFuzzExtraction(t, artifacts, err)
	})
}

func FuzzExtractClaudeCodeSource(f *testing.F) {
	f.Add([]byte(joinJSONLLines([]string{
		`{"uuid":"u1","sessionId":"session-1","timestamp":"2026-06-22T00:00:00.000Z","type":"user","message":{"role":"user","content":"hello"}}`,
		`{"uuid":"u2","sessionId":"session-1","timestamp":"2026-06-22T00:00:01.000Z","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
	})))
	f.Add([]byte(`{"uuid":"u1","timestamp":"2026-06-22T00:00:00.000Z","type":"user","message":{"role":"user","content":"hello"}}` + "\n"))
	f.Add([]byte(`{not-json`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > parityFuzzMaxInputBytes {
			return
		}
		root := t.TempDir()
		transcript := filepath.Join(root, "project", "session-1.jsonl")
		writeParityFuzzFile(t, transcript, raw)

		artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
			Root:     root,
			SourceID: "claude-code:fuzz",
		})
		assertParityFuzzExtraction(t, artifacts, err)
	})
}

func FuzzExtractCodexSource(f *testing.F) {
	f.Add([]byte(joinJSONLLines([]string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1","timestamp":"2026-06-22T00:00:00Z"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
	})))
	f.Add([]byte(`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1"}}` + "\n"))
	f.Add([]byte(`{not-json`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > parityFuzzMaxInputBytes {
			return
		}
		root := t.TempDir()
		rollout := filepath.Join(root, "2026", "06", "23", "rollout-fuzz.jsonl")
		writeParityFuzzFile(t, rollout, raw)

		artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
			Root:     root,
			SourceID: "codex:fuzz",
		})
		assertParityFuzzExtraction(t, artifacts, err)
	})
}

func FuzzExtractOpencodeSource(f *testing.F) {
	f.Add([]byte("SQLite format 3\x00"))
	f.Add([]byte(`{not-sqlite`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > parityFuzzMaxInputBytes {
			return
		}
		dbPath := filepath.Join(t.TempDir(), "opencode.db")
		writeParityFuzzFile(t, dbPath, raw)

		artifacts, err := ExtractOpencodeSource(context.Background(), OpencodeSourceOptions{
			DBPath:   dbPath,
			SourceID: "opencode:fuzz",
		})
		assertParityFuzzExtraction(t, artifacts, err)
	})
}

func decodeParityFuzzManifest(raw []byte) ([]Artifact, bool) {
	var artifacts []Artifact
	if err := json.Unmarshal(raw, &artifacts); err != nil {
		return nil, false
	}
	return artifacts, true
}

func parityFuzzManifestHasInvalidArtifact(artifacts []Artifact) bool {
	for _, artifact := range artifacts {
		if parityFuzzArtifactInvalid(artifact) {
			return true
		}
	}
	return false
}

func parityFuzzArtifactInvalid(artifact Artifact) bool {
	if err := artifact.Validate(); err != nil {
		return true
	}
	return validateArtifactAgainstMatrix(artifact) != nil
}

func parityFuzzArtifact() Artifact {
	return Artifact{
		SchemaVersion:      SchemaVersion,
		Adapter:            "codex",
		SourceID:           "codex:fuzz",
		CanonicalSessionID: "codex:fuzz:session-1",
		NativeSessionID:    "session-1",
		NativeArtifactID:   "session:session-1",
		Class:              ClassSessionBoundary,
		Availability:       AvailabilityAvailable,
		HashDomain:         HashIdentityJSON,
		Selector:           Selector{URI: "fuzz://session/session-1"},
		ComputedSHA256:     EmptySHA256,
	}
}

func parityFuzzSourceCorruptWithoutFailure() Artifact {
	artifact := parityFuzzArtifact()
	artifact.Class = ClassSourceCorruption
	artifact.Availability = AvailabilitySourceCorrupt
	artifact.NativeArtifactID = "source_corruption:file:fuzz"
	artifact.IntegrityFailures = nil
	return artifact
}

func assertParityFuzzExtraction(t *testing.T, artifacts []Artifact, _ error) {
	t.Helper()

	for i, artifact := range artifacts {
		if err := artifact.Validate(); err != nil {
			t.Fatalf("artifact[%d] failed validation: %v: %+v", i, err, artifact)
		}
		if err := validateArtifactAgainstMatrix(artifact); err != nil {
			t.Fatalf("artifact[%d] failed matrix validation: %v: %+v", i, err, artifact)
		}
	}
}

func writeParityFuzzFile(t *testing.T, path string, raw []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeParityFuzzGzipFile(t *testing.T, path string, raw []byte) {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	writeParityFuzzFile(t, path, buf.Bytes())
}
