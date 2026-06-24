package parity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExtractCodexSourceFileHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "rollout.jsonl")
	line := `{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1"}}`
	if err := os.WriteFile(sessionFile, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := extractCodexSourceFile(ctx, sessionFile, "codex:"+root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("extractCodexSourceFile error = %v, want context.Canceled", err)
	}
}

func TestExtractCodexSourcePayloadArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "22", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1","timestamp":"2026-06-22T00:00:00Z"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
		`{"timestamp":"2026-06-22T00:00:02Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}}`,
		`{"timestamp":"2026-06-22T00:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"timestamp":"2026-06-22T00:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"done"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	assertCodexTextArtifact(t, artifacts, ClassAssistantMessage, "line:2:/payload/content/0/text", sessionFile, 2, "/payload/content/0/text", "hello")
	assertCodexTextArtifact(t, artifacts, ClassReasoningText, "line:3:/payload/summary/0/text", sessionFile, 3, "/payload/summary/0/text", "think")
	assertCodexTextArtifact(t, artifacts, ClassToolRequest, "line:4:/payload/arguments", sessionFile, 4, "/payload/arguments", `{"cmd":"ls"}`)
	assertCodexTextArtifact(t, artifacts, ClassToolResponse, "line:5:/payload/output", sessionFile, 5, "/payload/output", "done")
}

func TestCodexPointerArtifactsFromDecodedDocument(t *testing.T) {
	t.Parallel()

	state := newCodexSourceState("codex:test-source", "/tmp/rollout.jsonl", time.Unix(0, 0))
	state.nativeSessionID = "session-1"
	payload := json.RawMessage(`{"type":"tool_search_call","arguments":{"query":"q"},"content":[{"type":"output_text","text":"hello"}]}`)
	doc, err := decodeCodexPayloadDocument(payload)
	if err != nil {
		t.Fatalf("decodeCodexPayloadDocument: %v", err)
	}

	textArtifacts, err := codexPointerArtifactsFromDocument(&state, 12, ClassAssistantMessage, "/payload", doc, []string{"/payload/content/0/text"})
	if err != nil {
		t.Fatalf("codexPointerArtifactsFromDocument text: %v", err)
	}
	assertCodexTextArtifact(t, textArtifacts, ClassAssistantMessage, "line:12:/payload/content/0/text", state.sourceFile, 12, "/payload/content/0/text", "hello")

	jsonArtifacts, err := codexPointerArtifactsFromDocument(&state, 12, ClassToolRequest, "/payload", doc, []string{"/payload/arguments"})
	if err != nil {
		t.Fatalf("codexPointerArtifactsFromDocument json: %v", err)
	}
	assertCodexLineJSONArtifact(t, jsonArtifacts, ClassToolRequest, "line:12:/payload/arguments", state.sourceFile, 12, "/payload/arguments", `{"query":"q"}`)
}

func TestExtractCodexUserMessageEventArtifactsFromDocument(t *testing.T) {
	t.Parallel()

	state := &codexSourceState{
		sourceID:        "codex:test-source",
		sourceFile:      "/tmp/codex-rollout.jsonl",
		nativeSessionID: "codex-session",
	}
	document := map[string]interface{}{
		"message": "hello from user",
		"images":  []interface{}{"image-one"},
	}

	artifacts, err := extractCodexUserMessageEventArtifacts(state, 9, document)
	if err != nil {
		t.Fatalf("extractCodexUserMessageEventArtifacts: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %+v, want prompt and image", artifacts)
	}
	prompt := findArtifact(t, artifacts, ClassUserPrompt, "line:9:/payload/message")
	if prompt.NativeSessionID != "codex-session" || prompt.Selector.JSONPointer != "/payload/message" {
		t.Fatalf("prompt artifact scope/selector mismatch: %+v", prompt)
	}
	if prompt.HashDomain != HashSemanticText || prompt.ComputedSHA256 != stringSHA256("hello from user") {
		t.Fatalf("prompt proof mismatch: %+v", prompt)
	}
	image := findArtifact(t, artifacts, ClassUserImage, "line:9:/payload/images/0")
	if image.NativeSessionID != "codex-session" || image.Selector.JSONPointer != "/payload/images/0" {
		t.Fatalf("image artifact scope/selector mismatch: %+v", image)
	}
	if image.HashDomain != HashSemanticText || image.ComputedSHA256 != stringSHA256("image-one") {
		t.Fatalf("image proof mismatch: %+v", image)
	}
}

func TestExtractCodexMessageEventLogUsesPayloadOrFallback(t *testing.T) {
	t.Parallel()

	state := &codexSourceState{
		sourceID:        "codex:test-source",
		sourceFile:      "/tmp/codex-rollout.jsonl",
		nativeSessionID: "codex-session",
	}
	withMessage, err := extractCodexMessageEventLog(state, 10, 1000, map[string]interface{}{"message": "logged payload"}, "info")
	if err != nil {
		t.Fatalf("extractCodexMessageEventLog with message: %v", err)
	}
	gotMessage := findArtifact(t, withMessage, ClassLogEntry, "line:10:/payload/message")
	if gotMessage.HashDomain != HashSemanticText || gotMessage.ComputedSHA256 != stringSHA256("logged payload") {
		t.Fatalf("message log proof mismatch: %+v", gotMessage)
	}

	fallback, err := extractCodexMessageEventLog(state, 11, 1100, map[string]interface{}{}, "error")
	if err != nil {
		t.Fatalf("extractCodexMessageEventLog fallback: %v", err)
	}
	nativeID := expectedLogNativeArtifactID("session", 1100, "ERR", "codex", "error")
	gotFallback := findArtifact(t, fallback, ClassLogEntry, nativeID)
	if gotFallback.NativeSessionID != "codex-session" || gotFallback.ComputedSHA256 != stringSHA256("error") {
		t.Fatalf("fallback log mismatch: %+v", gotFallback)
	}
}

func TestCodexHighVolumePayloadRoutesAvoidTypedRedecode(t *testing.T) {
	t.Parallel()

	const sourceFile = "codex_source.go"
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, sourceFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", sourceFile, err)
	}

	for _, name := range []string{"extractCodexResponseItemArtifacts", "extractCodexEventMsgArtifacts"} {
		fn := findCodexSourceFunc(t, parsed, name)
		if count := countCodexCall(fn.Body, "decodeCodexPayloadDocument"); count != 1 {
			t.Fatalf("%s decodeCodexPayloadDocument calls = %d, want 1", name, count)
		}
		if count := countCodexCall(fn.Body, "decodeJSONPayload"); count != 0 {
			t.Fatalf("%s decodeJSONPayload calls = %d, want 0", name, count)
		}
	}
}

func TestExtractCodexSourceToWriterMatchesSliceExtractor(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "22", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-jsonl","timestamp":"2026-06-22T00:00:00Z"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write jsonl fixture: %v", err)
	}
	legacyFile := filepath.Join(root, "rollout-2025-06-26-11111111-2222-3333-4444-555555555555.json")
	legacyBody := `{"session":{"timestamp":"2025-06-26T00:00:00Z","id":"session-legacy"},"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`
	if err := os.WriteFile(legacyFile, []byte(legacyBody), 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}

	opts := CodexSourceOptions{Root: root, SourceID: "codex:" + root}
	want, err := ExtractCodexSource(context.Background(), opts)
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}

	var got []Artifact
	err = ExtractCodexSourceToWriter(context.Background(), opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("ExtractCodexSourceToWriter: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed codex artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestExtractCodexSourceFileScopeMirrorsProductionDiscovery(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	activeFile := filepath.Join(root, "2026", "06", "22", "rollout-2026-06-22T00-00-00-11111111-1111-1111-1111-111111111111.jsonl")
	ignoredFiles := []string{
		filepath.Join(root, "rollout-2026-06-22T00-00-00-root.jsonl"),
		filepath.Join(root, "2026", "06", "22", "not-a-rollout.jsonl"),
		filepath.Join(root, "scratch", "rollout-2026-06-22T00-00-00-scratch.jsonl"),
		filepath.Join(root, "archived_sessions", "2026", "06", "22", "rollout-2026-06-22T00-00-00-archived.jsonl"),
	}
	legacyFile := filepath.Join(root, "rollout-2025-06-26-11111111-2222-3333-4444-555555555555.json")

	if err := os.MkdirAll(filepath.Dir(activeFile), 0o700); err != nil {
		t.Fatalf("mkdir active fixture: %v", err)
	}
	activeLine := `{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"active-session","timestamp":"2026-06-22T00:00:00Z"}}`
	if err := os.WriteFile(activeFile, []byte(activeLine+"\n"), 0o600); err != nil {
		t.Fatalf("write active fixture: %v", err)
	}
	for _, ignoredFile := range ignoredFiles {
		if err := os.MkdirAll(filepath.Dir(ignoredFile), 0o700); err != nil {
			t.Fatalf("mkdir ignored fixture: %v", err)
		}
		if err := os.WriteFile(ignoredFile, []byte("{not-json\n"), 0o600); err != nil {
			t.Fatalf("write ignored fixture: %v", err)
		}
	}
	legacyBody := `{"session":{"timestamp":"2025-06-26T00:00:00Z","id":"legacy-session"},"items":[]}`
	if err := os.WriteFile(legacyFile, []byte(legacyBody), 0o600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	findArtifact(t, artifacts, ClassSessionBoundary, "session:active-session")
	findArtifact(t, artifacts, ClassSessionBoundary, "session:legacy-session")
	for _, artifact := range artifacts {
		if strings.Contains(artifact.SourceFile, "archived_sessions") ||
			strings.Contains(artifact.SourceFile, "scratch") ||
			strings.Contains(artifact.SourceFile, "not-a-rollout") ||
			strings.Contains(artifact.SourceFile, "root.jsonl") {
			t.Fatalf("artifact from ignored codex source file: %+v", artifact)
		}
	}
}

func TestExtractCodexSourceRefusesSymlinkEscape(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	shardDir := filepath.Join(root, "2026", "06", "22")
	if err := os.MkdirAll(shardDir, 0o700); err != nil {
		t.Fatalf("mkdir shard fixture: %v", err)
	}
	activeFile := filepath.Join(shardDir, "rollout-2026-06-22T00-00-00-11111111-1111-1111-1111-111111111111.jsonl")
	activeLine := `{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"active-session","timestamp":"2026-06-22T00:00:00Z"}}`
	if err := os.WriteFile(activeFile, []byte(activeLine+"\n"), 0o600); err != nil {
		t.Fatalf("write active fixture: %v", err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "escaped.jsonl")
	if err := os.WriteFile(outsideFile, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatalf("write escaped target: %v", err)
	}
	escapeLink := filepath.Join(shardDir, "rollout-2026-06-22T00-00-01-22222222-2222-2222-2222-222222222222.jsonl")
	if err := os.Symlink(outsideFile, escapeLink); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err == nil {
		t.Fatal("extract codex source with symlink escape = nil error, want fail-closed error")
	}
	if !strings.Contains(err.Error(), "symlink escape") {
		t.Fatalf("extract codex source error = %v, want symlink escape", err)
	}
	findArtifact(t, artifacts, ClassSessionBoundary, "session:active-session")
	for _, artifact := range artifacts {
		if strings.Contains(artifact.SourceFile, "escaped.jsonl") ||
			strings.Contains(artifact.SourceFile, filepath.Base(escapeLink)) {
			t.Fatalf("artifact from symlink escape: %+v", artifact)
		}
	}
}

func findCodexSourceFunc(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

func countCodexCall(body *ast.BlockStmt, name string) int {
	count := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == name {
			count++
		}
		return true
	})
	return count
}

func TestExtractCodexSourceDirectResponseItemArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "04", "direct-response-items")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","id":"session-1","source":"exec"}`,
		`{"timestamp":"2026-07-04T00:00:01Z","type":"turn_context","turn_id":"turn-1","model":"gpt-5.1-codex-max"}`,
		`{"timestamp":"2026-07-04T00:00:02Z","type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]}`,
		`{"timestamp":"2026-07-04T00:00:03Z","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`,
		`{"timestamp":"2026-07-04T00:00:04Z","type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}`,
		`{"timestamp":"2026-07-04T00:00:05Z","type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"cmd\":\"ls\"}"}`,
		`{"timestamp":"2026-07-04T00:00:06Z","type":"function_call_output","call_id":"call-1","output":"done"}`,
		`{"timestamp":"2026-07-04T00:00:07Z","type":"ghost_snapshot","data":{}}`,
		`{"record_type":"state"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	assertCodexTextArtifact(t, artifacts, ClassUserPrompt, "line:3:/content/0/text", sessionFile, 3, "/content/0/text", "prompt")
	assertCodexTextArtifact(t, artifacts, ClassAssistantMessage, "line:4:/content/0/text", sessionFile, 4, "/content/0/text", "answer")
	assertCodexTextArtifact(t, artifacts, ClassReasoningText, "line:5:/summary/0/text", sessionFile, 5, "/summary/0/text", "think")
	assertCodexTextArtifact(t, artifacts, ClassToolRequest, "line:6:/arguments", sessionFile, 6, "/arguments", `{"cmd":"ls"}`)
	assertCodexTextArtifact(t, artifacts, ClassToolResponse, "line:7:/output", sessionFile, 7, "/output", "done")

	if got := countCodexTestArtifactsByClass(artifacts, ClassOpBoundary); got != 4 {
		t.Fatalf("op_boundary count = %d, want 4", got)
	}
}

func TestExtractCodexSourceUserImageArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "13", "user-image")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-13T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-13T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-13T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect this"},{"type":"input_image","image_url":"file:///tmp/screenshot.png"}]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	assertCodexLineJSONArtifact(t, artifacts, ClassUserImage, "line:3:/payload/content/1", sessionFile, 3, "/payload/content/1", `{"image_url":"file:///tmp/screenshot.png","type":"input_image"}`)
}

func TestExtractCodexSourceLegacyFlatJSONArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "rollout-2025-06-26-11111111-2222-3333-4444-555555555555.json")
	body := `{"session":{"timestamp":"2025-06-26T00:00:00Z","id":"session-1"},"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]},{"type":"local_shell_call","call_id":"call-1","action":{"cmd":"ls"}},{"type":"local_shell_call_output","call_id":"call-1","output":"done"}]}`
	if err := os.WriteFile(sessionFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	base := filepath.Base(sessionFile)
	assertCodexFileTextArtifact(t, artifacts, ClassUserPrompt, "file:"+base+":/items/0/content/0/text", sessionFile, "/items/0/content/0/text", "prompt")
	assertCodexFileTextArtifact(t, artifacts, ClassAssistantMessage, "file:"+base+":/items/1/content/0/text", sessionFile, "/items/1/content/0/text", "answer")
	assertCodexFileTextArtifact(t, artifacts, ClassReasoningText, "file:"+base+":/items/2/summary/0/text", sessionFile, "/items/2/summary/0/text", "think")
	assertCodexFileJSONArtifact(t, artifacts, ClassToolRequest, "file:"+base+":/items/3/action", sessionFile, "/items/3/action", `{"cmd":"ls"}`)
	assertCodexFileTextArtifact(t, artifacts, ClassToolResponse, "file:"+base+":/items/4/output", sessionFile, "/items/4/output", "done")
	if got := countCodexTestArtifactsByClass(artifacts, ClassOpBoundary); got != 4 {
		t.Fatalf("op_boundary count = %d, want 4", got)
	}
}

func TestExtractCodexSourceLegacyFlatJSONRecoversValidPrefixWithTrailingCorruption(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "rollout-2025-07-01-0187df2e-dbd3-4fb3-a837-8e51233dd60a.json")
	validPrefix := `{"session":{"timestamp":"2025-07-01T20:52:59.003Z","id":"session-1"},"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`
	trailingCorruption := `{"type":"message","role":"user","content":[`
	body := validPrefix + trailingCorruption
	if err := os.WriteFile(sessionFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err == nil || !strings.Contains(err.Error(), "trailing non-whitespace") {
		t.Fatalf("extract codex source error = %v, want trailing corruption error", err)
	}

	base := filepath.Base(sessionFile)
	assertCodexFileTextArtifact(t, artifacts, ClassUserPrompt, "file:"+base+":/items/0/content/0/text", sessionFile, "/items/0/content/0/text", "prompt")
	assertCodexFileTextArtifact(t, artifacts, ClassAssistantMessage, "file:"+base+":/items/1/content/0/text", sessionFile, "/items/1/content/0/text", "answer")
	assertCodexSourceCorruptionArtifact(t, artifacts, "source_corruption:file:"+base+":trailing", sessionFile, "session-1", int64(len(validPrefix)), []byte(trailingCorruption))
}

func TestExtractCodexSourceMalformedLegacyFlatJSONReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "rollout-2025-06-26-11111111-2222-3333-4444-555555555555.json")
	if err := os.WriteFile(sessionFile, []byte(`{"session":{"id":"session-1"},"items":[`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err == nil || !strings.Contains(err.Error(), "decode legacy flat JSON") {
		t.Fatalf("extract codex source error = %v, want malformed legacy JSON error", err)
	}
}

func TestExtractCodexSourceLegacyJSONLSessionHeader(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2025", "09", "10", "legacy-header")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-09-10T19:21:08Z","id":"legacy-session","instructions":null,"git":{"commit_hash":"abc123","branch":"main","repository_url":"git@github.com:example/example.git"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startedAt := mustCodexTestMicros(t, "2025-09-10T19:21:08Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:legacy-session"), sessionBoundaryIdentity{
		NativeSessionID:     "legacy-session",
		RootNativeSessionID: "legacy-session",
		Kind:                "root",
		Status:              "running",
		StartedAt:           startedAt,
	})
}

func TestExtractCodexSourceSessionMetadataArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "12", "metadata")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-12T00:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/workspace/project","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec","git":{"commit_hash":"abc123","branch":"main","repository_url":"git@github.com:example/project.git"}}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:session-1:metadata"), codexSessionMetadataIdentity{
		NativeSessionID: "session-1",
		AgentName:       "codex:codex_exec (project)",
		CwdSHA256:       stringSHA256("/workspace/project"),
		CLIVersion:      "0.125.0",
		Originator:      "codex_exec",
		Source:          "exec",
		ModelProvider:   "openai",
		GitSHA256:       mustCanonicalJSONHash(t, `{"commit_hash":"abc123","branch":"main","repository_url":"git@github.com:example/project.git"}`),
	})
}

