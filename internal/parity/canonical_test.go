package parity

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/store"
	_ "modernc.org/sqlite"
)

func TestExtractCanonicalPayloadRefComputesProofFromFileLine(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "payload.jsonl")
	const payload = "first payload line\nsecond payload line\n"
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	locationURI := (&url.URL{Scheme: "file", Path: payloadPath, Fragment: "L2"}).String()

	insertPayloadRef(t, ctx, db, "llm_response", "text", locationURI, nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "line:2")
	wantHash := sha256.Sum256([]byte("second payload line"))
	if got.Adapter != "codex" {
		t.Fatalf("adapter = %q, want codex", got.Adapter)
	}
	if got.SourceID != "codex:test-source" {
		t.Fatalf("source_id = %q, want codex:test-source", got.SourceID)
	}
	if got.NativeSessionID != "native-session-1" {
		t.Fatalf("native_session_id = %q, want native-session-1", got.NativeSessionID)
	}
	if got.NativeTurnID != "turn:7" {
		t.Fatalf("native_turn_id = %q, want turn:7", got.NativeTurnID)
	}
	if got.NativeArtifactID != "line:2" {
		t.Fatalf("native_artifact_id = %q, want line:2", got.NativeArtifactID)
	}
	if got.Class != ClassLLMResponse {
		t.Fatalf("class = %q, want %q", got.Class, ClassLLMResponse)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashRawBytes {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashRawBytes)
	}
	if got.Selector.URI != locationURI {
		t.Fatalf("selector.uri = %q, want %q", got.Selector.URI, locationURI)
	}
	if got.Bytes != int64(len("second payload line")) {
		t.Fatalf("bytes = %d, want %d", got.Bytes, len("second payload line"))
	}
	if got.Chars != -1 {
		t.Fatalf("chars = %d, want -1 for raw payload class", got.Chars)
	}
	if got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("computed_sha256 = %q, want %x", got.ComputedSHA256, wantHash)
	}
	if got.CanonicalSessionID != "session-1" || got.CanonicalTurnID != "turn-1" || got.CanonicalOpID != "op-1" || got.PayloadRefID != 1 {
		t.Fatalf("canonical evidence mismatch: %+v", got)
	}
}

func TestExtractCanonicalPayloadRefReadsLargeFileLine(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "large.jsonl")
	largeLine := strings.Repeat("x", 70*1024)
	if err := os.WriteFile(payloadPath, []byte("small\n"+largeLine+"\n"), 0o600); err != nil {
		t.Fatalf("write large payload fixture: %v", err)
	}
	locationURI := (&url.URL{Scheme: "file", Path: payloadPath, Fragment: "L2"}).String()

	insertPayloadRef(t, ctx, db, "llm_response", "text", locationURI, nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "line:2")
	if got.Bytes != int64(len(largeLine)) {
		t.Fatalf("bytes = %d, want large line length %d", got.Bytes, len(largeLine))
	}
}

func TestExtractCanonicalPayloadRefOversizedLineIsUnverifiable(t *testing.T) {
	t.Parallel()

	const wantLimit = 16 * 1024 * 1024

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "oversized.jsonl")
	oversizedLine := strings.Repeat("x", wantLimit+1)
	if err := os.WriteFile(payloadPath, []byte("small\n"+oversizedLine+"\n"), 0o600); err != nil {
		t.Fatalf("write oversized payload fixture: %v", err)
	}
	locationURI := (&url.URL{Scheme: "file", Path: payloadPath, Fragment: "L2"}).String()

	insertPayloadRef(t, ctx, db, "llm_response", "text", locationURI, nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "line:2")
	if got.Availability != AvailabilityUnverifiable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityUnverifiable)
	}
	if got.Bytes != -1 || got.Chars != -1 || got.ComputedSHA256 != "" {
		t.Fatalf("oversized line should not produce proof: %+v", got)
	}
}

func TestExtractCanonicalPayloadRefJsonPointerComputesExactText(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "payload.jsonl")
	if err := os.WriteFile(payloadPath, []byte(`{"message":"exact"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	location := &url.URL{Scheme: "file", Path: payloadPath, Fragment: "L1"}
	q := location.Query()
	q.Set("json_pointer", "/message")
	location.RawQuery = q.Encode()

	insertPayloadRef(t, ctx, db, "llm_response", "json", location.String(), nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "line:1:/message")
	if got.Selector.JSONPointer != "/message" {
		t.Fatalf("selector.json_pointer = %q, want /message", got.Selector.JSONPointer)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	if got.Bytes != int64(len("exact")) || got.Chars != int64(len("exact")) {
		t.Fatalf("json_pointer text length mismatch: %+v", got)
	}
	wantHash := sha256.Sum256([]byte("exact"))
	if got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("computed_sha256 = %q, want exact text hash %x", got.ComputedSHA256, wantHash)
	}
}

func TestExtractCanonicalPayloadRefJsonPointerComputesCanonicalJSON(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "payload.jsonl")
	if err := os.WriteFile(payloadPath, []byte(`{"tool":{"args":{"b":2,"a":1}}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	location := &url.URL{Scheme: "file", Path: payloadPath, Fragment: "L1"}
	q := location.Query()
	q.Set("json_pointer", "/tool/args")
	location.RawQuery = q.Encode()

	insertPayloadRef(t, ctx, db, "tool_request", "json", location.String(), nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassToolRequest, "line:1:/tool/args")
	if got.HashDomain != HashCanonicalJSON {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashCanonicalJSON)
	}
	const canonical = `{"a":1,"b":2}`
	if got.Bytes != int64(len(canonical)) || got.Chars != -1 {
		t.Fatalf("json_pointer object length mismatch: %+v", got)
	}
	wantHash := sha256.Sum256([]byte(canonical))
	if got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("computed_sha256 = %q, want canonical JSON hash %x", got.ComputedSHA256, wantHash)
	}
}

