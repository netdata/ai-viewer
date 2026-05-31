package opencode

import (
	"errors"
	"testing"
)

// TestDecodeMessageData_Assistant decodes a synthetic assistant message body
// and checks the load-bearing fields the mapper consumes (role, provider
// alias, model, cumulative token block, completed time, finish reason).
func TestDecodeMessageData_Assistant(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"id":"msg_x","sessionID":"ses_x","role":"assistant","parentID":"msg_u",
		"agent":"code-reviewer","modelID":"synth-model","providerID":"synthetic-alias",
		"mode":"code-reviewer","cost":0.5,
		"tokens":{"total":410,"input":250,"output":77,"reasoning":16,"cache":{"read":100,"write":0}},
		"time":{"created":1700000000000,"completed":1700000005000},
		"finish":"stop"
	}`)
	d, err := decodeMessageData(raw)
	if err != nil {
		t.Fatalf("decodeMessageData: %v", err)
	}
	if d.role() != roleAssistant {
		t.Errorf("role = %v, want assistant", d.role())
	}
	if d.ProviderID != "synthetic-alias" {
		t.Errorf("providerID = %q", d.ProviderID)
	}
	if d.ModelID != "synth-model" {
		t.Errorf("modelID = %q", d.ModelID)
	}
	if d.ParentID != "msg_u" {
		t.Errorf("parentID = %q", d.ParentID)
	}
	if d.Tokens.Input != 250 || d.Tokens.Cache.Read != 100 {
		t.Errorf("tokens = %+v", d.Tokens)
	}
	if d.Time.Completed == nil || *d.Time.Completed != 1700000005000 {
		t.Errorf("completed time = %v", d.Time.Completed)
	}
	if d.Finish != "stop" {
		t.Errorf("finish = %q", d.Finish)
	}
	if d.Error != nil {
		t.Errorf("unexpected error block: %+v", d.Error)
	}
}

// TestDecodeMessageData_User decodes a synthetic user message body and checks
// the nested model object resolves via modelID().
func TestDecodeMessageData_User(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"id":"msg_u","sessionID":"ses_x","role":"user",
		"time":{"created":1700000000000},
		"agent":"code-reviewer",
		"model":{"providerID":"synthetic-alias","modelID":"synth-model","variant":"default"}
	}`)
	d, err := decodeMessageData(raw)
	if err != nil {
		t.Fatalf("decodeMessageData: %v", err)
	}
	if d.role() != roleUser {
		t.Errorf("role = %v, want user", d.role())
	}
	if d.Time.Completed != nil {
		t.Errorf("user message must have no completed time, got %v", d.Time.Completed)
	}
	if d.Model == nil || d.Model.modelID() != "synth-model" {
		t.Errorf("nested model = %+v", d.Model)
	}
}

