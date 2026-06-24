package parity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractAIAgentV3SourcePayloadArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	sdkRequestRaw := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	sdkResponseRaw := []byte(`{"id":"msg_1","content":"hello"}`)
	reasoningRaw := []byte("think")
	toolRequestRaw := []byte(`{"path":"README.md"}`)
	sdkRequestRel := "payloads/root-session/turn-0001/sdk-request.json.gz"
	sdkResponseRel := "payloads/root-session/turn-0001/sdk-response.json.gz"
	reasoningRel := "payloads/root-session/turn-0001/reasoning.txt.gz"
	toolRequestRel := "payloads/root-session/turn-0001/tool-request.json.gz"
	sdkRequestPath, sdkRequestHash := writeAIAgentV3GzipPayload(t, root, sdkRequestRel, sdkRequestRaw)
	sdkResponsePath, sdkResponseHash := writeAIAgentV3GzipPayload(t, root, sdkResponseRel, sdkResponseRaw)
	reasoningPath, reasoningHash := writeAIAgentV3GzipPayload(t, root, reasoningRel, reasoningRaw)
	toolRequestPath, toolRequestHash := writeAIAgentV3GzipPayload(t, root, toolRequestRel, toolRequestRaw)

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		fmt.Sprintf(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","payloadRefs":[%s,%s,%s]},{"opId":"tool-1","opIndex":2,"kind":"tool","name":"read_file","provider":"filesystem","status":"ok","startedAt":"2026-06-22T00:00:03.000Z","endedAt":"2026-06-22T00:00:04.000Z","payloadRefs":[%s,{"kind":"tool_response","opId":"tool-1","turn":1,"opIndex":2,"format":"json","captured":false,"truncated":false,"redacted":false}]}]}`,
			aiAgentV3PayloadRefJSON("sdk_request", "llm-1", 1, sdkRequestRel, len(sdkRequestRaw), compressedAIAgentV3PayloadSize(sdkRequestPath), sdkRequestHash),
			aiAgentV3PayloadRefJSON("sdk_response", "llm-1", 1, sdkResponseRel, len(sdkResponseRaw), compressedAIAgentV3PayloadSize(sdkResponsePath), sdkResponseHash),
			aiAgentV3PayloadRefJSON("reasoning_stream", "llm-1", 1, reasoningRel, len(reasoningRaw), compressedAIAgentV3PayloadSize(reasoningPath), reasoningHash),
			aiAgentV3PayloadRefJSON("tool_request", "tool-1", 2, toolRequestRel, len(toolRequestRaw), compressedAIAgentV3PayloadSize(toolRequestPath), toolRequestHash),
		),
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	assertAIAgentV3PayloadArtifact(t, artifacts, ClassLLMSDKRequest, "file:"+sdkRequestRel, sdkRequestPath, int64(len(sdkRequestRaw)), sdkRequestHash, HashRawBytes, -1)
	assertAIAgentV3PayloadArtifact(t, artifacts, ClassLLMSDKResponse, "file:"+sdkResponseRel, sdkResponsePath, int64(len(sdkResponseRaw)), sdkResponseHash, HashRawBytes, -1)
	assertAIAgentV3PayloadArtifact(t, artifacts, ClassReasoningText, "file:"+reasoningRel, reasoningPath, int64(len(reasoningRaw)), reasoningHash, HashSemanticText, int64(len(reasoningRaw)))
	assertAIAgentV3PayloadArtifact(t, artifacts, ClassToolRequest, "file:"+toolRequestRel, toolRequestPath, int64(len(toolRequestRaw)), toolRequestHash, HashRawBytes, -1)

	uncaptured := findArtifact(t, artifacts, ClassToolResponse, "op:1:2:payload:tool_response:1")
	if uncaptured.Availability != AvailabilitySourceUnavailable {
		t.Fatalf("uncaptured availability = %q, want %q", uncaptured.Availability, AvailabilitySourceUnavailable)
	}
}

