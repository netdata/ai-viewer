package aiagent_v2

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestMap_PayloadRefEmittedForRequestAndResponse covers the ref-form
// payload path. The op carries both `request.payload.ref` and
// `response.payload.ref`; we expect one PayloadRefEvent per side.
func TestMap_PayloadRefEmittedForRequestAndResponse(t *testing.T) {
	t.Parallel()
	t.Run("producer shaped captured refs", func(t *testing.T) {
		t.Parallel()
		assertProducerPayloadRefs(t)
	})
	t.Run("uncaptured pathless ref still emits metadata", func(t *testing.T) {
		t.Parallel()
		assertUncapturedPayloadRef(t)
	})
	t.Run("sdk shaped captured ref emits SDK payload kind", func(t *testing.T) {
		t.Parallel()
		assertSDKPayloadRef(t)
	})
}

func assertProducerPayloadRefs(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	snap := simpleSnapshot(2, "payload-ref")
	reqRef := json.RawMessage(`{"ref":{"path":"payloads/req.http.gz","format":"http","compression":"gzip","originalBytes":1500,"compressedBytes":420,"sha256":"abc","captured":true}}`)
	respRef := json.RawMessage(`{"ref":{"path":"payloads/resp.http.gz","format":"http","compression":"gzip","originalBytes":4500,"compressedBytes":1100,"sha256":"def","captured":true}}`)
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{Kind: "llm", Payload: reqRef, Size: 1500}
	snap.OpTree.Turns[0].Ops[0].Response = &opPayload{Payload: respRef, Size: 4500}

	events := mapSnapshot(snap, "test-source", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(error) {})

	refs := payloadRefsFromEvents(events)
	if len(refs) != 2 {
		t.Fatalf("expected 2 PayloadRefEvent, got %d", len(refs))
	}
	wantPrefix := "file://" + filepath.ToSlash(filepath.Clean(root))
	for _, r := range refs {
		assertProducerRefLocation(t, r, wantPrefix)
		assertProducerRefHash(t, r)
	}
	assertProducerRefBytes(t, refs)
	assertProducerRefKinds(t, refs)
}

func assertProducerRefLocation(t *testing.T, ref canonical.PayloadRefEvent, wantPrefix string) {
	t.Helper()
	if !strings.HasPrefix(ref.LocationURI, wantPrefix) {
		t.Fatalf("LocationURI %q missing prefix %q", ref.LocationURI, wantPrefix)
	}
}

func assertProducerRefHash(t *testing.T, ref canonical.PayloadRefEvent) {
	t.Helper()
	if ref.SHA256 == "" {
		t.Fatalf("PayloadRefEvent missing SHA256")
	}
}

func assertProducerRefBytes(t *testing.T, refs []canonical.PayloadRefEvent) {
	t.Helper()
	if refs[0].StoredBytes != 420 || refs[1].StoredBytes != 1100 {
		t.Fatalf("StoredBytes = %d/%d, want compressedBytes 420/1100", refs[0].StoredBytes, refs[1].StoredBytes)
	}
}

func assertProducerRefKinds(t *testing.T, refs []canonical.PayloadRefEvent) {
	t.Helper()
	kinds := map[string]bool{refs[0].PayloadKind: true, refs[1].PayloadKind: true}
	if !kinds["llm_request"] || !kinds["llm_response"] {
		t.Fatalf("PayloadKinds = %v, want llm_request + llm_response", kinds)
	}
}

func assertUncapturedPayloadRef(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	snap := simpleSnapshot(2, "uncaptured-ref")
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{
		Payload: json.RawMessage(`{"ref":{"format":"json","originalBytes":77,"sha256":"uncaptured-sha","captured":false}}`),
		Size:    77,
	}

	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(error) {})
	refs := payloadRefsFromEvents(events)
	if len(refs) != 1 {
		t.Fatalf("expected 1 uncaptured PayloadRefEvent, got %d", len(refs))
	}
	got := refs[0]
	if got.LocationURI != "" || got.Compression != "" {
		t.Fatalf("uncaptured ref should have empty LocationURI/Compression: %+v", got)
	}
	if got.PayloadKind != "llm_request" || got.Format != "json" || got.OriginalBytes != 77 || got.SHA256 != "uncaptured-sha" {
		t.Fatalf("uncaptured ref metadata mismatch: %+v", got)
	}
}

func assertSDKPayloadRef(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	snap := simpleSnapshot(2, "sdk-ref")
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{
		Payload: json.RawMessage(`{"sdk":{"ref":{"path":"payloads/sdk-req.json.gz","format":"json","compression":"gzip","originalBytes":80,"compressedBytes":33,"captured":true}}}`),
		Size:    80,
	}

	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(error) {})
	refs := payloadRefsFromEvents(events)
	if len(refs) != 1 {
		t.Fatalf("expected 1 SDK PayloadRefEvent, got %d", len(refs))
	}
	got := refs[0]
	if got.PayloadKind != "llm_sdk_request" || got.StoredBytes != 33 || got.Format != "json" {
		t.Fatalf("SDK ref metadata mismatch: %+v", got)
	}
}

