package parity

import "context"

// ArtifactWriter receives artifacts from streaming extractors.
type ArtifactWriter interface {
	WriteArtifact(ctx context.Context, artifact Artifact) error
}

// ArtifactWriterFunc adapts a function to ArtifactWriter.
type ArtifactWriterFunc func(ctx context.Context, artifact Artifact) error

// WriteArtifact writes one artifact through the wrapped function.
func (f ArtifactWriterFunc) WriteArtifact(ctx context.Context, artifact Artifact) error {
	return f(ctx, artifact)
}
