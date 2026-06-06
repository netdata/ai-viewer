package claude_code

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// discoverTranscripts walks the projects root and returns every transcript
// (main + subagent) sorted by relative path for deterministic replay. The
// main-session sessionId is taken from the file basename; the subagent
// agentId from the agent-<agentId>.jsonl basename and the parent sessionId
// from the enclosing <sessionId>/ dir. Orphan-root sessions (a <sessionId>/
// dir with only subagents/ and no parent .jsonl) are detected here and a
// synthetic root transcript is NOT added — the orphan root session is
// synthesized in scanAll from the union of its children (spec §10.1).
//
// Every discovered transcript path is resolved with filepath.EvalSymlinks and
// verified to stay inside the resolved projects root before it is returned
// (spec §6.1, P2e, security.md §6 "No symlink traversal escape"). A path that
// escapes the root is refused with a SourceError via onError and skipped; it is
// never opened or read. onError may be nil for callers that do not need the
// diagnostic.
func discoverTranscripts(root string, onError func(error)) ([]transcript, error) {
	onError = ensureOnError(onError)
	resolvedRoot, rerr := filepath.EvalSymlinks(filepath.Clean(root))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve projects root %s: %w", root, rerr)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read projects root %s: %w", root, err)
	}
	out := discoverProjects(root, resolvedRoot, entries, onError)
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

func ensureOnError(onError func(error)) func(error) {
	if onError != nil {
		return onError
	}
	return func(error) {}
}

func discoverProjects(root, resolvedRoot string, entries []os.DirEntry, onError func(error)) []transcript {
	var out []transcript
	for _, projEntry := range entries {
		if !projEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(root, projEntry.Name())
		out = append(out, discoverProject(root, resolvedRoot, projDir, onError)...)
	}
	return out
}

// withinSourceRoot reports whether abs resolves (through symlinks) to a path
// inside resolvedRoot (spec §6.1, P2e). The first argument MUST be the
// ALREADY-symlink-resolved projects root.
func withinSourceRoot(resolvedRoot, abs string, onError func(error)) bool {
	resolved, ok, err := withinResolvedRoot(resolvedRoot, abs)
	if err != nil {
		onError(fmt.Errorf("claude_code: cannot resolve %s for containment; skipping: %w", abs, err))
		return false
	}
	if !ok {
		onError(fmt.Errorf("claude_code: %s resolves to %s outside the projects root; skipping (symlink escape)", abs, resolved))
		return false
	}
	return true
}

// discoverProject enumerates transcripts under a single
// <root>/<sanitized-cwd>/ project directory. Per-entry errors are fail-soft:
// an unreadable session subdir, broken subagent subtree, or relPath failure on
// one file surfaces a SourceError and skips only that entry.
func discoverProject(root, resolvedRoot, projDir string, onError func(error)) []transcript {
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return handleProjectReadError(projDir, err, onError)
	}
	var out []transcript
	for _, entry := range entries {
		out = append(out, discoverProjectEntry(root, resolvedRoot, projDir, entry, onError)...)
	}
	return out
}

func handleProjectReadError(projDir string, err error, onError func(error)) []transcript {
	if os.IsNotExist(err) {
		return nil
	}
	onError(fmt.Errorf("claude_code: read project dir %s: %w; skipping", projDir, err))
	return nil
}

func discoverProjectEntry(root, resolvedRoot, projDir string, entry os.DirEntry, onError func(error)) []transcript {
	name := entry.Name()
	if entry.IsDir() {
		return discoverSessionSubagents(root, resolvedRoot, projDir, name, onError)
	}
	if !strings.HasSuffix(name, transcriptExt) {
		return nil
	}
	tr, ok := discoverRootTranscript(root, resolvedRoot, projDir, name, onError)
	if !ok {
		return nil
	}
	return []transcript{tr}
}

func discoverRootTranscript(root, resolvedRoot, projDir, name string, onError func(error)) (transcript, bool) {
	abs := filepath.Join(projDir, name)
	if !withinSourceRoot(resolvedRoot, abs, onError) {
		return transcript{}, false
	}
	rel, err := relPath(root, abs)
	if err != nil {
		onError(fmt.Errorf("claude_code: relpath main transcript %s: %w; skipping", abs, err))
		return transcript{}, false
	}
	sessionID := strings.TrimSuffix(name, transcriptExt)
	return transcript{
		rel:        rel,
		abs:        abs,
		nativeID:   sessionID,
		kind:       canonical.KindRoot,
		sessionDir: filepath.Join(projDir, sessionID),
	}, true
}

// discoverSessionSubagents enumerates the subagent transcripts under
// <projDir>/<sessionId>/subagents/ recursively. Discovery is fail-soft per
// entry and returns only the transcripts it can safely resolve.
func discoverSessionSubagents(root, resolvedRoot, projDir, sessionID string, onError func(error)) []transcript {
	d := subagentDiscovery{
		root:         root,
		resolvedRoot: resolvedRoot,
		projDir:      projDir,
		sessionID:    sessionID,
		onError:      onError,
	}
	d.walk()
	return d.out
}

type subagentDiscovery struct {
	root         string
	resolvedRoot string
	projDir      string
	sessionID    string
	onError      func(error)
	out          []transcript
}

func (d *subagentDiscovery) walk() {
	subDir := filepath.Join(d.projDir, d.sessionID, subagentsDir)
	_ = filepath.WalkDir(subDir, d.walkEntry)
}

func (d *subagentDiscovery) walkEntry(path string, entry os.DirEntry, err error) error {
	if err != nil {
		return d.handleWalkError(path, entry, err)
	}
	if !isAgentTranscript(entry) {
		return nil
	}
	d.add(path, entry.Name())
	return nil
}

func (d *subagentDiscovery) handleWalkError(path string, entry os.DirEntry, err error) error {
	if os.IsNotExist(err) {
		return filepath.SkipDir
	}
	d.onError(fmt.Errorf("claude_code: walk subagents under %s: %w; skipping", path, err))
	if entry != nil && entry.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func isAgentTranscript(entry os.DirEntry) bool {
	if entry.IsDir() {
		return false
	}
	name := entry.Name()
	return strings.HasPrefix(name, "agent-") && strings.HasSuffix(name, transcriptExt)
}

func (d *subagentDiscovery) add(path, name string) {
	if !withinSourceRoot(d.resolvedRoot, path, d.onError) {
		return
	}
	rel, err := relPath(d.root, path)
	if err != nil {
		d.onError(fmt.Errorf("claude_code: relpath subagent %s: %w; skipping", path, err))
		return
	}
	agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), transcriptExt)
	d.out = append(d.out, transcript{
		rel:            rel,
		abs:            path,
		nativeID:       childNativeID(d.sessionID, agentID),
		parentNativeID: d.sessionID,
		kind:           canonical.KindSubAgent,
		sessionDir:     filepath.Join(d.projDir, d.sessionID),
	})
}

// relPath returns abs relative to root with forward slashes, the canonical
// cursor key form.
func relPath(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("relpath %s under %s: %w", abs, root, err)
	}
	return filepath.ToSlash(rel), nil
}
