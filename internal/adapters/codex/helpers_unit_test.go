package codex

import (
	"encoding/json"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// These unit tests exercise the pure helper functions' edge and error branches
// directly — the malformed-JSON guards, empty-input fallbacks, and numeric
// coercions that valid record streams do not reach. They pin the defensive
// behavior (no panic, sane defaults) the forward-compat contract requires.

func TestPayloadNumber_Variants(t *testing.T) {
	t.Parallel()
	// integer
	if got := payloadNumber([]byte(`{"payload":{"n":42}}`), "n"); got != 42 {
		t.Errorf("int = %d, want 42", got)
	}
	// float coerced to int
	if got := payloadNumber([]byte(`{"payload":{"n":42.9}}`), "n"); got != 42 {
		t.Errorf("float = %d, want 42", got)
	}
	// absent field
	if got := payloadNumber([]byte(`{"payload":{}}`), "n"); got != 0 {
		t.Errorf("absent = %d, want 0", got)
	}
	// non-numeric value
	if got := payloadNumber([]byte(`{"payload":{"n":"x"}}`), "n"); got != 0 {
		t.Errorf("string = %d, want 0", got)
	}
	// malformed JSON
	if got := payloadNumber([]byte(`{not json`), "n"); got != 0 {
		t.Errorf("malformed = %d, want 0", got)
	}
}

func TestCompletedAtMicros_Variants(t *testing.T) {
	t.Parallel()
	// RFC3339 string
	if got := completedAtMicros([]byte(`{"payload":{"completed_at":"2025-11-20T17:00:00.000Z"}}`)); got == 0 {
		t.Error("rfc3339 completed_at not parsed")
	}
	// unix seconds
	if got := completedAtMicros([]byte(`{"payload":{"completed_at":1763664600}}`)); got != 1763664600*1_000_000 {
		t.Errorf("unix = %d, want %d", got, int64(1763664600)*1_000_000)
	}
	// absent
	if got := completedAtMicros([]byte(`{"payload":{}}`)); got != 0 {
		t.Errorf("absent = %d, want 0", got)
	}
	// invalid string
	if got := completedAtMicros([]byte(`{"payload":{"completed_at":"not-a-date"}}`)); got != 0 {
		t.Errorf("bad string = %d, want 0", got)
	}
	// malformed
	if got := completedAtMicros([]byte(`{bad`)); got != 0 {
		t.Errorf("malformed = %d, want 0", got)
	}
}

func TestStartedAtMicros(t *testing.T) {
	t.Parallel()
	if got := startedAtMicros([]byte(`{"payload":{"started_at":100}}`)); got != 100_000_000 {
		t.Errorf("started_at = %d, want 100000000", got)
	}
	if got := startedAtMicros([]byte(`{"payload":{}}`)); got != 0 {
		t.Errorf("absent started_at = %d, want 0", got)
	}
}

func TestMcpResultStatus_Variants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		raw        string
		wantStatus string
		wantErr    string
	}{
		{"ok", `{"payload":{"result":{"Ok":{"is_error":false}}}}`, "completed", ""},
		{"ok-is-error", `{"payload":{"result":{"Ok":{"is_error":true}}}}`, "failed", "tool_error"},
		{"err", `{"payload":{"result":{"Err":"boom"}}}`, "failed", "tool_error"},
		{"absent", `{"payload":{}}`, "completed", ""},
		{"malformed", `{bad`, "completed", ""},
		{"unparseable-result", `{"payload":{"result":123}}`, "completed", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, e := mcpResultStatus([]byte(c.raw))
			if s != c.wantStatus || e != c.wantErr {
				t.Errorf("mcpResultStatus = {%q %q}, want {%q %q}", s, e, c.wantStatus, c.wantErr)
			}
		})
	}
}

