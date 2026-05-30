package codex

import (
	"errors"
	"testing"
)

func TestParseLine_BlankAndWhitespaceSkip(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "\t\n", "  \r\n  "} {
		rec, skip, err := parseLine([]byte(in))
		if err != nil {
			t.Errorf("parseLine(%q): unexpected err %v", in, err)
		}
		if !skip {
			t.Errorf("parseLine(%q): want skip=true", in)
		}
		_ = rec
	}
}

func TestParseLine_MalformedJSON(t *testing.T) {
	t.Parallel()
	_, _, err := parseLine([]byte(`{not json`))
	if err == nil {
		t.Fatal("parseLine(malformed): want error")
	}
}

func TestParseLine_MissingType(t *testing.T) {
	t.Parallel()
	_, _, err := parseLine([]byte(`{"timestamp":"2025-11-20T16:59:09.857Z","payload":{}}`))
	if err == nil {
		t.Fatal("parseLine(no type): want error")
	}
}

// TestParseLine_UnknownTopLevelType asserts an unknown RolloutItem.type is
// surfaced (not silently dropped) and detectable via errors.Is so the caller
// can dedup one SourceError per distinct variant (spec adapter-codex.md:220).
func TestParseLine_UnknownTopLevelType(t *testing.T) {
	t.Parallel()
	rec, skip, err := parseLine([]byte(`{"timestamp":"2025-11-20T16:59:09.857Z","type":"totally_made_up","payload":{}}`))
	if err == nil {
		t.Fatal("parseLine(unknown top-level type): want error")
	}
	if !errors.Is(err, errUnknownRecordType) {
		t.Fatalf("parseLine(unknown top-level type): want errUnknownRecordType, got %v", err)
	}
	var ute *unknownTypeError
	if !errors.As(err, &ute) || ute.Type != "totally_made_up" {
		t.Fatalf("unknownTypeError.Type = %v, want totally_made_up", err)
	}
	_ = rec
	_ = skip
}

// TestParseLine_UnknownNestedPayloadType asserts that an unknown nested
// payload.type (inside a known top-level type) is surfaced via a SEPARATE
// sentinel so the caller can dedup one SourceError per distinct nested variant
// (spec adapter-codex.md:221). It must be distinguishable from the top-level
// unknown so dedup keys never collide.
func TestParseLine_UnknownNestedPayloadType(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:09.857Z","type":"response_item","payload":{"type":"brand_new_variant","foo":1}}`
	rec, skip, err := parseLine([]byte(line))
	if err == nil {
		t.Fatal("parseLine(unknown nested payload type): want error")
	}
	if !errors.Is(err, errUnknownPayloadType) {
		t.Fatalf("want errUnknownPayloadType, got %v", err)
	}
	var upe *unknownPayloadTypeError
	if !errors.As(err, &upe) {
		t.Fatalf("want *unknownPayloadTypeError, got %T (%v)", err, err)
	}
	// The dedup key embeds both the owner top-level type and the nested type
	// so "response_item/brand_new_variant" never collides with a different
	// owner carrying the same nested name.
	if upe.Owner != "response_item" || upe.Type != "brand_new_variant" {
		t.Fatalf("unknownPayloadTypeError = {Owner:%q Type:%q}, want {response_item brand_new_variant}", upe.Owner, upe.Type)
	}
	// The two unknown sentinels must be distinct so dedup never conflates a
	// top-level unknown with a nested unknown of the same string.
	if errors.Is(err, errUnknownRecordType) {
		t.Fatal("nested-unknown must NOT match errUnknownRecordType")
	}
	_ = rec
	_ = skip
}