func TestExtractCanonicalPayloadRefJsonPointerSupportsEscapedTokensAndArrays(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "payload.jsonl")
	if err := os.WriteFile(payloadPath, []byte(`{"a/b":{"tilde~key":[{"value":"ok"}]}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	location := &url.URL{Scheme: "file", Path: payloadPath, Fragment: "L1"}
	q := location.Query()
	q.Set("json_pointer", "/a~1b/tilde~0key/0/value")
	location.RawQuery = q.Encode()

	insertPayloadRef(t, ctx, db, "llm_response", "json", location.String(), nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, "line:1:/a~1b/tilde~0key/0/value")
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.Bytes != 2 || got.Chars != 2 {
		t.Fatalf("escaped json_pointer text length mismatch: %+v", got)
	}
	wantHash := sha256.Sum256([]byte("ok"))
	if got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("computed_sha256 = %q, want escaped pointer text hash %x", got.ComputedSHA256, wantHash)
	}
}

func TestExtractCanonicalPayloadRefInvalidJsonPointerIsUnverifiable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "payload.jsonl")
	if err := os.WriteFile(payloadPath, []byte(`{"items":["first"]}`+"\n"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	location := &url.URL{Scheme: "file", Path: payloadPath, Fragment: "L1"}
	q := location.Query()
	q.Set("json_pointer", "/items/01")
	location.RawQuery = q.Encode()

	insertPayloadRef(t, ctx, db, "tool_response", "json", location.String(), nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassToolResponse, "line:1:/items/01")
	if got.Availability != AvailabilityUnverifiable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityUnverifiable)
	}
	if got.Bytes != -1 || got.ComputedSHA256 != "" {
		t.Fatalf("invalid json_pointer should not hash containing line: %+v", got)
	}
}

func TestCanonicalPayloadResolverCachesLineBeforeJSONPointer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.jsonl")
	if err := os.WriteFile(payloadPath, []byte("{}\n"+`{"first":"one","second":"two"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	resolver := newCanonicalPayloadResolver(1024)
	firstURL := &url.URL{Scheme: "file", Path: payloadPath, Fragment: "L2"}
	firstValues := firstURL.Query()
	firstValues.Set("json_pointer", "/first")
	firstURL.RawQuery = firstValues.Encode()
	first, err := resolver.resolve(root, firstURL.String(), "")
	if err != nil {
		t.Fatalf("resolve first pointer: %v", err)
	}
	if string(first.bytes) != "one" {
		t.Fatalf("first bytes = %q, want one", first.bytes)
	}
	indexKey := fileLineIndexKey{path: filepath.Clean(payloadPath), maxBytes: 1024}
	if got := len(resolver.fileLineIndexes[indexKey]); got != 2 {
		t.Fatalf("line offset index size = %d, want 2", got)
	}
	if err := os.Remove(payloadPath); err != nil {
		t.Fatalf("remove payload after cache fill: %v", err)
	}

	secondURL := &url.URL{Scheme: "file", Path: payloadPath, Fragment: "L2"}
	secondValues := secondURL.Query()
	secondValues.Set("json_pointer", "/second")
	secondURL.RawQuery = secondValues.Encode()
	second, err := resolver.resolve(root, secondURL.String(), "")
	if err != nil {
		t.Fatalf("resolve second pointer from cached line: %v", err)
	}
	if string(second.bytes) != "two" {
		t.Fatalf("second bytes = %q, want two", second.bytes)
	}
}

func TestExtractCanonicalPayloadRefUsesStoredProofWhenResolverCannotRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	const raw = `{"result":"kept"}`
	hash := sha256.Sum256([]byte(raw))
	locationURI := "opencode-sqlite://?part_id=part-1&field=data.output"
	originalBytes := int64(len(raw))
	storedBytes := int64(len(raw))

	insertPayloadRef(t, ctx, db, "tool_response", "json", locationURI, &originalBytes, &storedBytes, fmt.Sprintf("%x", hash))

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassToolResponse, "part:part-1:data.output")
	if got.Class != ClassToolResponse {
		t.Fatalf("class = %q, want %q", got.Class, ClassToolResponse)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.NativeArtifactID != "part:part-1:data.output" {
		t.Fatalf("native_artifact_id = %q, want part:part-1:data.output", got.NativeArtifactID)
	}
	if got.Selector.FieldPath != "data.output" {
		t.Fatalf("selector.field_path = %q, want data.output", got.Selector.FieldPath)
	}
	if got.Bytes != originalBytes {
		t.Fatalf("bytes = %d, want %d", got.Bytes, originalBytes)
	}
	if got.ComputedSHA256 != fmt.Sprintf("%x", hash) {
		t.Fatalf("computed_sha256 = %q, want %x", got.ComputedSHA256, hash)
	}
}

func TestExtractCanonicalPayloadRefPrefersResolvedProofOverStoredProof(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadPath := filepath.Join(t.TempDir(), "payload.txt")
	const payload = "actual payload"
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	locationURI := (&url.URL{Scheme: "file", Path: payloadPath}).String()
	staleBytes := int64(999)

	insertPayloadRef(t, ctx, db, "llm_request", "text", locationURI, &staleBytes, &staleBytes, strings.Repeat("a", 64))

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMRequest, testPayloadNativeArtifactID(payloadPath))
	wantHash := sha256.Sum256([]byte(payload))
	if got.Bytes != int64(len(payload)) {
		t.Fatalf("bytes = %d, want resolved length %d", got.Bytes, len(payload))
	}
	if got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("computed_sha256 = %q, want resolved hash %x", got.ComputedSHA256, wantHash)
	}
	if got.ProducerSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("producer_sha256 = %q, want stale producer hash retained as evidence", got.ProducerSHA256)
	}
}

func TestExtractCanonicalPayloadRefResolvesGzipReasoningAndEmptyLog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	reasoningPath := filepath.Join(t.TempDir(), "reasoning.txt.gz")
	if err := os.WriteFile(reasoningPath, gzipBytes(t, []byte("think")), 0o600); err != nil {
		t.Fatalf("write gzip payload fixture: %v", err)
	}
	emptyPath := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty payload fixture: %v", err)
	}

	insertPayloadRefCompressed(t, ctx, db, "llm_reasoning", "text", (&url.URL{Scheme: "file", Path: reasoningPath}).String(), "gzip", nil, nil, "")
	insertPayloadRef(t, ctx, db, "log", "text", (&url.URL{Scheme: "file", Path: emptyPath}).String(), nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	reasoning := findArtifact(t, artifacts, ClassReasoningText, testPayloadNativeArtifactID(reasoningPath))
	if reasoning.Availability != AvailabilityAvailable {
		t.Fatalf("reasoning availability = %q, want %q", reasoning.Availability, AvailabilityAvailable)
	}
	if reasoning.HashDomain != HashSemanticText {
		t.Fatalf("reasoning hash_domain = %q, want %q", reasoning.HashDomain, HashSemanticText)
	}
	if reasoning.Bytes != 5 || reasoning.Chars != 5 {
		t.Fatalf("reasoning length = bytes:%d chars:%d, want 5/5", reasoning.Bytes, reasoning.Chars)
	}

	logArtifact := findArtifact(t, artifacts, ClassLogEntry, testPayloadNativeArtifactID(emptyPath))
	if logArtifact.Availability != AvailabilitySourceEmpty {
		t.Fatalf("log availability = %q, want %q", logArtifact.Availability, AvailabilitySourceEmpty)
	}
	if logArtifact.Bytes != 0 || logArtifact.Chars != 0 || logArtifact.ComputedSHA256 != EmptySHA256 {
		t.Fatalf("empty log proof mismatch: %+v", logArtifact)
	}
}

func TestExtractCanonicalPayloadRefResolvesEmptyRawPayload(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	responsePath := filepath.Join(t.TempDir(), "empty-response.sse.gz")
	if err := os.WriteFile(responsePath, gzipBytes(t, nil), 0o600); err != nil {
		t.Fatalf("write empty gzip payload fixture: %v", err)
	}

	insertPayloadRefCompressed(t, ctx, db, "llm_response", "sse", (&url.URL{Scheme: "file", Path: responsePath}).String(), "gzip", nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassLLMResponse, testPayloadNativeArtifactID(responsePath))
	if got.Availability != AvailabilitySourceEmpty {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilitySourceEmpty)
	}
	if got.HashDomain != HashRawBytes {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashRawBytes)
	}
	if got.Bytes != 0 || got.Chars != -1 || got.ComputedSHA256 != EmptySHA256 {
		t.Fatalf("empty raw payload proof mismatch: %+v", got)
	}
}

func TestResolvePayloadBytesWholeFileExceedingCapReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(payloadPath, []byte("123456789"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	locationURI := (&url.URL{Scheme: "file", Path: payloadPath}).String()

	resolver := newCanonicalPayloadResolver(8)
	defer func() { _ = resolver.Close() }()
	_, err := resolver.resolve(root, locationURI, "")
	if err == nil {
		t.Fatal("resolver.resolve returned nil error for payload above cap")
	}
	if !strings.Contains(err.Error(), "payload_ref exceeds 8 bytes") {
		t.Fatalf("error = %q, want payload cap error", err)
	}
}

func TestResolvePayloadBytesGzipExpansionExceedingCapReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.txt.gz")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), 128)); err != nil {
		t.Fatalf("write gzip payload fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip payload fixture: %v", err)
	}
	if err := os.WriteFile(payloadPath, compressed.Bytes(), 0o600); err != nil {
		t.Fatalf("write compressed payload fixture: %v", err)
	}
	locationURI := (&url.URL{Scheme: "file", Path: payloadPath}).String()

	resolver := newCanonicalPayloadResolver(64)
	defer func() { _ = resolver.Close() }()
	_, err := resolver.resolve(root, locationURI, "gzip")
	if err == nil {
		t.Fatal("resolver.resolve returned nil error for gzip payload above cap")
	}
	if !strings.Contains(err.Error(), "decompressed payload_ref exceeds 64 bytes") {
		t.Fatalf("error = %q, want decompressed payload cap error", err)
	}
}

func TestCanonicalPayloadResolverRejectsFileOutsideSourceRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside payload: %v", err)
	}
	locationURI := (&url.URL{Scheme: "file", Path: outsidePath}).String()

	resolver := newCanonicalPayloadResolver(1024)
	defer func() { _ = resolver.Close() }()
	_, err := resolver.resolve(root, locationURI, "")
	if err == nil {
		t.Fatal("resolver.resolve outside source root = nil error, want containment error")
	}
	if !strings.Contains(err.Error(), "outside the source root") {
		t.Fatalf("error = %v, want outside source root", err)
	}
}

