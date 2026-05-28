package ingest

import (
	"context"
	"testing"
)

func TestHWMCache_LoadEmpty(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	c := newHWMCache()
	if err := c.Load(context.Background(), db); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Get("missing"); got != 0 {
		t.Errorf("Get(missing) = %d, want 0", got)
	}
}

func TestHWMCache_LoadPersisted(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('src-a', 'f', 'l', 0), ('src-b', 'f', 'l', 0)`); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO source_progress (source_id, last_seq, last_ts_us, updated_at) VALUES ('src-a', 100, 0, 0), ('src-b', 250, 0, 0)`); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}
	c := newHWMCache()
	if err := c.Load(ctx, db); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Get("src-a"); got != 100 {
		t.Errorf("Get(src-a) = %d, want 100", got)
	}
	if got := c.Get("src-b"); got != 250 {
		t.Errorf("Get(src-b) = %d, want 250", got)
	}
}

func TestHWMCache_LoadNegativeClamped(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('src', 'f', 'l', 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO source_progress (source_id, last_seq, last_ts_us, updated_at) VALUES ('src', -5, 0, 0)`); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}
	c := newHWMCache()
	if err := c.Load(ctx, db); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Get("src"); got != 0 {
		t.Errorf("Get(negative-row) = %d, want 0", got)
	}
}

func TestHWMCache_AdvanceMonotonic(t *testing.T) {
	t.Parallel()
	c := newHWMCache()
	c.Advance("src", 100)
	c.Advance("src", 50) // regression — must be ignored
	if got := c.Get("src"); got != 100 {
		t.Errorf("regression accepted: Get = %d, want 100", got)
	}
	c.Advance("src", 200)
	if got := c.Get("src"); got != 200 {
		t.Errorf("Get after advance = %d, want 200", got)
	}
}