// TestMap_PayloadRefForToolOpUsesToolKinds verifies tool ops get
// tool_request / tool_response, not llm_*.
func TestMap_PayloadRefForToolOpUsesToolKinds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "tool-payload")
	reqRef := json.RawMessage(`{"ref":{"path":"payloads/tool-req.json.gz","format":"json","compression":"gzip","originalBytes":80,"compressedBytes":33,"captured":true}}`)
	snap.OpTree.Turns[0].Ops[0] = operationNode{
		OpID: "tool-op", Kind: "tool", StartedAt: 1700000001500,
		EndedAt: int64Ptr(1700000001600), Status: "ok",
		Attributes: rawAttrs(map[string]any{"name": "shell", "provider": "builtin"}),
		Request:    &opPayload{Payload: reqRef, Size: 80},
	}
	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(error) {})
	var got string
	for _, ev := range events {
		if pr, ok := ev.(canonical.PayloadRefEvent); ok {
			got = pr.PayloadKind
		}
	}
	if got != "tool_request" {
		t.Fatalf("PayloadKind = %q, want %q", got, "tool_request")
	}
}

// TestMap_PayloadRefTraversalGuardRejects validates that a relative
// path escaping the root surfaces a SourceError via onError and only
// that malformed ref is skipped.
func TestMap_PayloadRefTraversalGuardRejects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "evil-ref")
	evilRef := json.RawMessage(`{"ref":{"path":"../../../etc/passwd","format":"text","captured":true}}`)
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{Payload: evilRef, Size: 10}
	snap.OpTree.Turns[0].Ops[0].Response = &opPayload{
		Payload: json.RawMessage(`{"ref":{"path":"payloads/ok.json.gz","format":"json","compression":"gzip","captured":true}}`),
		Size:    20,
	}
	var errs []error
	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(e error) { errs = append(errs, e) })
	refs := payloadRefsFromEvents(events)
	if len(errs) == 0 {
		t.Fatalf("expected onError for path-escape ref, none raised")
	}
	if len(refs) != 1 || refs[0].PayloadKind != "llm_response" {
		t.Fatalf("escaping ref should skip only bad ref, got refs=%+v", refs)
	}
}

