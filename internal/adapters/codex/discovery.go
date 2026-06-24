package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// modernExt is the required extension for a modern codex rollout file.
const modernExt = ".jsonl"

// rolloutPrefix is the required filename prefix for both modern and legacy
// rollout files (openai/codex codex-rs/rollout/src/list.rs:898 filters on
// starts_with("rollout-")).
const rolloutPrefix = "rollout-"

// archivedSessionsDir is the codex session archive, explicitly out of scope for
// ingest (spec adapter-codex.md §"Filesystem Layout").
const archivedSessionsDir = "archived_sessions"

// modernNameRe matches a modern rollout filename: "rollout-*.jsonl" (the strict
// upstream filter, codex-rs/rollout/src/list.rs:898,918,932). Anchored so a
// name like "x-rollout-….jsonl" or "rollout-….jsonl.bak" does not match.
var modernNameRe = regexp.MustCompile(`^rollout-.*\.jsonl$`)

// shardComponentRe matches a single numeric path component of a YYYY/MM/DD
// shard. The upstream layout is strictly numeric date shards
// (codex-rs/rollout/src/recorder.rs:1325-1354), so a non-numeric component
// (e.g. "rollout-…jsonl" placed directly under sessions/, or under a
// "scratch/" dir) is NOT a real rollout location and must not be ingested (F8).
var shardComponentRe = regexp.MustCompile(`^[0-9]+$`)

// hasShardDepth reports whether rel (forward-slashed, root-relative) is a modern
// rollout at the required YYYY/MM/DD shard depth: exactly three leading numeric
// path components followed by the "rollout-….jsonl" basename (four components
// total). A stray "rollout-….jsonl" directly under the sessions root, or at the
// wrong depth, returns false so discovery, the tailer, and the observability
// counts all agree on what is ingestable (F8; spec §"Filesystem Layout",
// recorder.rs:1325-1354). The basename match itself is the caller's job; this
// only validates the shard prefix and component count.
func hasShardDepth(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts[:3] {
		if !shardComponentRe.MatchString(p) {
			return false
		}
	}
	return true
}

// legacyNameRe matches a legacy flat rollout filename: "rollout-*.json" (no
// time component, directly under sessions/; spec §"Legacy `.json` layout").
var legacyNameRe = regexp.MustCompile(`^rollout-.*\.json$`)

// rollout describes one modern rollout file discovered under the sessions root.
type rollout struct {
	// rel is the path relative to the root (the cursor key),
	// "YYYY/MM/DD/rollout-….jsonl", forward-slashed.
	rel string
	// abs is the absolute path on disk.
	abs string
}

// discovered is the result of one discovery walk: the modern rollout files
// (sorted by rel for deterministic replay) plus the basenames of the legacy
// flat .json files found directly under the root.
type discovered struct {
	modern []rollout
	legacy []string
}

