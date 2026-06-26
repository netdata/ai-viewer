package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/netdata/ai-viewer/internal/ingest"
)

// runBackfill implements the `rollups-backfill` subcommand: a one-shot
// recompute of rollup_hourly + rollup_daily from the existing ops/sessions
// rows. It opens the store read-write, calls ingest.BackfillRollups with the
// current wall-clock cutoff, and returns 0 on success / 1 on any error (always
// logged with full context — no silent failure).
//
// It deliberately reuses resolveDBPath + newLogger from main.go so the flag
// surface stays consistent with the daemon path.
func runBackfill(ctx context.Context, args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("ai-viewer-ingest rollups-backfill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite path (default ~/.local/share/ai-viewer/index.db)")
	stateDir := fs.String("state-dir", "", "state directory (default ~/.local/share/ai-viewer)")
	logLevel := fs.String("log-level", "info", "log level (debug|info|warn|error)")
	logFormat := fs.String("log-format", "json", "log format (json|text)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: ai-viewer-ingest rollups-backfill [flags]\n\n"+
			"Recomputes the time-bucketed rollup tables (rollup_hourly, rollup_daily)\n"+
			"from the existing ops/sessions rows. Idempotent and re-runnable.\n\n"+
			"Flags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	logger, err := newLogger(*logLevel, *logFormat, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest: %v\n", err)
		return 2
	}

	resolvedDB, err := resolveDBPath(*dbPath)
	if err != nil {
		logger.Error("rollups-backfill: failed to resolve --db", "err", err)
		return 1
	}

	releaseLock, resolvedStateDir, err := acquireOneShotDaemonLock(*stateDir, logger)
	if err != nil {
		logger.Error("rollups-backfill: daemon lock unavailable",
			"state_dir", resolvedStateDir,
			"err", err,
			"hint", "stop ai-viewer-ingest.service or pass the matching --state-dir for an offline database")
		return 1
	}
	defer releaseLock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ws, err := openWriterStore(ctx, resolvedDB, logger)
	if err != nil {
		logger.Error("rollups-backfill: failed to open store", "db", resolvedDB, "err", err)
		return 1
	}
	defer func() { _ = ws.Close() }()

	logger.Info("rollups-backfill starting", "db", resolvedDB)
	stats, err := ingest.BackfillRollups(ctx, ws.DB(), time.Now().UTC().UnixMicro(), logger)
	if err != nil {
		logger.Error("rollups-backfill: failed", "db", resolvedDB, "err", err)
		return 1
	}
	logger.Info(
		"rollups-backfill complete",
		"hourly_rows", stats.HourlyRows,
		"daily_rows", stats.DailyRows,
		"days_processed", stats.DaysProcessed,
		"elapsed", stats.Elapsed.String(),
	)

	// The one-shot backfill also rebuilds the FTS5 search index from scratch
	// (ingester.md §"One-shot backfill"). fts_ops covers every op; fts_logs
	// covers only fts5_index_logs=1 sources' session-scoped logs.
	ftsStats, err := ingest.BackfillFTS(ctx, ws.DB(), logger)
	if err != nil {
		logger.Error("fts-backfill: failed", "db", resolvedDB, "err", err)
		return 1
	}
	logger.Info(
		"fts-backfill complete",
		"fts_ops_rows", ftsStats.OpRows,
		"fts_logs_rows", ftsStats.LogRows,
		"elapsed", ftsStats.Elapsed.String(),
	)
	return 0
}