func TestExtractAIAgentV3SourceEmptyPayloadArtifact(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	responseRel := "payloads/root-session/turn-0001/empty-response.sse.gz"
	responsePath, responseHash := writeAIAgentV3GzipPayload(t, root, responseRel, nil)
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		fmt.Sprintf(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","payloadRefs":[%s]}]}`,
			aiAgentV3PayloadRefJSON("llm_response", "llm-1", 1, responseRel, 0, compressedAIAgentV3PayloadSize(responsePath), responseHash),
		),
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "file:"+responseRel)
	if got.Availability != AvailabilitySourceEmpty {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilitySourceEmpty)
	}
	if got.Bytes != 0 || got.Chars != -1 || got.ComputedSHA256 != EmptySHA256 {
		t.Fatalf("empty raw payload proof mismatch: %+v", got)
	}
}

func TestExtractAIAgentV3SourceUncapturedPayloadUsesEnclosingOpIndex(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":2}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":2,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"},{"opId":"tool-output-1","opIndex":2,"kind":"session","name":"tool_output","provider":"tool-output","status":"ok","startedAt":"2026-06-22T00:00:03.000Z","endedAt":"2026-06-22T00:00:03.500Z"},{"opId":"tool-output-2","opIndex":3,"kind":"session","name":"tool_output","provider":"tool-output","status":"ok","startedAt":"2026-06-22T00:00:03.500Z","endedAt":"2026-06-22T00:00:04.000Z"},{"opId":"llm-2","opIndex":4,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:04.000Z","endedAt":"2026-06-22T00:00:05.000Z","payloadRefs":[{"kind":"llm_response","opId":"llm-2","turn":2,"opIndex":2,"format":"sse","captured":false,"truncated":false,"redacted":false}]}]}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "op:2:4:payload:llm_response:1")
	if got.Availability != AvailabilitySourceUnavailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilitySourceUnavailable)
	}
	for _, artifact := range artifacts {
		if artifact.Class == ClassLLMResponse && artifact.NativeArtifactID == "op:2:2:payload:llm_response:1" {
			t.Fatalf("uncaptured payload used ref opIndex instead of enclosing op index: %+v", artifact)
		}
	}
}

func TestExtractAIAgentV3SourcePayloadHashMismatchIsSourceCorrupt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	responseRaw := []byte(`{"id":"msg_current","content":"changed after ledger"}`)
	responseRel := "payloads/root-session/turn-0001/llm-response.sse.gz"
	responsePath, responseHash := writeAIAgentV3GzipPayload(t, root, responseRel, responseRaw)
	staleHash := strings.Repeat("0", 64)
	if staleHash == responseHash {
		staleHash = strings.Repeat("1", 64)
	}

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		fmt.Sprintf(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","payloadRefs":[%s]}]}`,
			aiAgentV3PayloadRefJSON("llm_response", "llm-1", 1, responseRel, len(responseRaw), compressedAIAgentV3PayloadSize(responsePath), staleHash),
		),
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "file:"+responseRel)
	if got.Selector.URI != (&url.URL{Scheme: "file", Path: responsePath}).String() {
		t.Fatalf("selector.uri = %q, want payload file URI", got.Selector.URI)
	}
	if got.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilitySourceCorrupt)
	}
	if got.Bytes != int64(len(responseRaw)) || got.ComputedSHA256 != responseHash || got.ProducerSHA256 != staleHash {
		t.Fatalf("payload proof mismatch: %+v", got)
	}
	assertIntegrityFailures(t, got, []IntegrityFailure{{
		Field:    "sha256",
		Expected: staleHash,
		Actual:   responseHash,
	}})
}

