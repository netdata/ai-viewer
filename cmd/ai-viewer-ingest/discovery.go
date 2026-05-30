// Auto-discovery helpers for ai-viewer-ingest. Split out of sources.go so each
// file stays under the 400-line budget. This file owns the default-location
// resolvers (one per adapter, honoring the relevant env override) and the
// best-effort observability counters the auto-discovery log line carries
// (acceptance #8). The counters are deliberately lightweight predicates that
// mirror each adapter's ingest match WITHOUT importing the adapter package, so
// the surfaced count matches what the source will actually yield. They never
// fail discovery: a read/walk error yields 0.
package main

import (
	"os"
	"path/filepath"
	"strings"
)

// claudeProjectsDir returns the claude-code projects root, honoring
// $CLAUDE_CONFIG_DIR (spec adapter-claude-code.md §2.1). When the env var is
// set, the root is "$CLAUDE_CONFIG_DIR/projects"; otherwise "~/.claude/projects".
func claudeProjectsDir(home string) string {
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
		return filepath.Join(cfg, "projects")
	}
	return filepath.Join(home, ".claude", "projects")
}

// countProjectDirs returns the number of immediate subdirectories under the
// claude-code projects root (each is one sanitized-cwd project). Returns 0
// on any read error — the count is observability, not a gate.
func countProjectDirs(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// codexSessionsDir returns the codex sessions root, honoring $CODEX_HOME
// (SOW-0004 C#3). When the env var is set, the root is "$CODEX_HOME/sessions";
// otherwise "~/.codex/sessions". This is the directory the adapter walks and
// tails; the probe checks it for existence.
func codexSessionsDir(home string) string {
	if ch := os.Getenv("CODEX_HOME"); ch != "" {
		return filepath.Join(ch, "sessions")
	}
	return filepath.Join(home, ".codex", "sessions")
}

// opencodeDBPath resolves the opencode database file path the auto-discovery
// probe checks (deployment.md §"Source Auto-Discovery"). Resolution order:
//
//  1. $OPENCODE_DB, if non-empty — used VERBATIM as a full path to the database.
//  2. else $XDG_DATA_HOME/opencode/opencode.db, if $XDG_DATA_HOME is non-empty.
//  3. else ~/.local/share/opencode/opencode.db (the XDG default base).
//
// CAVEAT: $OPENCODE_DB is honoured as the conventional override name, but it
// could NOT be confirmed against opencode's upstream source during this work
// (the mirror was unavailable), so it is treated as best-effort. The XDG base
// (~/.local/share == $XDG_DATA_HOME) IS opencode's verified default location.
// Either way the probe os.Stats the resolved path and registers the source only
// if it exists; pointing --source opencode:<path> at a non-default database
// remains the explicit escape hatch.
func opencodeDBPath(home string) string {
	if db := os.Getenv("OPENCODE_DB"); db != "" {
		return db
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode", "opencode.db")
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// codexRolloutPrefix is the shared filename prefix for both modern and legacy
// codex rollouts (openai/codex codex-rs/rollout/src/list.rs filters on
// starts_with("rollout-")). Duplicated here as a lightweight observability
// predicate; the adapter's discovery.go holds the authoritative anchored
// regexes used for actual ingest.
const codexRolloutPrefix = "rollout-"

// codexArchivedDir is the codex session archive, pruned from both ingest and
// these observability counts (spec adapter-codex.md §"Filesystem Layout").
const codexArchivedDir = "archived_sessions"

// countRolloutFiles returns the number of modern sharded codex rollouts
// ("rollout-*.jsonl") under the sessions root, counting ONLY files at the
// YYYY/MM/DD shard depth and pruning archived_sessions/. Returns 0 on any walk
// error — the count is observability for acceptance #8, not a gate, so it is
// read best-effort and never blocks discovery. Mirrors discovery.go's modern
// match (^rollout-.*\.jsonl$) AND its shard-depth requirement (F8) without
// importing the adapter package, so the surfaced count matches what is ingested.
func countRolloutFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == codexArchivedDir && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, codexRolloutPrefix) && strings.HasSuffix(name, ".jsonl") && codexAtShardDepth(root, path) {
			n++
		}
		return nil
	})
	return n
}

// codexAtShardDepth reports whether path is a rollout at the required YYYY/MM/DD
// shard depth relative to root: exactly three leading numeric path components
// then the basename (F8). Mirrors discovery.go's hasShardDepth without importing
// the adapter package, so countRolloutFiles never over-counts a stray
// rollout-*.jsonl placed at the wrong depth. A relpath failure counts the file
// out (best-effort observability).
func codexAtShardDepth(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts[:3] {
		if len(p) == 0 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// countLegacyJSON returns the number of legacy flat codex rollouts
// ("rollout-*.json") directly under the sessions root (NOT in shards). These are
// recognized but NOT ingested in v1 (one informational SourceError per file);
// the count is surfaced separately so the operator sees the deferred-legacy
// volume (acceptance #8). Returns 0 on any read error. Mirrors discovery.go's
// legacy match (^rollout-.*\.json$, root-only).
func countLegacyJSON(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, codexRolloutPrefix) && strings.HasSuffix(name, ".json") {
			n++
		}
	}
	return n
}
