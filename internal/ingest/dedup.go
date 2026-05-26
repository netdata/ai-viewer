package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// hwmCache is the per-source high-water-mark cache. The ingester loads
// it at Start() from source_progress.last_seq and updates it atomically
// with every batch commit. Events with SourceSeq <= cached value are
// dropped before any SQL touches the database.
//
// The cache is keyed by sourceID (the canonical Event.SourceID()).
// Concurrent access is safe via sync.RWMutex — workers read on every
// event, writers (one per batch commit) take the write lock briefly.
type hwmCache struct {
	mu  sync.RWMutex
	hwm map[string]uint64
}

// newHWMCache returns an empty cache. Load() populates it from the
// database.
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

// IsAfter reports whether seq is strictly greater than the cached HWM
// for sourceID. Returns true for any sourceID not yet seen (HWM=0).
func (c *hwmCache) IsAfter(sourceID string, seq uint64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return seq > c.hwm[sourceID]
}

// Advance updates the cached HWM for sourceID to max(current, seq).
// Workers call this after a batch commit so dropped events from the
// next batch are caught at IsAfter.
func (c *hwmCache) Advance(sourceID string, seq uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq > c.hwm[sourceID] {
		c.hwm[sourceID] = seq
	}
}

// Get returns the current HWM for sourceID; 0 if not yet seen. Used by
// tests to verify the dedup state after a batch.
func (c *hwmCache) Get(sourceID string) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hwm[sourceID]
}
