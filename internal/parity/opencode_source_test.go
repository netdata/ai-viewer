package parity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestExtractOpencodeSourceStructuralAndPayloadArtifacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	sourceID := "opencode:" + dbPath

	artifacts, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}

	if got := findArtifact(t, artifacts, ClassSessionBoundary, "session:ses_open01"); got.NativeSessionID != "ses_open01" {
		t.Fatalf("session boundary native session = %q, want ses_open01", got.NativeSessionID)
	}
	if got := findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"); got.NativeTurnID != "turn:1" {
		t.Fatalf("turn boundary native turn = %q, want turn:1", got.NativeTurnID)
	}
	if got := findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"); got.NativeArtifactID != "op:1:1" {
		t.Fatalf("user_input op boundary native id = %q, want op:1:1", got.NativeArtifactID)
	}
	if got := findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"); got.NativeArtifactID != "op:1:2" {
		t.Fatalf("llm op boundary native id = %q, want op:1:2", got.NativeArtifactID)
	}
	if got := findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"); got.NativeArtifactID != "op:1:3" {
		t.Fatalf("reasoning op boundary native id = %q, want op:1:3", got.NativeArtifactID)
	}
	if got := findArtifact(t, artifacts, ClassOpBoundary, "op:1:4"); got.NativeArtifactID != "op:1:4" {
		t.Fatalf("tool op boundary native id = %q, want op:1:4", got.NativeArtifactID)
	}
	if got := findArtifact(t, artifacts, ClassToolError, "op:1:4:error"); got.ComputedSHA256 == "" {
		t.Fatalf("tool_error is missing identity hash: %+v", got)
	}
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:ses_open01:metadata"), struct {
		NativeSessionID string `json:"native_session_id"`
		AgentName       string `json:"agent_name,omitempty"`
		ModelID         string `json:"model_id,omitempty"`
		ProviderID      string `json:"provider_id,omitempty"`
		Variant         string `json:"variant,omitempty"`
		Version         string `json:"version,omitempty"`
		Slug            string `json:"slug,omitempty"`
		Title           string `json:"title,omitempty"`
		ProjectID       string `json:"project_id,omitempty"`
		DirectorySHA256 string `json:"directory_sha256,omitempty"`
	}{
		NativeSessionID: "ses_open01",
		AgentName:       "general",
		ModelID:         "claude-x",
		ProviderID:      "anthropic",
		Version:         "1.0.0",
		Slug:            "calm-otter",
		Title:           "Opencode parity",
		ProjectID:       "prj_x",
		DirectorySHA256: stringSHA256("/work/proj"),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "session_message:evt_agent:system_op"), struct {
		NativeSessionID  string `json:"native_session_id"`
		SessionMessageID string `json:"session_message_id"`
		EventType        string `json:"event_type"`
		Seq              int64  `json:"seq,omitempty"`
		Timestamp        int64  `json:"timestamp"`
		Severity         string `json:"severity"`
		Message          string `json:"message"`
		Agent            string `json:"agent,omitempty"`
		DataSHA256       string `json:"data_sha256"`
	}{
		NativeSessionID:  "ses_open01",
		SessionMessageID: "evt_agent",
		EventType:        "agent-switched",
		Seq:              1,
		Timestamp:        1600000,
		Severity:         "INF",
		Message:          "session agent switched",
		Agent:            "reviewer",
		DataSHA256:       opencodeCanonicalJSONHash(t, `{"agent":"reviewer"}`),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "session_message:evt_model:system_op"), struct {
		NativeSessionID  string `json:"native_session_id"`
		SessionMessageID string `json:"session_message_id"`
		EventType        string `json:"event_type"`
		Seq              int64  `json:"seq,omitempty"`
		Timestamp        int64  `json:"timestamp"`
		Severity         string `json:"severity"`
		Message          string `json:"message"`
		ModelID          string `json:"model_id,omitempty"`
		ProviderID       string `json:"provider_id,omitempty"`
		Variant          string `json:"variant,omitempty"`
		DataSHA256       string `json:"data_sha256"`
	}{
		NativeSessionID:  "ses_open01",
		SessionMessageID: "evt_model",
		EventType:        "model-switched",
		Seq:              2,
		Timestamp:        1700000,
		Severity:         "INF",
		Message:          "session model switched",
		ModelID:          "gpt-5",
		ProviderID:       "openai",
		Variant:          "fast",
		DataSHA256:       opencodeCanonicalJSONHash(t, `{"model":{"id":"gpt-5","providerID":"openai","variant":"fast"}}`),
	})

	prompt := findArtifact(t, artifacts, ClassUserPrompt, "input:msg_user:prompt.text")
	if prompt.HashDomain != HashSemanticText || prompt.ComputedSHA256 != stringSHA256("summarize README.md") {
		t.Fatalf("user prompt proof = domain:%q hash:%s", prompt.HashDomain, prompt.ComputedSHA256)
	}
	userImage := findArtifact(t, artifacts, ClassUserImage, "input:msg_user:prompt.files.0")
	if userImage.HashDomain != HashCanonicalJSON ||
		userImage.ComputedSHA256 != opencodeCanonicalJSONHash(t, `{"description":"screen","mime":"image/png","name":"diagram.png","uri":"file:///tmp/diagram.png"}`) {
		t.Fatalf("user image proof = domain:%q hash:%s", userImage.HashDomain, userImage.ComputedSHA256)
	}
	if userImage.Selector.FieldPath != "prompt.files.0" {
		t.Fatalf("user image field path = %q, want prompt.files.0", userImage.Selector.FieldPath)
	}
	reasoning := findArtifact(t, artifacts, ClassReasoningText, "part:prt_reason:text")
	if reasoning.HashDomain != HashSemanticText || reasoning.ComputedSHA256 != stringSHA256("thinking") {
		t.Fatalf("reasoning proof = domain:%q hash:%s", reasoning.HashDomain, reasoning.ComputedSHA256)
	}
	text := findArtifact(t, artifacts, ClassAssistantMessage, "part:prt_text:text")
	if text.HashDomain != HashSemanticText || text.ComputedSHA256 != stringSHA256("answer") {
		t.Fatalf("assistant text proof = domain:%q hash:%s", text.HashDomain, text.ComputedSHA256)
	}
	request := findArtifact(t, artifacts, ClassToolRequest, "part:prt_tool:state.input")
	if request.HashDomain != HashCanonicalJSON || request.ComputedSHA256 != opencodeCanonicalJSONHash(t, `{"cmd":"cat","path":"README.md"}`) {
		t.Fatalf("tool request proof = domain:%q hash:%s", request.HashDomain, request.ComputedSHA256)
	}
	response := findArtifact(t, artifacts, ClassToolResponse, "part:prt_tool:state.output")
	if response.HashDomain != HashSemanticText || response.ComputedSHA256 != stringSHA256("partial output") {
		t.Fatalf("tool response proof = domain:%q hash:%s", response.HashDomain, response.ComputedSHA256)
	}

	compaction := findArtifact(t, artifacts, ClassCompactionEvent, "part:prt_step_zcompaction:compaction")
	if compaction.HashDomain != HashIdentityJSON || compaction.NativeTurnID != "turn:1" {
		t.Fatalf("compaction proof = domain:%q turn:%q", compaction.HashDomain, compaction.NativeTurnID)
	}
	attachment := findArtifact(t, artifacts, ClassAttachmentMetadata, "part:prt_step_zfile:file")
	if attachment.HashDomain != HashIdentityJSON || attachment.NativeTurnID != "turn:1" {
		t.Fatalf("attachment metadata proof = domain:%q turn:%q", attachment.HashDomain, attachment.NativeTurnID)
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
		OpSeq:           3,
		PartID:          "prt_step_zpatch",
		Hash:            "abc123",
		FilesCount:      2,
		FilesSHA256:     opencodeCanonicalJSONHash(t, `["/work/proj/internal/a.go","/work/proj/internal/b.go"]`),
	})
	compactionLogID := logNativeArtifactID("op:1:3", 2700000, "INF", "opencode", "session compacted (auto=true)")
	compactionLog := findArtifact(t, artifacts, ClassLogEntry, compactionLogID)
	if compactionLog.HashDomain != HashSemanticText || compactionLog.ComputedSHA256 != stringSHA256("session compacted (auto=true)") {
		t.Fatalf("compaction log proof = domain:%q hash:%s", compactionLog.HashDomain, compactionLog.ComputedSHA256)
	}
	retryLogID := logNativeArtifactID("op:1:3", 2800000, "WRN", "opencode", "API retry attempt 2: RateLimit")
	retryLog := findArtifact(t, artifacts, ClassLogEntry, retryLogID)
	if retryLog.HashDomain != HashSemanticText || retryLog.ComputedSHA256 != stringSHA256("API retry attempt 2: RateLimit") {
		t.Fatalf("retry log proof = domain:%q hash:%s", retryLog.HashDomain, retryLog.ComputedSHA256)
	}
	fileLogID := logNativeArtifactID("op:1:3", 2900000, "INF", "opencode", "file attachment")
	fileLog := findArtifact(t, artifacts, ClassLogEntry, fileLogID)
	if fileLog.HashDomain != HashSemanticText || fileLog.ComputedSHA256 != stringSHA256("file attachment") {
		t.Fatalf("file log proof = domain:%q hash:%s", fileLog.HashDomain, fileLog.ComputedSHA256)
	}
	agentLog := findArtifact(t, artifacts, ClassLogEntry, "session_message:evt_agent:log")
	if agentLog.HashDomain != HashSemanticText || agentLog.ComputedSHA256 != stringSHA256("session agent switched") {
		t.Fatalf("agent switch log proof = domain:%q hash:%s", agentLog.HashDomain, agentLog.ComputedSHA256)
	}
	modelLog := findArtifact(t, artifacts, ClassLogEntry, "session_message:evt_model:log")
	if modelLog.HashDomain != HashSemanticText || modelLog.ComputedSHA256 != stringSHA256("session model switched") {
		t.Fatalf("model switch log proof = domain:%q hash:%s", modelLog.HashDomain, modelLog.ComputedSHA256)
	}
}

func TestOpencodeTaskChildSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data opencodeSourcePartData
		want string
	}{
		{
			name: "metadata session id",
			data: opencodeSourcePartData{State: &opencodeSourceToolState{
				Metadata: json.RawMessage(`{"sessionId":"child-session"}`),
			}},
			want: "child-session",
		},
		{
			name: "nil state",
			data: opencodeSourcePartData{},
		},
		{
			name: "blank metadata",
			data: opencodeSourcePartData{State: &opencodeSourceToolState{
				Metadata: json.RawMessage(`  `),
			}},
		},
		{
			name: "malformed metadata",
			data: opencodeSourcePartData{State: &opencodeSourceToolState{
				Metadata: json.RawMessage(`{"sessionId":`),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := opencodeTaskChildSessionID(tt.data); got != tt.want {
				t.Fatalf("opencodeTaskChildSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendSubagentLink(t *testing.T) {
	t.Parallel()

	var artifacts []Artifact
	state := &opencodeSourceState{
		sourceID: "opencode:/tmp/state.db",
		dbPath:   "/tmp/state.db",
		ctx:      context.Background(),
		writer: ArtifactWriterFunc(func(_ context.Context, artifact Artifact) error {
			artifacts = append(artifacts, artifact)
			return nil
		}),
	}

	scope := opencodeSourceTurnScope{sessionID: "parent-session", turnSeq: 7}
	if err := state.appendSubagentLink(scope, 3, "child-session"); err != nil {
		t.Fatalf("appendSubagentLink: %v", err)
	}

	artifact := findArtifact(t, artifacts, ClassSubagentLink, "op:7:3:child_session:child-session")
	if artifact.NativeSessionID != "parent-session" || artifact.NativeTurnID != "turn:7" {
		t.Fatalf("subagent link artifact has wrong native coordinates: %+v", artifact)
	}
	assertIdentityArtifact(t, artifact, subagentLinkIdentity{
		ParentNativeSessionID: "parent-session",
		ParentTurnSeq:         7,
		ParentOpSeq:           3,
		ChildNativeSessionID:  "child-session",
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	})
}

func TestExtractOpencodeSourceUserPromptSourceUnavailable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, false)
	sourceID := "opencode:" + dbPath

	artifacts, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}

	prompt := findArtifact(t, artifacts, ClassUserPrompt, "op:1:1:payload:tool_request:1")
	if prompt.Availability != AvailabilitySourceUnavailable {
		t.Fatalf("user prompt availability = %q, want source_unavailable", prompt.Availability)
	}
}

func TestExtractOpencodeSourceLLMErrorArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceLLMErrorFixtureDB(t)
	sourceID := "opencode:" + dbPath

	artifacts, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:ses_err01"), sessionBoundaryIdentity{
		NativeSessionID:     "ses_err01",
		RootNativeSessionID: "ses_err01",
		Kind:                "root",
		Status:              "failed",
		StartedAt:           1_000_000,
		EndedAt:             ptrInt64(9_000_000),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassLLMError, "turn:1:assistant_error"), struct {
		NativeSessionID    string `json:"native_session_id"`
		TurnSeq            int64  `json:"turn_seq"`
		ErrorClass         string `json:"error_class"`
		ErrorMessageSHA256 string `json:"error_message_sha256"`
		Timestamp          int64  `json:"timestamp"`
	}{
		NativeSessionID:    "ses_err01",
		TurnSeq:            1,
		ErrorClass:         "MessageAbortedError",
		ErrorMessageSHA256: stringSHA256("request was aborted by the user"),
		Timestamp:          9_000_000,
	})
}

