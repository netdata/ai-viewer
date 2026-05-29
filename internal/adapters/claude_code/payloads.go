package claude_code

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// payloadLocationURI builds a containment-checked "file://<abs>" location for
// a payload artifact (spec §5.4, §6.1). The path is resolved with
// filepath.EvalSymlinks and verified to stay inside the configured projects
// root before it is surfaced (security.md §6 "No symlink traversal escape").
// Returns ("", err) when the path escapes the root so the caller can refuse
// the ref and surface a SourceError. When root is empty (mapper-only unit
// tests) the check is skipped and the cleaned absolute path is returned.
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
// (security.md §6 "No symlink traversal escape", spec §6.1, P2e). It resolves
// the root every call, so per-file walk callers that share one root should
// resolve it once and use withinResolvedRoot instead. A legitimately symlinked
// projects root (e.g. ~/.claude → an external volume) still works: containment
// is judged against the RESOLVED root. A symlink inside the tree that points
// outside the resolved root is refused (ok=false). Returns:
//   - (resolvedAbs, true, nil)  — abs resolves to a path under the root.
//   - ("", false, nil)          — abs resolves outside the root (escape).
//   - ("", false, err)          — the path or root could not be resolved.
//
// When abs does not yet exist on disk, the deepest existing ancestor is
// resolved and the non-existent tail re-appended, so a not-yet-created file is
// judged by where it WOULD live (a non-existent path cannot itself be a
// symlink to elsewhere).
func resolveWithinRoot(root, abs string) (string, bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", false, fmt.Errorf("resolve root %q: %w", root, err)
	}
	return withinResolvedRoot(resolvedRoot, abs)
}

// withinResolvedRoot is resolveWithinRoot's core for callers that have ALREADY
// resolved the projects root once (the directory-walk hot path: every meta and
// every discovered transcript shares one resolved root, so re-running
// EvalSymlinks on the root per file is wasted work — P2-perf). resolvedRoot MUST
// be the output of filepath.EvalSymlinks on the configured root; only abs is
// resolved here. Containment semantics are identical to resolveWithinRoot.
func withinResolvedRoot(resolvedRoot, abs string) (string, bool, error) {
	resolvedAbs, err := evalSymlinksAllowingTail(filepath.Clean(abs))
	if err != nil {
		return "", false, fmt.Errorf("resolve path %q: %w", abs, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedAbs)
	if err != nil {
		return "", false, fmt.Errorf("relative %q under %q: %w", resolvedAbs, resolvedRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, nil
	}
	return resolvedAbs, true, nil
}

// evalSymlinksAllowingTail resolves symlinks in abs, tolerating a not-yet-
// created leaf/tail: it walks up to the deepest existing ancestor, resolves
// that, and re-joins the non-existent remainder. A non-existent path cannot be
// a symlink itself, so judging it by its resolved parent is sound.
func evalSymlinksAllowingTail(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		// Reached the filesystem root without an existing ancestor.
		return abs, nil
	}
	resolvedParent, perr := evalSymlinksAllowingTail(parent)
	if perr != nil {
		return "", perr
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}

// emitToolResultPayload returns a PayloadRefEvent for a user record's
// top-level toolUseResult body (spec §5.4): the structured tool-result echo
// lives inline in the transcript, so the ref points at the transcript file.
// Attached to the just-finalized tool op (turn/op from the matched
// tool_result). Returns an error when the URI cannot be resolved (containment
// failure) so the caller can surface it.
func (m *fileMapper) emitToolResultPayload(base canonical.EventBase, turnSeq, opSeq int) (canonical.PayloadRefEvent, error) {
	uri, err := payloadLocationURI(m.root, m.absPath)
	if err != nil {
		return canonical.PayloadRefEvent{}, err
	}
	return canonical.PayloadRefEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PayloadKind:     "tool_response",
		Format:          "text",
		LocationURI:     uri,
		OriginalBytes:   -1,
	}, nil
}

// emitSummaryPayload returns a PayloadRefEvent for the post-compaction summary
// user message (spec §5.4, §9.2). The summary text lives inline in the
// transcript; the ref lets the UI render it in a compaction lane. It is scoped
// to the compaction op (turnSeq/opSeq) so it references an op that EXISTS
// (P1.1a): payload_refs.op_id is NOT NULL REFERENCES ops(id), and the summary
// belongs to its compaction. The drop guard keys on opSeq == 0 ONLY — the real
// "no owning compaction op" sentinel (a compaction always sets opSeq>=1). A
// compaction can legitimately occur BEFORE any operator prompt (turn 0,
// P2.3b): keying the guard on turnSeq==0 too would wrongly drop that valid
// turn-0 summary. Returns (zero, false, nil) only when no compaction op has
// been seen on the file (opSeq==0) — without an owning op the ref would
// FK-roll-back the batch.
func (m *fileMapper) emitSummaryPayload(base canonical.EventBase, turnSeq, opSeq int) (canonical.PayloadRefEvent, bool, error) {
	if opSeq == 0 {
		return canonical.PayloadRefEvent{}, false, nil
	}
	uri, err := payloadLocationURI(m.root, m.absPath)
	if err != nil {
		return canonical.PayloadRefEvent{}, false, err
	}
	return canonical.PayloadRefEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PayloadKind:     "log",
		Format:          "text",
		LocationURI:     uri,
		OriginalBytes:   -1,
	}, true, nil
}
