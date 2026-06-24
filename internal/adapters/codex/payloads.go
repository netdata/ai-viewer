package codex

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/netdata/ai-viewer/internal/canonical"
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

// payloadURI builds the PayloadRef LocationURI for a body inline in this
// rollout file at the given 1-based line number (spec rule #6/#7/#8, edge #7).
// The base form is "file://<symlink-resolved-abs>#L<line>"; pointer-backed refs
// add a json_pointer query so parity can resolve the exact nested artifact
// without ai-viewer ever copying the body into SQLite.
//
// Containment (Chunk D, security.md §6): the absolute path is resolved through
// symlinks and verified to stay inside the configured sessions root via
// payloadLocationURI. The "#L<line>" anchor is appended AFTER the file:// path is
// built so the anchor is identical to Chunk B's contract
// (TestMapper_PayloadRefLineAnchor). When m.root is empty (mapper-only tests) the
// containment resolve is skipped and the cleaned absolute path is used; when
// m.absPath is empty the URI is just the line anchor.
//
// The scanner is the authoritative containment gate: readRollout (scanner.go)
// refuses any file that resolves outside the root BEFORE a single line is
// streamed, so by the time the mapper builds a ref the owning file is already
// known to be contained. A resolve failure or apparent escape here (e.g. the
// file removed between the scanner's open and this build — impossible while the
// scanner holds the fd, but handled defensively) therefore falls back to the
// cleaned absolute path rather than dropping the anchor, keeping the ref usable
// and the op→payload linkage (payload_refs.op_id NOT NULL) intact.
func (m *fileMapper) payloadURI(lineNo int) string {
	anchor := ""
	if lineNo > 0 {
		anchor = fmt.Sprintf("#L%d", lineNo)
	}
	if m.absPath == "" {
		return anchor
	}
	uri, err := payloadLocationURI(m.root, m.absPath)
	if err != nil {
		// Containment resolve failed (escape or unresolvable). The scanner
		// already vetted the file before streaming, so fall back to the cleaned
		// absolute path rather than emit a lossy ref.
		uri = "file://" + filepath.ToSlash(filepath.Clean(m.absPath))
	}
	return uri + anchor
}

func (m *fileMapper) payloadURIWithPointer(lineNo int, pointer string) string {
	uri := m.payloadURI(lineNo)
	if pointer == "" {
		return uri
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	values := parsed.Query()
	values.Set("json_pointer", pointer)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

// payloadRef builds a PayloadRefEvent for a body inline in this rollout at the
// record currently being mapped (m.lineNo). It is scoped to the owning op
// (turnSeq/opSeq) so it references an op that EXISTS — payload_refs.op_id is NOT
// NULL REFERENCES ops(id), so an orphan ref would FK-roll-back the ingest batch
// (mirrors claude_code's P1.1a discipline). OriginalBytes is the byte length of
// the selected logical payload for pointer-backed refs, or the containing record
// for whole-record fallback refs; -1 when unknown.
func (m *fileMapper) payloadRef(base canonical.EventBase, turnSeq, opSeq int, kind, format string, originalBytes int64) canonical.PayloadRefEvent {
	return m.payloadRefAtPointer(base, turnSeq, opSeq, kind, format, originalBytes, "")
}

func (m *fileMapper) payloadRefAtPointer(base canonical.EventBase, turnSeq, opSeq int, kind, format string, originalBytes int64, pointer string) canonical.PayloadRefEvent {
	return canonical.PayloadRefEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PayloadKind:     kind,
		Format:          format,
		LocationURI:     m.payloadURIWithPointer(m.lineNo, pointer),
		OriginalBytes:   originalBytes,
	}
}