func TestExtractOpencodeSourceUnknownMessageRoleReturnsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	execOpencodeSourceFixtureSQL(t, dbPath, `INSERT INTO message (id, session_id, time_created, time_updated, data)
		VALUES ('msg_future_role', 'ses_open01', 1800, 1800, '{"role":"future-role","time":{"created":1800}}')`)

	_, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: "opencode:" + dbPath,
	})
	if err == nil || !strings.Contains(err.Error(), `unknown opencode message role "future-role"`) {
		t.Fatalf("ExtractOpencodeSource error = %v, want unknown message role", err)
	}
}

func TestExtractOpencodeSourceMalformedMessageDataEmitsSourceCorruptionAndContinues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	sourceID := "opencode:" + dbPath
	execOpencodeSourceFixtureSQL(t, dbPath, `INSERT INTO message (id, session_id, time_created, time_updated, data)
		VALUES ('msg_bad_json', 'ses_open01', 1800, 1800, '{"role":')`)

	artifacts, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}
	corrupt := findArtifact(t, artifacts, ClassSourceCorruption, "source_corruption:message:msg_bad_json:data")
	if corrupt.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("availability = %q, want %q", corrupt.Availability, AvailabilitySourceCorrupt)
	}
	if corrupt.Selector.URI != opencodeSourceSelector("message", "msg_bad_json") {
		t.Fatalf("selector.uri = %q", corrupt.Selector.URI)
	}
	assertIntegrityFailures(t, corrupt, []IntegrityFailure{{
		Field:    "data",
		Expected: "valid opencode message JSON",
		Actual:   "decode_error",
	}})
	findArtifact(t, artifacts, ClassTurnBoundary, "turn:1")
}