// TestMap_PayloadInlineSkipsRefEmission confirms that an inline
// (non-ref) payload is silently skipped: no PayloadRefEvent and no
// onError. Inline payloads are deferred per spec Canonical Model Gaps
// item 10.
func TestMap_PayloadInlineSkipsRefEmission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snap := simpleSnapshot(2, "inline")
	snap.OpTree.Turns[0].Ops[0].Request = &opPayload{
		Payload: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`),
		Size:    20,
	}
	snap.OpTree.Turns[0].Ops[0].Response = &opPayload{
		Payload: json.RawMessage(`"base64-blob..."`),
		Size:    40,
	}
	var errs []error
	events := mapSnapshot(snap, "src", snap.OpTree.TraceID, root, snap.OpTree.TraceID+".json.gz", func(e error) { errs = append(errs, e) })
	for _, ev := range events {
		if _, ok := ev.(canonical.PayloadRefEvent); ok {
			t.Fatalf("inline payload should not emit PayloadRefEvent")
		}
	}
	if len(errs) != 0 {
		t.Fatalf("inline payload should not produce errors, got %v", errs)
	}
}

// TestExtractPayloadRef_VariousShapes hits the JSON-shape probe paths
// directly so they stay covered when the calling sites are rare in
// fixtures.
func TestExtractPayloadRef_VariousShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want payloadRef
		ok   bool
	}{
		{name: "empty"},
		{name: "string scalar", in: `"opaque-base64-blob"`},
		{name: "array scalar", in: `[1,2,3]`},
		{name: "object no ref/path", in: `{"messages":[]}`},
		{name: "legacy ref-only", in: `{"ref":"a/b/c.gz"}`, ok: true, want: payloadRef{Ref: "a/b/c.gz", Path: "a/b/c.gz"}},
		{name: "bare path-only inline object", in: `{"path":"x/y/z.json"}`},
		{name: "legacy path with evidence metadata", in: `{"path":"x/y/z.json","format":"json","captured":true}`, ok: true, want: payloadRef{Path: "x/y/z.json", Format: "json", Captured: boolPtr(true)}},
		{name: "legacy both prefers path", in: `{"ref":"X","path":"Y"}`, ok: true, want: payloadRef{Ref: "X", Path: "Y"}},
		{name: "producer ref wrapper", in: `{"ref":{"path":"payloads/req.http.gz","format":"http","compression":"gzip","originalBytes":1500,"compressedBytes":420,"sha256":"abc","captured":true}}`, ok: true, want: payloadRef{Path: "payloads/req.http.gz", Format: "http", Compression: "gzip", OriginalBytes: 1500, StoredBytes: 420, SHA256: "abc", Captured: boolPtr(true)}},
		{name: "sdk ref wrapper", in: `{"sdk":{"ref":{"path":"payloads/sdk-req.json.gz","format":"json","compression":"gzip","originalBytes":80,"compressedBytes":33,"captured":true}}}`, ok: true, want: payloadRef{Path: "payloads/sdk-req.json.gz", Format: "json", Compression: "gzip", OriginalBytes: 80, StoredBytes: 33, Captured: boolPtr(true), SDK: true}},
		{name: "malformed json", in: `{not json`},
		{name: "whitespace then string", in: "   \"x\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, ok := extractPayloadRef(json.RawMessage(c.in))
			if ok != c.ok {
				t.Fatalf("extractPayloadRef(%q) ok = %v, want %v", c.in, ok, c.ok)
			}
			if c.ok {
				assertPayloadRefFields(t, c.in, ref, c.want)
			}
		})
	}
}

func assertPayloadRefFields(t *testing.T, input string, got, want payloadRef) {
	t.Helper()
	assertPayloadRefStrings(t, input, got, want)
	assertPayloadRefSizes(t, input, got, want)
	assertPayloadRefFlags(t, input, got, want)
}

func assertPayloadRefStrings(t *testing.T, input string, got, want payloadRef) {
	t.Helper()
	if got.Ref != want.Ref || got.Path != want.Path || got.Format != want.Format || got.Compression != want.Compression {
		t.Fatalf("extractPayloadRef(%q) = %+v, want %+v", input, got, want)
	}
}

func assertPayloadRefSizes(t *testing.T, input string, got, want payloadRef) {
	t.Helper()
	if got.OriginalBytes != want.OriginalBytes || got.StoredBytes != want.StoredBytes || got.SHA256 != want.SHA256 {
		t.Fatalf("extractPayloadRef(%q) = %+v, want %+v", input, got, want)
	}
}

func assertPayloadRefFlags(t *testing.T, input string, got, want payloadRef) {
	t.Helper()
	gotCaptured, gotCapturedSet := boolPtrState(got.Captured)
	wantCaptured, wantCapturedSet := boolPtrState(want.Captured)
	if got.SDK != want.SDK || gotCaptured != wantCaptured || gotCapturedSet != wantCapturedSet {
		t.Fatalf("extractPayloadRef(%q) = %+v, want %+v", input, got, want)
	}
}

func boolPtrState(ptr *bool) (bool, bool) {
	if ptr == nil {
		return false, false
	}
	return *ptr, true
}

func payloadRefsFromEvents(events []canonical.Event) []canonical.PayloadRefEvent {
	refs := make([]canonical.PayloadRefEvent, 0, 2)
	for _, ev := range events {
		if pr, ok := ev.(canonical.PayloadRefEvent); ok {
			refs = append(refs, pr)
		}
	}
	return refs
}

// TestResolvePayloadPath_RootHandling exercises empty inputs and
// traversal-guard rejections without requiring a real file.
func TestResolvePayloadPath_RootHandling(t *testing.T) {
	t.Parallel()
	assertEmptyPayloadRoot(t)
	assertEmptyPayloadRefPath(t)
	assertEscapingPayloadPathRejected(t)
	assertPayloadPathResolvesUnderRoot(t)
}

func assertEmptyPayloadRoot(t *testing.T) {
	t.Helper()
	if uri, err := resolvePayloadPath("", "x/y.bin"); err != nil || uri != "" {
		t.Fatalf("empty root should yield ('', nil), got (%q, %v)", uri, err)
	}
}

func assertEmptyPayloadRefPath(t *testing.T) {
	t.Helper()
	if uri, err := resolvePayloadPath("/tmp", ""); err != nil || uri != "" {
		t.Fatalf("empty refPath should yield ('', nil), got (%q, %v)", uri, err)
	}
}

func assertEscapingPayloadPathRejected(t *testing.T) {
	t.Helper()
	if _, err := resolvePayloadPath("/tmp", "../etc/passwd"); err == nil {
		t.Fatalf("expected traversal-guard rejection for ../etc/passwd")
	}
}

func assertPayloadPathResolvesUnderRoot(t *testing.T) {
	t.Helper()
	uri, err := resolvePayloadPath("/tmp", "payloads/x/y.bin")
	if err != nil {
		t.Fatalf("legit ref: %v", err)
	}
	if uri != "file:///tmp/payloads/x/y.bin" {
		t.Fatalf("uri = %q, want file:///tmp/payloads/x/y.bin", uri)
	}
}