func TestExtractCodexSourceLegacyJSONLSessionHeaderAfterSessionStartReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2025", "09", "10", "late-legacy-header")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-09-10T19:21:08Z","type":"session_meta","payload":{"id":"session-1"}}`,
		`{"timestamp":"2025-09-10T19:21:09Z","id":"late-legacy","instructions":null}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err == nil || !strings.Contains(err.Error(), "legacy session header after session start") {
		t.Fatalf("extract codex source error = %v, want late legacy header error", err)
	}
}

func TestExtractCodexSourceDefaultEventLogArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2025", "12", "10", "default-event-logs")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-10T16:59:09Z","type":"session_meta","payload":{"id":"session-1"}}`,
		`{"timestamp":"2025-12-10T16:59:10Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-10T16:59:11Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-10T16:59:12Z","type":"event_msg","payload":{"type":"thread_goal_updated"}}`,
		`{"timestamp":"2025-12-10T16:59:13Z","type":"event_msg","payload":{"type":"view_image_tool_call"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}
	if got := countCodexTestArtifactsByClass(artifacts, ClassLogEntry); got != 2 {
		t.Fatalf("log_entry count = %d, want 2", got)
	}
	if got := countCodexTestArtifactsByClass(artifacts, ClassSystemOp); got != 2 {
		t.Fatalf("system_op count = %d, want 2", got)
	}

	threadGoalAt := mustCodexTestMicros(t, "2025-12-10T16:59:12Z")
	threadGoalID := logNativeArtifactID("turn:1", threadGoalAt, "DBG", "codex", "event_msg:thread_goal_updated")
	assertCodexLogMessageArtifact(t, findArtifact(t, artifacts, ClassLogEntry, threadGoalID), "turn:1", "event_msg:thread_goal_updated")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, threadGoalID), codexSystemOpIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		EventType:       "thread_goal_updated",
		Severity:        "DBG",
		Message:         "event_msg:thread_goal_updated",
		Timestamp:       threadGoalAt,
	})
	viewImageID := logNativeArtifactID("turn:1", mustCodexTestMicros(t, "2025-12-10T16:59:13Z"), "DBG", "codex", "event_msg:view_image_tool_call")
	assertCodexLogMessageArtifact(t, findArtifact(t, artifacts, ClassLogEntry, viewImageID), "turn:1", "event_msg:view_image_tool_call")
}