func TestExtractOpencodeSourceUnknownPartTypeReturnsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	execOpencodeSourceFixtureSQL(t, dbPath, `INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
		VALUES ('prt_future_type', 'msg_assistant', 'ses_open01', 2350, 2350, '{"type":"future-part","text":"redacted"}')`)

	_, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: "opencode:" + dbPath,
	})
	if err == nil || !strings.Contains(err.Error(), `unknown opencode part type "future-part"`) {
		t.Fatalf("ExtractOpencodeSource error = %v, want unknown part type", err)
	}
}

func TestExtractOpencodeSourceMalformedPartDataEmitsSourceCorruptionAndContinues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	sourceID := "opencode:" + dbPath
	execOpencodeSourceFixtureSQL(t, dbPath, `INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
		VALUES ('prt_bad_json', 'msg_assistant', 'ses_open01', 2350, 2350, '{"type":')`)

	artifacts, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}
	corrupt := findArtifact(t, artifacts, ClassSourceCorruption, "source_corruption:part:prt_bad_json:data")
	if corrupt.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("availability = %q, want %q", corrupt.Availability, AvailabilitySourceCorrupt)
	}
	assertIntegrityFailures(t, corrupt, []IntegrityFailure{{
		Field:    "data",
		Expected: "valid opencode part JSON",
		Actual:   "decode_error",
	}})
	findArtifact(t, artifacts, ClassTurnBoundary, "turn:1")
}