func TestPatchApplyStatus_Variants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw        string
		wantStatus string
	}{
		{`{"payload":{"success":true}}`, "completed"},
		{`{"payload":{"success":false}}`, "failed"},
		{`{"payload":{"status":"error"}}`, "failed"},
		{`{"payload":{"status":"completed"}}`, "completed"},
		{`{"payload":{}}`, "completed"},
		{`{bad`, "completed"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.wantStatus+"_"+c.raw, func(t *testing.T) {
			t.Parallel()
			s, _ := patchApplyStatus([]byte(c.raw))
			if s != c.wantStatus {
				t.Errorf("patchApplyStatus(%s) = %q, want %q", c.raw, s, c.wantStatus)
			}
		})
	}
}

func TestEnrichStatus_Variants(t *testing.T) {
	t.Parallel()
	if s, _ := enrichStatus([]byte(`{"payload":{"exit_code":0}}`)); s != "completed" {
		t.Errorf("exit 0 status = %q, want completed", s)
	}
	if s, e := enrichStatus([]byte(`{"payload":{"exit_code":1}}`)); s != "failed" || e != "command_failed" {
		t.Errorf("exit 1 = {%q %q}, want {failed command_failed}", s, e)
	}
	if s, _ := enrichStatus([]byte(`{"payload":{}}`)); s != "" {
		t.Errorf("no exit_code status = %q, want empty", s)
	}
	if s, _ := enrichStatus([]byte(`{bad`)); s != "" {
		t.Errorf("malformed status = %q, want empty", s)
	}
}

func TestOutputStatusAndText(t *testing.T) {
	t.Parallel()
	// bare-string success
	if s, _ := outputStatus(json.RawMessage(`"all good"`)); s != "completed" {
		t.Errorf("ok string status = %q, want completed", s)
	}
	// empty/null
	if s, _ := outputStatus(json.RawMessage(`null`)); s != "completed" {
		t.Errorf("null status = %q, want completed", s)
	}
	// sandbox denial
	if s, e := outputStatus(json.RawMessage(`"operation not permitted"`)); s != "failed" || e != "sandbox_denied" {
		t.Errorf("denial = {%q %q}, want {failed sandbox_denied}", s, e)
	}
	// object with output field carrying error
	if s, e := outputStatus(json.RawMessage(`{"output":"error: nope"}`)); s != "failed" || e != "tool_error" {
		t.Errorf("obj output error = {%q %q}, want {failed tool_error}", s, e)
	}
	// object with neither output nor content → scans raw bytes (no error markers)
	if s, _ := outputStatus(json.RawMessage(`{"misc":1}`)); s != "completed" {
		t.Errorf("obj-no-fields status = %q, want completed", s)
	}
	// scalarOrJSON: a non-string JSON value returns its raw form
	if v := scalarOrJSON(json.RawMessage(`{"a":1}`)); v == "" {
		t.Error("scalarOrJSON(object) returned empty")
	}
	if v := scalarOrJSON(json.RawMessage(`null`)); v != "" {
		t.Errorf("scalarOrJSON(null) = %q, want empty", v)
	}
}

func TestMessageText_And_ReasoningKindEdge(t *testing.T) {
	t.Parallel()
	if got := messageText(json.RawMessage(`[{"type":"input_text","text":"a"},{"text":"b"}]`)); got != "ab" {
		t.Errorf("messageText = %q, want ab", got)
	}
	if got := messageText(json.RawMessage(`null`)); got != "" {
		t.Errorf("messageText(null) = %q, want empty", got)
	}
	if got := messageText(json.RawMessage(`"notarray"`)); got != "" {
		t.Errorf("messageText(non-array) = %q, want empty", got)
	}
	// reasoningKind with NO summary, NO content, NO encrypted → defaults raw.
	p := &responseItemPayload{}
	if k, f := reasoningKind(p); k != "raw" || f != "json" {
		t.Errorf("empty reasoningKind = {%q %q}, want {raw json}", k, f)
	}
}

