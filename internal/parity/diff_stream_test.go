package parity

import (
	"context"
	"reflect"
	"testing"
)

func TestStableKeyStringLengthPrefixesPreventBoundaryCollisions(t *testing.T) {
	t.Parallel()

	left := stableKeyString("ab", "c")
	right := stableKeyString("a", "bc")
	if left == right {
		t.Fatalf("stableKeyString collision: %q", left)
	}
	if left != "2:ab|1:c|" {
		t.Fatalf("stableKeyString = %q, want length-prefixed form", left)
	}
	if got := stableKeyString("", "x"); got != "0:|1:x|" {
		t.Fatalf("stableKeyString with empty component = %q", got)
	}
}

func TestDiffArtifactStreamsPreservesUnavailableAndCorruptLikeMemoryDiff(t *testing.T) {
	t.Parallel()

	unavailable := testArtifact("op:1:2:payload:tool_response:1")
	unavailable.Class = ClassToolResponse
	unavailable.Availability = AvailabilitySourceUnavailable
	unavailable.HashDomain = ""
	unavailable.Selector = Selector{URI: "aiagent-v3://payloads/uncaptured/op-2/tool_response/1"}
	unavailable.Bytes = 0
	unavailable.Chars = -1
	unavailable.ComputedSHA256 = ""

	corrupt := testArtifact("line:9:/msg/content/0/text")
	corrupt.Availability = AvailabilitySourceCorrupt
	corrupt.IntegrityFailures = []IntegrityFailure{{
		Field:    "sha256",
		Expected: "producer-hash",
		Actual:   corrupt.ComputedSHA256,
	}}

	source := []Artifact{unavailable, corrupt}
	canonical := []Artifact{unavailable}

	memory, err := DiffContextCapped(context.Background(), source, canonical, 20)
	if err != nil {
		t.Fatalf("DiffContextCapped: %v", err)
	}
	streamed, err := DiffArtifactStreamsContext(context.Background(),
		NewArtifactSliceReader(source),
		NewArtifactSliceReader(canonical),
		StreamDiffOptions{MaxFindings: 20, WorkDir: t.TempDir()},
	)
	if err != nil {
		t.Fatalf("DiffArtifactStreamsContext: %v", err)
	}
	if !reflect.DeepEqual(streamed, memory) {
		t.Fatalf("stream diff mismatch\nstreamed=%+v\nmemory=%+v", streamed, memory)
	}
	assertFinding(t, streamed, StateIncomplete, CodeSourceCorrupt, SeverityP1)
}