func TestExtractOpencodeSourceUnknownSessionMessageTypeReturnsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	execOpencodeSourceFixtureSQL(t, dbPath, `INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
		VALUES ('evt_future', 'ses_open01', 'future-event', 3, 1750, 1750, '{"kind":"future"}')`)

	_, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: "opencode:" + dbPath,
	})
	if err == nil || !strings.Contains(err.Error(), `unknown opencode session_message type "future-event"`) {
		t.Fatalf("ExtractOpencodeSource error = %v, want unknown session_message type", err)
	}
}

func TestExtractOpencodeSourceMalformedSessionMessageDataEmitsSourceCorruptionAndContinues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	sourceID := "opencode:" + dbPath
	execOpencodeSourceFixtureSQL(t, dbPath, `INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
		VALUES ('evt_bad_json', 'ses_open01', 'agent-switched', 3, 1750, 1750, '{"kind":')`)

	artifacts, err := ExtractOpencodeSource(ctx, OpencodeSourceOptions{
		DBPath:   dbPath,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}
	corrupt := findArtifact(t, artifacts, ClassSourceCorruption, "source_corruption:session_message:evt_bad_json:data")
	if corrupt.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("availability = %q, want %q", corrupt.Availability, AvailabilitySourceCorrupt)
	}
	assertIntegrityFailures(t, corrupt, []IntegrityFailure{{
		Field:    "data",
		Expected: "valid opencode session_message JSON",
		Actual:   "decode_error",
	}})
	findArtifact(t, artifacts, ClassSessionBoundary, "session:ses_open01")
}

