package aiagent_v3

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolvedPayload describes a payload artifact whose path has been
// resolved against the configured source root, with the traversal guard
// applied per spec §4.3.
type resolvedPayload struct {
	// LocationURI is "file://<absolute-cleaned-path>" when the payload
	// is captured and resolvable; empty otherwise.
	LocationURI string
	// AbsolutePath is the cleaned absolute path or empty when unresolved.
	AbsolutePath string
}

// resolvePayloadPath mirrors ai-agent.git/src/evidence/reader.ts:354-365.
// Returns a resolvedPayload describing the on-disk location of the
// payload, or one with empty fields when the ref is uncaptured or
// pathless. Returns an error when the relative path escapes the
// configured root (path-traversal guard).
func resolvePayloadPath(root string, ref payloadRef) (resolvedPayload, error) {
	if !ref.Captured || ref.Path == "" {
		return resolvedPayload{}, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return resolvedPayload{}, fmt.Errorf("resolve root %q: %w", root, err)
	}
	cleanedRoot := filepath.Clean(absRoot)
	// ref.Path uses forward slashes per ai-agent paths.ts:9,16,45 (it is
	// emitted via path.posix.join). Normalise to the host separator.
	relParts := strings.Split(ref.Path, "/")
	joined := filepath.Join(append([]string{cleanedRoot}, relParts...)...)
	cleaned := filepath.Clean(joined)
	// Traversal guard: require the cleaned absolute path to live under
	// cleanedRoot. filepath.Rel is the idiomatic check.
	rel, err := filepath.Rel(cleanedRoot, cleaned)
	if err != nil {
		return resolvedPayload{}, fmt.Errorf("relative %q: %w", ref.Path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return resolvedPayload{}, fmt.Errorf("payload path escapes root: %q", ref.Path)
	}
	return resolvedPayload{
		LocationURI:  "file://" + filepath.ToSlash(cleaned),
		AbsolutePath: cleaned,
	}, nil
}
