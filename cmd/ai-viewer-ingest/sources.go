// Source plumbing for ai-viewer-ingest. Kept separate from main.go so the
// CLI-flag/lifecycle file stays under the 400-line budget.
//
// This file owns:
//   - configuredSource: the (id, format, location) tuple the binary uses.
//   - resolveSources / parseSourceFlag: the --source flag → source list path.
//   - startSource / sourceSupervisor / loadSourceCursor: per-source goroutine
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
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
)

// configuredSource holds one parsed (format, location) pair plus the
// canonical sourceID used by the ingester. Built from --source flags or
// auto-discovery. metaJSON carries the adapter-owned JSON metadata blob to
// persist on sources.meta_json (SOW-0024); the empty string means "no
// adapter-owned metadata" and the worker binds NULL to the column.
type configuredSource struct {
	id       string
	format   string
	location string
	metaJSON string
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
func startSource(ctx context.Context, wg *sync.WaitGroup, scanWG *sync.WaitGroup, ing *ingest.Ingester, lookup cursorLookup, src configuredSource, logger *slog.Logger) error {
	return startSourceWithFactoryLookup(ctx, wg, scanWG, ing, lookup, src, logger, adapters.Get)
}

func sourceRegistrations(sources []configuredSource) []ingest.SourceRegistration {
	out := make([]ingest.SourceRegistration, 0, len(sources))
	for _, src := range sources {
		out = append(out, ingest.SourceRegistration{
			ID:       src.id,
			Format:   src.format,
			Location: src.location,
		})
	}
	return out
}

type adapterFactoryLookup func(format string) (canonical.AdapterFactory, bool)

func startSourceWithFactoryLookup(ctx context.Context, wg *sync.WaitGroup, scanWG *sync.WaitGroup, ing *ingest.Ingester, lookup cursorLookup, src configuredSource, logger *slog.Logger, factoryLookup adapterFactoryLookup) error {
	scanStarted := false
	defer func() {
		if !scanStarted {
			scanWG.Done()
		}
	}()
	srcLogger := logger.With("source", src.id, "format", src.format, "location", src.location)
	if err := recordSourceLifecycleWithRetry(ctx, ing, src, srcLogger, ingest.SourceLifecycleUpdate{
		State: ingest.SourceLifecycleStarting,
	}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceTailCancelGrace)
		defer cancel()
		_ = recordSourceLifecycle(recordCtx, ing, src, srcLogger, ingest.SourceLifecycleUpdate{
			State: ingest.SourceLifecycleStopped,
		})
		return err
	}
	factory, ok := factoryLookup(src.format)
	if !ok {
		err := fmt.Errorf("unknown adapter format %q (registered: %v)", src.format, adapters.Formats())
		recordSourceLifecycleBestEffort(ctx, ing, src, srcLogger, ingest.SourceLifecycleUpdate{
			State: ingest.SourceLifecycleStartFailed,
			Error: err.Error(),
		})
		return err
	}
	if _, err := os.Stat(src.location); err != nil {
		wrapped := fmt.Errorf("location %q is not accessible: %w", src.location, err)
		recordSourceLifecycleBestEffort(ctx, ing, src, srcLogger, ingest.SourceLifecycleUpdate{
			State: ingest.SourceLifecycleStartFailed,
			Error: wrapped.Error(),
		})
		return wrapped
	}
	adapterLocation, err := adapterConstructionLocation(src)
	if err != nil {
		wrapped := fmt.Errorf("resolve adapter location for %q: %w", src.location, err)
		recordSourceLifecycleBestEffort(ctx, ing, src, srcLogger, ingest.SourceLifecycleUpdate{
			State: ingest.SourceLifecycleStartFailed,
			Error: wrapped.Error(),
		})
		return wrapped
	}

	events := make(chan canonical.Event, adapterEventChanSize)
	supervisor := sourceSupervisor{
		ctx:             ctx,
		src:             src,
		ing:             ing,
		lookup:          lookup,
		factory:         factory,
		adapterLocation: adapterLocation,
		logger:          srcLogger,
		events:          events,
	}
	adapter, err := supervisor.constructAdapter()
	if err != nil {
		wrapped := fmt.Errorf("construct adapter: %w", err)
		recordSourceLifecycleBestEffort(ctx, ing, src, srcLogger, ingest.SourceLifecycleUpdate{
			State: ingest.SourceLifecycleConstructFailed,
			Error: wrapped.Error(),
		})
		return wrapped
	}

	since := loadSourceCursor(ctx, adapter, lookup, src.id, srcLogger)

	if err := ing.Submit(src.id, events); err != nil {
		wrapped := fmt.Errorf("submit to ingester: %w", err)
		recordSourceLifecycleBestEffort(ctx, ing, src, srcLogger, ingest.SourceLifecycleUpdate{
			State: ingest.SourceLifecycleStartFailed,
			Error: wrapped.Error(),
		})
		return wrapped
	}

	restartRequests := make(chan struct{}, 1)
	repairRequests := make(chan struct{}, 1)
	unregisterRestart := func() {}
	unregisterRepair := func() {}
	if ing != nil {
		unregisterRestart = ing.RegisterTailRestart(src.id, restartRequests)
		unregisterRepair = ing.RegisterSourceReadModelRepair(src.id, repairRequests)
	}
	supervisor.restartRequests = restartRequests
	supervisor.repairRequests = repairRequests
	supervisor.unregisterTail = unregisterRestart
	supervisor.unregisterRepair = unregisterRepair

	wg.Add(1)
	scanStarted = true
	// NOTE: scanWG.Add was already called by main.go (scanWG.Add(len(sources)))
	// BEFORE the scanWG.Wait goroutine started — required by sync.WaitGroup
	// semantics (Add must happen before Wait). Do NOT Add here.
	go func() {
		defer wg.Done()
		defer close(events)
		supervisor.run(adapter, since, scanWG.Done)
	}()

	srcLogger.Info("ai-viewer-ingest: source started")
	return nil
}

func adapterConstructionLocation(src configuredSource) (string, error) {
	if src.format != "opencode" || filepath.IsAbs(src.location) {
		return src.location, nil
	}
	abs, err := filepath.Abs(src.location)
	if err != nil {
		return "", err
	}
	return abs, nil
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

// runAdapter is the legacy scan-then-tail helper kept for the focused
// background-backfill regression test. Production source lifecycle, durable
// state, restarts, and read-model repair are owned by sourceSupervisor.
//
// Tail starts immediately after this source's Scan outcome. The global
// startup read-model reconciliation is background repair and must not gate
// realtime canonical ingestion for this source.
func runAdapter(ctx context.Context, adapter canonical.Adapter, since canonical.Cursor, events chan<- canonical.Event, logger *slog.Logger, scanDone func()) {
	logger.Info("ai-viewer-ingest: adapter scan starting", "resume", since != nil)
	if err := adapter.Scan(ctx, since, events); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Info("ai-viewer-ingest: adapter scan cancelled")
			scanDone()
			return
		}
		logger.Error("ai-viewer-ingest: adapter scan failed", "err", err)
		// Fall through to Tail anyway — partial backfill is better than
		// no realtime data.
	} else {
		logger.Info("ai-viewer-ingest: adapter scan complete")
	}
	// Scan is done (success or error) — signal the centralized backfill
	// coordinator that this source's Scan phase is complete.
	scanDone()
	logger.Info("ai-viewer-ingest: tail starting")
	if err := adapter.Tail(ctx, events); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logger.Info("ai-viewer-ingest: adapter tail cancelled")
			return
		}
		logger.Error("ai-viewer-ingest: adapter tail failed", "err", err)
	}
}