func TestExtractOpencodeSourceToWriterMatchesSliceExtractor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceFixtureDB(t, true)
	opts := OpencodeSourceOptions{DBPath: dbPath, SourceID: "opencode:" + dbPath}
	want, err := ExtractOpencodeSource(ctx, opts)
	if err != nil {
		t.Fatalf("ExtractOpencodeSource: %v", err)
	}

	var got []Artifact
	err = ExtractOpencodeSourceToWriter(ctx, opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("ExtractOpencodeSourceToWriter: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed opencode artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestExtractOpencodeSourceToWriterDoesNotPreloadLaterSessionRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourcePreloadSentinelDB(t)
	opts := OpencodeSourceOptions{DBPath: dbPath, SourceID: "opencode:" + dbPath}
	errStop := errors.New("stop after first artifact")

	err := ExtractOpencodeSourceToWriter(ctx, opts, ArtifactWriterFunc(func(_ context.Context, artifact Artifact) error {
		if artifact.NativeSessionID == "a_ok" && artifact.Class == ClassSessionBoundary {
			return errStop
		}
		return nil
	}))
	if !errors.Is(err, errStop) {
		t.Fatalf("ExtractOpencodeSourceToWriter error = %v, want %v", err, errStop)
	}
}

func TestExtractOpencodeSourceToWriterUsesSingleReadSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := writeOpencodeSourceSnapshotFixtureDB(t)
	opts := OpencodeSourceOptions{DBPath: dbPath, SourceID: "opencode:" + dbPath}
	mutated := false
	var artifacts []Artifact

	err := ExtractOpencodeSourceToWriter(ctx, opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		if !mutated && artifact.NativeSessionID == "ses_a" && artifact.Class == ClassSessionBoundary {
			mutated = true
			mutateOpencodeSourceSnapshotFixture(t, dbPath)
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("ExtractOpencodeSourceToWriter: %v", err)
	}
	if !mutated {
		t.Fatal("test did not mutate the fixture after the first session")
	}
	text := findArtifact(t, artifacts, ClassAssistantMessage, "part:prt_b_text:text")
	if text.ComputedSHA256 != stringSHA256("before mutation") {
		t.Fatalf("assistant text hash = %s, want original snapshot hash %s", text.ComputedSHA256, stringSHA256("before mutation"))
	}
}

func writeOpencodeSourceFixtureDB(t *testing.T, withSessionInput bool) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			cost REAL NOT NULL DEFAULT 0,
			tokens_input INTEGER NOT NULL DEFAULT 0,
			tokens_output INTEGER NOT NULL DEFAULT 0,
			tokens_reasoning INTEGER NOT NULL DEFAULT 0,
			tokens_cache_read INTEGER NOT NULL DEFAULT 0,
			tokens_cache_write INTEGER NOT NULL DEFAULT 0,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER, time_compacting INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			seq INTEGER NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_input (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, prompt TEXT NOT NULL,
			delivery TEXT, admitted_seq INTEGER NOT NULL, promoted_seq INTEGER,
			time_created INTEGER NOT NULL)`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('ses_open01', 'prj_x', '', 'calm-otter', '/work/proj', 'Opencode parity', '1.0.0', 'general',
			 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES
			('msg_user', 'ses_open01', 1500, 1500,
			 '{"role":"user","time":{"created":1500}}'),
			('msg_assistant', 'ses_open01', 2000, 9000,
			 '{"role":"assistant","parentID":"msg_user","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.02,"tokens":{"input":500,"output":80,"reasoning":0,"cache":{"read":100,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop"}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES
			('prt_step_start', 'msg_assistant', 'ses_open01', 2100, 2100, '{"type":"step-start"}'),
			('prt_reason', 'msg_assistant', 'ses_open01', 2200, 2300, '{"type":"reasoning","text":"thinking","time":{"start":2200,"end":2300}}'),
			('prt_text', 'msg_assistant', 'ses_open01', 2400, 2400, '{"type":"text","text":"answer"}'),
			('prt_step_zpatch', 'msg_assistant', 'ses_open01', 2450, 2450, '{"type":"patch","hash":"abc123","files":["/work/proj/internal/a.go","/work/proj/internal/b.go"]}'),
			('prt_step_zcompaction', 'msg_assistant', 'ses_open01', 2700, 2700, '{"type":"compaction","auto":true}'),
			('prt_step_zretry', 'msg_assistant', 'ses_open01', 2800, 2800, '{"type":"retry","attempt":2,"error":{"name":"RateLimit"}}'),
			('prt_step_zfile', 'msg_assistant', 'ses_open01', 2900, 2900, '{"type":"file","filename":"diagram.png","url":"https://cdn.example.invalid/diagram.png","mime":"image/png"}'),
			('prt_tool', 'msg_assistant', 'ses_open01', 2500, 2600, '{"type":"tool","callID":"call_1","tool":"bash","state":{"status":"error","input":{"cmd":"cat","path":"README.md"},"output":"partial output","error":"permission denied","time":{"start":2500,"end":2600}}}'),
			('prt_step_finish', 'msg_assistant', 'ses_open01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.02,"tokens":{"input":500,"output":80,"reasoning":0,"cache":{"read":100,"write":0}}}')`,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
			VALUES
			('evt_agent', 'ses_open01', 'agent-switched', 1, 1600, 1600, '{"agent":"reviewer"}'),
			('evt_model', 'ses_open01', 'model-switched', 2, 1700, 1700, '{"model":{"id":"gpt-5","providerID":"openai","variant":"fast"}}')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	if withSessionInput {
		if _, err := db.Exec(`INSERT INTO session_input
				(id, session_id, prompt, delivery, admitted_seq, promoted_seq, time_created)
				VALUES ('msg_user', 'ses_open01', '{"text":"summarize README.md","files":[{"uri":"file:///tmp/diagram.png","mime":"image/png","name":"diagram.png","description":"screen"},{"uri":"file:///tmp/readme.txt","mime":"text/plain","name":"readme.txt"}]}', 'delivered', 1, 1, 1500)`); err != nil {
			t.Fatalf("insert opencode session_input: %v", err)
		}
	}
	return dbPath
}

func execOpencodeSourceFixtureSQL(t *testing.T, dbPath string, statement string) {
	t.Helper()

	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode fixture mutator: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("exec opencode fixture mutator statement:\n%s\nerror: %v", statement, err)
	}
}