func TestExtractAIAgentV3SourcePayloadByteCountMismatchIsSourceCorrupt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	requestRaw := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	responseRaw := []byte(`{"id":"msg_current","content":"unchanged hash"}`)
	requestRel := "payloads/root-session/turn-0001/llm-request.json.gz"
	responseRel := "payloads/root-session/turn-0001/llm-response.sse.gz"
	requestPath, requestHash := writeAIAgentV3GzipPayload(t, root, requestRel, requestRaw)
	responsePath, responseHash := writeAIAgentV3GzipPayload(t, root, responseRel, responseRaw)

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		fmt.Sprintf(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","payloadRefs":[%s,%s]}]}`,
			aiAgentV3PayloadRefJSON("llm_request", "llm-1", 1, requestRel, len(requestRaw)+7, compressedAIAgentV3PayloadSize(requestPath), requestHash),
			aiAgentV3PayloadRefJSON("llm_response", "llm-1", 1, responseRel, len(responseRaw), compressedAIAgentV3PayloadSize(responsePath)+7, responseHash),
		),
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	request := findArtifact(t, artifacts, ClassLLMRequest, "file:"+requestRel)
	if request.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("request availability = %q, want %q", request.Availability, AvailabilitySourceCorrupt)
	}
	if request.Bytes != int64(len(requestRaw)) || request.ComputedSHA256 != requestHash || request.ProducerSHA256 != requestHash {
		t.Fatalf("request proof mismatch: %+v", request)
	}
	assertIntegrityFailures(t, request, []IntegrityFailure{{
		Field:    "original_bytes",
		Expected: fmt.Sprintf("%d", len(requestRaw)+7),
		Actual:   fmt.Sprintf("%d", len(requestRaw)),
	}})
	response := findArtifact(t, artifacts, ClassLLMResponse, "file:"+responseRel)
	if response.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("response availability = %q, want %q", response.Availability, AvailabilitySourceCorrupt)
	}
	if response.Bytes != int64(len(responseRaw)) || response.ComputedSHA256 != responseHash || response.ProducerSHA256 != responseHash {
		t.Fatalf("response proof mismatch: %+v", response)
	}
	assertIntegrityFailures(t, response, []IntegrityFailure{{
		Field:    "compressed_bytes",
		Expected: fmt.Sprintf("%d", compressedAIAgentV3PayloadSize(responsePath)+7),
		Actual:   fmt.Sprintf("%d", compressedAIAgentV3PayloadSize(responsePath)),
	}})
}

func TestAIAgentV3PayloadArtifactExceedingCapReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	relPath := "payloads/root-session/turn-0001/request.txt"
	absPath := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		t.Fatalf("mkdir payload dir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("123456789"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}

	state := newAIAgentV3SourceState(root, "aiagent_v3:"+root, filepath.Join(root, "session", "root-session.jsonl"))
	state.nativeSessionID = "root-session"
	ref := aiAgentV3PayloadRef{
		Kind:     "llm_request",
		Turn:     1,
		OpIndex:  1,
		Format:   "text",
		Path:     relPath,
		Captured: true,
	}

	_, err := state.aiAgentV3PayloadArtifactWithLimit(1, 1, ref, 1, 8)
	if err == nil {
		t.Fatal("aiAgentV3PayloadArtifactWithLimit returned nil error for payload above cap")
	}
	if !strings.Contains(err.Error(), "payload_ref exceeds 8 bytes") {
		t.Fatalf("error = %q, want payload cap error", err)
	}
}

func TestExtractAIAgentV3SourceRefusesLedgerSymlinkEscape(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	outsideFile := filepath.Join(t.TempDir(), "escaped.jsonl")
	if err := os.WriteFile(outsideFile, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatalf("write escaped target: %v", err)
	}
	escapeLink := filepath.Join(sessionDir, "escaped.jsonl")
	if err := os.Symlink(outsideFile, escapeLink); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	_, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err == nil {
		t.Fatal("ExtractAIAgentV3Source with symlink escape = nil error, want fail-closed error")
	}
	if !strings.Contains(err.Error(), "symlink escape") {
		t.Fatalf("ExtractAIAgentV3Source error = %v, want symlink escape", err)
	}
}