func TestArtifactFromPayloadRefCapErrorIgnoresStoredProof(t *testing.T) {
	t.Parallel()

	payload := []byte("123456789")
	payloadPath := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	hash := sha256.Sum256(payload)
	row := canonicalPayloadRefRow{
		sourceID:        "codex:test-source",
		adapter:         "codex",
		sourceLocation:  payloadPath,
		sessionID:       "session-1",
		nativeSessionID: "native-session-1",
		turnID:          "turn-1",
		turnSeq:         7,
		opID:            "op-1",
		opSeq:           3,
		opKind:          "llm",
		opName:          "message",
		payloadRefID:    99,
		kind:            "llm_response",
		locationURI:     (&url.URL{Scheme: "file", Path: payloadPath}).String(),
		originalBytes:   sql.NullInt64{Int64: int64(len(payload)), Valid: true},
		sha256:          sql.NullString{String: fmt.Sprintf("%x", hash), Valid: true},
	}

	got, err := artifactFromPayloadRefWithLimit(row, 1, 8)
	if err != nil {
		t.Fatalf("artifactFromPayloadRefWithLimit: %v", err)
	}
	if got.Availability != AvailabilityUnverifiable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityUnverifiable)
	}
	if got.Bytes != -1 || got.ComputedSHA256 != "" {
		t.Fatalf("cap error should not reuse stored proof: %+v", got)
	}
}

func TestArtifactFromPayloadRefContainmentErrorIgnoresStoredProof(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	payload := []byte("outside payload")
	payloadPath := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	hash := sha256.Sum256(payload)
	row := canonicalPayloadRefRow{
		sourceID:        "codex:" + root,
		adapter:         "codex",
		sourceLocation:  root,
		sessionID:       "session-1",
		nativeSessionID: "native-session-1",
		turnID:          "turn-1",
		turnSeq:         7,
		opID:            "op-1",
		opSeq:           3,
		opKind:          "llm",
		opName:          "message",
		payloadRefID:    99,
		kind:            "llm_response",
		locationURI:     (&url.URL{Scheme: "file", Path: payloadPath}).String(),
		originalBytes:   sql.NullInt64{Int64: int64(len(payload)), Valid: true},
		sha256:          sql.NullString{String: fmt.Sprintf("%x", hash), Valid: true},
	}

	got, err := artifactFromPayloadRefWithLimit(row, 1, 1024)
	if err != nil {
		t.Fatalf("artifactFromPayloadRefWithLimit: %v", err)
	}
	if got.Availability != AvailabilityUnverifiable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityUnverifiable)
	}
	if got.Bytes != -1 || got.ComputedSHA256 != "" {
		t.Fatalf("containment error should not reuse stored proof: %+v", got)
	}
}

func TestExtractCanonicalPayloadRefNormalizesAIAgentV3Aliases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	payloadDir := t.TempDir()
	sdkRequestPath := filepath.Join(payloadDir, "sdk-request.json")
	sdkResponsePath := filepath.Join(payloadDir, "sdk-response.json")
	reasoningPath := filepath.Join(payloadDir, "reasoning.txt")
	if err := os.WriteFile(sdkRequestPath, []byte(`{"request":true}`), 0o600); err != nil {
		t.Fatalf("write sdk request: %v", err)
	}
	if err := os.WriteFile(sdkResponsePath, []byte(`{"response":true}`), 0o600); err != nil {
		t.Fatalf("write sdk response: %v", err)
	}
	if err := os.WriteFile(reasoningPath, []byte("think"), 0o600); err != nil {
		t.Fatalf("write reasoning: %v", err)
	}

	insertPayloadRef(t, ctx, db, "sdk_request", "json", (&url.URL{Scheme: "file", Path: sdkRequestPath}).String(), nil, nil, "")
	insertPayloadRef(t, ctx, db, "sdk_response", "json", (&url.URL{Scheme: "file", Path: sdkResponsePath}).String(), nil, nil, "")
	insertPayloadRef(t, ctx, db, "reasoning_stream", "text", (&url.URL{Scheme: "file", Path: reasoningPath}).String(), nil, nil, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	if got := findArtifact(t, artifacts, ClassLLMSDKRequest, testPayloadNativeArtifactID(sdkRequestPath)); got.Class != ClassLLMSDKRequest {
		t.Fatalf("sdk_request class = %q, want %q", got.Class, ClassLLMSDKRequest)
	}
	if got := findArtifact(t, artifacts, ClassLLMSDKResponse, testPayloadNativeArtifactID(sdkResponsePath)); got.Class != ClassLLMSDKResponse {
		t.Fatalf("sdk_response class = %q, want %q", got.Class, ClassLLMSDKResponse)
	}
	reasoning := findArtifact(t, artifacts, ClassReasoningText, testPayloadNativeArtifactID(reasoningPath))
	if reasoning.HashDomain != HashSemanticText || reasoning.Chars != 5 {
		t.Fatalf("reasoning_stream proof = domain:%q chars:%d, want semantic_text/5", reasoning.HashDomain, reasoning.Chars)
	}
}

func TestPayloadRefClassAdapterContentMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		row      canonicalPayloadRefRow
		selector Selector
		want     ArtifactClass
	}{
		{
			name: "opencode assistant text field",
			row: canonicalPayloadRefRow{
				adapter: "opencode",
				kind:    "llm_response",
				opKind:  "llm",
			},
			selector: Selector{FieldPath: "text"},
			want:     ClassAssistantMessage,
		},
		{
			name: "codex assistant payload content",
			row: canonicalPayloadRefRow{
				adapter: "codex",
				kind:    "llm_response",
				opKind:  "llm",
				opName:  "message",
			},
			selector: Selector{JSONPointer: "/payload/content/0/text"},
			want:     ClassAssistantMessage,
		},
		{
			name: "codex legacy assistant item content",
			row: canonicalPayloadRefRow{
				adapter: "codex",
				kind:    "llm_response",
				opKind:  "llm",
				opName:  "message",
			},
			selector: Selector{JSONPointer: "/items/0/content/0/text"},
			want:     ClassAssistantMessage,
		},
		{
			name: "claude code assistant content",
			row: canonicalPayloadRefRow{
				adapter: "claude-code",
				kind:    "llm_response",
				opKind:  "llm",
			},
			selector: Selector{JSONPointer: "/message/content/0/text"},
			want:     ClassAssistantMessage,
		},
		{
			name: "claude code user image",
			row: canonicalPayloadRefRow{
				adapter: "claude-code",
				kind:    "tool_request",
				opKind:  "internal",
				opName:  "user_input",
			},
			selector: Selector{JSONPointer: "/message/content/0"},
			want:     ClassUserImage,
		},
		{
			name: "codex user image item",
			row: canonicalPayloadRefRow{
				adapter: "codex",
				kind:    "tool_request",
				opKind:  "internal",
				opName:  "user_input",
			},
			selector: Selector{JSONPointer: "/payload/images/0"},
			want:     ClassUserImage,
		},
		{
			name: "codex user image aggregate",
			row: canonicalPayloadRefRow{
				adapter: "codex",
				kind:    "tool_request",
				opKind:  "internal",
				opName:  "user_input",
			},
			selector: Selector{JSONPointer: "/payload/image_details"},
			want:     ClassUserImage,
		},
		{
			name: "opencode user image field",
			row: canonicalPayloadRefRow{
				adapter: "opencode",
				kind:    "tool_request",
				opKind:  "internal",
				opName:  "user_input",
			},
			selector: Selector{FieldPath: "prompt.files.0"},
			want:     ClassUserImage,
		},
		{
			name: "opencode user prompt field",
			row: canonicalPayloadRefRow{
				adapter: "opencode",
				kind:    "tool_request",
				opKind:  "internal",
				opName:  "user_input",
			},
			selector: Selector{FieldPath: "prompt.files.thumbnail"},
			want:     ClassUserPrompt,
		},
		{
			name: "ordinary tool request",
			row: canonicalPayloadRefRow{
				adapter: "codex",
				kind:    "tool_request",
				opKind:  "tool",
				opName:  "read_file",
			},
			selector: Selector{JSONPointer: "/payload/images/0"},
			want:     ClassToolRequest,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := payloadRefClass(tt.row, tt.selector)
			if err != nil {
				t.Fatalf("payloadRefClass: %v", err)
			}
			if got != tt.want {
				t.Fatalf("payloadRefClass = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractCanonicalPayloadRefMarksMissingProofUnverifiable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	storedBytes := int64(4096)
	insertPayloadRef(t, ctx, db, "tool_response", "json", "opencode-sqlite://?part_id=part-2&field=data.output", nil, &storedBytes, "")

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassToolResponse, "part:part-2:data.output")
	if got.Availability != AvailabilityUnverifiable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityUnverifiable)
	}
	if got.ComputedSHA256 != "" {
		t.Fatalf("computed_sha256 = %q, want empty", got.ComputedSHA256)
	}
	if got.Bytes != -1 {
		t.Fatalf("bytes = %d, want -1 when length proof is missing", got.Bytes)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("unverifiable artifact should still have comparable identity: %v", err)
	}
}