// TestParseLine_SessionMeta decodes line 1 of a real rollout: a session_meta
// carrying id, originator, cli_version, cwd, source, and a flattened git block.
func TestParseLine_SessionMeta(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{` +
		`"id":"019aa234-a2a1-75c3-a9bf-d8425e1785f5",` +
		`"timestamp":"2025-11-20T16:59:09.857Z",` +
		`"cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0",` +
		`"source":"exec","model_provider":"openai",` +
		`"git":{"commit_hash":"abc123","branch":"main","repository_url":"git@github.com:example/example.git"}}}`
	rec, skip, err := parseLine([]byte(line))
	if err != nil || skip {
		t.Fatalf("parseLine(session_meta): err=%v skip=%v", err, skip)
	}
	if rec.Type() != recSessionMeta {
		t.Fatalf("Type() = %q, want %q", rec.Type(), recSessionMeta)
	}
	if rec.SessionMeta == nil {
		t.Fatal("SessionMeta payload not decoded")
	}
	if rec.SessionMeta.ID != "019aa234-a2a1-75c3-a9bf-d8425e1785f5" {
		t.Errorf("id = %q", rec.SessionMeta.ID)
	}
	if rec.SessionMeta.Originator != "codex_exec" || rec.SessionMeta.CLIVersion != "0.125.0" {
		t.Errorf("originator/cli_version wrong: %+v", rec.SessionMeta)
	}
	if rec.SessionMeta.Git == nil || rec.SessionMeta.Git.Branch != "main" {
		t.Errorf("git not decoded: %+v", rec.SessionMeta.Git)
	}
}

// TestParseLine_SessionMetaSourceVariants exercises the polymorphic
// SessionSource enum: bare strings, {custom}, {internal}, and the nested
// {subagent:{thread_spawn:{parent_thread_id,...}}} (spec adapter-codex.md:114-122).
func TestParseLine_SessionMetaSourceVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		sourceJSON string
		wantKind   sourceKind
		wantParent string
	}{
		{"string-exec", `"exec"`, sourceRoot, ""},
		{"string-cli", `"cli"`, sourceRoot, ""},
		{"string-unknown", `"unknown"`, sourceRoot, ""},
		{"custom", `{"custom":"my_tool"}`, sourceRoot, ""},
		{"internal", `{"internal":"memory_consolidation"}`, sourceInternal, ""},
		{"subagent-string", `{"subagent":"review"}`, sourceSubagent, ""},
		{"subagent-threadspawn", `{"subagent":{"thread_spawn":{"parent_thread_id":"parent-uuid","depth":1,"agent_role":"explorer"}}}`, sourceSubagent, "parent-uuid"},
		{"other-forward-compat", `{"brand_new":"x"}`, sourceOther, ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			line := `{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{"id":"sid","source":` + c.sourceJSON + `}}`
			rec, _, err := parseLine([]byte(line))
			if err != nil {
				t.Fatalf("parseLine: %v", err)
			}
			if rec.SessionMeta == nil {
				t.Fatal("SessionMeta nil")
			}
			gotKind, gotParent := rec.SessionMeta.classifySource()
			if gotKind != c.wantKind {
				t.Errorf("classifySource kind = %q, want %q", gotKind, c.wantKind)
			}
			if gotParent != c.wantParent {
				t.Errorf("classifySource parent = %q, want %q", gotParent, c.wantParent)
			}
		})
	}
}

// TestParseLine_TurnContext decodes a turn_context with model, sandbox_policy,
// approval_policy, effort and turn_id.
func TestParseLine_TurnContext(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:10.000Z","type":"turn_context","payload":{` +
		`"turn_id":"turn-1","cwd":"<ROOT>","model":"gpt-5.1-codex-max","effort":"high",` +
		`"approval_policy":"on-request","sandbox_policy":{"type":"workspace-write"}}}`
	rec, skip, err := parseLine([]byte(line))
	if err != nil || skip {
		t.Fatalf("parseLine(turn_context): err=%v skip=%v", err, skip)
	}
	if rec.TurnContext == nil {
		t.Fatal("TurnContext payload not decoded")
	}
	if rec.TurnContext.TurnID != "turn-1" || rec.TurnContext.Model != "gpt-5.1-codex-max" {
		t.Errorf("turn_context fields wrong: %+v", rec.TurnContext)
	}
	if rec.TurnContext.Effort != "high" || rec.TurnContext.ApprovalPolicy != "on-request" {
		t.Errorf("policy fields wrong: %+v", rec.TurnContext)
	}
	if rec.TurnContext.sandboxType() != "workspace-write" {
		t.Errorf("sandboxType = %q, want workspace-write", rec.TurnContext.sandboxType())
	}
}