func TestExtractAIAgentV3SourceUnknownRecordTypeReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	line := `{"version":3,"recordType":"future_artifact","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","payload":{"text":"must not be ignored"}}`
	if err := os.WriteFile(sessionFile, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	_, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err == nil {
		t.Fatal("ExtractAIAgentV3Source succeeded, want unknown record type error")
	}
	if !strings.Contains(err.Error(), `unknown aiagent_v3 record type "future_artifact"`) {
		t.Fatalf("ExtractAIAgentV3Source error = %v", err)
	}
}

func TestExtractAIAgentV3SourceStructuralArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"},{"opId":"tool-1","opIndex":2,"kind":"tool","name":"read_file","provider":"filesystem","status":"failed","startedAt":"2026-06-22T00:00:03.000Z","endedAt":"2026-06-22T00:00:04.000Z","error":"permission denied"},{"opId":"session-1","opIndex":3,"kind":"session","name":"delegate","provider":"sub-agent","status":"running","startedAt":"2026-06-22T00:00:04.000Z","endedAt":"2026-06-22T00:00:05.000Z","childSessions":[{"sessionId":"child-1","originId":"root-session","parentSessionId":"root-session","parentOpId":"session-1","ledgerPath":"session/child-1.jsonl","status":"ok"},{"sessionId":"child-2","originId":"root-session","parentSessionId":"root-session","parentOpId":"session-1","ledgerPath":"session/child-2.jsonl","status":"ok"}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	startSession := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:00.000Z")
	startTurn := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:01.000Z")
	llmStart := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:02.000Z")
	llmEnd := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:03.000Z")
	toolStart := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:03.000Z")
	toolEnd := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:04.000Z")
	sessionOpStart := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:04.000Z")
	turnEnd := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:05.000Z")
	sessionEnd := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:06.000Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:root-session"), sessionBoundaryIdentity{
		NativeSessionID:     "root-session",
		RootNativeSessionID: "root-session",
		Kind:                "root",
		Status:              "completed",
		StartedAt:           startSession,
		EndedAt:             &sessionEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       startTurn,
		EndedAt:         &turnEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       llmStart,
		EndedAt:         &llmEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "tool",
		Name:            "read_file",
		ToolNamespace:   "filesystem",
		Status:          "failed",
		StartedAt:       toolStart,
		EndedAt:         &toolEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "session",
		Name:            "delegate",
		Status:          "running",
		StartedAt:       sessionOpStart,
		EndedAt:         &turnEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSubagentLink, "op:1:3:child_session:child-1"), subagentLinkIdentity{
		ParentNativeSessionID: "root-session",
		ParentTurnSeq:         1,
		ParentOpSeq:           3,
		ChildNativeSessionID:  "child-1",
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSubagentLink, "op:1:3:child_session:child-2"), subagentLinkIdentity{
		ParentNativeSessionID: "root-session",
		ParentTurnSeq:         1,
		ParentOpSeq:           3,
		ChildNativeSessionID:  "child-2",
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	})
	child1Boundary := findArtifact(t, artifacts, ClassSessionBoundary, "session:child-1")
	if child1Boundary.Availability != AvailabilityPartialSource {
		t.Fatalf("child-1 boundary availability = %q, want %q", child1Boundary.Availability, AvailabilityPartialSource)
	}
	assertIdentityArtifactWithAvailability(t, child1Boundary, AvailabilityPartialSource, sessionBoundaryIdentity{
		NativeSessionID:       "child-1",
		ParentNativeSessionID: "root-session",
		RootNativeSessionID:   "root-session",
		Kind:                  "sub_agent",
		Status:                "running",
		StartedAt:             turnEnd,
	})
	child2Boundary := findArtifact(t, artifacts, ClassSessionBoundary, "session:child-2")
	if child2Boundary.Availability != AvailabilityPartialSource {
		t.Fatalf("child-2 boundary availability = %q, want %q", child2Boundary.Availability, AvailabilityPartialSource)
	}
	assertIdentityArtifactWithAvailability(t, child2Boundary, AvailabilityPartialSource, sessionBoundaryIdentity{
		NativeSessionID:       "child-2",
		ParentNativeSessionID: "root-session",
		RootNativeSessionID:   "root-session",
		Kind:                  "sub_agent",
		Status:                "running",
		StartedAt:             turnEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:2:error"), opErrorIdentity{
		NativeSessionID:    "root-session",
		TurnSeq:            1,
		OpSeq:              2,
		OpKind:             "tool",
		ErrorClass:         "",
		ErrorMessageSHA256: stringSHA256("permission denied"),
	})
}