func TestExtractCanonicalRejectsUnknownPayloadKind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	insertPayloadRef(t, ctx, db, "new_kind", "text", "file:///tmp/payload.txt", nil, nil, "")

	if _, err := ExtractCanonical(ctx, db); err == nil {
		t.Fatalf("ExtractCanonical returned nil error for unknown payload kind")
	}
}

func TestExtractCanonicalForSourceIDsIgnoresUnrelatedBrokenPayloadRefs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	insertUnrelatedCanonicalPayloadRefFixture(t, ctx, db)

	artifacts, err := ExtractCanonicalForSourceIDs(ctx, db, []string{"codex:test-source"})
	if err != nil {
		t.Fatalf("extract scoped canonical manifest: %v", err)
	}

	for _, artifact := range artifacts {
		if artifact.SourceID != "codex:test-source" {
			t.Fatalf("scoped extractor returned source_id=%q artifact: %+v", artifact.SourceID, artifact)
		}
	}
	if got := findArtifact(t, artifacts, ClassOpBoundary, "op:7:3"); got.SourceID != "codex:test-source" {
		t.Fatalf("focused op source_id = %q, want codex:test-source", got.SourceID)
	}
}

func TestExtractCanonicalForSourceIDsToWriterMatchesSliceExtractor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message)
VALUES ('session-1', NULL, 'turn-1', 'op-1', 1500, 'WRN', 'codex', 'tool output unmatched')`)

	payloadPath := filepath.Join(t.TempDir(), "payload.jsonl")
	const payload = `{"message":"exact"}`
	if err := os.WriteFile(payloadPath, []byte(payload+"\n"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	location := &url.URL{Scheme: "file", Path: payloadPath, Fragment: "L1"}
	q := location.Query()
	q.Set("json_pointer", "/message")
	location.RawQuery = q.Encode()
	insertPayloadRef(t, ctx, db, "llm_response", "json", location.String(), nil, nil, "")

	want, err := ExtractCanonicalForSourceIDs(ctx, db, []string{"codex:test-source"})
	if err != nil {
		t.Fatalf("extract scoped canonical manifest: %v", err)
	}

	var got []Artifact
	err = ExtractCanonicalForSourceIDsToWriter(ctx, db, []string{"codex:test-source"}, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("stream scoped canonical manifest: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed canonical artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestExtractCanonicalQuerierWrappersAndFilter(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message)
VALUES ('session-1', NULL, 'turn-1', 'op-1', 1500, 'WRN', 'codex', 'tool output unmatched')`)

	want, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}
	streamed := []Artifact{}
	err = ExtractCanonicalToWriter(ctx, db, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		streamed = append(streamed, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("stream canonical manifest: %v", err)
	}
	if !reflect.DeepEqual(streamed, want) {
		t.Fatalf("streamed canonical artifacts mismatch\ngot=%+v\nwant=%+v", streamed, want)
	}

	got, err := ExtractCanonicalFromQuerier(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest from querier: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("querier artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}

	streamed = nil
	err = ExtractCanonicalFromQuerierToWriter(ctx, db, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		streamed = append(streamed, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("stream canonical manifest from querier: %v", err)
	}
	if !reflect.DeepEqual(streamed, want) {
		t.Fatalf("streamed querier artifacts mismatch\ngot=%+v\nwant=%+v", streamed, want)
	}

	scopedWant, err := ExtractCanonicalForSourceIDs(ctx, db, []string{"codex:test-source"})
	if err != nil {
		t.Fatalf("extract scoped canonical manifest: %v", err)
	}
	scopedGot, err := ExtractCanonicalForSourceIDsFromQuerier(ctx, db, []string{"codex:test-source"})
	if err != nil {
		t.Fatalf("extract scoped canonical manifest from querier: %v", err)
	}
	if !reflect.DeepEqual(scopedGot, scopedWant) {
		t.Fatalf("scoped querier artifacts mismatch\ngot=%+v\nwant=%+v", scopedGot, scopedWant)
	}

	streamed = nil
	err = ExtractCanonicalForSourceIDsFromQuerierToWriter(ctx, db, []string{"codex:test-source"}, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		streamed = append(streamed, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("stream scoped canonical manifest from querier: %v", err)
	}
	if !reflect.DeepEqual(streamed, scopedWant) {
		t.Fatalf("streamed scoped querier artifacts mismatch\ngot=%+v\nwant=%+v", streamed, scopedWant)
	}

	streamed = nil
	err = ExtractCanonicalForSourceIDsFromQuerierToWriterFiltered(ctx, db, []string{"codex:test-source"}, allowAllArtifactFilter{}, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		streamed = append(streamed, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("stream filtered scoped canonical manifest from querier: %v", err)
	}
	if !reflect.DeepEqual(streamed, scopedWant) {
		t.Fatalf("filtered scoped querier artifacts mismatch\ngot=%+v\nwant=%+v", streamed, scopedWant)
	}
}

func TestExtractCanonicalStructuralRowsEmitIdentityArtifacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	session := findArtifact(t, artifacts, ClassSessionBoundary, "session:native-session-1")
	if session.HashDomain != HashIdentityJSON {
		t.Fatalf("session hash_domain = %q, want %q", session.HashDomain, HashIdentityJSON)
	}
	if session.CanonicalSessionID != "session-1" || session.CanonicalTurnID != "" || session.CanonicalOpID != "" {
		t.Fatalf("session evidence mismatch: %+v", session)
	}

	turn := findArtifact(t, artifacts, ClassTurnBoundary, "turn:7")
	if turn.NativeTurnID != "turn:7" {
		t.Fatalf("turn native_turn_id = %q, want turn:7", turn.NativeTurnID)
	}
	if turn.CanonicalSessionID != "session-1" || turn.CanonicalTurnID != "turn-1" || turn.CanonicalOpID != "" {
		t.Fatalf("turn evidence mismatch: %+v", turn)
	}

	op := findArtifact(t, artifacts, ClassOpBoundary, "op:7:3")
	if op.CanonicalSessionID != "session-1" || op.CanonicalTurnID != "turn-1" || op.CanonicalOpID != "op-1" {
		t.Fatalf("op evidence mismatch: %+v", op)
	}
	for _, artifact := range []Artifact{session, turn, op} {
		if artifact.Availability != AvailabilityAvailable {
			t.Fatalf("%s availability = %q, want %q", artifact.Class, artifact.Availability, AvailabilityAvailable)
		}
		if artifact.Bytes <= 0 {
			t.Fatalf("%s bytes = %d, want positive identity_json length", artifact.Class, artifact.Bytes)
		}
		if artifact.ComputedSHA256 == "" {
			t.Fatalf("%s computed_sha256 empty", artifact.Class)
		}
		if err := artifact.Validate(); err != nil {
			t.Fatalf("%s artifact invalid: %v", artifact.Class, err)
		}
	}
}

func TestExtractCanonicalOpErrorAndSubagentLinkArtifacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	execSQL(t, ctx, db, `UPDATE sources SET format='claude-code' WHERE id='codex:test-source'`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, parent_session_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('child-session-1', 'codex:test-source', 'native-child-session-1', 'session-1', 'session-1', 'child', 'completed', 1300, 1400)`)
	execSQL(t, ctx, db, `
UPDATE ops
SET status='failed', error_class='model_error', error_message='upstream failed', child_session_id='child-session-1'
WHERE id='op-1'`)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	errorArtifact := findArtifact(t, artifacts, ClassLLMError, "op:7:3:error")
	if errorArtifact.HashDomain != HashIdentityJSON {
		t.Fatalf("error hash_domain = %q, want %q", errorArtifact.HashDomain, HashIdentityJSON)
	}
	if errorArtifact.CanonicalOpID != "op-1" || errorArtifact.NativeTurnID != "turn:7" {
		t.Fatalf("error artifact evidence mismatch: %+v", errorArtifact)
	}

	linkArtifact := findArtifact(t, artifacts, ClassSubagentLink, "op:7:3:child_session:native-child-session-1")
	if linkArtifact.HashDomain != HashIdentityJSON {
		t.Fatalf("link hash_domain = %q, want %q", linkArtifact.HashDomain, HashIdentityJSON)
	}
	if linkArtifact.CanonicalOpID != "op-1" || linkArtifact.NativeSessionID != "native-session-1" {
		t.Fatalf("link artifact evidence mismatch: %+v", linkArtifact)
	}
}

func TestExtractCanonicalCodexFailedLLMErrorIsNotSourceVisible(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	execSQL(t, ctx, db, `
UPDATE ops
SET status='failed', error_class='model_error', error_message='upstream failed'
WHERE id='op-1'`)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	op := findArtifact(t, artifacts, ClassOpBoundary, "op:7:3")
	if op.Adapter != codexFormat || op.Availability != AvailabilityAvailable {
		t.Fatalf("codex failed op boundary should remain available: %+v", op)
	}
	for _, artifact := range artifacts {
		if artifact.Class == ClassLLMError && artifact.NativeArtifactID == "op:7:3:error" {
			t.Fatalf("codex llm_error artifact should be suppressed as not_source_visible: %+v", artifact)
		}
	}
}

func TestExtractCanonicalOpencodePatchMetadataArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	filesSHA := opencodePatchFilesSHA256([]string{"/work/proj/internal/a.go", "/work/proj/internal/b.go"})
	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('opencode:test-source', 'opencode', '/tmp/opencode.db', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('opencode-session-1', 'opencode:test-source', 'ses_open01', 'opencode-session-1', 'root', 'completed', 1000, 9000)`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES ('opencode-turn-1', 'opencode-session-1', 1, 2000, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status, extras_json)
VALUES ('opencode-op-llm-1', 'opencode-turn-1', 'opencode-session-1', 2, 'llm', 'claude-x', 2100, 'completed', ?)`,
		fmt.Sprintf(`{"patches":[{"part_id":"prt_step_zpatch","hash":"abc123","files_count":2,"files_sha256":%q}]}`, filesSHA))

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassPatchMetadata, "part:prt_step_zpatch:patch"), struct {
		NativeSessionID string `json:"native_session_id"`
		TurnSeq         int64  `json:"turn_seq"`
		OpSeq           int64  `json:"op_seq"`
		PartID          string `json:"part_id"`
		Hash            string `json:"hash,omitempty"`
		FilesCount      int64  `json:"files_count"`
		FilesSHA256     string `json:"files_sha256"`
	}{
		NativeSessionID: "ses_open01",
		TurnSeq:         1,
		OpSeq:           2,
		PartID:          "prt_step_zpatch",
		Hash:            "abc123",
		FilesCount:      2,
		FilesSHA256:     filesSHA,
	})
}

func TestExtractCanonicalAIAgentV2ReasoningFinalArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('aiagent_v2:test-source', 'aiagent_v2', '/tmp/aiagent-v2', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('aiagent-v2-session-1', 'aiagent_v2:test-source', 'root-session', 'aiagent-v2-session-1', 'root', 'completed', 1000, 2000)`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES ('aiagent-v2-turn-1', 'aiagent-v2-session-1', 1, 1100, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status, extras_json)
VALUES ('aiagent-v2-op-reasoning-1', 'aiagent-v2-turn-1', 'aiagent-v2-session-1', 3, 'reasoning', '', 1200, 1300, 'completed', '{"reasoning.final":"root reasoning summary"}')`)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassReasoningText, "op:1:3:reasoning.final")
	if got.Adapter != aiAgentV2Format || got.SourceID != "aiagent_v2:test-source" {
		t.Fatalf("source identity mismatch: %+v", got)
	}
	if got.NativeSessionID != "root-session" || got.NativeTurnID != "turn:1" {
		t.Fatalf("native scope mismatch: %+v", got)
	}
	if got.CanonicalSessionID != "aiagent-v2-session-1" || got.CanonicalTurnID != "aiagent-v2-turn-1" || got.CanonicalOpID != "aiagent-v2-op-reasoning-1" {
		t.Fatalf("canonical evidence mismatch: %+v", got)
	}
	if got.Availability != AvailabilityAvailable || got.HashDomain != HashSemanticText {
		t.Fatalf("availability/hash_domain mismatch: %+v", got)
	}
	if got.Selector.URI != aiAgentV2SelectorURI("ops", "root-session", "op:1:3") || got.Selector.FieldPath != "reasoning.final" {
		t.Fatalf("selector = %+v, want aiagent_v2 reasoning.final selector", got.Selector)
	}
	if got.Bytes != int64(len("root reasoning summary")) || got.Chars != int64(len("root reasoning summary")) {
		t.Fatalf("reasoning proof length mismatch: %+v", got)
	}
	if got.ComputedSHA256 != stringSHA256("root reasoning summary") {
		t.Fatalf("computed_sha256 = %q, want %q", got.ComputedSHA256, stringSHA256("root reasoning summary"))
	}
}

func TestExtractCanonicalAIAgentV2FinalReportArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('aiagent_v2:test-source', 'aiagent_v2', '/tmp/aiagent-v2', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts, extras_json)
VALUES ('aiagent-v2-session-1', 'aiagent_v2:test-source', 'root-session', 'aiagent-v2-session-1', 'root', 'completed', 1000, 2000, '{"final_report":{"format":"json","captured":true,"summary":"All checks passed."}}')`)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	got := findArtifact(t, artifacts, ClassAssistantMessage, "session:root-session:final_report")
	if got.Adapter != aiAgentV2Format || got.SourceID != "aiagent_v2:test-source" {
		t.Fatalf("source identity mismatch: %+v", got)
	}
	if got.NativeSessionID != "root-session" || got.NativeTurnID != "" {
		t.Fatalf("native scope mismatch: %+v", got)
	}
	if got.CanonicalSessionID != "aiagent-v2-session-1" || got.CanonicalTurnID != "" || got.CanonicalOpID != "" {
		t.Fatalf("canonical evidence mismatch: %+v", got)
	}
	if got.Availability != AvailabilityAvailable || got.HashDomain != HashCanonicalJSON {
		t.Fatalf("availability/hash_domain mismatch: %+v", got)
	}
	if got.Selector.URI != aiAgentV2SelectorURI("sessions", "root-session", "session:root-session") || got.Selector.FieldPath != "finalReport" {
		t.Fatalf("selector = %+v, want aiagent_v2 finalReport selector", got.Selector)
	}
	want := aiAgentV2FinalReportCanonicalJSON(t)
	if got.Bytes != int64(len(want)) || got.Chars != -1 {
		t.Fatalf("final_report proof length mismatch: %+v", got)
	}
	if got.ComputedSHA256 != sha256HexForTest(want) {
		t.Fatalf("computed_sha256 = %q, want %q", got.ComputedSHA256, sha256HexForTest(want))
	}
}

func TestExtractCanonicalAdapterSpecificDerivedArtifacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalAIAgentV2DerivedFixture(t, ctx, db)
	insertCanonicalAIAgentV3DerivedFixture(t, ctx, db)
	insertCanonicalClaudeCodeDerivedFixture(t, ctx, db)
	insertCanonicalCodexDerivedFixture(t, ctx, db)
	insertCanonicalOpencodeDerivedFixture(t, ctx, db)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "op:3:1:system"), systemOpIdentity{
		NativeSessionID: "v2-root",
		TurnSeq:         3,
		OpSeq:           1,
		OpKind:          "system",
		Name:            "maintenance",
		Status:          "completed",
		StartedAt:       820,
		EndedAt:         ptrInt64ForTest(830),
		OriginalKind:    "internal",
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:3:2:compaction"), aiAgentV2CompactionEventIdentity{
		NativeSessionID:      "v2-root",
		TurnSeq:              3,
		OpSeq:                2,
		Trigger:              "history_compaction",
		StepKind:             "internal",
		Name:                 "history_compaction.turn_summarizer",
		Provider:             "history-compaction",
		ChildNativeSessionID: "v2-compaction-child",
		ArchivedTurn:         2,
		CurrentTurn:          3,
		Status:               "completed",
		StartedAt:            840,
		EndedAt:              ptrInt64ForTest(850),
	})

	v3Metadata := findArtifact(t, artifacts, ClassSessionMetadata, "session:v3-child:metadata")
	assertIdentityArtifact(t, v3Metadata, aiAgentV3SessionMetadataIdentity{
		NativeSessionID:       "v3-child",
		OriginID:              "v3-root",
		AgentID:               "summarizer",
		CallPath:              "root:child",
		ParentNativeSessionID: "v3-root",
		ParentOpID:            "parent-op",
		HeadendID:             "sub-agent",
		CapturePayloads:       true,
		Attributes: map[string]any{
			"ledgerPath": "session/v3-child.jsonl",
		},
	})
	v3Session := findArtifact(t, artifacts, ClassSessionBoundary, "session:v3-child")
	assertIdentityArtifact(t, v3Session, sessionBoundaryIdentity{
		NativeSessionID:       "v3-child",
		ParentNativeSessionID: "v3-root",
		RootNativeSessionID:   "v3-root",
		Kind:                  "sub_agent",
		Status:                "completed",
		StartedAt:             1000,
		EndedAt:               ptrInt64ForTest(2000),
	})
	v3Synthetic := findArtifact(t, artifacts, ClassSessionBoundary, "session:v3-synthetic")
	if v3Synthetic.Availability != AvailabilityPartialSource {
		t.Fatalf("synthetic aiagent_v3 availability = %q, want %q", v3Synthetic.Availability, AvailabilityPartialSource)
	}
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "op:5:1:system"), systemOpIdentity{
		NativeSessionID: "v3-child",
		TurnSeq:         5,
		OpSeq:           1,
		OpKind:          "system",
		Name:            "maintenance",
		Status:          "completed",
		StartedAt:       1200,
		EndedAt:         ptrInt64ForTest(1300),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:5:2:compaction"), aiAgentV3CompactionEventIdentity{
		NativeSessionID:      "v3-child",
		TurnSeq:              5,
		OpSeq:                2,
		Trigger:              "history_compaction",
		Name:                 "history_compaction.turn_summarizer",
		Provider:             "history-compaction",
		ChildNativeSessionID: "v3-compaction-child",
		ArchivedTurn:         4,
		CurrentTurn:          5,
		Status:               "completed",
		StartedAt:            1400,
		EndedAt:              ptrInt64ForTest(1500),
	})

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:claude-root:metadata"), claudeCodeSessionMetadataIdentity{
		NativeSessionID:       "claude-root",
		LastPromptSHA256:      stringSHA256("summarize this project"),
		CustomTitle:           "Custom title",
		AITitle:               "AI title",
		PermissionMode:        "acceptEdits",
		BridgeSessionID:       "bridge-1",
		BridgeLastSequenceNum: 9,
		FileHistorySHA256:     sha256HexForTest([]byte(`{"files":["main.go"]}`)),
		PRLinks: []claudeCodePRLinkIdentity{{
			Number:     12,
			URL:        "https://example.invalid/pr/12",
			Repository: "org/repo",
		}},
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:2:3:compaction"), compactionEventIdentity{
		NativeSessionID:         "claude-root",
		TurnSeq:                 2,
		OpSeq:                   3,
		Trigger:                 "manual",
		PreTokens:               700,
		PostTokens:              70,
		MetadataPreTokens:       650,
		MetadataPostTokens:      65,
		DurationMs:              25,
		StartedAt:               2400,
		EndedAt:                 ptrInt64ForTest(2500),
		PreservedSegmentSHA256:  sha256HexForTest([]byte(`{"headUuid":"u1"}`)),
		PreservedMessagesSHA256: sha256HexForTest([]byte(`{"uuids":["u1"]}`)),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "line:3:/system"), claudeCodeSystemOpIdentity{
		NativeSessionID: "claude-root",
		TurnSeq:         2,
		Subtype:         "git_status",
		Severity:        "INF",
		Message:         "git status captured",
		Timestamp:       2600,
		ContentSHA256:   stringSHA256("branch main"),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassAttachmentMetadata, "line:4:/attachment"), claudeCodeAttachmentMetadataIdentity{
		NativeSessionID: "claude-root",
		TurnSeq:         2,
		AttachmentType:  "file",
		Filename:        "main.go",
		DisplayPath:     "main.go",
	})

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:1:1:compaction"), codexCompactionEventIdentity{
		NativeSessionID:        "codex-root",
		TurnSeq:                1,
		OpSeq:                  1,
		Trigger:                "manual",
		ReplacementHistorySize: 42,
		MessagePreviewSHA256:   stringSHA256("summary text"),
		StartedAt:              3100,
		EndedAt:                ptrInt64ForTest(3200),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:codex-root:metadata"), codexSessionMetadataIdentity{
		NativeSessionID: "codex-root",
		AgentName:       "codex-reviewer",
		CwdSHA256:       stringSHA256("/work/codex"),
		CLIVersion:      "1.2.3",
		Originator:      "cli",
		Source:          "subagent",
		ModelProvider:   "openai",
		GitSHA256:       sha256HexForTest([]byte(`{"branch":"main","commit_hash":"abc","repository_url":"https://example.invalid/repo.git"}`)),
		Relationship:    "sub_agent",
		SubagentDepth:   2,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, expectedLogNativeArtifactID("turn:1", 3300, "INF", "codex", "event_msg:thread_goal_updated")), codexSystemOpIdentity{
		NativeSessionID: "codex-root",
		TurnSeq:         1,
		EventType:       "thread_goal_updated",
		Severity:        "INF",
		Message:         "event_msg:thread_goal_updated",
		Timestamp:       3300,
	})

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:opencode-root:metadata"), opencodeSessionMetadataIdentity{
		NativeSessionID: "opencode-root",
		AgentName:       "builder",
		ModelID:         "model-x",
		ProviderID:      "provider-x",
		Variant:         "max",
		Version:         "0.9.0",
		Slug:            "demo",
		Title:           "Demo",
		ProjectID:       "project-1",
		DirectorySHA256: stringSHA256("/work/opencode"),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassLLMError, "turn:1:assistant_error"), opencodeAssistantErrorIdentity{
		NativeSessionID:    "opencode-root",
		TurnSeq:            1,
		ErrorClass:         "assistant_error",
		ErrorMessageSHA256: stringSHA256("assistant failed"),
		Timestamp:          4550,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "part:prt_compact:compaction"), opencodeCompactionEventIdentity{
		NativeSessionID: "opencode-root",
		TurnSeq:         1,
		OpSeq:           1,
		Auto:            true,
		Timestamp:       4300,
		Severity:        "INF",
		Message:         opencodeCompactionLogMessage(true),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassAttachmentMetadata, "part:prt_file:file"), opencodeAttachmentMetadataIdentity{
		NativeSessionID: "opencode-root",
		TurnSeq:         1,
		OpSeq:           1,
		Filename:        "main.go",
		URL:             "file:///main.go",
		MIME:            "text/plain",
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "session_message:msg1:system_op"), opencodeSessionMessageIdentity{
		NativeSessionID:  "opencode-root",
		SessionMessageID: "msg1",
		EventType:        "agent-switched",
		Seq:              7,
		Timestamp:        4500,
		Severity:         "INF",
		Message:          "session agent switched",
		Agent:            "build",
		ModelID:          "m1",
		ProviderID:       "p1",
		Variant:          "v",
		DataSHA256:       "abc123",
	})
}

