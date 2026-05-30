package codex

import (
	"fmt"
	"path/filepath"
)

// payloadLocationURI builds a containment-checked "file://<resolved-abs>"
// location for a body inline in a rollout file (spec rule #6/#7/#8, edge #7:
// large bodies are referenced, never inlined). The path is resolved with
// filepath.EvalSymlinks and verified to stay inside the configured sessions
// root before it is surfaced (security.md §6 "No symlink traversal escape").
// Returns ("", err) when the path escapes the root so the caller can fall back
// to the cleaned absolute path rather than emit a lossy ref. When root is empty
// (mapper-only unit tests) the check is skipped and the cleaned absolute path is
// returned. Mirrors claude_code/payloads.go verbatim; the only codex difference
// is that withinResolvedRoot + evalSymlinksAllowingTail already live in
// stream.go (the scanner reuses them), so this file re-adds only the per-call
// resolveWithinRoot wrapper Chunk C noted it had dropped.
func payloadLocationURI(root, abs string) (string, error) {
	cleaned := filepath.Clean(abs)
	if root == "" {
		return "file://" + filepath.ToSlash(cleaned), nil
	}
	resolved, ok, err := resolveWithinRoot(root, cleaned)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("payload path escapes root: %q", abs)
	}
	return "file://" + filepath.ToSlash(resolved), nil
}

// resolveWithinRoot resolves both root AND abs through filepath.EvalSymlinks and
// reports whether the fully-resolved path stays inside the fully-resolved root
// (security.md §6 "No symlink traversal escape"). It resolves the root every
// call, so per-file walk callers that share one root resolve it once and use
// withinResolvedRoot (stream.go) instead — this single-shot wrapper is for the
// mapper's payload-URI builder, which is handed only the absolute file path. A
// legitimately symlinked sessions root (e.g. ~/.codex → an external volume)
// still works: containment is judged against the RESOLVED root. A symlink inside
// the tree that points outside the resolved root is refused (ok=false). Returns:
//   - (resolvedAbs, true, nil)  — abs resolves to a path under the root.
//   - ("", false, nil)          — abs resolves outside the root (escape).
//   - ("", false, err)          — the path or root could not be resolved.
//
// When abs does not yet exist on disk, the deepest existing ancestor is resolved
// and the non-existent tail re-appended, so a not-yet-created file is judged by
// where it WOULD live (a non-existent path cannot itself be a symlink to
// elsewhere). Mirrors claude_code/payloads.go's resolveWithinRoot.
func resolveWithinRoot(root, abs string) (string, bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", false, fmt.Errorf("resolve root %q: %w", root, err)
	}
	return withinResolvedRoot(resolvedRoot, abs)
}
