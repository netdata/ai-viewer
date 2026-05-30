// Source plumbing for ai-viewer-ingest. Kept separate from main.go so the
// CLI-flag/lifecycle file stays under the 400-line budget.
//
// This file owns:
//   - configuredSource: the (id, format, location) tuple the binary uses.
//   - resolveSources / parseSourceFlag: the --source flag → source list path.
//   - autoDiscoverSources: deployment.md §"Source Auto-Discovery" probes.
//   - startSource / runAdapter / loadSourceCursor: per-source goroutine
//     lifecycle and the cursor-resume path that satisfies ingester.md §17.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/netdata/ai-viewer/internal/adapters"
	// Side-effect import: the codex adapter registers its factory with
	// internal/adapters via init() so the auto-discovery probe added below can
	// construct it. main.go blank-imports the other adapters in the same way;
	// codex is registered from here to keep this chunk's change additive and
	// co-located with its probe.
	_ "github.com/netdata/ai-viewer/internal/adapters/codex"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
)

// configuredSource holds one parsed (format, location) pair plus the
// canonical sourceID used by the ingester. Built from --source flags or
// auto-discovery.
type configuredSource struct {
	id       string
	format   string
	location string
}

// resolveSources returns the source list to start. When the operator
// passes any --source flag, auto-discovery is bypassed entirely (per
// deployment.md §"Source Auto-Discovery": explicit replaces implicit).
// Each location is verified to exist; missing locations produce a
// structured warning and are dropped rather than crashing the binary.
func resolveSources(cli []string, logger *slog.Logger) ([]configuredSource, error) {
	if len(cli) > 0 {
		out := make([]configuredSource, 0, len(cli))
		seen := make(map[string]struct{}, len(cli))
		for _, raw := range cli {
			format, location, err := parseSourceFlag(raw)
			if err != nil {
				return nil, err
			}
			key := format + ":" + location
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, configuredSource{id: key, format: format, location: location})
		}
		return out, nil
	}
	return autoDiscoverSources(logger), nil
}

// parseSourceFlag splits "format:location" into its two parts. The
// format is the registry key (e.g. "aiagent_v3"). The location is the
// adapter-specific path or DSN; everything after the first ':' is taken
// verbatim so locations containing colons (e.g. Windows drive letters in
// a future build) survive.
func parseSourceFlag(raw string) (format, location string, err error) {
	idx := strings.IndexByte(raw, ':')
	if idx < 1 || idx == len(raw)-1 {
		return "", "", fmt.Errorf("--source %q is not in format:location form", raw)
	}
	return raw[:idx], raw[idx+1:], nil
}

// autoDiscoverSources probes the default locations from deployment.md
// §"Source Auto-Discovery". Each existing location becomes a source;
// missing locations are silently skipped. The claude-code probe honors
// $CLAUDE_CONFIG_DIR (spec adapter-claude-code.md §2.1) and falls back to
// ~/.claude; both resolve to a <root>/projects directory.
func autoDiscoverSources(logger *slog.Logger) []configuredSource {
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Warn("ai-viewer-ingest: auto-discovery skipped — cannot resolve $HOME", "err", err)
		return nil
	}

	probes := []struct {
		format   string
		location string
		probe    string
	}{
		{
			format:   "aiagent_v3",
			location: filepath.Join(home, ".ai-agent", "sessions"),
			probe:    filepath.Join(home, ".ai-agent", "sessions", "session"),
		},
		{
			format:   "aiagent_v2",
			location: filepath.Join(home, ".ai-agent", "sessions"),
			probe:    filepath.Join(home, ".ai-agent", "sessions"),
		},
		{
			format:   "claude-code",
			location: claudeProjectsDir(home),
			probe:    claudeProjectsDir(home),
		},
		{
			format:   "codex",
			location: codexSessionsDir(home),
			probe:    codexSessionsDir(home),
		},
	}

	var out []configuredSource
	seen := make(map[string]struct{}, len(probes))
	for _, p := range probes {
		if _, err := os.Stat(p.probe); err != nil {
			continue
		}
		key := p.format + ":" + p.location
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, configuredSource{id: key, format: p.format, location: p.location})
		attrs := []any{"format", p.format, "location", p.location}
		switch p.format {
		case "claude-code":
			// Surface the project-dir count so the operator sees the source
			// is non-empty (acceptance #8). The count is the number of
			// immediate subdirectories under the projects root.
			attrs = append(attrs, "project_dirs", countProjectDirs(p.location))
		case "codex":
			// Surface modern + legacy counts SEPARATELY (SOW-0004 acceptance
			// #8): modern sharded rollouts are ingested, legacy flat .json are
			// only logged (one SourceError each, not ingested in v1). Reporting
			// them apart lets the operator see how many sessions the source will
			// actually surface vs how many remain on the deferred legacy path.
			attrs = append(attrs,
				"modern_rollouts", countRolloutFiles(p.location),
				"legacy_json", countLegacyJSON(p.location))
		}
		logger.Info("ai-viewer-ingest: auto-discovered source", attrs...)
	}
	return out
}

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

