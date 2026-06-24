package opencode

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// ScanSessionsOptions configures the diagnostic selected-session scan.
type ScanSessionsOptions struct {
	Logger  *slog.Logger
	OnError func(error)
}

// ScanSessions maps only the requested native session ids through the same
// read-only full-session-tree load and mapper used by Scan.
func ScanSessions(ctx context.Context, dbPath, sourceID string, sessionIDs []string, out chan<- canonical.Event, opts ScanSessionsOptions) error {
	logger := orDefaultLogger(opts.Logger)
	onError := orNoop(opts.OnError)
	selected := uniqueSessionIDs(sessionIDs)
	if len(selected) == 0 {
		return nil
	}

	db, err := openReadOnly(ctx, dbPath, withMaxOpenConns(2))
	if err != nil {
		return fmt.Errorf("opencode: scan selected sessions open %s (ro): %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()

	schema, err := introspectAll(ctx, db)
	if err != nil {
		return fmt.Errorf("opencode: scan selected sessions introspect %s: %w", dbPath, err)
	}
	logMissingColumns(logger, schema)

	return reloadAndEmit(ctx, db, schema, sourceID, selected, out, logger, onError)
}

func uniqueSessionIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