func TestExtractCanonicalLogEntriesEmitSemanticArtifacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message)
VALUES ('session-1', NULL, 'turn-1', 'op-1', 1500, 'WRN', 'codex', 'tool output unmatched')`)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	nativeID := expectedLogNativeArtifactID("op:7:3", 1500, "WRN", "codex", "tool output unmatched")
	got := findArtifact(t, artifacts, ClassLogEntry, nativeID)
	if got.NativeSessionID != "native-session-1" || got.NativeTurnID != "turn:7" {
		t.Fatalf("log native scope mismatch: %+v", got)
	}
	if got.CanonicalSessionID != "session-1" || got.CanonicalTurnID != "turn-1" || got.CanonicalOpID != "op-1" {
		t.Fatalf("log canonical evidence mismatch: %+v", got)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	wantHash := sha256.Sum256([]byte("tool output unmatched"))
	if got.Bytes != int64(len("tool output unmatched")) || got.Chars != int64(len("tool output unmatched")) || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("log proof mismatch: %+v", got)
	}
}

func TestExtractCanonicalSourceLevelLogEntryUsesSourceScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message)
VALUES (NULL, 'codex:test-source', NULL, NULL, 1600, 'ERR', 'codex', 'parse failed')`)

	artifacts, err := ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("extract canonical manifest: %v", err)
	}

	nativeID := expectedLogNativeArtifactID("source", 1600, "ERR", "codex", "parse failed")
	got := findArtifact(t, artifacts, ClassLogEntry, nativeID)
	if got.NativeSessionID != "source:codex:test-source" {
		t.Fatalf("native_session_id = %q, want source:codex:test-source", got.NativeSessionID)
	}
	if got.CanonicalSessionID != "" || got.CanonicalTurnID != "" || got.CanonicalOpID != "" {
		t.Fatalf("source-level log should not have canonical session/turn/op evidence: %+v", got)
	}
}

