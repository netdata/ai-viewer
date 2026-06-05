package aiagent_v2

import (
	"encoding/json"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestMap_PayloadSideEmitsRegularAndSDKRefs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "dual-ref")
	snap.OpTree.Turns[0].Ops[0].Request = dualPayloadSide("req", 100)
	snap.OpTree.Turns[0].Ops[0].Response = dualPayloadSide("resp", 200)

	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(error) {})
	kinds := payloadRefKinds(payloadRefsFromEvents(events))

	for _, want := range []string{"llm_request", "llm_sdk_request", "llm_response", "llm_sdk_response"} {
		if !kinds[want] {
			t.Fatalf("PayloadKinds = %v, missing %q", kinds, want)
		}
	}
}

func dualPayloadSide(side string, size int64) *opPayload {
	return &opPayload{
		Payload: json.RawMessage(`{"ref":{"path":"payloads/` + side + `.json","format":"json"},"sdk":{"ref":{"path":"payloads/` + side + `-sdk.json","format":"json"}}}`),
		Size:    size,
	}
}

func payloadRefKinds(refs []canonical.PayloadRefEvent) map[string]bool {
	out := make(map[string]bool, len(refs))
	for _, ref := range refs {
		out[ref.PayloadKind] = true
	}
	return out
}

func TestMap_PayloadSideBadRegularRefDoesNotSuppressSDKRef(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "bad-regular-good-sdk")
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{
		Payload: json.RawMessage(`{"ref":{"path":"../../../etc/passwd","format":"text","captured":true},"sdk":{"ref":{"path":"payloads/sdk.json","format":"json","captured":true}}}`),
		Size:    100,
	}

	var errs []error
	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(e error) {
		errs = append(errs, e)
	})
	refs := payloadRefsFromEvents(events)
	if len(errs) != 1 {
		t.Fatalf("error count = %d, want 1", len(errs))
	}
	if len(refs) != 1 || refs[0].PayloadKind != "llm_sdk_request" {
		t.Fatalf("refs = %+v, want only valid SDK request ref", refs)
	}
}

func TestExtractPayloadRefs_MultipleAndMixedShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []payloadRef
	}{
		{
			name: "regular and sdk",
			in:   `{"ref":{"path":"payloads/r.json","format":"json"},"sdk":{"ref":{"path":"payloads/sdk.json","format":"json"}}}`,
			want: []payloadRef{
				{Path: "payloads/r.json", Format: "json"},
				{Path: "payloads/sdk.json", Format: "json", SDK: true},
			},
		},
		{
			name: "sdk string ref",
			in:   `{"sdk":{"ref":"payloads/sdk.gz"}}`,
			want: []payloadRef{{Ref: "payloads/sdk.gz", Path: "payloads/sdk.gz", SDK: true}},
		},
		{
			name: "mixed shape prefers nested regular ref",
			in:   `{"ref":{"path":"payloads/r.json","format":"json"},"captured":true}`,
			want: []payloadRef{{Path: "payloads/r.json", Format: "json"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractPayloadRefs(json.RawMessage(c.in))
			assertPayloadRefs(t, c.in, got, c.want)
		})
	}
}

func assertPayloadRefs(t *testing.T, input string, got, want []payloadRef) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("extractPayloadRefs(%q) len = %d, want %d: %+v", input, len(got), len(want), got)
	}
	for i := range want {
		assertPayloadRefFields(t, input, got[i], want[i])
	}
}