// TestDecodeMessageData_AssistantError decodes a failed assistant message and
// confirms the error name (future ErrorClass) is captured.
func TestDecodeMessageData_AssistantError(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"role":"assistant","error":{"name":"ProviderAuthError","data":{"detail":"synthetic"}}}`)
	d, err := decodeMessageData(raw)
	if err != nil {
		t.Fatalf("decodeMessageData: %v", err)
	}
	if d.Error == nil || d.Error.Name != "ProviderAuthError" {
		t.Fatalf("error block = %+v", d.Error)
	}
}

// TestDecodeMessageData_UnknownRole asserts an unrecognised role decodes
// without error and reports roleUnknown so the mapper can skip-with-WARN.
func TestDecodeMessageData_UnknownRole(t *testing.T) {
	t.Parallel()
	d, err := decodeMessageData([]byte(`{"role":"system","note":"future variant"}`))
	if err != nil {
		t.Fatalf("decodeMessageData: %v", err)
	}
	if d.role() != roleUnknown {
		t.Errorf("role = %v, want unknown", d.role())
	}
}

// TestDecodeMessageData_Errors covers the empty body and malformed JSON
// rejections.
func TestDecodeMessageData_Errors(t *testing.T) {
	t.Parallel()
	if _, err := decodeMessageData(nil); !errors.Is(err, errEmptyData) {
		t.Errorf("nil body: err = %v, want errEmptyData", err)
	}
	if _, err := decodeMessageData([]byte("   ")); !errors.Is(err, errEmptyData) {
		t.Errorf("blank body: err = %v, want errEmptyData", err)
	}
	if _, err := decodeMessageData([]byte("{bad")); err == nil {
		t.Error("malformed body: want error")
	}
}

// TestDecodePartData_AllKnownVariants decodes one synthetic body per known
// $.type and checks the discriminator classifies correctly plus a load-bearing
// field per variant.
func TestDecodePartData_AllKnownVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want partType
		chk  func(t *testing.T, d partData)
	}{
		{
			name: "step-start", raw: `{"type":"step-start"}`, want: partStepStart,
			chk: func(t *testing.T, d partData) {},
		},
		{
			name: "step-finish", raw: `{"type":"step-finish","reason":"stop","cost":0.1,"tokens":{"input":250,"output":7,"cache":{"read":10,"write":0}}}`, want: partStepFinish,
			chk: func(t *testing.T, d partData) {
				if d.Tokens.Input != 250 {
					t.Errorf("step-finish tokens.input = %d, want 250 (cumulative; mapper deltas later)", d.Tokens.Input)
				}
			},
		},
		{
			name: "text", raw: `{"type":"text","text":"synthetic","time":{"start":1,"end":2}}`, want: partText,
			chk: func(t *testing.T, d partData) {
				if d.Text != "synthetic" {
					t.Errorf("text = %q", d.Text)
				}
			},
		},
		{
			name: "reasoning", raw: `{"type":"reasoning","text":"thinking","time":{"start":1}}`, want: partReasoning,
			chk: func(t *testing.T, d partData) {
				if d.Time.End != nil {
					t.Errorf("reasoning end must be nil (running), got %v", d.Time.End)
				}
			},
		},
		{
			name: "tool", raw: `{"type":"tool","callID":"c1","tool":"github_get_file_contents","state":{"status":"completed","input":{"path":"x"},"output":"ok","time":{"start":1,"end":2}}}`, want: partTool,
			chk: func(t *testing.T, d partData) {
				if d.Tool != "github_get_file_contents" || d.State == nil || d.State.Status != "completed" {
					t.Errorf("tool = %q state = %+v", d.Tool, d.State)
				}
			},
		},
		{
			name: "tool-task-subagent", raw: `{"type":"tool","tool":"task","state":{"status":"completed","metadata":{"sessionId":"ses_child"},"time":{"start":1,"end":2}}}`, want: partTool,
			chk: func(t *testing.T, d partData) {
				if d.State == nil || d.State.subAgentSessionID() != "ses_child" {
					t.Errorf("sub-agent sessionId not extracted: %+v", d.State)
				}
			},
		},
		{
			name: "patch", raw: `{"type":"patch","hash":"abc","files":["/work/example/a.go","/work/example/b.go"]}`, want: partPatch,
			chk: func(t *testing.T, d partData) {
				if len(d.Files) != 2 || d.Hash != "abc" {
					t.Errorf("patch files = %v hash = %q", d.Files, d.Hash)
				}
			},
		},
		{
			name: "compaction", raw: `{"type":"compaction","auto":true}`, want: partCompaction,
			chk: func(t *testing.T, d partData) {
				if !d.Auto {
					t.Error("compaction auto = false, want true")
				}
			},
		},
		{
			name: "retry", raw: `{"type":"retry","attempt":3}`, want: partRetry,
			chk: func(t *testing.T, d partData) {
				if d.Attempt != 3 {
					t.Errorf("retry attempt = %d, want 3", d.Attempt)
				}
			},
		},
		{
			name: "file", raw: `{"type":"file","mime":"image/png","filename":"shot.png","url":"opencode-sqlite://x"}`, want: partFile,
			chk: func(t *testing.T, d partData) {
				if d.MIME != "image/png" || d.URL == "" {
					t.Errorf("file mime = %q url = %q", d.MIME, d.URL)
				}
			},
		},
		{name: "snapshot", raw: `{"type":"snapshot","snapshot":"h"}`, want: partSnapshot, chk: func(t *testing.T, d partData) {}},
		{name: "subtask", raw: `{"type":"subtask","prompt":"p","agent":"general"}`, want: partSubtask, chk: func(t *testing.T, d partData) {}},
		{name: "agent", raw: `{"type":"agent","name":"general"}`, want: partAgent, chk: func(t *testing.T, d partData) {}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := decodePartData([]byte(tc.raw))
			if err != nil {
				t.Fatalf("decodePartData: %v", err)
			}
			if d.kind() != tc.want {
				t.Fatalf("kind = %v, want %v", d.kind(), tc.want)
			}
			tc.chk(t, d)
		})
	}
}

// TestDecodePartData_UnknownType asserts an unrecognised $.type decodes without
// error and classifies as partUnknown (forward-compat skip), and that unknown
// sibling columns/keys do not hard-fail the decode (tolerance requirement).
func TestDecodePartData_UnknownTypeAndColumns(t *testing.T) {
	t.Parallel()
	// Unknown $.type plus an unknown nested key.
	d, err := decodePartData([]byte(`{"type":"future-variant","brandNewField":{"x":1},"text":"still readable"}`))
	if err != nil {
		t.Fatalf("decodePartData: %v", err)
	}
	if d.kind() != partUnknown {
		t.Errorf("kind = %v, want unknown", d.kind())
	}
	// A known type carrying extra unknown columns must still decode the known
	// fields (encoding/json drops the unknown ones).
	d2, err := decodePartData([]byte(`{"type":"text","text":"ok","unknownColumnFromNewerSchema":42}`))
	if err != nil {
		t.Fatalf("decodePartData with extra column: %v", err)
	}
	if d2.kind() != partText || d2.Text != "ok" {
		t.Errorf("extra-column decode lost known fields: %+v", d2)
	}
}

// TestDecodePartData_Errors covers empty and malformed bodies.
func TestDecodePartData_Errors(t *testing.T) {
	t.Parallel()
	if _, err := decodePartData(nil); !errors.Is(err, errEmptyData) {
		t.Errorf("nil body: err = %v, want errEmptyData", err)
	}
	if _, err := decodePartData([]byte("{bad")); err == nil {
		t.Error("malformed body: want error")
	}
}

// TestToolState_SubAgentSessionID covers the absent/null/malformed metadata
// paths return "".
func TestToolState_SubAgentSessionID(t *testing.T) {
	t.Parallel()
	if got := (toolState{}).subAgentSessionID(); got != "" {
		t.Errorf("absent metadata: got %q, want empty", got)
	}
	if got := (toolState{Metadata: []byte("null")}).subAgentSessionID(); got != "" {
		t.Errorf("null metadata: got %q, want empty", got)
	}
	if got := (toolState{Metadata: []byte("{bad")}).subAgentSessionID(); got != "" {
		t.Errorf("malformed metadata: got %q, want empty", got)
	}
	if got := (toolState{Metadata: []byte(`{"sessionId":"ses_c"}`)}).subAgentSessionID(); got != "ses_c" {
		t.Errorf("valid metadata: got %q, want ses_c", got)
	}
}

// TestModelRef_ModelID covers both the session-style "id" and the
// assistant-user-message "modelID" resolution.
func TestModelRef_ModelID(t *testing.T) {
	t.Parallel()
	if got := (modelRef{ID: "from-id"}).modelID(); got != "from-id" {
		t.Errorf("id form = %q", got)
	}
	if got := (modelRef{ModelID: "from-modelID"}).modelID(); got != "from-modelID" {
		t.Errorf("modelID form = %q", got)
	}
	if got := (modelRef{}).modelID(); got != "" {
		t.Errorf("empty form = %q", got)
	}
}