// TestParseLine_TurnContextSandboxVariants covers the three observed sandbox
// modes plus an unknown one (must NOT hard-fail; forward-compat per
// adapter-codex.md:222).
func TestParseLine_TurnContextSandboxVariants(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"workspace-write", "danger-full-access", "read-only", "brand-new-mode"} {
		line := `{"timestamp":"2025-11-20T16:59:10.000Z","type":"turn_context","payload":{"model":"m","sandbox_policy":{"type":"` + mode + `"}}}`
		rec, _, err := parseLine([]byte(line))
		if err != nil {
			t.Fatalf("parseLine(sandbox %q): %v", mode, err)
		}
		if rec.TurnContext.sandboxType() != mode {
			t.Errorf("sandboxType = %q, want %q", rec.TurnContext.sandboxType(), mode)
		}
	}
}

// TestParseLine_ResponseItemVariants covers the persisted ResponseItem nested
// payload.type variants (spec adapter-codex.md:149-163). Each must decode into
// a record with ResponseItem populated and the nested type recoverable.
func TestParseLine_ResponseItemVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		payloadJSON string
		wantType    string
	}{
		{"message", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`, "message"},
		{"reasoning", `{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"AAAA"}`, "reasoning"},
		{"function_call", `{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"}`, "function_call"},
		{"function_call_output", `{"type":"function_call_output","call_id":"c1","output":"done"}`, "function_call_output"},
		{"custom_tool_call", `{"type":"custom_tool_call","call_id":"c2","name":"x","input":"i","status":"completed"}`, "custom_tool_call"},
		{"custom_tool_call_output", `{"type":"custom_tool_call_output","call_id":"c2","output":"o"}`, "custom_tool_call_output"},
		{"web_search_call", `{"type":"web_search_call","call_id":"w1","status":"completed","action":{"type":"search","query":"q"}}`, "web_search_call"},
		{"image_generation_call", `{"type":"image_generation_call","id":"i1","status":"completed"}`, "image_generation_call"},
		{"compaction", `{"type":"compaction","encrypted_content":"BBBB"}`, "compaction"},
		{"context_compaction", `{"type":"context_compaction","encrypted_content":null}`, "context_compaction"},
		{"local_shell_call-legacy", `{"type":"local_shell_call","call_id":"l1"}`, "local_shell_call"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			line := `{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":` + c.payloadJSON + `}`
			rec, skip, err := parseLine([]byte(line))
			if err != nil || skip {
				t.Fatalf("parseLine(%s): err=%v skip=%v", c.name, err, skip)
			}
			if rec.ResponseItem == nil {
				t.Fatalf("ResponseItem nil for %s", c.name)
			}
			if rec.ResponseItem.Type != c.wantType {
				t.Errorf("ResponseItem.Type = %q, want %q", rec.ResponseItem.Type, c.wantType)
			}
		})
	}
}

// TestParseLine_ResponseItemOtherIsTolerated asserts the Rust #[serde(other)]
// ResponseItem::Other catch-all behavior: a known-as-catch-all variant
// (ghost_snapshot) is NOT a hard fail and NOT an unknown-payload error — it
// decodes into Other and is skipped silently (spec adapter-codex.md:163-165,
// rule #21 strip-and-ignore).
func TestParseLine_GhostSnapshotSkippedSilently(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"ghost_snapshot","data":{}}}`
	rec, skip, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("ghost_snapshot must not error, got %v", err)
	}
	if !skip {
		t.Fatal("ghost_snapshot must be skipped silently (skip=true)")
	}
	_ = rec
}

// TestParseLine_EventMsgVariants covers a representative spread of the
// persisted EventMsg variants across both Limited and Extended modes
// (spec adapter-codex.md:173-204).
func TestParseLine_EventMsgVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		payloadJSON string
		wantType    string
	}{
		{"user_message", `{"type":"user_message","message":"hi"}`, "user_message"},
		{"agent_message", `{"type":"agent_message","message":"answer","phase":"final_answer"}`, "agent_message"},
		{"agent_reasoning", `{"type":"agent_reasoning","text":"reason"}`, "agent_reasoning"},
		{"agent_reasoning_raw_content", `{"type":"agent_reasoning_raw_content","text":"raw cot"}`, "agent_reasoning_raw_content"},
		{"token_count", `{"type":"token_count","info":{"total_token_usage":{"total_tokens":100},"last_token_usage":{"input_tokens":10,"output_tokens":5}},"model_context_window":200000}`, "token_count"},
		{"task_started", `{"type":"task_started","turn_id":"turn-1","started_at":1763664000}`, "task_started"},
		{"task_complete", `{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-11-20T17:00:00.000Z","duration_ms":1000}`, "task_complete"},
		{"turn_aborted", `{"type":"turn_aborted","turn_id":"turn-1","reason":"interrupted"}`, "turn_aborted"},
		{"exec_command_end", `{"type":"exec_command_end","call_id":"c1","exit_code":0,"aggregated_output":"out"}`, "exec_command_end"},
		{"mcp_tool_call_end", `{"type":"mcp_tool_call_end","call_id":"c1","invocation":{"server":"gh","tool":"list"}}`, "mcp_tool_call_end"},
		{"patch_apply_end", `{"type":"patch_apply_end","call_id":"c1","success":true,"status":"completed"}`, "patch_apply_end"},
		{"context_compacted", `{"type":"context_compacted"}`, "context_compacted"},
		{"web_search_end", `{"type":"web_search_end","call_id":"w1","query":"q"}`, "web_search_end"},
		{"error", `{"type":"error","message":"boom"}`, "error"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			line := `{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":` + c.payloadJSON + `}`
			rec, skip, err := parseLine([]byte(line))
			if err != nil || skip {
				t.Fatalf("parseLine(%s): err=%v skip=%v", c.name, err, skip)
			}
			if rec.EventMsg == nil {
				t.Fatalf("EventMsg nil for %s", c.name)
			}
			if rec.EventMsg.Type != c.wantType {
				t.Errorf("EventMsg.Type = %q, want %q", rec.EventMsg.Type, c.wantType)
			}
		})
	}
}