func TestExtractAIAgentV3SourceToWriterMatchesSliceExtractor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := filepath.Join(root, "session", "b-session.jsonl")
	second := filepath.Join(root, "session", "a-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(first), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	firstLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"b-session","headendId":"cli","capturePayloads":false}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"b-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:03.000Z","originId":"root-session","sessionId":"b-session","turn":1,"status":"ok","ops":[{"opId":"tool-1","opIndex":1,"kind":"tool","name":"read_file","provider":"filesystem","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:04.000Z","originId":"root-session","sessionId":"b-session","status":"ok"}`,
	}
	secondLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:10.000Z","originId":"root-session","sessionId":"a-session","headendId":"cli","capturePayloads":false}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:11.000Z","originId":"root-session","sessionId":"a-session","status":"ok"}`,
	}
	if err := os.WriteFile(first, []byte(joinJSONLLines(firstLines)), 0o600); err != nil {
		t.Fatalf("write first ledger: %v", err)
	}
	if err := os.WriteFile(second, []byte(joinJSONLLines(secondLines)), 0o600); err != nil {
		t.Fatalf("write second ledger: %v", err)
	}

	opts := AIAgentV3SourceOptions{Root: root, SourceID: "aiagent_v3:" + root}
	want, err := ExtractAIAgentV3Source(context.Background(), opts)
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	var got []Artifact
	err = ExtractAIAgentV3SourceToWriter(context.Background(), opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("ExtractAIAgentV3SourceToWriter: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed aiagent_v3 artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestExtractAIAgentV3SourceSystemOpArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"system-1","opIndex":1,"kind":"system","name":"maintenance","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	startedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:02.000Z")
	endedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:03.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "op:1:1:system"), systemOpIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           1,
		OpKind:          "system",
		Name:            "maintenance",
		Status:          "completed",
		StartedAt:       startedAt,
		EndedAt:         &endedAt,
	})
}

func TestExtractAIAgentV3SourceCompactionEventArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":2}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":2,"status":"ok","ops":[{"opId":"compact-1","opIndex":3,"kind":"session","name":"history_compaction.turn_summarizer","provider":"history-compaction","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","childSessions":[{"sessionId":"compact-child","originId":"root-session","parentSessionId":"root-session","parentOpId":"compact-1","ledgerPath":"session/compact-child.jsonl","status":"ok"}],"attributes":{"archivedTurn":1,"currentTurn":2}}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	startedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:02.000Z")
	endedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:03.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:2:3:compaction"), aiAgentV3CompactionEventIdentity{
		NativeSessionID:      "root-session",
		TurnSeq:              2,
		OpSeq:                3,
		Trigger:              "history_compaction",
		Name:                 "history_compaction.turn_summarizer",
		Provider:             "history-compaction",
		ChildNativeSessionID: "compact-child",
		ArchivedTurn:         1,
		CurrentTurn:          2,
		Status:               "completed",
		StartedAt:            startedAt,
		EndedAt:              &endedAt,
	})
}

func TestExtractAIAgentV3SourceParentSideLineageEnrichesRealChild(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	rootFile := filepath.Join(sessionDir, "root-session.jsonl")
	rootLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:02.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"parent-op-1","opIndex":1,"kind":"session","name":"child-agent","provider":"agent","status":"ok","startedAt":"2026-06-22T00:00:01.100Z","endedAt":"2026-06-22T00:00:01.900Z","childSessions":[{"sessionId":"child-session","originId":"root-session","parentSessionId":"root-session","parentOpId":"parent-op-1","ledgerPath":"session/child-session.jsonl","status":"ok"}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:03.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(rootFile, []byte(joinJSONLLines(rootLines)), 0o600); err != nil {
		t.Fatalf("write root ledger: %v", err)
	}
	childFile := filepath.Join(sessionDir, "child-session.jsonl")
	childLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:01.200Z","originId":"root-session","sessionId":"child-session","agentId":"child-agent","callPath":"root-agent:child-agent","headendId":"sub-agent","capturePayloads":true,"attributes":{"ledgerPath":"session/child-session.jsonl"}}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:02.500Z","originId":"root-session","sessionId":"child-session","status":"ok"}`,
	}
	if err := os.WriteFile(childFile, []byte(joinJSONLLines(childLines)), 0o600); err != nil {
		t.Fatalf("write child ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	startedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:01.200Z")
	endedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:02.500Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:child-session"), sessionBoundaryIdentity{
		NativeSessionID:       "child-session",
		ParentNativeSessionID: "root-session",
		RootNativeSessionID:   "root-session",
		Kind:                  "sub_agent",
		Status:                "completed",
		StartedAt:             startedAt,
		EndedAt:               &endedAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:child-session:metadata"), aiAgentV3SessionMetadataIdentity{
		NativeSessionID: "child-session",
		OriginID:        "root-session",
		AgentID:         "child-agent",
		CallPath:        "root-agent:child-agent",
		HeadendID:       "sub-agent",
		CapturePayloads: true,
		Attributes: map[string]any{
			"ledgerPath": "session/child-session.jsonl",
		},
	})
}