func TestSourceStringAndDepth(t *testing.T) {
	t.Parallel()
	// bare string
	if got := sourceString(json.RawMessage(`"exec"`)); got != "exec" {
		t.Errorf("sourceString(bare) = %q, want exec", got)
	}
	// object → key name
	if got := sourceString(json.RawMessage(`{"subagent":{"thread_spawn":{}}}`)); got != "subagent" {
		t.Errorf("sourceString(subagent) = %q, want subagent", got)
	}
	// null/absent
	if got := sourceString(json.RawMessage(`null`)); got != "" {
		t.Errorf("sourceString(null) = %q, want empty", got)
	}
	// unrecognized object shape
	if got := sourceString(json.RawMessage(`{"weird":1}`)); got != "" {
		t.Errorf("sourceString(weird) = %q, want empty", got)
	}
	// malformed → empty (not a string, not an object)
	if got := sourceString(json.RawMessage(`12`)); got != "" {
		t.Errorf("sourceString(number) = %q, want empty", got)
	}
	// subagentDepth
	if d := subagentDepth(json.RawMessage(`{"subagent":{"thread_spawn":{"depth":3}}}`)); d != 3 {
		t.Errorf("subagentDepth = %d, want 3", d)
	}
	if d := subagentDepth(json.RawMessage(`null`)); d != 0 {
		t.Errorf("subagentDepth(null) = %d, want 0", d)
	}
	if d := subagentDepth(json.RawMessage(`{bad`)); d != 0 {
		t.Errorf("subagentDepth(malformed) = %d, want 0", d)
	}
}

func TestMcpInvocationAndExecExtras(t *testing.T) {
	t.Parallel()
	s, tool := mcpInvocation([]byte(`{"payload":{"invocation":{"server":"gh","tool":"list"}}}`))
	if s != "gh" || tool != "list" {
		t.Errorf("mcpInvocation = {%q %q}, want {gh list}", s, tool)
	}
	if s, tool := mcpInvocation([]byte(`{bad`)); s != "" || tool != "" {
		t.Errorf("mcpInvocation(malformed) = {%q %q}, want empty", s, tool)
	}
	// execCommandExtras: all fields
	ex := execCommandExtras([]byte(`{"payload":{"exit_code":0,"cwd":"<ROOT>","source":"model","aggregated_output":"abc"}}`))
	if ex["exec_exit_code"] != int64(0) || ex["exec_cwd"] != "<ROOT>" || ex["exec_source"] != "model" || ex["exec_output_bytes"] != 3 {
		t.Errorf("execCommandExtras = %+v", ex)
	}
	// empty payload → nil
	if ex := execCommandExtras([]byte(`{"payload":{}}`)); ex != nil {
		t.Errorf("execCommandExtras(empty) = %+v, want nil", ex)
	}
	// malformed → nil
	if ex := execCommandExtras([]byte(`{bad`)); ex != nil {
		t.Errorf("execCommandExtras(malformed) = %+v, want nil", ex)
	}
	// webSearchExtras
	if w := webSearchExtras([]byte(`{"payload":{"query":"q"}}`)); w["query"] != "q" {
		t.Errorf("webSearchExtras = %+v", w)
	}
	if w := webSearchExtras([]byte(`{"payload":{}}`)); w != nil {
		t.Errorf("webSearchExtras(empty) = %+v, want nil", w)
	}
	if w := webSearchExtras([]byte(`{bad`)); w != nil {
		t.Errorf("webSearchExtras(malformed) = %+v, want nil", w)
	}
}

func TestDecodeTokenCount_Placements(t *testing.T) {
	t.Parallel()
	// model_context_window as sibling of info (older shape).
	info := decodeTokenCount([]byte(`{"payload":{"info":{"last_token_usage":{"input_tokens":3}},"model_context_window":128000}}`))
	if info.last.InputTokens != 3 || info.modelContextWindow != 128000 {
		t.Errorf("decodeTokenCount sibling mcw = %+v", info)
	}
	// malformed
	if info := decodeTokenCount([]byte(`{bad`)); info.modelContextWindow != 0 {
		t.Errorf("decodeTokenCount(malformed) = %+v, want zero", info)
	}
}