func writeOpencodeSourceSnapshotFixtureDB(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode snapshot fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('ses_a', 'prj_x', '', 'a', '/work/a', 'A', '1.0.0', 'general', NULL, 1000, 3000, NULL),
			('ses_b', 'prj_x', '', 'b', '/work/b', 'B', '1.0.0', 'general', NULL, 4000, 6000, NULL)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES
			('msg_a_user', 'ses_a', 1100, 1100, '{"role":"user","time":{"created":1100}}'),
			('msg_a_assistant', 'ses_a', 1200, 3000, '{"role":"assistant","parentID":"msg_a_user","providerID":"anthropic","modelID":"claude-x","agent":"general","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1200,"completed":3000},"finish":"stop"}'),
			('msg_b_user', 'ses_b', 4100, 4100, '{"role":"user","time":{"created":4100}}'),
			('msg_b_assistant', 'ses_b', 4200, 6000, '{"role":"assistant","parentID":"msg_b_user","providerID":"anthropic","modelID":"claude-x","agent":"general","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":4200,"completed":6000},"finish":"stop"}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES
			('prt_a_start', 'msg_a_assistant', 'ses_a', 1250, 1250, '{"type":"step-start"}'),
			('prt_a_text', 'msg_a_assistant', 'ses_a', 1300, 1300, '{"type":"text","text":"first session"}'),
			('prt_a_finish', 'msg_a_assistant', 'ses_a', 3000, 3000, '{"type":"step-finish","reason":"stop","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}'),
			('prt_b_start', 'msg_b_assistant', 'ses_b', 4250, 4250, '{"type":"step-start"}'),
			('prt_b_text', 'msg_b_assistant', 'ses_b', 4300, 4300, '{"type":"text","text":"before mutation"}'),
			('prt_b_finish', 'msg_b_assistant', 'ses_b', 6000, 6000, '{"type":"step-finish","reason":"stop","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode snapshot fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	return dbPath
}

func mutateOpencodeSourceSnapshotFixture(t *testing.T, dbPath string) {
	t.Helper()

	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode snapshot fixture mutator: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(
		`UPDATE part SET data = ?, time_updated = ? WHERE id = ?`,
		`{"type":"text","text":"after mutation"}`,
		7000,
		"prt_b_text",
	); err != nil {
		t.Fatalf("mutate opencode snapshot fixture: %v", err)
	}
}

func writeOpencodeSourcePreloadSentinelDB(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode preload fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part_raw (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			raw TEXT NOT NULL)`,
		`CREATE VIEW part AS
			SELECT id, message_id, session_id, time_created, time_updated,
				json_extract(raw, '$.data') AS data
			FROM part_raw`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('a_ok', 'prj_x', '', 'ok', '/work/ok', 'OK', '1.0.0', 'general', NULL, 1000, 1000, NULL),
			('z_bad', 'prj_x', '', 'bad', '/work/bad', 'Bad', '1.0.0', 'general', NULL, 2000, 2000, NULL)`,
		`INSERT INTO part_raw (id, message_id, session_id, time_created, time_updated, raw)
			VALUES ('bad_part', 'bad_message', 'z_bad', 2100, 2100, '{')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode preload fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	return dbPath
}

func writeOpencodeSourceLLMErrorFixtureDB(t *testing.T) string {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatalf("open opencode error fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()

	statements := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			cost REAL NOT NULL DEFAULT 0,
			tokens_input INTEGER NOT NULL DEFAULT 0,
			tokens_output INTEGER NOT NULL DEFAULT 0,
			tokens_reasoning INTEGER NOT NULL DEFAULT 0,
			tokens_cache_read INTEGER NOT NULL DEFAULT 0,
			tokens_cache_write INTEGER NOT NULL DEFAULT 0,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER, time_compacting INTEGER)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`INSERT INTO session
			(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
			VALUES
			('ses_err01', 'prj_x', '', 'amber-wolf', '/work/proj', 'Aborted session', '1.0.0', 'general',
			 '{"id":"claude-x","providerID":"anthropic"}', 1000, 9000, NULL)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES
			('msg_e1', 'ses_err01', 2000, 9000,
			 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop","error":{"name":"MessageAbortedError","data":{"message":"request was aborted by the user"}}}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES
			('prt_e01', 'msg_e1', 'ses_err01', 2100, 2100, '{"type":"step-start"}'),
			('prt_e03', 'msg_e1', 'ses_err01', 9000, 9000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":300,"output":40,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec opencode llm-error fixture statement:\n%s\nerror: %v", stmt, err)
		}
	}
	return dbPath
}

func opencodeCanonicalJSONHash(t *testing.T, raw string) string {
	t.Helper()

	var value interface{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	body, err := canonicalIdentityBytes(value)
	if err != nil {
		t.Fatalf("canonical json bytes: %v", err)
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum)
}
