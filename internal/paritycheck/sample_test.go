package paritycheck

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/netdata/ai-viewer/internal/parity"
)

func TestBoundedSourceSampleWriterKeepsStableFirstN(t *testing.T) {
	t.Parallel()

	writer := newBoundedSourceSampleWriter(2)
	for _, artifact := range []parity.Artifact{
		sampleTestArtifact("z-last", parity.ClassSessionBoundary),
		sampleTestArtifact("a-first", parity.ClassSessionBoundary),
		sampleTestArtifact("m-middle", parity.ClassSessionBoundary),
		sampleTestArtifact("b-second", parity.ClassSessionBoundary),
	} {
		if err := writer.WriteArtifact(context.Background(), artifact); err != nil {
			t.Fatalf("WriteArtifact: %v", err)
		}
	}

	gotArtifacts := writer.Sample()
	got := make([]string, 0, len(gotArtifacts))
	for _, artifact := range gotArtifacts {
		got = append(got, artifact.NativeArtifactID)
	}
	want := []string{"a-first", "b-second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sample ids = %+v, want %+v", got, want)
	}
	if len(writer.artifacts) != 2 {
		t.Fatalf("retained artifacts = %d, want bounded 2", len(writer.artifacts))
	}
}

func TestBoundedSourceSampleWriterAlwaysKeepsCorruptArtifacts(t *testing.T) {
	t.Parallel()

	writer := newBoundedSourceSampleWriter(1)
	for _, artifact := range []parity.Artifact{
		sampleTestArtifact("z-last", parity.ClassSessionBoundary),
		sampleCorruptArtifact("source_corruption:file:bad.json:trailing"),
		sampleTestArtifact("a-first", parity.ClassSessionBoundary),
	} {
		if err := writer.WriteArtifact(context.Background(), artifact); err != nil {
			t.Fatalf("WriteArtifact: %v", err)
		}
	}

	gotArtifacts := writer.Sample()
	got := make([]string, 0, len(gotArtifacts))
	for _, artifact := range gotArtifacts {
		got = append(got, artifact.NativeArtifactID)
	}
	want := []string{"a-first", "source_corruption:file:bad.json:trailing"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sample ids = %+v, want %+v", got, want)
	}
	if len(writer.artifacts) != 2 {
		t.Fatalf("retained artifacts = %d, want one sampled plus one corrupt", len(writer.artifacts))
	}
}

func TestEarlyStopSourceSampleWriterStopsAfterLimitAndKeepsPriorCorruption(t *testing.T) {
	t.Parallel()

	inner := newBoundedSourceSampleWriter(1)
	writer := newEarlyStopSourceSampleWriter(inner)
	if err := writer.WriteArtifact(context.Background(), sampleCorruptArtifact("source_corruption:file:bad.json.gz:snapshot")); err != nil {
		t.Fatalf("WriteArtifact corrupt: %v", err)
	}
	if err := writer.WriteArtifact(context.Background(), sampleTestArtifact("sampled", parity.ClassSessionBoundary)); !errors.Is(err, errSourceSampleComplete) {
		t.Fatalf("WriteArtifact sampled error = %v, want %v", err, errSourceSampleComplete)
	}

	gotArtifacts := inner.Sample()
	got := make([]string, 0, len(gotArtifacts))
	for _, artifact := range gotArtifacts {
		got = append(got, artifact.NativeArtifactID)
	}
	want := []string{"sampled", "source_corruption:file:bad.json.gz:snapshot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sample ids = %+v, want %+v", got, want)
	}
}

func TestSampledArtifactSetIncludesClassMismatchCandidate(t *testing.T) {
	t.Parallel()

	sampled := sampleTestArtifact("same-native-id", parity.ClassToolRequest)
	filter := newSampledArtifactSet([]parity.Artifact{sampled})
	mismatchKey := sampled.Key()
	mismatchKey.Class = parity.ClassToolResponse

	if !filter.IncludeArtifactKey(mismatchKey, sampled.ClasslessKey()) {
		t.Fatal("sample filter rejected class-mismatch candidate for sampled native id")
	}
}

func sampleTestArtifact(nativeArtifactID string, class parity.ArtifactClass) parity.Artifact {
	return parity.Artifact{
		SchemaVersion:    parity.SchemaVersion,
		Adapter:          "aiagent_v3",
		SourceID:         "aiagent_v3:/tmp/source",
		NativeSessionID:  "session-1",
		NativeArtifactID: nativeArtifactID,
		Class:            class,
	}
}

func sampleCorruptArtifact(nativeArtifactID string) parity.Artifact {
	artifact := sampleTestArtifact(nativeArtifactID, parity.ClassSourceCorruption)
	artifact.Availability = parity.AvailabilitySourceCorrupt
	return artifact
}
