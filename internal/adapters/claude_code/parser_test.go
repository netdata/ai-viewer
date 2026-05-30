package claude_code

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
	_, _, err := parseLine([]byte(`{"uuid":"x","sessionId":"s"}`))
	if err == nil {
		t.Fatal("parseLine(no type): want error")
	}
}

func TestParseLine_UnknownType(t *testing.T) {
	t.Parallel()
	_, _, err := parseLine([]byte(`{"type":"totally-made-up","sessionId":"s"}`))
	if err == nil {
		t.Fatal("parseLine(unknown type): want error")
	}
	if !errors.Is(err, errUnknownRecordType) {
		t.Fatalf("parseLine(unknown type): want errUnknownRecordType, got %v", err)
	}
}

func TestParseLine_KnownNoOpTypesSkippedSilently(t *testing.T) {
	t.Parallel()
	// Declared-but-ignored producer types skip WITHOUT surfacing an error.
	for _, typ := range []string{
		"summary", "task-summary", "tag", "mode", "worktree-state",
		"content-replacement", "attribution-snapshot", "speculation-accept",
		"marble-origami-commit", "marble-origami-snapshot", "agent-name",
	} {
		line := `{"type":"` + typ + `","sessionId":"s"}`
		rec, skip, err := parseLine([]byte(line))
		if err != nil {
			t.Errorf("parseLine(%q): unexpected err %v", typ, err)
		}
		if !skip {
			t.Errorf("parseLine(%q): want skip=true (known no-op)", typ)
		}
		_ = rec
	}
}

func TestParseLine_UserStringContent(t *testing.T) {
	t.Parallel()
	line := `{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"hello"},"timestamp":"2026-05-26T10:00:00.000Z"}`
	rec, skip, err := parseLine([]byte(line))
	if err != nil || skip {
		t.Fatalf("parseLine(user string): err=%v skip=%v", err, skip)
	}
	str, blocks, isString := classifyUserContent(rec.User)
	if !isString || str != "hello" || blocks != nil {
		t.Fatalf("classifyUserContent: got str=%q isString=%v blocks=%v", str, isString, blocks)
	}
}

func TestParseLine_UserArrayContent(t *testing.T) {
	t.Parallel()
	line := `{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok","is_error":false}]},"timestamp":"2026-05-26T10:00:00.000Z"}`
	rec, _, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine(user array): %v", err)
	}
	_, blocks, isString := classifyUserContent(rec.User)
	if isString || len(blocks) != 1 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "toolu_1" {
		t.Fatalf("classifyUserContent(array): got blocks=%+v isString=%v", blocks, isString)
	}
}

func TestParseLine_AssistantWithUsageAndBlocks(t *testing.T) {
	t.Parallel()
	line := `{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"s","message":{"id":"msg_1","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100},"content":[{"type":"thinking","thinking":"t","signature":"sig"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},"timestamp":"2026-05-26T10:00:00.000Z"}`
	rec, _, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine(assistant): %v", err)
	}
	if rec.Assistant == nil || rec.Assistant.Model != "claude-opus-4-7" {
		t.Fatalf("assistant model not decoded: %+v", rec.Assistant)
	}
	if rec.Assistant.Usage == nil || rec.Assistant.Usage.InputTokens != 10 {
		t.Fatalf("usage not decoded: %+v", rec.Assistant.Usage)
	}
	if len(rec.Assistant.Content) != 2 {
		t.Fatalf("want 2 content blocks, got %d", len(rec.Assistant.Content))
	}
}

func TestParseLine_SystemCompactBoundary(t *testing.T) {
	t.Parallel()
	line := `{"type":"system","subtype":"compact_boundary","uuid":"sy1","sessionId":"s","compactMetadata":{"trigger":"auto","preTokens":1000,"postTokens":50,"durationMs":2000},"timestamp":"2026-05-26T10:00:00.000Z"}`
	rec, _, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine(compact_boundary): %v", err)
	}
	if rec.System == nil || rec.System.Compact == nil {
		t.Fatalf("compactMetadata not decoded: %+v", rec.System)
	}
	if rec.System.Compact.Trigger != "auto" || rec.System.Compact.PreTokens != 1000 {
		t.Fatalf("compactMetadata fields wrong: %+v", rec.System.Compact)
	}
}

func TestSplitToolName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, wantName, wantNS string
	}{
		{"Read", "Read", "builtin"},
		{"Agent", "Agent", "builtin"},
		{"mcp__github__create_issue", "create_issue", "mcp:github"},
		{"mcp__playwright_demo__browser_click", "browser_click", "mcp:playwright_demo"},
		{"mcp__malformed", "mcp__malformed", "builtin"},
	}
	for _, c := range cases {
		gotName, gotNS := splitToolName(c.in)
		if gotName != c.wantName || gotNS != c.wantNS {
			t.Errorf("splitToolName(%q) = (%q,%q), want (%q,%q)", c.in, gotName, gotNS, c.wantName, c.wantNS)
		}
	}
}

func TestChildNativeID(t *testing.T) {
	t.Parallel()
	got := childNativeID("sess-1", "agent-15hex")
	want := "sess-1:agent:agent-15hex"
	if got != want {
		t.Fatalf("childNativeID = %q, want %q", got, want)
	}
	if id := agentIDFromNative(got); id != "agent-15hex" {
		t.Fatalf("agentIDFromNative(%q) = %q, want agent-15hex", got, id)
	}
}
