package opencode

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestBuildPayloadURI pins the canonical opencode-sqlite:// grammar: scheme,
// part_id + field query params, in that order, with the literal form for
// unreserved values.
func TestBuildPayloadURI(t *testing.T) {
	t.Parallel()
	got := buildPayloadURI("prt_123", "text")
	want := "opencode-sqlite://?part_id=prt_123&field=text"
	if got != want {
		t.Fatalf("buildPayloadURI = %q, want %q", got, want)
	}
}

// TestBuildPayloadURI_EncodesReservedChars verifies a part id or field path
// containing reserved characters (&, =, space, /) is URL-encoded so it cannot
// corrupt the query string or be misread by the future resolver.
func TestBuildPayloadURI_EncodesReservedChars(t *testing.T) {
	t.Parallel()
	got := buildPayloadURI("prt &=?", "state.output/v2")
	// & and = and space and ? in the id must be percent-encoded; the '.' in the
	// field is unreserved and stays literal, the '/' is encoded by QueryEscape.
	want := "opencode-sqlite://?part_id=prt+%26%3D%3F&field=state.output%2Fv2"
	if got != want {
		t.Fatalf("buildPayloadURI(reserved) = %q, want %q", got, want)
	}
}

// TestDefaultPayloadURI_DelegatesToBuilder confirms the mapper's built-in default
// is byte-identical to buildPayloadURI (single source of truth) — so the chunk-B
// mapper goldens are unchanged after the relocation.
func TestDefaultPayloadURI_DelegatesToBuilder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ id, field string }{
		{"prt_9", "state.output"},
		{"prt_2", "text"},
		{"prt_123", "state.input"},
	} {
		if got, want := defaultPayloadURI(tc.id, tc.field), buildPayloadURI(tc.id, tc.field); got != want {
			t.Errorf("defaultPayloadURI(%q,%q) = %q, want %q (must delegate)", tc.id, tc.field, got, want)
		}
	}
}

// TestMapSession_PayloadRefUsesBuilder is the integration check that the mapper
// emits PayloadRef LocationURIs through the builder: a reasoning part yields the
// exact opencode-sqlite:// form, proving the relocated builder is wired and the
// op→payload linkage is intact. This is the byte-identical contract the chunk-B
// goldens depend on.
func TestMapSession_PayloadRefUsesBuilder(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_p", 0)
	end := int64(2200)
	msgs := []messageWithParts{
		mwp(asgMsg("msg_a", 1500, nil, "the-alias", "the-model", tokenCounts{}, 0, "", ""),
			stepStart("prt_1"),
			reasoningPart("prt_2", 2000, &end, false),
		),
	}
	evs, err := mapSession(testSourceID, s, msgs)
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	var found bool
	for _, ev := range evs {
		if p, ok := ev.(canonical.PayloadRefEvent); ok && p.PayloadKind == "llm_reasoning" {
			found = true
			want := buildPayloadURI("prt_2", "text")
			if p.LocationURI != want {
				t.Fatalf("reasoning PayloadRef URI = %q, want %q", p.LocationURI, want)
			}
		}
	}
	if !found {
		t.Fatal("no llm_reasoning PayloadRef emitted")
	}
}
