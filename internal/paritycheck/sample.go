package paritycheck

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/netdata/ai-viewer/internal/parity"
)

var errSourceSampleComplete = errors.New("source sample complete")

func sortedArtifacts(in []parity.Artifact) []parity.Artifact {
	out := append([]parity.Artifact(nil), in...)
	sortArtifacts(out)
	return out
}

func sortArtifacts(artifacts []parity.Artifact) {
	sort.SliceStable(artifacts, func(i int, j int) bool {
		return artifactSortKey(artifacts[i]) < artifactSortKey(artifacts[j])
	})
}

func artifactSortKey(a parity.Artifact) string {
	parts := []string{
		a.Adapter,
		a.SourceID,
		a.NativeSessionID,
		string(a.Class),
		a.NativeArtifactID,
		a.NativeTurnID,
		a.SourceFile,
		a.Selector.URI,
		a.Selector.JSONPointer,
		a.Selector.FieldPath,
	}
	return strings.Join(parts, "\x00")
}

type boundedSourceSampleWriter struct {
	limit     int
	artifacts []parity.Artifact
}

func newBoundedSourceSampleWriter(limit int) *boundedSourceSampleWriter {
	return &boundedSourceSampleWriter{limit: limit}
}

func (w *boundedSourceSampleWriter) WriteArtifact(ctx context.Context, artifact parity.Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sourceCorruptArtifact(artifact) {
		w.artifacts = append(w.artifacts, artifact)
		return nil
	}
	if w.limit <= 0 {
		return nil
	}
	if retainedSampleArtifacts(w.artifacts) < w.limit {
		w.artifacts = append(w.artifacts, artifact)
		return nil
	}

	maxIndex := maxSampleArtifactIndex(w.artifacts)
	for i := range w.artifacts {
		if sourceCorruptArtifact(w.artifacts[i]) {
			continue
		}
		if artifactSortKey(w.artifacts[maxIndex]) < artifactSortKey(w.artifacts[i]) {
			maxIndex = i
		}
	}
	if artifactSortKey(artifact) < artifactSortKey(w.artifacts[maxIndex]) {
		w.artifacts[maxIndex] = artifact
	}
	return nil
}

func (w *boundedSourceSampleWriter) Sample() []parity.Artifact {
	return sortedArtifacts(w.artifacts)
}

type earlyStopSourceSampleWriter struct {
	inner *boundedSourceSampleWriter
}

func newEarlyStopSourceSampleWriter(inner *boundedSourceSampleWriter) *earlyStopSourceSampleWriter {
	return &earlyStopSourceSampleWriter{inner: inner}
}

func (w *earlyStopSourceSampleWriter) WriteArtifact(ctx context.Context, artifact parity.Artifact) error {
	if err := w.inner.WriteArtifact(ctx, artifact); err != nil {
		return err
	}
	if w.inner.limit <= 0 || sourceCorruptArtifact(artifact) {
		return nil
	}
	if retainedSampleArtifacts(w.inner.artifacts) >= w.inner.limit {
		return errSourceSampleComplete
	}
	return nil
}

func sourceCorruptArtifact(artifact parity.Artifact) bool {
	return artifact.Availability == parity.AvailabilitySourceCorrupt
}

func retainedSampleArtifacts(artifacts []parity.Artifact) int {
	count := 0
	for _, artifact := range artifacts {
		if !sourceCorruptArtifact(artifact) {
			count++
		}
	}
	return count
}

func maxSampleArtifactIndex(artifacts []parity.Artifact) int {
	for i, artifact := range artifacts {
		if !sourceCorruptArtifact(artifact) {
			return i
		}
	}
	return 0
}

type sampledArtifactSet struct {
	keys      map[parity.MatchKey]struct{}
	classless map[parity.ClasslessKey]struct{}
}

func newSampledArtifactSet(sampled []parity.Artifact) sampledArtifactSet {
	filter := sampledArtifactSet{
		keys:      make(map[parity.MatchKey]struct{}, len(sampled)),
		classless: make(map[parity.ClasslessKey]struct{}, len(sampled)),
	}
	for _, artifact := range sampled {
		filter.keys[artifact.Key()] = struct{}{}
		filter.classless[artifact.ClasslessKey()] = struct{}{}
	}
	return filter
}

func (f sampledArtifactSet) IncludeArtifactKey(key parity.MatchKey, classless parity.ClasslessKey) bool {
	if _, ok := f.keys[key]; ok {
		return true
	}
	return f.IncludeClasslessKey(classless)
}

func (f sampledArtifactSet) IncludeClasslessKey(classless parity.ClasslessKey) bool {
	_, ok := f.classless[classless]
	return ok
}
