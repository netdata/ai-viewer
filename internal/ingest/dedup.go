package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// hwmCache is the per-source max-SourceSeq observability counter. The
// ingester loads it at Start() from source_progress.last_seq and
// advances it atomically with every batch commit so the running maximum
// surfaces via /api/health.
//
// It is NOT a dedup gate. A per-source scalar high-water-mark cannot
// dedup events because one sourceID aggregates many independently
// sequenced files (SourceSeq is per-file, not per-source); using it to
// drop events silently lost valid rows and orphaned FK children
// (SOW-0015). Resume-skipping is the adapter cursor's job; event-level
// idempotency is a SQL-layer guarantee. See ingester.md §Dedup and
// Idempotency.
//
// The cache is keyed by sourceID (the canonical Event.SourceID()).
// Concurrent access is safe via sync.RWMutex — Advance takes the write
// lock briefly per batch commit; Get reads it for observability.
type hwmCache struct {
	mu  sync.RWMutex
	hwm map[string]uint64
}

// newHWMCache returns an empty observability counter cache. Load()
// populates it from the database.
func newHWMCache() *hwmCache {
	return &hwmCache{hwm: make(map[string]uint64)}
}

// Load reads source_progress and seeds the cache. Called once at
// Ingester.Start. Missing rows are treated as HWM=0 (the ingester
// inserts a fresh source_progress row on first flush).
func (c *hwmCache) Load(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT source_id, last_seq FROM source_progress`)
	if err != nil {
		return fmt.Errorf("load source_progress: %w", err)
	}
	defer func() { _ = rows.Close() }()

	c.mu.Lock()
	defer c.mu.Unlock()
	for rows.Next() {
		var (
			id  string
			seq int64
		)
		if scanErr := rows.Scan(&id, &seq); scanErr != nil {
			return fmt.Errorf("scan source_progress row: %w", scanErr)
		}
		if seq < 0 {
			// Defensive: the column is INTEGER but unsigned conversion
			// from a negative value would wrap. Treat negatives as zero
			// so the worker can re-ingest from the start of the source.
			seq = 0
		}
		c.hwm[id] = uint64(seq)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate source_progress: %w", err)
	}
	return nil
}

// Advance updates the cached max-seq counter for sourceID to
// max(current, seq). Workers call this after a batch commit so the
// counter mirrors source_progress.last_seq for observability.
func (c *hwmCache) Advance(sourceID string, seq uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq > c.hwm[sourceID] {
		c.hwm[sourceID] = seq
	}
}

// Get returns the current max-seq counter for sourceID; 0 if not yet
// seen. Surfaced via Ingester.HWM for /api/health and used by tests to
// verify the counter advanced after a batch.
func (c *hwmCache) Get(sourceID string) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hwm[sourceID]
}