func TestCanonicalLogEntryQueriesUseSplitSourceScope(t *testing.T) {
	t.Parallel()

	scope, err := canonicalScopeForSourceIDs([]string{"codex:test-source"})
	if err != nil {
		t.Fatalf("canonicalScopeForSourceIDs: %v", err)
	}
	queries := canonicalLogEntryQueries(scope)
	if len(queries) != 2 {
		t.Fatalf("scoped log query count = %d, want 2", len(queries))
	}
	joined := queries[0].sql + "\n" + queries[1].sql
	if strings.Contains(joined, "COALESCE(l.source_id, sess.source_id)") {
		t.Fatalf("scoped log queries must not use COALESCE source join:\n%s", joined)
	}
	if !strings.Contains(joined, "l.source_id IN (?)") {
		t.Fatalf("scoped log queries missing direct source branch:\n%s", joined)
	}
	if !strings.Contains(joined, "sess.source_id IN (?)") || !strings.Contains(joined, "l.source_id IS NULL") {
		t.Fatalf("scoped log queries missing session-derived branch:\n%s", joined)
	}
}

func TestExtractCanonicalForSourceIDsScopesLogEntries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openCanonicalTestDB(t, ctx)
	insertCanonicalFixture(t, ctx, db)
	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('codex:other-source', 'codex', 'file:///tmp/other-codex', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('session-other', 'codex:other-source', 'native-session-other', 'session-other', 'root', 'completed', 1000, 2000)`)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message)
VALUES
  ('session-1', NULL, NULL, NULL, 1500, 'WRN', 'codex', 'target session log'),
  (NULL, 'codex:test-source', NULL, NULL, 1600, 'ERR', 'codex', 'target source log'),
  ('session-other', NULL, NULL, NULL, 1700, 'WRN', 'codex', 'unrelated session log'),
  (NULL, 'codex:other-source', NULL, NULL, 1800, 'ERR', 'codex', 'unrelated source log')`)

	artifacts, err := ExtractCanonicalForSourceIDs(ctx, db, []string{"codex:test-source"})
	if err != nil {
		t.Fatalf("extract scoped canonical manifest: %v", err)
	}

	logs := 0
	for _, artifact := range artifacts {
		if artifact.Class != ClassLogEntry {
			continue
		}
		logs++
		if artifact.SourceID != "codex:test-source" {
			t.Fatalf("scoped log artifact source_id = %q, want target source: %+v", artifact.SourceID, artifact)
		}
	}
	if logs != 2 {
		t.Fatalf("scoped log artifact count = %d, want 2", logs)
	}
}