func TestExtractAIAgentV3SourceSessionMetadataArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "child-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"child-session","parentSessionId":"root-session","parentOpId":"op-parent-1","agentId":"child-agent","callPath":"root-agent:child-agent","headendId":"sub-agent","capturePayloads":false,"attributes":{"ledgerPath":"session/child-session.jsonl","priority":2,"nested":{"enabled":true}}}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"child-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:child-session:metadata"), aiAgentV3SessionMetadataIdentity{
		NativeSessionID:       "child-session",
		OriginID:              "root-session",
		AgentID:               "child-agent",
		CallPath:              "root-agent:child-agent",
		ParentNativeSessionID: "root-session",
		ParentOpID:            "op-parent-1",
		HeadendID:             "sub-agent",
		CapturePayloads:       false,
		Attributes: map[string]any{
			"ledgerPath": "session/child-session.jsonl",
			"nested": map[string]any{
				"enabled": true,
			},
			"priority": float64(2),
		},
	})
}

func TestExtractAIAgentV3SourceSessionErrorFinalizesFailedSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "failed-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"failed-session","agentId":"root-agent","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"session_error","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"failed-session","error":"boom"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	startedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:00.000Z")
	endedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:01.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:failed-session"), sessionBoundaryIdentity{
		NativeSessionID:     "failed-session",
		RootNativeSessionID: "root-session",
		Kind:                "root",
		Status:              "failed",
		StartedAt:           startedAt,
		EndedAt:             &endedAt,
	})
}

