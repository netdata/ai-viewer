package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/store"
)

// runBackfillFTSContent implements the `fts-content-backfill` subcommand:
// a one-shot rebuild of the fts_content FTS5 index that backs prompt/
// response full-text search (SOW-0091).
//
// The backfill reads every op's primary payload file, runs
// extract.ReadableText on the first ~32 KB, and INSERTs into fts_content.
// Idempotent and re-runnable — wipes fts_content before streaming.
//
// The subcommand deliberately reuses resolveDBPath + newLogger from
// main.go so the flag surface stays consistent with the daemon path.
func runBackfillFTSContent(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("ai-viewer-ingest fts-content-backfill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite path (default ~/.local/share/ai-viewer/index.db)")
	logLevel := fs.String("log-level", "info", "log level (debug|info|warn|error)")
	logFormat := fs.String("log-format", "json", "log format (json|text)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: ai-viewer-ingest fts-content-backfill [flags]\n\n"+
			"Rebuilds the fts_content full-text index for prompt/response search\n"+
			"(SOW-0091). Reads each op's primary payload file, runs\n"+
			"extract.ReadableText on the first ~32 KB, and INSERTs into\n"+
			"fts_content. Idempotent and re-runnable.\n\n"+
			"Run separately from rollups-backfill / fts-backfill (which rebuild\n"+
			"fts_ops + fts_logs).\n\n"+
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
		logger.Error("fts-content-backfill: failed to resolve --db", "err", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open the store read-write so we can DELETE from fts_content + bulk
	// INSERT. The OpenWriter path pins SetMaxOpenConns(1) — single-writer
	// discipline required by the FTS backfill.
	ws, err := store.OpenWriter(ctx, resolvedDB, logger)
	if err != nil {
		logger.Error("fts-content-backfill: failed to open store", "db", resolvedDB, "err", err)
		return 1
	}
	defer func() { _ = ws.Close() }()

	stats, err := ingest.BackfillFTSContent(ctx, ws.DB(), logger)
	if err != nil {
		logger.Error("fts-content-backfill: failed", "err", err)
		return 1
	}
	logger.Info("fts-content-backfill: complete",
		"indexed_rows", stats.IndexedRows,
		"empty_rows", stats.EmptyRows,
		"error_rows", stats.ErrorRows,
		"elapsed_s", stats.Elapsed.Seconds(),
	)
	return 0
}

// keep the slog import alive even when newLogger evolves
var _ = slog.LevelInfo
