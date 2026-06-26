package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestE2E_AllFixtures sweeps every v3 testdata scenario through the
// ingester so each event-kind code path runs against real adapter
// output. The set deliberately mirrors the v3 adapter's golden_test
// scenarios so coverage stays in lockstep with adapter coverage.
func TestE2E_AllFixtures(t *testing.T) {
	t.Parallel()
	base := filepath.Join("..", "..", "testdata", "aiagent_v3")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixtureDir, err := filepath.Abs(filepath.Join(base, name, "INPUT"))
			if err != nil {
				t.Fatalf("abs: %v", err)
			}
			if _, err := os.Stat(fixtureDir); err != nil {
				t.Skipf("INPUT missing: %v", err)
				return
			}
			a, err := aiagent_v3.New(fixtureDir, canonical.AdapterOptions{Logger: silentLogger()})
			if err != nil {
				t.Fatalf("aiagent_v3.New: %v", err)
			}
			_, db := openTestStore(t)
			sourceID := "aiagent_v3:" + fixtureDir
			ing, err := New(
				db,
				WithLogger(silentLogger()),
				WithBatchSize(1),
				WithBatchInterval(50*time.Millisecond),
				WithSourceFormat(sourceID, "aiagent_v3"),
				WithLocation(sourceID, fixtureDir),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx := context.Background()
			if err := ing.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			events := make(chan canonical.Event)
			scanDone := make(chan struct{})
			var scanErr error
			go func() {
				defer close(scanDone)
				defer close(events)
				scanErr = a.Scan(ctx, nil, events)
			}()
			if err := ing.Submit(sourceID, events); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			waitForScan(t, scanDone, "aiagent_v3 "+name)
			if scanErr != nil {
				t.Fatalf("Scan: %v", scanErr)
			}
			if !waitFor(20*time.Second, func() bool {
				return scanInt(t, db, `SELECT COUNT(*) FROM sessions`) >= 1
			}) {
				t.Fatalf("session count before Stop = %d, want >=1", scanInt(t, db, `SELECT COUNT(*) FROM sessions`))
			}
			if err := ing.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if err := ing.ResolveOrphans(ctx); err != nil {
				t.Fatalf("ResolveOrphans: %v", err)
			}
			// Every fixture writes at least one session.
			if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions`); got < 1 {
				t.Errorf("session count = %d, want >=1", got)
			}
			// source_progress row exists for the source.
			if got := scanInt(t, db, `SELECT COUNT(*) FROM source_progress`); got < 1 {
				t.Errorf("source_progress rows = %d, want >=1", got)
			}
		})
	}
}