func TestAIAgentV3SourceStateSessionArtifactsWithoutSyntheticTracker(t *testing.T) {
	t.Parallel()

	state := newAIAgentV3SourceState("/tmp/aiagent-v3", "aiagent_v3:test-source", "/tmp/aiagent-v3/session/direct-session.jsonl")
	startedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:00.000Z")
	endedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:02.000Z")
	state.sessionStarted = true
	state.sessionStartedAt = startedAt
	state.sessionEndedAt = &endedAt
	state.sessionLineNo = 7
	state.nativeSessionID = "direct-session"
	state.rootNativeSessionID = "root-session"
	state.parentNativeSessionID = "parent-session"
	state.parentOpID = "op-parent"
	state.agentID = "agent-1"
	state.callPath = "root:agent-1"
	state.headendID = "sub-agent"
	state.capturePayloads = true
	state.sessionKind = "sub_agent"
	state.sessionStatus = "completed"
	state.sessionAttributes = map[string]json.RawMessage{
		"ledgerPath": json.RawMessage(`"session/direct-session.jsonl"`),
		"priority":   json.RawMessage(`3`),
	}

	boundary, err := state.aiAgentV3SessionBoundary()
	if err != nil {
		t.Fatalf("aiAgentV3SessionBoundary: %v", err)
	}
	assertIdentityArtifact(t, boundary, sessionBoundaryIdentity{
		NativeSessionID:       "direct-session",
		ParentNativeSessionID: "parent-session",
		RootNativeSessionID:   "root-session",
		Kind:                  "sub_agent",
		Status:                "completed",
		StartedAt:             startedAt,
		EndedAt:               &endedAt,
	})

	metadata, err := state.aiAgentV3SessionMetadata()
	if err != nil {
		t.Fatalf("aiAgentV3SessionMetadata: %v", err)
	}
	assertIdentityArtifact(t, metadata, aiAgentV3SessionMetadataIdentity{
		NativeSessionID:       "direct-session",
		OriginID:              "root-session",
		AgentID:               "agent-1",
		CallPath:              "root:agent-1",
		ParentNativeSessionID: "parent-session",
		ParentOpID:            "op-parent",
		HeadendID:             "sub-agent",
		CapturePayloads:       true,
		Attributes: map[string]any{
			"ledgerPath": "session/direct-session.jsonl",
			"priority":   float64(3),
		},
	})
}

func TestAIAgentV3SyntheticSessionCandidateMergesMissingLineage(t *testing.T) {
	t.Parallel()

	candidate := aiAgentV3SyntheticSessionCandidate{
		sessionID:     "child-session",
		startedAt:     2000,
		rootSessionID: "child-session",
	}
	merged := candidate.withMissingFrom(aiAgentV3SyntheticSessionCandidate{
		parentSessionID: "parent-session",
		parentOpID:      "op-parent",
		rootSessionID:   "root-session",
		startedAt:       1000,
	})

	if merged.parentSessionID != "parent-session" ||
		merged.parentOpID != "op-parent" ||
		merged.rootSessionID != "root-session" ||
		merged.startedAt != 2000 {
		t.Fatalf("merged candidate = %+v, want missing lineage filled without changing timestamp", merged)
	}
}

func TestExtractAIAgentV3SourceToolOutputSessionKind(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "tool-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"tool-session","parentSessionId":"root-session","headendId":"tool_output","capturePayloads":true}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"tool-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	startedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:00.000Z")
	endedAt := mustAIAgentV3TestMicros(t, "2026-06-22T00:00:01.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:tool-session"), sessionBoundaryIdentity{
		NativeSessionID:       "tool-session",
		ParentNativeSessionID: "root-session",
		RootNativeSessionID:   "root-session",
		Kind:                  "tool_internal",
		Status:                "completed",
		StartedAt:             startedAt,
		EndedAt:               &endedAt,
	})
}

func TestExtractAIAgentV3SourceLogArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"failed","ops":[],"warnings":["slow request"],"errors":["provider failed"]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"failed","error":"session failed"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	artifacts, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{
		Root:     root,
		SourceID: "aiagent_v3:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}

	assertAIAgentV3LogArtifact(t, artifacts, root, "root-session", "turn:1", "seq:3:/warnings/0", "/warnings/0", "slow request")
	assertAIAgentV3LogArtifact(t, artifacts, root, "root-session", "turn:1", "seq:3:/errors/0", "/errors/0", "provider failed")
	assertAIAgentV3LogArtifact(t, artifacts, root, "root-session", "", "seq:4:/error", "/error", "session failed")
}

func writeAIAgentV3GzipPayload(t *testing.T, root string, relPath string, raw []byte) (string, string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir payload dir: %v", err)
	}
	if err := os.WriteFile(path, gzipBytes(t, raw), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	sum := sha256.Sum256(raw)
	return path, fmt.Sprintf("%x", sum)
}