// discoverRollouts walks the sessions root and returns every modern rollout
// file (sorted by relative path) plus the legacy flat .json basenames found
// directly under the root. Discovery is fail-soft per entry (SOW gate: a
// non-IsNotExist error reading one shard/file surfaces a SourceError via
// onError and is skipped so the walk continues); ONLY the configured root being
// unreadable is fatal. Modern files are matched by ^rollout-.*\.jsonl$ at any
// depth under YYYY/MM/DD/; legacy files by ^rollout-.*\.json$ at the root only.
// archived_sessions/, *.sqlite*, history*, session_index.jsonl, and any other
// name are ignored (spec §"Watch Strategy").
//
// Every discovered modern path is symlink-resolved and verified to stay inside
// the resolved root before it is returned (security.md §6 "No symlink traversal
// escape"); a path that escapes is refused with a SourceError and skipped.
func discoverRollouts(root string, onError func(error)) (discovered, error) {
	if onError == nil {
		onError = func(error) {}
	}
	resolvedRoot, rerr := filepath.EvalSymlinks(filepath.Clean(root))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return discovered{}, nil
		}
		return discovered{}, fmt.Errorf("resolve sessions root %s: %w", root, rerr)
	}
	// Stat-probe the root: a non-IsNotExist failure (e.g. unreadable) is fatal
	// (the source is broken), an absent root is benign-empty (first run).
	if _, serr := os.ReadDir(root); serr != nil {
		if os.IsNotExist(serr) {
			return discovered{}, nil
		}
		return discovered{}, fmt.Errorf("read sessions root %s: %w", root, serr)
	}
	var out discovered
	// Walk the RESOLVED root: filepath.WalkDir does not descend INTO a symlinked
	// walk-root, so walking the unresolved root would yield nothing under a
	// legitimately-symlinked sessions dir. Keys are rel to resolvedRoot, which
	// equals rel to root for the same subtree (the tail handleEvent keys the
	// same way, so scan and tail cursor keys match).
	_ = filepath.WalkDir(resolvedRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			// Fail-soft: surface the unreadable entry and continue past it. SkipDir
			// on a directory prunes just that subtree; a file error is reported and
			// the walk resumes with the next sibling.
			onError(fmt.Errorf("codex: walk sessions tree %s: %w; skipping", path, err))
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Prune the archive subtree (out of scope for ingest).
			if d.Name() == archivedSessionsDir && path != resolvedRoot {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		atRoot := filepath.Dir(path) == resolvedRoot
		switch {
		case modernNameRe.MatchString(name):
			rel, rrErr := relPath(resolvedRoot, path)
			if rrErr != nil {
				onError(fmt.Errorf("codex: relpath rollout %s: %w; skipping", path, rrErr))
				return nil
			}
			// Require the YYYY/MM/DD shard depth (F8): a stray rollout-*.jsonl
			// directly under the root, or at the wrong depth, is not a real codex
			// rollout location (recorder.rs:1325-1354) and is silently ignored.
			if !hasShardDepth(rel) {
				return nil
			}
			if !withinSourceRoot(resolvedRoot, path, onError) {
				return nil
			}
			out.modern = append(out.modern, rollout{rel: rel, abs: path})
		case atRoot && legacyNameRe.MatchString(name):
			// Legacy flat .json directly under the root: recorded by basename so
			// Scan can consume the static file once.
			out.legacy = append(out.legacy, name)
		}
		return nil
	})
	sort.Slice(out.modern, func(i, j int) bool { return out.modern[i].rel < out.modern[j].rel })
	sort.Strings(out.legacy)
	return out, nil
}

// relPath returns abs relative to root with forward slashes, the canonical
// cursor key form. Mirrors claude_code.
func relPath(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("relpath %s under %s: %w", abs, root, err)
	}
	return filepath.ToSlash(rel), nil
}

// nativeIDForRollout derives the fallback session NativeID for a rollout file
// from its filename (the UUIDv7 ThreadId tail of "rollout-<ts>-<ThreadId>"),
// used as mapperConfig.nativeID. The mapper overrides this from the
// session_meta.id when the meta is read (mapper.go applySessionMeta), so this
// is only the pre-meta anchor; a file without a parseable id tail falls back to
// the basename so events still attach to a stable id. Rule #24 ensures a
// session_meta is present before this file is streamed, so the override
// normally wins; the fallback only matters for the degenerate "meta present but
// id empty" case.
func nativeIDForRollout(r rollout) string {
	base := strings.TrimSuffix(filepath.Base(r.abs), modernExt)
	base = strings.TrimPrefix(base, rolloutPrefix)
	// base is "YYYY-MM-DDTHH-MM-SS-<ThreadId>"; the ThreadId is a UUIDv7 whose
	// five hyphen-separated groups are the last 5 of the dash-split. Extract the
	// trailing UUID (8-4-4-4-12) when present; else use the whole tail.
	if id := uuidTail(base); id != "" {
		return id
	}
	return base
}

// uuidTail returns the trailing 8-4-4-4-12 UUID embedded at the end of a
// dash-joined filename stem, or "" when the tail does not look like a UUID. The
// stem is "YYYY-MM-DDTHH-MM-SS-<8>-<4>-<4>-<4>-<12>"; the UUID is the last five
// dash groups.
func uuidTail(stem string) string {
	parts := strings.Split(stem, "-")
	if len(parts) < 5 {
		return ""
	}
	tail := parts[len(parts)-5:]
	want := []int{8, 4, 4, 4, 12}
	for i, p := range tail {
		if len(p) != want[i] || !isHex(p) {
			return ""
		}
	}
	return strings.Join(tail, "-")
}

// isHex reports whether s is non-empty and all lowercase/uppercase hex digits.
func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(s) > 0
}