// cursorLookup is the minimal contract startSource needs to resume from
// the durable cursor. The production wiring uses *sql.DB through
// sqlCursorLookup; tests inject a fake to verify the round-trip without
// a SQLite dependency.
type cursorLookup interface {
	LookupCursor(ctx context.Context, sourceID string) (string, error)
}

// sqlCursorLookup reads source_progress.cursor for a sourceID. A missing
// row (first-ever run for this source) returns empty string + nil so
// startSource passes a nil Cursor to adapter.Scan.
type sqlCursorLookup struct{ db *sql.DB }

// LookupCursor returns the persisted cursor JSON for sourceID, or "" +
// nil when the row does not exist. Any other error is propagated so the
// caller can decide whether to fall back to a full re-scan or abort.
func (l sqlCursorLookup) LookupCursor(ctx context.Context, sourceID string) (string, error) {
	var cur sql.NullString
	err := l.db.QueryRowContext(ctx,
		`SELECT cursor FROM source_progress WHERE source_id = ?`, sourceID).Scan(&cur)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("select source_progress.cursor: %w", err)
	}
	if !cur.Valid {
		return "", nil
	}
	return cur.String, nil
}

// startSource constructs the adapter for src, validates the location
// exists, registers a worker with the ingester, and spawns the scan +
// tail goroutines feeding events into the worker's channel.
//
// A missing location is reported via OnError but does NOT abort the
// binary — the constraint in the chunk brief mandates "log a structured
// warning and continue with the rest". Per-source crashes surface via
// the adapter's OnError callback that is wired both to a structured log
// line AND to a SourceErrorEvent pushed onto the same events channel so
// /api/health surfaces the parse error via sources.parse_errors and a
// log_entries row.
//
// The persisted source_progress.cursor (if any) is loaded and passed to
// Scan so the binary resumes from the last committed checkpoint instead
// of replaying the entire history on every restart. Cursor corruption
// logs a WARN and falls back to a full
// re-scan; the spec mandates that the daemon keeps making progress
// rather than refusing to start.
func startSource(ctx context.Context, wg *sync.WaitGroup, ing *ingest.Ingester, lookup cursorLookup, src configuredSource, logger *slog.Logger) error {
	factory, ok := adapters.Get(src.format)
	if !ok {
		return fmt.Errorf("unknown adapter format %q (registered: %v)", src.format, adapters.Formats())
	}
	if _, err := os.Stat(src.location); err != nil {
		return fmt.Errorf("location %q is not accessible: %w", src.location, err)
	}

	srcLogger := logger.With("source", src.id, "format", src.format, "location", src.location)
	events := make(chan canonical.Event, adapterEventChanSize)

	adapter, err := factory(src.location, canonical.AdapterOptions{
		Logger:  srcLogger,
		OnError: newOnErrorHandler(ctx, src.id, events, srcLogger),
	})
	if err != nil {
		return fmt.Errorf("construct adapter: %w", err)
	}

	since := loadSourceCursor(ctx, adapter, lookup, src.id, srcLogger)

	if err := ing.Submit(src.id, events); err != nil {
		return fmt.Errorf("submit to ingester: %w", err)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(events)
		runAdapter(ctx, adapter, since, events, srcLogger)
	}()

	srcLogger.Info("ai-viewer-ingest: source started")
	return nil
}

