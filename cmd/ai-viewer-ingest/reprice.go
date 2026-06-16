package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/pricing"
	"github.com/netdata/ai-viewer/internal/store"
)

// runReprice implements the `reprice` subcommand: a one-shot re-pricing of all
// LLM ops whose cost_usd is 0 but that carry tokens (the pricing table was
// empty or missing the model at ingest time). It opens the store read-write,
// loads the embedded pricing table, re-prices each qualifying op, and cascades
// the cost into turns/sessions aggregates. Idempotent: re-running does nothing
// when all ops are already priced.
func runReprice(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("ai-viewer-ingest reprice", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "SQLite path (default ~/.local/share/ai-viewer/index.db)")
	logLevel := fs.String("log-level", "info", "log level (debug|info|warn|error)")
	logFormat := fs.String("log-format", "json", "log format (json|text)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: ai-viewer-ingest reprice [flags]\n\n"+
			"Re-prices LLM ops whose cost_usd is 0 using the current embedded pricing\n"+
			"table. Cascades cost into turns/sessions. Idempotent.\n\n"+
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
		logger.Error("reprice: failed to resolve --db", "err", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ws, err := store.OpenWriter(ctx, resolvedDB, logger)
	if err != nil {
		logger.Error("reprice: failed to open store", "db", resolvedDB, "err", err)
		return 1
	}
	defer func() { _ = ws.Close() }()

	pricer, err := pricing.New()
	if err != nil {
		logger.Error("reprice: failed to load pricing table", "err", err)
		return 1
	}

	logger.Info("reprice starting", "db", resolvedDB)
	stats, err := ingest.BackfillPricing(ctx, ws.DB(), pricer, logger)
	if err != nil {
		logger.Error("reprice: failed", "db", resolvedDB, "err", err)
		return 1
	}
	logger.Info("reprice complete",
		"ops_priced", stats.OpsPriced,
		"ops_skipped", stats.OpsSkipped,
		"total_cost_added", fmt.Sprintf("%.4f", stats.TotalCostAdded),
		"elapsed", stats.Elapsed.String(),
	)
	return 0
}