func TestExtractCodexSourceAgentMessageDoesNotDuplicateAssistantArtifact(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "22", "agent-message")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1","timestamp":"2026-06-22T00:00:00Z"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
		`{"timestamp":"2026-06-22T00:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final_answer"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	if got := countCodexTestArtifactsByClass(artifacts, ClassAssistantMessage); got != 1 {
		t.Fatalf("assistant_message source artifact count = %d, want 1", got)
	}
	assertCodexTextArtifact(t, artifacts, ClassAssistantMessage, "line:2:/payload/content/0/text", sessionFile, 2, "/payload/content/0/text", "answer")
}

func TestExtractCodexSourceLoneContextCompactedEmitsCompaction(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "23", "context-compacted")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-23T00:00:00Z","type":"session_meta","payload":{"id":"session-1","timestamp":"2026-06-23T00:00:00Z"}}`,
		`{"timestamp":"2026-06-23T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-23T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-23T00:00:03Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}
	if got := countCodexTestArtifactsByClass(artifacts, ClassOpBoundary); got != 1 {
		t.Fatalf("op_boundary count = %d, want 1", got)
	}
	if got := countCodexTestArtifactsByClass(artifacts, ClassLogEntry); got != 1 {
		t.Fatalf("log_entry count = %d, want 1", got)
	}
	if got := countCodexTestArtifactsByClass(artifacts, ClassCompactionEvent); got != 1 {
		t.Fatalf("compaction_event count = %d, want 1", got)
	}
	compactedAt := mustCodexTestMicros(t, "2026-06-23T00:00:03Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "compaction",
		Name:            "compaction",
		Status:          "completed",
		StartedAt:       compactedAt,
		EndedAt:         &compactedAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:1:1:compaction"), struct {
		NativeSessionID string `json:"native_session_id"`
		TurnSeq         int64  `json:"turn_seq"`
		OpSeq           int64  `json:"op_seq"`
		Trigger         string `json:"trigger,omitempty"`
		StartedAt       int64  `json:"started_at"`
		EndedAt         *int64 `json:"ended_at,omitempty"`
	}{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Trigger:         "auto",
		StartedAt:       compactedAt,
		EndedAt:         &compactedAt,
	})
	assertCodexSemanticLineArtifact(t, artifacts, ClassLogEntry, "line:4", sessionFile, 4, lines[3])
}

func TestExtractCodexSourceEventReasoningDoesNotDuplicateReasoningArtifact(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "22", "agent-reasoning")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1","timestamp":"2026-06-22T00:00:00Z"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"durable"}]}}`,
		`{"timestamp":"2026-06-22T00:00:02Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"visible summary"}}`,
		`{"timestamp":"2026-06-22T00:00:03Z","type":"event_msg","payload":{"type":"agent_reasoning_raw_content","text":"raw cot"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	if got := countCodexTestArtifactsByClass(artifacts, ClassReasoningText); got != 1 {
		t.Fatalf("reasoning_text source artifact count = %d, want 1", got)
	}
	assertCodexTextArtifact(t, artifacts, ClassReasoningText, "line:2:/payload/summary/0/text", sessionFile, 2, "/payload/summary/0/text", "durable")
}

func TestExtractCodexSourceStructuralArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "22", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-22T00:00:00Z","type":"session_meta","payload":{"id":"session-1","timestamp":"2026-06-22T00:00:00Z","source":"exec"}}`,
		`{"timestamp":"2026-06-22T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-22T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]}}`,
		`{"timestamp":"2026-06-22T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
		`{"timestamp":"2026-06-22T00:00:04Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}}`,
		`{"timestamp":"2026-06-22T00:00:05Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":""}}`,
		`{"timestamp":"2026-06-22T00:00:06Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"done"}}`,
		`{"timestamp":"2026-06-22T00:00:07Z","type":"compacted","payload":{"message":"summary","replacement_history":[]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startSession := mustCodexTestMicros(t, "2026-06-22T00:00:00Z")
	startTurn := mustCodexTestMicros(t, "2026-06-22T00:00:01Z")
	userAt := mustCodexTestMicros(t, "2026-06-22T00:00:02Z")
	assistantAt := mustCodexTestMicros(t, "2026-06-22T00:00:03Z")
	reasoningAt := mustCodexTestMicros(t, "2026-06-22T00:00:04Z")
	toolStart := mustCodexTestMicros(t, "2026-06-22T00:00:05Z")
	toolEnd := mustCodexTestMicros(t, "2026-06-22T00:00:06Z")
	compactionAt := mustCodexTestMicros(t, "2026-06-22T00:00:07Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:session-1"), sessionBoundaryIdentity{
		NativeSessionID:     "session-1",
		RootNativeSessionID: "session-1",
		Kind:                "root",
		Status:              "running",
		StartedAt:           startSession,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       startTurn,
		EndedAt:         &compactionAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       userAt,
		EndedAt:         &userAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       assistantAt,
		EndedAt:         &assistantAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "reasoning",
		Name:            "reasoning",
		Status:          "completed",
		StartedAt:       reasoningAt,
		EndedAt:         &reasoningAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:4"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           4,
		Kind:            "tool",
		Name:            "shell",
		Status:          "completed",
		StartedAt:       toolStart,
		EndedAt:         &toolEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:5"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           5,
		Kind:            "compaction",
		Name:            "compaction",
		Status:          "completed",
		StartedAt:       compactionAt,
		EndedAt:         &compactionAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:1:5:compaction"), struct {
		NativeSessionID      string `json:"native_session_id"`
		TurnSeq              int64  `json:"turn_seq"`
		OpSeq                int64  `json:"op_seq"`
		Trigger              string `json:"trigger,omitempty"`
		MessagePreviewSHA256 string `json:"message_preview_sha256,omitempty"`
		StartedAt            int64  `json:"started_at"`
		EndedAt              *int64 `json:"ended_at,omitempty"`
	}{
		NativeSessionID:      "session-1",
		TurnSeq:              1,
		OpSeq:                5,
		Trigger:              "auto",
		MessagePreviewSHA256: stringSHA256("summary"),
		StartedAt:            compactionAt,
		EndedAt:              &compactionAt,
	})
	assertCodexSemanticLineArtifact(t, artifacts, ClassLogEntry, "line:8", sessionFile, 8, lines[7])
}

func TestExtractCodexSourceNewFormatTurnUsesCompletedAt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "23", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-23T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-23T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-23T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-23T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
		`{"timestamp":"2026-06-23T00:00:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-06-23T00:00:09Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startTurn := mustCodexTestMicros(t, "2026-06-23T00:00:01Z")
	completedAt := mustCodexTestMicros(t, "2026-06-23T00:00:09Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       startTurn,
		EndedAt:         &completedAt,
	})
}