// loadSourceCursor reads source_progress.cursor for srcID and decodes it
// via adapter.ParseCursor. Empty / missing rows return nil so the
// adapter performs a full historical scan (first-ever run). A corrupt
// cursor logs WARN and also returns nil — the spec keeps the daemon
// making progress rather than refusing to start.
func loadSourceCursor(ctx context.Context, adapter canonical.Adapter, lookup cursorLookup, srcID string, logger *slog.Logger) canonical.Cursor {
	if lookup == nil {
		return nil
	}
	stored, err := lookup.LookupCursor(ctx, srcID)
	if err != nil {
		logger.Warn("ai-viewer-ingest: cursor lookup failed; falling back to full scan",
			"err", err)
		return nil
	}
	if stored == "" {
		return nil
	}
	cur, err := adapter.ParseCursor(stored)
	if err != nil {
		logger.Warn("ai-viewer-ingest: cursor decode failed; falling back to full scan",
			"stored_len", len(stored), "err", err)
		return nil
	}
	logger.Info("ai-viewer-ingest: resuming from persisted cursor",
		"stored_len", len(stored))
	return cur
}

// newOnErrorHandler returns the OnError callback wired into the
// adapter. Non-fatal adapter parse errors flow through here; the
// handler emits a structured WARN log AND pushes a SourceErrorEvent
// onto the events channel so /api/health surfaces the failure
// (a guaranteed send).
//
// The send is BLOCKING (with ctx.Done() escape): the previous
// `default: drop` branch was a silent-failure path that could
// under-report parse_errors under load. Backpressure
// from a saturated worker should pause the adapter goroutine, not lose
// the event. Cancellation of ctx is the only way to drop a
// SourceErrorEvent here, and that path runs only on ingester
// shutdown — at which point losing the event is acceptable because
// the daemon is exiting anyway.
func newOnErrorHandler(ctx context.Context, srcID string, events chan<- canonical.Event, logger *slog.Logger) func(error) {
	return func(err error) {
		if err == nil {
			return
		}
		logger.Warn("ai-viewer-ingest: adapter parse error", "err", err)
		ev := canonical.SourceErrorEvent{
			EventBase: canonical.EventBase{
				SourceID:  srcID,
				SourceSeq: 0,
				Ts:        nowMicros(),
			},
			Message: err.Error(),
		}
		select {
		case events <- ev:
		case <-ctx.Done():
			logger.Warn("ai-viewer-ingest: source-error event dropped on shutdown",
				"err", err)
		}
	}
}

// runAdapter drives a single adapter's lifecycle: Scan(since) to drain
// historical data, then Tail() to follow changes until ctx is cancelled.
// Errors from either call are logged with the source's sticky logger;
// the caller closes the channel after we return.
func runAdapter(ctx context.Context, adapter canonical.Adapter, since canonical.Cursor, events chan<- canonical.Event, logger *slog.Logger) {
	logger.Info("ai-viewer-ingest: adapter scan starting", "resume", since != nil)
	if err := adapter.Scan(ctx, since, events); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Info("ai-viewer-ingest: adapter scan cancelled")
			return
		}
		logger.Error("ai-viewer-ingest: adapter scan failed", "err", err)
		// Fall through to Tail anyway — partial backfill is better than
		// no realtime data.
	} else {
		logger.Info("ai-viewer-ingest: adapter scan complete; tail starting")
	}
	if err := adapter.Tail(ctx, events); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Info("ai-viewer-ingest: adapter tail cancelled")
			return
		}
		logger.Error("ai-viewer-ingest: adapter tail failed", "err", err)
	}
}