// TestParseLine_Compacted decodes the top-level compacted line.
func TestParseLine_Compacted(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:13.000Z","type":"compacted","payload":{"message":"summary text","replacement_history":[{"type":"message","role":"user","content":[]}]}}`
	rec, skip, err := parseLine([]byte(line))
	if err != nil || skip {
		t.Fatalf("parseLine(compacted): err=%v skip=%v", err, skip)
	}
	if rec.Compacted == nil {
		t.Fatal("Compacted payload not decoded")
	}
	if rec.Compacted.Message != "summary text" {
		t.Errorf("compacted.message = %q", rec.Compacted.Message)
	}
	if rec.Compacted.replacementHistorySize() != 1 {
		t.Errorf("replacementHistorySize = %d, want 1", rec.Compacted.replacementHistorySize())
	}
}

// TestParseLine_TimestampPreserved asserts the envelope timestamp is captured
// verbatim on every record (it is the canonical Ts source per
// adapter-codex.md:56-60, 100-101).
func TestParseLine_TimestampPreserved(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:09.857Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`
	rec, _, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if rec.Timestamp() != "2025-11-20T16:59:09.857Z" {
		t.Errorf("Timestamp() = %q", rec.Timestamp())
	}
}

// TestParseLine_RawPreserved asserts the verbatim line bytes are retained so a
// later chunk's mapper can build a file://...#L<line> PayloadRef without
// re-typing every nested variant (mirrors claude_code rec.Raw).
func TestParseLine_RawPreserved(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:09.857Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`
	rec, _, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if string(rec.Raw) != line {
		t.Errorf("Raw = %q, want verbatim line", rec.Raw)
	}
}

// TestParseLine_EmptyPayloadTolerated asserts a known top-level type with an
// absent payload does not panic and does not hard-fail (some metadata-only
// appends may carry an empty body).
func TestParseLine_EmptyPayloadTolerated(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:09.857Z","type":"event_msg"}`
	rec, skip, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine(empty payload): %v", err)
	}
	// An event_msg with no payload carries no nested type; the parser must not
	// crash. It surfaces as a skip (nothing actionable) rather than an error.
	if !skip {
		t.Fatalf("empty-payload event_msg: want skip=true, got rec=%+v", rec)
	}
}

// TestParseLine_MissingNestedType asserts a known top-level type whose payload
// lacks the nested discriminator is tolerated (skip, no panic) rather than
// being reported as an unknown nested variant.
func TestParseLine_MissingNestedType(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"2025-11-20T16:59:09.857Z","type":"response_item","payload":{"role":"assistant"}}`
	rec, skip, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine(missing nested type): %v", err)
	}
	if !skip {
		t.Fatalf("missing nested type: want skip=true, got rec=%+v", rec)
	}
}