func TestExtractCodexSourceAbortedTurnFinalizesFailed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "24", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-24T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-24T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-24T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-24T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"working"}]}}`,
		`{"timestamp":"2026-06-24T00:00:04Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1","reason":"interrupted","completed_at":"2026-06-24T00:00:08Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startTurn := mustCodexTestMicros(t, "2026-06-24T00:00:01Z")
	abortedAt := mustCodexTestMicros(t, "2026-06-24T00:00:08Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "failed",
		StartedAt:       startTurn,
		EndedAt:         &abortedAt,
	})
}

func TestExtractCodexSourceTaskCompleteFinalizesDanglingToolAtCompletedAt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "24", "task-complete-dangling")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-24T01:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-24T01:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-24T01:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-24T01:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"cmd\":\"sleep 1\"}"}}`,
		`{"timestamp":"2026-06-24T01:00:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-06-24T01:00:09Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	toolStart := mustCodexTestMicros(t, "2026-06-24T01:00:03Z")
	completedAt := mustCodexTestMicros(t, "2026-06-24T01:00:09Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "shell",
		Status:          "completed",
		StartedAt:       toolStart,
		EndedAt:         &completedAt,
	})
}

func TestExtractCodexSourceTurnAbortedCancelsDanglingToolAtCompletedAt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "24", "turn-aborted-dangling")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-24T02:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-24T02:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-24T02:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-24T02:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"cmd\":\"sleep 1\"}"}}`,
		`{"timestamp":"2026-06-24T02:00:04Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1","reason":"interrupted","completed_at":"2026-06-24T02:00:08Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	toolStart := mustCodexTestMicros(t, "2026-06-24T02:00:03Z")
	abortedAt := mustCodexTestMicros(t, "2026-06-24T02:00:08Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "shell",
		Status:          "cancelled",
		StartedAt:       toolStart,
		EndedAt:         &abortedAt,
	})
}