type allowAllArtifactFilter struct{}

func (allowAllArtifactFilter) IncludeArtifactKey(MatchKey, ClasslessKey) bool {
	return true
}

func (allowAllArtifactFilter) IncludeClasslessKey(ClasslessKey) bool {
	return true
}

func insertCanonicalAIAgentV2DerivedFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('aiagent_v2:derived-source', 'aiagent_v2', '/tmp/aiagent-v2', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('v2-session', 'aiagent_v2:derived-source', 'v2-root', 'v2-session', 'root', 'completed', 800, 900)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, parent_session_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('v2-compaction-child-session', 'aiagent_v2:derived-source', 'v2-compaction-child', 'v2-session', 'v2-session', 'sub_agent', 'completed', 840, 850)`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status)
VALUES ('v2-turn-3', 'v2-session', 3, 810, 890, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status, extras_json)
VALUES ('v2-op-system', 'v2-turn-3', 'v2-session', 1, 'system', 'maintenance', 820, 830, 'completed', '{"original_kind":"internal"}')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status, child_session_id, extras_json)
VALUES ('v2-op-compaction', 'v2-turn-3', 'v2-session', 2, 'session', 'history_compaction.turn_summarizer', 840, 850, 'completed', 'v2-compaction-child-session', ?)`,
		`{"step.kind":"internal","attr.provider":"history-compaction","attr.archivedTurn":2,"attr.currentTurn":3}`)
}

func insertCanonicalAIAgentV3DerivedFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('aiagent_v3:test-source', 'aiagent_v3', '/tmp/aiagent-v3', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, end_ts, last_activity_ts, agent_name, call_path, extras_json)
VALUES ('v3-child-session', 'aiagent_v3:test-source', 'v3-child', 'v3-child-session', 'sub_agent', 'completed', 1000, 2000, 2000, 'summarizer', 'root:child', ?)`,
		`{"capturePayloads":true,"originId":"","parentSessionId":"v3-root","parentOpId":"parent-op","headendId":"sub-agent","attr.ledgerPath":"session/v3-child.jsonl","aiViewer":{"rootNativeId":"v3-root","parentNativeId":"v3-root"}}`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, parent_session_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('v3-compaction-child-session', 'aiagent_v3:test-source', 'v3-compaction-child', 'v3-child-session', 'v3-child-session', 'sub_agent', 'completed', 1400, 1500)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts, extras_json)
VALUES ('v3-synthetic-session', 'aiagent_v3:test-source', 'v3-synthetic', 'v3-synthetic-session', 'sub_agent', 'running', 1600, 1600, ?)`,
		`{"synthesizedFromParent":true,"aiViewer":{"rootNativeId":"v3-root","parentNativeId":"v3-root"}}`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status)
VALUES ('v3-turn-5', 'v3-child-session', 5, 1100, 1900, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status)
VALUES ('v3-op-system', 'v3-turn-5', 'v3-child-session', 1, 'system', 'maintenance', 1200, 1300, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status, child_session_id, extras_json)
VALUES ('v3-op-compaction', 'v3-turn-5', 'v3-child-session', 2, 'session', 'history_compaction.turn_summarizer', 1400, 1500, 'completed', 'v3-compaction-child-session', ?)`,
		`{"attr.provider":"history-compaction","attr.archivedTurn":4,"attr.currentTurn":5}`)
}

func insertCanonicalClaudeCodeDerivedFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('claude-code:test-source', 'claude-code', '/tmp/claude-code.jsonl', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts, extras_json)
VALUES ('claude-session', 'claude-code:test-source', 'claude-root', 'claude-session', 'root', 'completed', 2000, 3000, ?)`,
		`{"lastPrompt":"summarize this project","customTitle":"Custom title","aiTitle":"AI title","permissionMode":"acceptEdits","bridge.bridgeSessionId":"bridge-1","bridge.lastSequenceNum":9,"fileHistory":{"files":["main.go"]},"prLinks":[{"prNumber":12,"prUrl":"https://example.invalid/pr/12","prRepository":"org/repo"}]}`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status)