func TestSmallHelpers(t *testing.T) {
	t.Parallel()
	// userFingerprint: empty in → empty out (never deduped).
	if userFingerprint("  ") != "" {
		t.Error("blank userFingerprint not empty")
	}
	if userFingerprint(" hi ") != "hi" {
		t.Errorf("userFingerprint trim = %q, want hi", userFingerprint(" hi "))
	}
	// firstSeenUser: empty always first; non-empty dedups.
	m := newTestMapper("sid")
	if !m.firstSeenUser("") {
		t.Error("empty fp should be first")
	}
	if !m.firstSeenUser("x") || m.firstSeenUser("x") {
		t.Error("firstSeenUser dedup broken")
	}
	// trimPreview: max<=0 → empty; truncation.
	if trimPreview("abc", 0) != "" {
		t.Error("trimPreview max=0 not empty")
	}
	if trimPreview("abcdef", 3) != "abc" {
		t.Errorf("trimPreview trunc = %q, want abc", trimPreview("abcdef", 3))
	}
	if trimPreview("ab", 5) != "ab" {
		t.Errorf("trimPreview short = %q, want ab", trimPreview("ab", 5))
	}
	// parseTsToMicros: invalid → error.
	if _, err := parseTsToMicros("not-a-ts"); err == nil {
		t.Error("parseTsToMicros(bad) should error")
	}
	// mergeExtras nil-op paths.
	mergeExtras(nil, map[string]any{"a": 1})
	op := &openOp{}
	mergeExtras(op, nil)
	mergeExtras(op, map[string]any{"a": 1})
	if op.extras["a"] != 1 {
		t.Error("mergeExtras did not merge")
	}
	// trackOp empty call_id is not tracked.
	m.trackOp("", "t", 1, 1, canonical.OpTool, "x", "shell")
	if len(m.openOps) != 0 {
		t.Error("trackOp tracked an empty call_id")
	}
	// sortByOpSeq single element (degenerate).
	xs := []int{5}
	sortByOpSeq(xs, func(v int) int { return v })
	if xs[0] != 5 {
		t.Error("sortByOpSeq mangled single element")
	}
	// agentNameFromMeta role fallback + bare codex.
	if got := agentNameFromMeta(&sessionMetaPayload{AgentRole: "explorer"}); got != "explorer" {
		t.Errorf("agentNameFromMeta role = %q, want explorer", got)
	}
	if got := agentNameFromMeta(&sessionMetaPayload{}); got != "codex" {
		t.Errorf("agentNameFromMeta empty = %q, want codex", got)
	}
	// phaseFromRaw malformed → empty.
	if phaseFromRaw([]byte(`{bad`)) != "" {
		t.Error("phaseFromRaw(malformed) not empty")
	}
}

func TestSessionExtras_EmptyAndProvider(t *testing.T) {
	t.Parallel()
	// A meta with only model_provider yields extras carrying it.
	ex := sessionExtras(&sessionMetaPayload{ModelProvider: "openai"}, "")
	if ex["model_provider"] != "openai" {
		t.Errorf("sessionExtras model_provider = %+v", ex)
	}
	// A fully-empty meta yields nil extras.
	if ex := sessionExtras(&sessionMetaPayload{}, ""); ex != nil {
		t.Errorf("sessionExtras(empty) = %+v, want nil", ex)
	}
	// Git block surfaces only the non-empty git fields.
	ex = sessionExtras(&sessionMetaPayload{Git: &gitInfo{Branch: "main"}}, "")
	git, _ := ex["git"].(map[string]any)
	if git["branch"] != "main" {
		t.Errorf("sessionExtras git = %+v", ex["git"])
	}
}