func TestExtractCodexSourceOldFormatTurnContextSupersedesPriorTurn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "25", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-25T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-25T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-25T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}`,
		`{"timestamp":"2026-06-25T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"one"}]}}`,
		`{"timestamp":"2026-06-25T00:00:10Z","type":"turn_context","payload":{"turn_id":"turn-2","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-25T00:00:11Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}`,
		`{"timestamp":"2026-06-25T00:00:12Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"two"}]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	turn1Start := mustCodexTestMicros(t, "2026-06-25T00:00:01Z")
	turn1End := mustCodexTestMicros(t, "2026-06-25T00:00:10Z")
	turn2Start := turn1End
	turn2End := mustCodexTestMicros(t, "2026-06-25T00:00:12Z")
	user1At := mustCodexTestMicros(t, "2026-06-25T00:00:02Z")
	assistant1At := mustCodexTestMicros(t, "2026-06-25T00:00:03Z")
	user2At := mustCodexTestMicros(t, "2026-06-25T00:00:11Z")
	assistant2At := mustCodexTestMicros(t, "2026-06-25T00:00:12Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       turn1Start,
		EndedAt:         &turn1End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:2"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		Status:          "completed",
		StartedAt:       turn2Start,
		EndedAt:         &turn2End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user1At,
		EndedAt:         &user1At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       assistant1At,
		EndedAt:         &assistant1At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user2At,
		EndedAt:         &user2At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       assistant2At,
		EndedAt:         &assistant2At,
	})
}

func TestExtractCodexSourceOldFormatSupersedeFinalizesDanglingTools(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "26", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-26T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-26T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-26T00:00:02Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"cmd\":\"one\"}"}}`,
		`{"timestamp":"2026-06-26T00:00:10Z","type":"turn_context","payload":{"turn_id":"turn-2","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-26T00:00:11Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-2","arguments":"{\"cmd\":\"two\"}"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	turn1Start := mustCodexTestMicros(t, "2026-06-26T00:00:01Z")
	turn1End := mustCodexTestMicros(t, "2026-06-26T00:00:10Z")
	turn2Start := turn1End
	turn2End := mustCodexTestMicros(t, "2026-06-26T00:00:11Z")
	tool1Start := mustCodexTestMicros(t, "2026-06-26T00:00:02Z")
	tool2Start := turn2End

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       turn1Start,
		EndedAt:         &turn1End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:2"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		Status:          "completed",
		StartedAt:       turn2Start,
		EndedAt:         &turn2End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "shell",
		Status:          "completed",
		StartedAt:       tool1Start,
		EndedAt:         &turn1End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "shell",
		Status:          "completed",
		StartedAt:       tool2Start,
		EndedAt:         &turn2End,
	})
}

func TestExtractCodexSourceTaskStartedSupersedesPriorNewFormatTurn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "27", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-27T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-27T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-27T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}`,
		`{"timestamp":"2026-06-27T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"one"}]}}`,
		`{"timestamp":"2026-06-27T00:00:10Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-2"}}`,
		`{"timestamp":"2026-06-27T00:00:11Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}`,
		`{"timestamp":"2026-06-27T00:00:12Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"two"}]}}`,
		`{"timestamp":"2026-06-27T00:00:13Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-2","completed_at":"2026-06-27T00:00:20Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	turn1Start := mustCodexTestMicros(t, "2026-06-27T00:00:01Z")
	turn1End := mustCodexTestMicros(t, "2026-06-27T00:00:10Z")
	turn2Start := turn1End
	turn2End := mustCodexTestMicros(t, "2026-06-27T00:00:20Z")
	user1At := mustCodexTestMicros(t, "2026-06-27T00:00:02Z")
	assistant1At := mustCodexTestMicros(t, "2026-06-27T00:00:03Z")
	user2At := mustCodexTestMicros(t, "2026-06-27T00:00:11Z")
	assistant2At := mustCodexTestMicros(t, "2026-06-27T00:00:12Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "failed",
		StartedAt:       turn1Start,
		EndedAt:         &turn1End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:2"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		Status:          "completed",
		StartedAt:       turn2Start,
		EndedAt:         &turn2End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user1At,
		EndedAt:         &user1At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       assistant1At,
		EndedAt:         &assistant1At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user2At,
		EndedAt:         &user2At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       assistant2At,
		EndedAt:         &assistant2At,
	})
}

func TestExtractCodexSourceTaskStartedUsesStartedAtWhenNewer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "28", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"1970-01-01T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"1970-01-01T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1","started_at":5}}`,
		`{"timestamp":"1970-01-01T00:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"started"}]}}`,
		`{"timestamp":"1970-01-01T00:00:07Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"1970-01-01T00:00:08Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startedAt := int64(5_000_000)
	completedAt := int64(8_000_000)
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       startedAt,
		EndedAt:         &completedAt,
	})
}

func TestExtractCodexSourceStaleNewFormatEOFFinalizesFailed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "29", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-29T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-29T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-29T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-29T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"still running"}]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	staleMtime := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(sessionFile, staleMtime, staleMtime); err != nil {
		t.Fatalf("set stale mtime: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startSession := mustCodexTestMicros(t, "2026-06-29T00:00:00Z")
	startTurn := mustCodexTestMicros(t, "2026-06-29T00:00:01Z")
	endedAt := staleMtime.UnixMicro()
	if endedAt < startTurn {
		endedAt = startTurn
	}
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:session-1"), sessionBoundaryIdentity{
		NativeSessionID:     "session-1",
		RootNativeSessionID: "session-1",
		Kind:                "root",
		Status:              "failed",
		StartedAt:           startSession,
		EndedAt:             &endedAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "failed",
		StartedAt:       startTurn,
		EndedAt:         &endedAt,
	})
}

func TestExtractCodexSourceFreshNewFormatEOFRemainsRunning(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "30", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-30T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-06-30T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-06-30T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-06-30T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"still running"}]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	freshMtime := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	if err := os.Chtimes(sessionFile, freshMtime, freshMtime); err != nil {
		t.Fatalf("set fresh mtime: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startSession := mustCodexTestMicros(t, "2026-06-30T00:00:00Z")
	startTurn := mustCodexTestMicros(t, "2026-06-30T00:00:01Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:session-1"), sessionBoundaryIdentity{
		NativeSessionID:     "session-1",
		RootNativeSessionID: "session-1",
		Kind:                "root",
		Status:              "running",
		StartedAt:           startSession,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "running",
		StartedAt:       startTurn,
	})
}

func TestExtractCodexSourceMalformedStartedAtReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "06", "29", "bad-started-at")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-06-29T00:00:00Z","type":"session_meta","payload":{"id":"session-1"}}`,
		`{"timestamp":"2026-06-29T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1","started_at":"bad"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := ExtractCodexSource(context.Background(), CodexSourceOptions{Root: root, SourceID: "codex:" + root}); err == nil {
		t.Fatalf("ExtractCodexSource returned nil error for malformed started_at")
	}
}

func TestExtractCodexSourceSubTurnSplitOnSecondUserInput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "01", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-01T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-01T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-01T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-01T00:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}`,
		`{"timestamp":"2026-07-01T00:00:04Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,
		`{"timestamp":"2026-07-01T00:00:10Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second request"}]}}`,
		`{"timestamp":"2026-07-01T00:00:11Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}`,
		`{"timestamp":"2026-07-01T00:00:20Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-01T00:00:20Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	turn1Start := mustCodexTestMicros(t, "2026-07-01T00:00:01Z")
	turn1End := mustCodexTestMicros(t, "2026-07-01T00:00:10Z")
	turn2Start := turn1End
	turn2End := mustCodexTestMicros(t, "2026-07-01T00:00:20Z")
	user1At := mustCodexTestMicros(t, "2026-07-01T00:00:03Z")
	assistant1At := mustCodexTestMicros(t, "2026-07-01T00:00:04Z")
	user2At := mustCodexTestMicros(t, "2026-07-01T00:00:10Z")
	assistant2At := mustCodexTestMicros(t, "2026-07-01T00:00:11Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       turn1Start,
		EndedAt:         &turn1End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:2"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		Status:          "completed",
		StartedAt:       turn2Start,
		EndedAt:         &turn2End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user1At,
		EndedAt:         &user1At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       assistant1At,
		EndedAt:         &assistant1At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user2At,
		EndedAt:         &user2At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       assistant2At,
		EndedAt:         &assistant2At,
	})
}

func TestExtractCodexSourceSubTurnSplitDeferredWhileToolOpen(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "02", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-02T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-02T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-02T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-02T00:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}`,
		`{"timestamp":"2026-07-02T00:00:04Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"echo hi"}}`,
		`{"timestamp":"2026-07-02T00:00:05Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second request while tool is open"}]}}`,
		`{"timestamp":"2026-07-02T00:00:06Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"hi"}}`,
		`{"timestamp":"2026-07-02T00:00:07Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"third request after tool closes"}]}}`,
		`{"timestamp":"2026-07-02T00:00:08Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-02T00:00:08Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	turn1Start := mustCodexTestMicros(t, "2026-07-02T00:00:01Z")
	turn1End := mustCodexTestMicros(t, "2026-07-02T00:00:07Z")
	turn2End := mustCodexTestMicros(t, "2026-07-02T00:00:08Z")
	user1At := mustCodexTestMicros(t, "2026-07-02T00:00:03Z")
	toolStart := mustCodexTestMicros(t, "2026-07-02T00:00:04Z")
	user2At := mustCodexTestMicros(t, "2026-07-02T00:00:05Z")
	toolEnd := mustCodexTestMicros(t, "2026-07-02T00:00:06Z")
	user3At := mustCodexTestMicros(t, "2026-07-02T00:00:07Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       turn1Start,
		EndedAt:         &turn1End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:2"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		Status:          "completed",
		StartedAt:       turn1End,
		EndedAt:         &turn2End,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user1At,
		EndedAt:         &user1At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "tool",
		Name:            "shell",
		Status:          "completed",
		StartedAt:       toolStart,
		EndedAt:         &toolEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user2At,
		EndedAt:         &user2At,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:2:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         2,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       user3At,
		EndedAt:         &user3At,
	})
}

func TestExtractCodexSourceWebSearchFIFO(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "03", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	firstSearch := `{"timestamp":"2026-07-03T00:00:03Z","type":"response_item","payload":{"type":"web_search_call","action":{"type":"search","query":"alpha"}}}`
	secondSearch := `{"timestamp":"2026-07-03T00:00:04Z","type":"response_item","payload":{"type":"web_search_call","action":{"type":"open_page","url":"https://example.invalid/p"}}}`
	lines := []string{
		`{"timestamp":"2026-07-03T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-03T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-03T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		firstSearch,
		secondSearch,
		`{"timestamp":"2026-07-03T00:00:05Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"end-1","query":"alpha","action":{"type":"search","query":"alpha"}}}`,
		`{"timestamp":"2026-07-03T00:00:06Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"end-2","action":{"type":"open_page","url":"https://example.invalid/p"}}}`,
		`{"timestamp":"2026-07-03T00:00:07Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-03T00:00:07Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	firstStart := mustCodexTestMicros(t, "2026-07-03T00:00:03Z")
	secondStart := mustCodexTestMicros(t, "2026-07-03T00:00:04Z")
	firstEnd := mustCodexTestMicros(t, "2026-07-03T00:00:05Z")
	secondEnd := mustCodexTestMicros(t, "2026-07-03T00:00:06Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "web_search",
		Status:          "completed",
		StartedAt:       firstStart,
		EndedAt:         &firstEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "tool",
		Name:            "web_search",
		Status:          "completed",
		StartedAt:       secondStart,
		EndedAt:         &secondEnd,
	})
	assertCodexRawLineArtifact(t, artifacts, ClassToolRequest, "line:4", sessionFile, 4, firstSearch)
	assertCodexRawLineArtifact(t, artifacts, ClassToolRequest, "line:5", sessionFile, 5, secondSearch)
}

func TestExtractCodexSourceCollabSpawnEmitsSubagentLink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "04", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-04T00:00:00Z","type":"session_meta","payload":{"id":"parent-session","source":"exec"}}`,
		`{"timestamp":"2026-07-04T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-04T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-04T00:00:03Z","type":"event_msg","payload":{"type":"collab_agent_spawn_end","sender_thread_id":"parent-session","new_thread_id":"child-session","new_agent_nickname":"Tesla","new_agent_role":"explorer","status":"completed"}}`,
		`{"timestamp":"2026-07-04T00:00:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-04T00:00:04Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	spawnAt := mustCodexTestMicros(t, "2026-07-04T00:00:03Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "parent-session",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "session",
		Name:            "spawn",
		Status:          "completed",
		StartedAt:       spawnAt,
		EndedAt:         &spawnAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSubagentLink, "op:1:1:child_session:child-session"), subagentLinkIdentity{
		ParentNativeSessionID: "parent-session",
		ParentTurnSeq:         1,
		ParentOpSeq:           1,
		ChildNativeSessionID:  "child-session",
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	})
}

func TestExtractCodexSourcePatchApplyEndEmitsFailedToolError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "05", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-05T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-05T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-05T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-05T00:00:03Z","type":"response_item","payload":{"type":"function_call","name":"apply_patch","call_id":"patch-1","arguments":"{}"}}`,
		`{"timestamp":"2026-07-05T00:00:04Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"patch-1","success":false,"status":"failed"}}`,
		`{"timestamp":"2026-07-05T00:00:05Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-05T00:00:05Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startAt := mustCodexTestMicros(t, "2026-07-05T00:00:03Z")
	endAt := mustCodexTestMicros(t, "2026-07-05T00:00:04Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "apply_patch",
		Status:          "failed",
		StartedAt:       startAt,
		EndedAt:         &endAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:1:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              1,
		OpKind:             "tool",
		ErrorClass:         "patch_failed",
		ErrorMessageSHA256: EmptySHA256,
	})
}

func TestExtractCodexSourceExecCommandEndNonZeroExitWins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "06", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-06T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-06T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-06T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-06T00:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"exec-1","arguments":"{}"}}`,
		`{"timestamp":"2026-07-06T00:00:04Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"exec-1","exit_code":2,"aggregated_output":"done","duration":{"secs":0,"nanos":500000000}}}`,
		`{"timestamp":"2026-07-06T00:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"exec-1","output":"done"}}`,
		`{"timestamp":"2026-07-06T00:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-06T00:00:06Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startAt := mustCodexTestMicros(t, "2026-07-06T00:00:03Z")
	endAt := mustCodexTestMicros(t, "2026-07-06T00:00:05Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "shell",
		Status:          "failed",
		StartedAt:       startAt,
		EndedAt:         &endAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:1:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              1,
		OpKind:             "tool",
		ErrorClass:         "command_failed",
		ErrorMessageSHA256: EmptySHA256,
	})
}

func TestExtractCodexSourceExecCommandEndFinalizesDanglingTool(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "07", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-07T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-07T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-07T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-07T00:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"exec-1","arguments":"{}"}}`,
		`{"timestamp":"2026-07-07T00:00:04Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"exec-1","exit_code":2,"aggregated_output":"done"}}`,
		`{"timestamp":"2026-07-07T00:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-07T00:00:06Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startAt := mustCodexTestMicros(t, "2026-07-07T00:00:03Z")
	endAt := mustCodexTestMicros(t, "2026-07-07T00:00:06Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "shell",
		Status:          "failed",
		StartedAt:       startAt,
		EndedAt:         &endAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:1:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              1,
		OpKind:             "tool",
		ErrorClass:         "command_failed",
		ErrorMessageSHA256: EmptySHA256,
	})
}

func TestExtractCodexSourceMcpToolCallEndRestampsAndFinalizes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "08", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-08T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-08T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-08T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-08T00:00:03Z","type":"response_item","payload":{"type":"function_call","name":"github.list","call_id":"mcp-1","arguments":"{}"}}`,
		`{"timestamp":"2026-07-08T00:00:04Z","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"mcp-1","invocation":{"server":"github","tool":"list"},"result":{"Ok":{"is_error":false}}}}`,
		`{"timestamp":"2026-07-08T00:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-08T00:00:06Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startAt := mustCodexTestMicros(t, "2026-07-08T00:00:03Z")
	endAt := mustCodexTestMicros(t, "2026-07-08T00:00:04Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "list",
		ToolNamespace:   "mcp:github",
		Status:          "completed",
		StartedAt:       startAt,
		EndedAt:         &endAt,
	})
}

func TestExtractCodexSourceMcpToolCallEndErrorEmitsToolError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "09", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-09T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-09T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-09T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-09T00:00:03Z","type":"response_item","payload":{"type":"function_call","name":"github.list","call_id":"mcp-1","arguments":"{}"}}`,
		`{"timestamp":"2026-07-09T00:00:04Z","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"mcp-1","invocation":{"server":"github","tool":"list"},"result":{"Ok":{"is_error":true}}}}`,
		`{"timestamp":"2026-07-09T00:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-09T00:00:06Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startAt := mustCodexTestMicros(t, "2026-07-09T00:00:03Z")
	endAt := mustCodexTestMicros(t, "2026-07-09T00:00:04Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "list",
		ToolNamespace:   "mcp:github",
		Status:          "failed",
		StartedAt:       startAt,
		EndedAt:         &endAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:1:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              1,
		OpKind:             "tool",
		ErrorClass:         "tool_error",
		ErrorMessageSHA256: EmptySHA256,
	})
}

func TestExtractCodexSourceImageGenerationEndFinalizes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "10", "rollout")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-10T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-10T00:00:01Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-10T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-10T00:00:03Z","type":"response_item","payload":{"type":"image_generation_call","call_id":"img-1","status":"in_progress","revised_prompt":"draw a chart"}}`,
		`{"timestamp":"2026-07-10T00:00:04Z","type":"event_msg","payload":{"type":"image_generation_end","call_id":"img-1"}}`,
		`{"timestamp":"2026-07-10T00:00:06Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2026-07-10T00:00:06Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	artifacts, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err != nil {
		t.Fatalf("extract codex source: %v", err)
	}

	startAt := mustCodexTestMicros(t, "2026-07-10T00:00:03Z")
	endAt := mustCodexTestMicros(t, "2026-07-10T00:00:04Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "image_generation",
		Status:          "completed",
		StartedAt:       startAt,
		EndedAt:         &endAt,
	})
}

func TestCodexSourceToolNameNamespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind          string
		name          string
		wantName      string
		wantNamespace string
	}{
		{kind: "web_search_call", wantName: "web_search", wantNamespace: "web"},
		{kind: "image_generation_call", wantName: "image_generation", wantNamespace: "media"},
		{kind: "custom_tool_call", name: "lookup", wantName: "lookup", wantNamespace: "custom"},
		{kind: "local_shell_call", wantName: "shell", wantNamespace: "shell"},
		{kind: "local_shell_call", name: "terminal", wantName: "terminal", wantNamespace: "shell"},
		{kind: "tool_search_call", wantName: "tool_search", wantNamespace: "custom"},
		{kind: "function_call", name: "shell", wantName: "shell", wantNamespace: "shell"},
		{kind: "function_call", name: "exec_command", wantName: "exec_command", wantNamespace: "shell"},
		{kind: "function_call", name: "apply_patch", wantName: "apply_patch", wantNamespace: "fs"},
		{kind: "function_call", name: "read", wantName: "read", wantNamespace: "fs"},
		{kind: "function_call", name: "view_image", wantName: "view_image", wantNamespace: "fs"},
		{kind: "function_call", name: "github.list", wantName: "github.list", wantNamespace: "custom"},
		{kind: "future_tool", wantName: "future_tool", wantNamespace: "custom"},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.name, func(t *testing.T) {
			t.Parallel()
			gotName, gotNamespace := codexSourceToolNameNamespace(tt.kind, tt.name)
			if gotName != tt.wantName || gotNamespace != tt.wantNamespace {
				t.Fatalf("codexSourceToolNameNamespace(%q, %q) = (%q, %q), want (%q, %q)",
					tt.kind, tt.name, gotName, gotNamespace, tt.wantName, tt.wantNamespace)
			}
		})
	}
}

func TestCodexMcpResultStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		result       string
		wantStatus   string
		wantErrClass string
	}{
		{name: "missing", wantStatus: "completed"},
		{name: "ok", result: `{"Ok":{"is_error":false}}`, wantStatus: "completed"},
		{name: "ok-is-error", result: `{"Ok":{"is_error":true}}`, wantStatus: "failed", wantErrClass: "tool_error"},
		{name: "err", result: `{"Err":"boom"}`, wantStatus: "failed", wantErrClass: "tool_error"},
		{name: "malformed", result: `{"Ok":`, wantStatus: "completed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStatus, gotErrClass := codexMcpResultStatus(json.RawMessage(tt.result))
			if gotStatus != tt.wantStatus || gotErrClass != tt.wantErrClass {
				t.Fatalf("codexMcpResultStatus(%s) = (%q, %q), want (%q, %q)",
					tt.result, gotStatus, gotErrClass, tt.wantStatus, tt.wantErrClass)
			}
		})
	}
}

func TestExtractCodexSourceToolOutputUnmatchedEventReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "11", "unsupported")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-11T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-11T00:00:01Z","type":"event_msg","payload":{"type":"tool_output_unmatched","message":"orphan output"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err == nil {
		t.Fatal("ExtractCodexSource succeeded, want unsupported event error")
	}
	if !strings.Contains(err.Error(), `unknown codex event_msg payload type "tool_output_unmatched"`) {
		t.Fatalf("ExtractCodexSource error = %v", err)
	}
}

func TestExtractCodexSourceUnknownRecordTypeReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "11", "unknown-record")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-11T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-11T00:00:01Z","type":"future_artifact","payload":{"text":"must not be ignored"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err == nil {
		t.Fatal("ExtractCodexSource succeeded, want unknown record type error")
	}
	if !strings.Contains(err.Error(), `unknown codex record type "future_artifact"`) {
		t.Fatalf("ExtractCodexSource error = %v", err)
	}
}

func TestExtractCodexSourceUnknownResponseItemPayloadTypeReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "11", "unknown-response-item")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-11T00:00:00Z","type":"session_meta","payload":{"id":"session-1","source":"exec"}}`,
		`{"timestamp":"2026-07-11T00:00:01Z","type":"response_item","payload":{"type":"future_item","content":[{"type":"output_text","text":"must not be ignored"}]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := ExtractCodexSource(context.Background(), CodexSourceOptions{
		Root:     root,
		SourceID: "codex:" + root,
	})
	if err == nil {
		t.Fatal("ExtractCodexSource succeeded, want unknown response item error")
	}
	if !strings.Contains(err.Error(), `unknown codex response_item payload type "future_item"`) {
		t.Fatalf("ExtractCodexSource error = %v", err)
	}
}

func TestExtractCodexSourceMalformedJSONReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "11", "bad")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(sessionFile, []byte("{bad json}\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := ExtractCodexSource(context.Background(), CodexSourceOptions{Root: root, SourceID: "codex:" + root}); err == nil {
		t.Fatalf("ExtractCodexSource returned nil error for malformed JSON")
	}
}

func TestExtractCodexSourceMalformedTimestampReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionFile := codexSourceTestRollout(root, "2026", "07", "11", "bad-time")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	line := `{"timestamp":"not-a-time","type":"session_meta","payload":{"id":"session-1"}}` + "\n"
	if err := os.WriteFile(sessionFile, []byte(line), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := ExtractCodexSource(context.Background(), CodexSourceOptions{Root: root, SourceID: "codex:" + root}); err == nil {
		t.Fatalf("ExtractCodexSource returned nil error for malformed timestamp")
	}
}

func assertCodexTextArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, filePath string, line int, pointer string, text string) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath, Fragment: fmt.Sprintf("L%d", line)}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("selector = %+v, want uri=%q pointer=%q", got.Selector, wantURI, pointer)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	wantHash := sha256.Sum256([]byte(text))
	if got.Bytes != int64(len(text)) || got.Chars != int64(len(text)) || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("text proof mismatch: %+v", got)
	}
}

func assertCodexFileTextArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, filePath string, pointer string, text string) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("selector = %+v, want uri=%q pointer=%q", got.Selector, wantURI, pointer)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	wantHash := sha256.Sum256([]byte(text))
	if got.Bytes != int64(len(text)) || got.Chars != int64(len(text)) || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("text proof mismatch: %+v", got)
	}
}

func assertCodexFileJSONArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, filePath string, pointer string, canonicalJSON string) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("selector = %+v, want uri=%q pointer=%q", got.Selector, wantURI, pointer)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashCanonicalJSON {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashCanonicalJSON)
	}
	wantHash := sha256.Sum256([]byte(canonicalJSON))
	if got.Bytes != int64(len(canonicalJSON)) || got.Chars != -1 || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("json proof mismatch: %+v", got)
	}
}

func assertCodexLineJSONArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, filePath string, line int, pointer string, canonicalJSON string) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath, Fragment: fmt.Sprintf("L%d", line)}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("selector = %+v, want uri=%q pointer=%q", got.Selector, wantURI, pointer)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashCanonicalJSON {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashCanonicalJSON)
	}
	wantHash := sha256.Sum256([]byte(canonicalJSON))
	if got.Bytes != int64(len(canonicalJSON)) || got.Chars != -1 || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("json proof mismatch: %+v", got)
	}
}

func assertCodexRawLineArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, filePath string, line int, raw string) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath, Fragment: fmt.Sprintf("L%d", line)}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != "" {
		t.Fatalf("selector = %+v, want uri=%q with no json_pointer", got.Selector, wantURI)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashRawBytes {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashRawBytes)
	}
	wantHash := sha256.Sum256([]byte(raw))
	if got.Bytes != int64(len(raw)) || got.Chars != -1 || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("raw-line proof mismatch: %+v", got)
	}
}

func assertCodexSemanticLineArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, filePath string, line int, text string) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath, Fragment: fmt.Sprintf("L%d", line)}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != "" {
		t.Fatalf("selector = %+v, want uri=%q with no json_pointer", got.Selector, wantURI)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	wantHash := sha256.Sum256([]byte(text))
	if got.Bytes != int64(len(text)) || got.Chars != int64(len(text)) || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("semantic-line proof mismatch: %+v", got)
	}
}

func assertCodexSourceCorruptionArtifact(t *testing.T, artifacts []Artifact, nativeID string, filePath string, nativeSessionID string, offset int64, corruptBytes []byte) {
	t.Helper()

	got := findArtifact(t, artifacts, ClassSourceCorruption, nativeID)
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != nativeSessionID {
		t.Fatalf("native_session_id = %q, want %q", got.NativeSessionID, nativeSessionID)
	}
	if got.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilitySourceCorrupt)
	}
	if got.HashDomain != HashRawBytes {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashRawBytes)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != "" {
		t.Fatalf("selector = %+v, want uri=%q without json_pointer", got.Selector, wantURI)
	}
	if got.Selector.ByteRange == nil {
		t.Fatalf("selector byte range is nil; artifact=%+v", got)
	}
	if got.Selector.ByteRange.Start != offset || got.Selector.ByteRange.End != offset+int64(len(corruptBytes)) {
		t.Fatalf("selector byte range = %+v, want [%d,%d)", got.Selector.ByteRange, offset, offset+int64(len(corruptBytes)))
	}
	wantHash := sha256.Sum256(corruptBytes)
	if got.Bytes != int64(len(corruptBytes)) || got.Chars != -1 || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("source corruption proof mismatch: %+v", got)
	}
	assertIntegrityFailures(t, got, []IntegrityFailure{{
		Field:    "trailing_bytes",
		Expected: "0",
		Actual:   fmt.Sprintf("%d", len(corruptBytes)),
	}})
}

func countCodexTestArtifactsByClass(artifacts []Artifact, class ArtifactClass) int {
	count := 0
	for _, artifact := range artifacts {
		if artifact.Class == class {
			count++
		}
	}
	return count
}

func assertCodexLogMessageArtifact(t *testing.T, got Artifact, nativeTurnID string, message string) {
	t.Helper()

	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	if got.NativeTurnID != nativeTurnID {
		t.Fatalf("native_turn_id = %q, want %q", got.NativeTurnID, nativeTurnID)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	if got.Selector.JSONPointer != "" || !strings.HasPrefix(got.Selector.URI, "log://") {
		t.Fatalf("selector = %+v, want log:// selector without json_pointer", got.Selector)
	}
	if got.Bytes != int64(len(message)) || got.Chars != int64(len(message)) || got.ComputedSHA256 != stringSHA256(message) {
		t.Fatalf("log proof mismatch: %+v, want message %q", got, message)
	}
}

func assertIdentityArtifact(t *testing.T, artifact Artifact, identity interface{}) {
	t.Helper()

	if artifact.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", artifact.Availability, AvailabilityAvailable)
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

func mustCodexTestMicros(t *testing.T, timestamp string) int64 {
	t.Helper()

	tsUs, err := parseCodexSourceTimestamp(timestamp)
	if err != nil {
		t.Fatalf("parseCodexSourceTimestamp: %v", err)
	}
	return tsUs
}

func mustCanonicalJSONHash(t *testing.T, raw string) string {
	t.Helper()

	hash, err := canonicalJSONHash(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("canonicalJSONHash: %v", err)
	}
	return hash
}

func codexSourceTestRollout(root string, year string, month string, day string, name string) string {
	if !strings.HasPrefix(name, "rollout-") {
		name = "rollout-" + name
	}
	if !strings.HasSuffix(name, ".jsonl") {
		name += ".jsonl"
	}
	return filepath.Join(root, year, month, day, name)
}

func joinJSONLLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}