VALUES ('claude-turn-2', 'claude-session', 2, 2100, 2900, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status, bytes_in, bytes_out, extras_json)
VALUES ('claude-op-compaction', 'claude-turn-2', 'claude-session', 3, 'compaction', 'compaction', 2400, 2500, 'completed', 700, 70, ?)`,
		`{"trigger":"manual","preTokens":650,"postTokens":65,"durationMs":25,"preservedSegment":{"headUuid":"u1"},"preservedMessages":{"uuids":["u1"]}}`)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES ('claude-session', NULL, 'claude-turn-2', NULL, 2600, 'INF', 'claude-code', 'git status captured', ?)`,
		`{"recordType":"system","subtype":"git_status","content":"branch main","aiViewer":{"parity":{"class":"system_op","nativeArtifactId":"line:3:/system","selectorURI":"file:///tmp/claude-code.jsonl#L3","jsonPointer":"/system"}}}`)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES ('claude-session', NULL, 'claude-turn-2', NULL, 2700, 'INF', 'claude-code', 'file attachment', ?)`,
		`{"attachmentType":"file","filename":"main.go","displayPath":"main.go","aiViewer":{"parity":{"class":"attachment_metadata","nativeArtifactId":"line:4:/attachment","selectorURI":"file:///tmp/claude-code.jsonl#L4","jsonPointer":"/attachment"}}}`)
}

func insertCanonicalCodexDerivedFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('codex:derived-source', 'codex', '/tmp/codex.jsonl', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, agent_name, status, start_ts, last_activity_ts, extras_json)
VALUES ('codex-session', 'codex:derived-source', 'codex-root', 'codex-session', 'root', 'codex-reviewer', 'completed', 3000, 3300, ?)`,
		`{"cwd":"/work/codex","cli_version":"1.2.3","originator":"cli","source":"subagent","model_provider":"openai","relationship":"sub_agent","subagent_depth":2,"git":{"branch":"main","commit_hash":"abc","repository_url":"https://example.invalid/repo.git"}}`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status)
VALUES ('codex-turn-1', 'codex-session', 1, 3050, 3250, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status, extras_json)
VALUES ('codex-op-compaction', 'codex-turn-1', 'codex-session', 1, 'compaction', 'compaction', 3100, 3200, 'completed', ?)`,
		`{"trigger":"manual","replacement_history_size":42,"message_preview":"summary text"}`)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message)
VALUES ('codex-session', NULL, 'codex-turn-1', NULL, 3300, 'INF', 'codex', 'event_msg:thread_goal_updated')`)
}

func insertCanonicalOpencodeDerivedFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('opencode:derived-source', 'opencode', '/tmp/opencode.db', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, agent_name, model, provider_alias, cwd, status, error_class, error_message, start_ts, last_activity_ts, extras_json)
VALUES ('opencode-session', 'opencode:derived-source', 'opencode-root', 'opencode-session', 'root', 'builder', 'model-x', 'provider-x', '/work/opencode', 'failed', 'assistant_error', 'assistant failed', 4000, 4600, ?)`,
		`{"variant":"max","version":"0.9.0","slug":"demo","title":"Demo","project_id":"project-1"}`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status, error_class)
VALUES ('opencode-turn-1', 'opencode-session', 1, 4100, 4550, 'failed', 'assistant_error')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status)
VALUES ('opencode-op-1', 'opencode-turn-1', 'opencode-session', 1, 'llm', 'assistant', 4200, 4400, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES ('opencode-session', NULL, 'opencode-turn-1', 'opencode-op-1', 4300, 'INF', 'opencode', ?, ?)`,
		opencodeCompactionLogMessage(true),
		`{"part_id":"prt_compact","auto":true}`)
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES ('opencode-session', NULL, 'opencode-turn-1', 'opencode-op-1', 4400, 'INF', 'opencode', 'file attachment', ?)`,
		`{"part_id":"prt_file","filename":"main.go","url":"file:///main.go","mime":"text/plain"}`)
	sessionMessage, ok := opencodeSessionMessageLogMessage("agent-switched")
	if !ok {
		t.Fatal("agent-switched opencode session message log is not registered")
	}
	execSQL(t, ctx, db, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES ('opencode-session', NULL, NULL, NULL, 4500, 'INF', 'opencode', ?, ?)`,
		sessionMessage,
		`{"session_message_id":"msg1","session_message_type":"agent-switched","seq":7,"agent":"build","model_id":"m1","provider_id":"p1","variant":"v","data_sha256":"abc123"}`)
}

func ptrInt64ForTest(v int64) *int64 {
	return &v
}

func openCanonicalTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+url.QueryEscape(t.Name())+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := store.Up(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func insertCanonicalFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	execSQL(t, ctx, db, `
	INSERT INTO sources (id, format, location, created_at)
	VALUES ('codex:test-source', 'codex', ?, 1000)`, os.TempDir())
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('session-1', 'codex:test-source', 'native-session-1', 'session-1', 'root', 'completed', 1000, 2000)`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES ('turn-1', 'session-1', 7, 1100, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status)
VALUES ('op-1', 'turn-1', 'session-1', 3, 'llm', 'response', 1200, 'completed')`)
}

func insertUnrelatedCanonicalPayloadRefFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	execSQL(t, ctx, db, `
INSERT INTO sources (id, format, location, created_at)
VALUES ('codex:other-source', 'codex', 'file:///tmp/other-codex', 1000)`)
	execSQL(t, ctx, db, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES ('session-other', 'codex:other-source', 'other-session', 'session-other', 'root', 'completed', 1000, 2000)`)
	execSQL(t, ctx, db, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES ('turn-other', 'session-other', 1, 1100, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status)
VALUES ('op-other', 'turn-other', 'session-other', 1, 'llm', 'response', 1200, 'completed')`)
	execSQL(t, ctx, db, `
INSERT INTO payload_refs (op_id, kind, format, location_uri)
VALUES ('op-other', 'unexpected_future_kind', 'json', 'file:///definitely/not/here.jsonl#L1')`)
}

func insertPayloadRef(t *testing.T, ctx context.Context, db *sql.DB, kind string, format string, locationURI string, originalBytes *int64, storedBytes *int64, sha string) {
	t.Helper()

	insertPayloadRefCompressed(t, ctx, db, kind, format, locationURI, "", originalBytes, storedBytes, sha)
}

func insertPayloadRefCompressed(t *testing.T, ctx context.Context, db *sql.DB, kind string, format string, locationURI string, compression string, originalBytes *int64, storedBytes *int64, sha string) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
INSERT INTO payload_refs (op_id, kind, format, compression, location_uri, original_bytes, stored_bytes, sha256)
VALUES ('op-1', ?, ?, ?, ?, ?, ?, ?)`,
		kind,
		format,
		nullableString(compression),
		locationURI,
		nullableInt64(originalBytes),
		nullableInt64(storedBytes),
		sha,
	)
	if err != nil {
		t.Fatalf("insert payload_ref: %v", err)
	}
}

func nullableInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func nullableString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func execSQL(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()

	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func testPayloadNativeArtifactID(path string) string {
	rel, err := filepath.Rel(os.TempDir(), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "file:" + filepath.Base(path)
	}
	return "file:" + filepath.ToSlash(rel)
}

func findArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeArtifactID string) Artifact {
	t.Helper()

	for _, artifact := range artifacts {
		if artifact.Class == class && artifact.NativeArtifactID == nativeArtifactID {
			return artifact
		}
	}
	t.Fatalf("artifact class=%s native_artifact_id=%s not found in %+v", class, nativeArtifactID, artifacts)
	return Artifact{}
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatalf("write gzip fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip fixture: %v", err)
	}
	return buf.Bytes()
}

func expectedLogNativeArtifactID(scope string, ts int64, severity string, source string, message string) string {
	sourceHash := sha256.Sum256([]byte(source))
	messageHash := sha256.Sum256([]byte(message))
	return fmt.Sprintf("log:%s:%d:%s:%x:%x", scope, ts, severity, sourceHash[:6], messageHash[:6])
}
