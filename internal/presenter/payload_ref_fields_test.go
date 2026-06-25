package presenter

import "testing"

func TestPayloadArtifactClassMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind string
		want string
	}{
		{kind: "llm_request", want: "llm_request"},
		{kind: "llm_response", want: "llm_response"},
		{kind: "llm_sdk_request", want: "llm_sdk_request"},
		{kind: "sdk_request", want: "llm_sdk_request"},
		{kind: "llm_sdk_response", want: "llm_sdk_response"},
		{kind: "sdk_response", want: "llm_sdk_response"},
		{kind: "llm_reasoning", want: "reasoning_text"},
		{kind: "reasoning_stream", want: "reasoning_text"},
		{kind: "tool_request", want: "tool_request"},
		{kind: "tool_response", want: "tool_response"},
		{kind: "log", want: "log"},
		{kind: "unknown_kind", want: "unknown_kind"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()
			if got := payloadArtifactClass(tt.kind); got != tt.want {
				t.Fatalf("payloadArtifactClass(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}