func aiAgentV3PayloadRefJSON(kind string, opID string, opIndex int, path string, originalBytes int, compressedBytes int64, hash string) string {
	return fmt.Sprintf(`{"kind":%q,"opId":%q,"turn":1,"opIndex":%d,"format":"json","compression":"gzip","path":%q,"originalBytes":%d,"compressedBytes":%d,"sha256":%q,"captured":true,"truncated":false,"redacted":false}`,
		kind, opID, opIndex, path, originalBytes, compressedBytes, hash)
}

func compressedAIAgentV3PayloadSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func assertAIAgentV3PayloadArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, path string, bytesLen int64, hash string, domain HashDomain, chars int64) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "aiagent_v3" {
		t.Fatalf("adapter = %q, want aiagent_v3", got.Adapter)
	}
	if got.NativeSessionID != "root-session" {
		t.Fatalf("native_session_id = %q, want root-session", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: path}).String()
	if got.Selector.URI != wantURI {
		t.Fatalf("selector.uri = %q, want %q", got.Selector.URI, wantURI)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != domain {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, domain)
	}
	if got.Bytes != bytesLen || got.Chars != chars || got.ComputedSHA256 != hash || got.ProducerSHA256 != hash {
		t.Fatalf("payload proof mismatch: %+v", got)
	}
}

func assertAIAgentV3LogArtifact(t *testing.T, artifacts []Artifact, root string, sessionID string, turnID string, nativeID string, pointer string, message string) {
	t.Helper()

	got := findArtifact(t, artifacts, ClassLogEntry, nativeID)
	if got.Adapter != "aiagent_v3" {
		t.Fatalf("adapter = %q, want aiagent_v3", got.Adapter)
	}
	if got.NativeSessionID != sessionID {
		t.Fatalf("native_session_id = %q, want %q", got.NativeSessionID, sessionID)
	}
	if got.NativeTurnID != turnID {
		t.Fatalf("native_turn_id = %q, want %q", got.NativeTurnID, turnID)
	}
	seq := strings.Split(nativeID, ":")[1]
	wantURI := (&url.URL{
		Scheme:   "file",
		Path:     filepath.Join(root, "session", sessionID+".jsonl"),
		RawQuery: "seq=" + seq,
	}).String()
	if got.Selector.URI != wantURI {
		t.Fatalf("selector.uri = %q, want %q", got.Selector.URI, wantURI)
	}
	if got.Selector.JSONPointer != pointer {
		t.Fatalf("selector.json_pointer = %q, want %q", got.Selector.JSONPointer, pointer)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	if got.Bytes != int64(len(message)) || got.Chars != int64(len(message)) || got.ComputedSHA256 != stringSHA256(message) {
		t.Fatalf("log proof mismatch: %+v", got)
	}
}

func assertIdentityArtifactWithAvailability(t *testing.T, artifact Artifact, availability Availability, identity interface{}) {
	t.Helper()

	if artifact.Availability != availability {
		t.Fatalf("availability = %q, want %q", artifact.Availability, availability)
	}
	if artifact.HashDomain != HashIdentityJSON {
		t.Fatalf("hash_domain = %q, want %q", artifact.HashDomain, HashIdentityJSON)
	}
	identityBytes, err := canonicalIdentityBytes(identity)
	if err != nil {
		t.Fatalf("canonicalIdentityBytes: %v", err)
	}
	wantHash := sha256.Sum256(identityBytes)
	if artifact.Bytes != int64(len(identityBytes)) || artifact.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("identity proof mismatch: got bytes=%d hash=%s want bytes=%d hash=%x", artifact.Bytes, artifact.ComputedSHA256, len(identityBytes), wantHash)
	}
}

func mustAIAgentV3TestMicros(t *testing.T, timestamp string) int64 {
	t.Helper()

	tsUs, err := parseAIAgentV3Timestamp(timestamp)
	if err != nil {
		t.Fatalf("parseAIAgentV3Timestamp: %v", err)
	}
	return tsUs
}
