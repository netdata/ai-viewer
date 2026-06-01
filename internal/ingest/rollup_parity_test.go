package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v2"
	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	"github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/adapters/codex"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// parityNow is the fixed wall-clock both rollup engines read (UTC µs). It sits
// in the year 2050 — far beyond every committed fixture's timestamps (which run
// 2025-11 .. 2026-05). With now this far in the future EVERY fixture bucket is
// closed, so BOTH the incremental refresh and BackfillRollups materialize the
// full dataset → maximal column/dimension coverage and no open-bucket asymmetry
// to mask a divergence. The test asserts MAX(ops.start_ts)+2d < parityNow so a
// future fixture with a later timestamp fails loudly here (bump this constant)
// rather than silently shrinking coverage.
var parityNow = time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()

// dirAdapterFactory builds a registered adapter over a fixture INPUT directory.
// The four directory-based adapters share the canonical.AdapterOptions signature
// (opencode is excluded — its INPUT is a fixture.sql that only its own package's
// unexported builder can materialize; the parity property is format-agnostic, so
// four formats over the full fixture matrix is strictly sufficient coverage).
//
// root maps a scenario's INPUT directory to the path the adapter scans. Most
// adapters scan INPUT directly (the fixtures place their tree there); codex
// nests a CLI home so the root is INPUT/<home>/sessions, exactly as the codex
// adapter's own golden harness resolves it.
type dirAdapterFactory struct {
	format string                                       // canonical source_format, e.g. "claude_code".
	subdir string                                       // testdata/<subdir> holding the scenario directories.
	root   func(t *testing.T, inputDir string) string   // INPUT → scan root.
	build  func(root string) (canonical.Adapter, error) // construct over the scan root.
}

// inputAsRoot is the identity root resolver (adapter scans INPUT directly).
func inputAsRoot(_ *testing.T, inputDir string) string { return inputDir }

// codexSessionsRoot resolves INPUT/<home>/sessions, mirroring the codex golden
// harness's scenarioRoot: codex fixtures nest a CLI home (e.g. "codex-home")
// whose "sessions" subdirectory is the actual scan root. Falls back to INPUT.
func codexSessionsRoot(t *testing.T, inputDir string) string {
	t.Helper()
	homes, err := os.ReadDir(inputDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", inputDir, err)
	}
	for _, h := range homes {
		if !h.IsDir() {
			continue
		}
		sessions := filepath.Join(inputDir, h.Name(), "sessions")
		if fi, sErr := os.Stat(sessions); sErr == nil && fi.IsDir() {
			return sessions
		}
	}
	return inputDir
}

func parityAdapters() []dirAdapterFactory {
	opts := canonical.AdapterOptions{Logger: silentLogger()}
	return []dirAdapterFactory{
		{"claude_code", "claude_code", inputAsRoot, func(d string) (canonical.Adapter, error) { return claude_code.New(d, opts) }},
		{"codex", "codex", codexSessionsRoot, func(d string) (canonical.Adapter, error) { return codex.New(d, opts) }},
		{"aiagent_v2", "aiagent_v2", inputAsRoot, func(d string) (canonical.Adapter, error) { return aiagent_v2.New(d, opts) }},
		{"aiagent_v3", "aiagent_v3", inputAsRoot, func(d string) (canonical.Adapter, error) { return aiagent_v3.New(d, opts) }},
	}
}

// TestRollupParity_AllFixtures is the rollup-correctness diff gate (SOW-0007
// acceptance #2, quality-gates.md §"Rollup correctness diff"). It proves that
// the INCREMENTAL rollup refresh (run inside each batch transaction during
// ingestion) and the one-shot BackfillRollups (recompute from scratch) produce
// BYTE-IDENTICAL rollup_hourly + rollup_daily over the SAME data and the SAME
// now. cost_usd is compared for EXACT float equality (no epsilon): the whole
// point of the deterministic ORDER BY start_ts, id fold is that both engines sum
// to the same bits; an epsilon would hide the very divergence this gate exists
// to catch.
func TestRollupParity_AllFixtures(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	now := func() int64 { return parityNow }

	// 1+2. Ingest every directory-based fixture into ONE store. The ingester
	// propagates now to each per-source writer, so the incremental refresh that
	// runs during ingestion uses parityNow for its open-bucket cutoffs.
	ingested := 0
	formats := make(map[string]struct{})
	srcIDsByFormat := make(map[string]map[string]struct{})
	for _, fa := range parityAdapters() {
		base := filepath.Join("..", "..", "testdata", fa.subdir)
		entries, err := os.ReadDir(base)
		if err != nil {
			t.Fatalf("readdir %s: %v", base, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			inputDir, err := filepath.Abs(filepath.Join(base, e.Name(), "INPUT"))
			if err != nil {
				t.Fatalf("abs: %v", err)
			}
			if _, err := os.Stat(inputDir); err != nil {
				continue // not every layout uses INPUT/ (e.g. opencode fixture.sql).
			}
			// Each fixture is ingested under its NATURAL adapter source_format
			// (claude_code, codex, aiagent_v2, aiagent_v3) with a DISTINCT
			// source_id per scenario (the scan root). So every adapter that owns
			// >1 fixture puts >=2 distinct source_ids under one shared
			// source_format — exercising the multi-source-same-format aggregation
			// the rollup PK is keyed for (the rollup cell is (bucket,
			// source_format, dim, val), and BackfillRollups folds ops by
			// src.format). This is the documented two-`--source` config; using a
			// SYNTHETIC unique-per-fixture format here would silently DODGE that
			// path (it would only ever see one source_id per format), so the gate
			// would never prove the incremental refresh aggregates siblings the
			// same way the backfill does.
			sourceFormat := fa.format
			sourceID := ingestParityFixture(t, db, now, sourceFormat, fa, e.Name(), inputDir)
			ingested++
			formats[fa.format] = struct{}{}
			if srcIDsByFormat[sourceFormat] == nil {
				srcIDsByFormat[sourceFormat] = make(map[string]struct{})
			}
			srcIDsByFormat[sourceFormat][sourceID] = struct{}{}
		}
	}
	if ingested < 4 || len(formats) < 2 {
		t.Fatalf("ingested %d fixtures across %d formats; want >=4 fixtures and >=2 formats", ingested, len(formats))
	}

	// Guard: at least one source_format must carry >=2 distinct source_ids, so
	// the byte-diff below ACTUALLY exercises multi-source-same-format
	// aggregation (the bug class this gate must catch). Without this, a future
	// fixture-set reshuffle could leave every format with a single source and
	// silently revert the gate to the dodged 1:1 path.
	maxSrcIDs := 0
	for _, ids := range srcIDsByFormat {
		if len(ids) > maxSrcIDs {
			maxSrcIDs = len(ids)
		}
	}
	if maxSrcIDs < 2 {
		t.Fatalf("no source_format carries >=2 distinct source_ids (max=%d); the gate is not exercising multi-source-same-format aggregation", maxSrcIDs)
	}

	// 3. Snapshot the incrementally-built tables (fully drained into maps before
	// any further query — the writer store pins a single connection).
	incHourly := readRollups(t, db, "rollup_hourly")
	incDaily := readRollups(t, db, "rollup_daily")

	// 8. Guard against a vacuous all-empty-vs-all-empty pass.
	assertNonVacuous(t, incHourly, incDaily)

	// Guard: parityNow must dominate every op so both engines see a fully-closed
	// dataset. If a future fixture pushes start_ts past parityNow this fails
	// loudly (bump parityNow) instead of silently dropping the late buckets.
	maxStart := scanInt(t, db, `SELECT IFNULL(MAX(start_ts),0) FROM ops`)
	if maxStart+2*daySpanUS >= parityNow {
		t.Fatalf("parityNow %d is not far enough in the future: MAX(ops.start_ts)=%d; bump parityNow", parityNow, maxStart)
	}

	// 4. Wipe both rollup tables.
	if _, err := db.ExecContext(context.Background(), `DELETE FROM rollup_hourly`); err != nil {
		t.Fatalf("wipe rollup_hourly: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM rollup_daily`); err != nil {
		t.Fatalf("wipe rollup_daily: %v", err)
	}

	// 5. Recompute from scratch with the SAME now.
	stats, err := BackfillRollups(context.Background(), db, parityNow, silentLogger())
	if err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
	if stats.HourlyRows == 0 || stats.DailyRows == 0 {
		t.Fatalf("backfill produced zero rows: %+v", stats)
	}

	// 6. Snapshot the backfilled tables.
	bfHourly := readRollups(t, db, "rollup_hourly")
	bfDaily := readRollups(t, db, "rollup_daily")

	// 7. Assert byte-identical.
	diffRollups(t, "rollup_hourly", incHourly, bfHourly)
	diffRollups(t, "rollup_daily", incDaily, bfDaily)

	t.Logf("rollup parity OK: %d fixtures across %d formats; hourly=%d daily=%d rows (incremental == backfill, byte-identical)",
		ingested, len(formats), len(incHourly), len(incDaily))
}

// ingestParityFixture drives one fixture through a fresh ingester into the
// SHARED db, mirroring the completion-wait of TestE2E_SubAgentFixture: Start,
// run the adapter's Scan into a channel, wait for Scan to FINISH (deterministic,
// not a session-count poll), then Stop — which cancels the worker and blocks on
// its final flush, so the batch is durably committed before this returns. now is
// injected so the incremental refresh applies parityNow's open-bucket cutoffs.
//
// Each fixture uses its own ingester (one worker per source ID) writing to the
// same store, exactly as a multi-source deployment would. Fixtures that produce
// zero sessions (e.g. the deliberately empty aiagent_v2/zero_byte) are tolerated:
// they add no rollup rows and the test asserts non-vacuous coverage globally.
func ingestParityFixture(t *testing.T, db *sql.DB, now func() int64, sourceFormat string, fa dirAdapterFactory, name, inputDir string) string {
	t.Helper()
	root := fa.root(t, inputDir)
	a, err := fa.build(root)
	if err != nil {
		t.Fatalf("%s/%s: build adapter: %v", fa.format, name, err)
	}
	// sourceID is unique per scenario (the scan root); sourceFormat is the
	// NATURAL adapter format, SHARED across that adapter's fixtures. So one
	// format carries multiple distinct source_ids — the documented two-`--source`
	// config — and the WithSourceFormat override pins sources.format to the
	// natural format on every rollup row. The returned sourceID lets the caller
	// count distinct source_ids per format (the multi-source coverage guard).
	sourceID := sourceFormat + ":" + root
	ing, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2000),
		WithBatchInterval(50*time.Millisecond),
		WithNow(now),
		WithSourceFormat(sourceID, sourceFormat),
		WithLocation(sourceID, inputDir),
	)
	if err != nil {
		t.Fatalf("%s/%s: New ingester: %v", fa.format, name, err)
	}
	ctx := context.Background()
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("%s/%s: Start: %v", fa.format, name, err)
	}

	events := make(chan canonical.Event, 256)
	var scanErr error
	scanDone := make(chan struct{})
	go func() {
		defer close(events)
		defer close(scanDone)
		scanErr = a.Scan(ctx, nil, events)
	}()
	if err := ing.Submit(sourceID, events); err != nil {
		t.Fatalf("%s/%s: Submit: %v", fa.format, name, err)
	}

	// Wait for Scan to complete, then Stop (blocks on the worker's final flush).
	select {
	case <-scanDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s/%s: Scan did not finish within 10s", fa.format, name)
	}
	if err := ing.Stop(); err != nil {
		t.Fatalf("%s/%s: Stop: %v", fa.format, name, err)
	}
	if scanErr != nil {
		t.Fatalf("%s/%s: Scan: %v", fa.format, name, scanErr)
	}
	if err := ing.ResolveOrphans(ctx); err != nil {
		t.Fatalf("%s/%s: ResolveOrphans: %v", fa.format, name, err)
	}
	return sourceID
}

// assertNonVacuous fails if the incremental snapshots are empty (the fixtures
// produce ops, so rollup_hourly MUST have rows) or carry no daily row or no
// non-total dimension row — any of which would make the byte-diff a meaningless
// empty-vs-empty comparison.
func assertNonVacuous(t *testing.T, hourly, daily map[rollupKey]rollupVals) {
	t.Helper()
	if len(hourly) == 0 {
		t.Fatal("rollup_hourly is empty after ingestion — fixtures must produce ops; vacuous parity check")
	}
	if len(daily) == 0 {
		t.Fatal("rollup_daily is empty after ingestion — closed days must produce daily rows; vacuous parity check")
	}
	hasNonTotal := false
	for k := range hourly {
		if k.dimension != "total" {
			hasNonTotal = true
			break
		}
	}
	if !hasNonTotal {
		t.Fatal("rollup_hourly has only 'total' rows — fixtures span models/tools/agents/cwds; expected non-total dimensions")
	}
}

// diffRollups asserts two rollup snapshots are byte-identical: same row set, and
// for every PK every column equal (cost_usd by EXACT float equality, no epsilon).
// On mismatch it reports the precise PK, column, and the two differing values so
// the orchestrator can pin a real Chunk-4/5 parity bug.
func diffRollups(t *testing.T, table string, incremental, backfill map[rollupKey]rollupVals) {
	t.Helper()
	if len(incremental) != len(backfill) {
		reportMissingKeys(t, table, incremental, backfill)
		t.Fatalf("%s row count: incremental=%d backfill=%d", table, len(incremental), len(backfill))
	}
	mismatches := 0
	for _, k := range sortedKeys(incremental) {
		inc := incremental[k]
		bf, ok := backfill[k]
		if !ok {
			t.Errorf("%s PK %s: present in incremental, absent in backfill", table, fmtKey(k))
			mismatches++
			continue
		}
		for _, d := range diffCols(inc, bf) {
			t.Errorf("%s PK %s column %s: incremental=%s backfill=%s", table, fmtKey(k), d.col, d.inc, d.bf)
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Fatalf("%s: %d byte-diff mismatch(es) between incremental and backfill", table, mismatches)
	}
}

// reportMissingKeys logs PKs present in one snapshot but not the other, to make
// a row-count divergence actionable (which buckets/dimensions diverged).
func reportMissingKeys(t *testing.T, table string, incremental, backfill map[rollupKey]rollupVals) {
	t.Helper()
	for _, k := range sortedKeys(incremental) {
		if _, ok := backfill[k]; !ok {
			t.Errorf("%s PK %s: in incremental only", table, fmtKey(k))
		}
	}
	for _, k := range sortedKeys(backfill) {
		if _, ok := incremental[k]; !ok {
			t.Errorf("%s PK %s: in backfill only", table, fmtKey(k))
		}
	}
}

// colDiff is one differing column for the failure message.
type colDiff struct{ col, inc, bf string }

// diffCols returns every column whose value differs between two rows. cost_usd
// uses %v (exact) — any ULP difference is a real divergence and must surface.
func diffCols(inc, bf rollupVals) []colDiff {
	var out []colDiff
	add := func(col string, a, b any) {
		if a != b {
			out = append(out, colDiff{col, fmt.Sprintf("%v", a), fmt.Sprintf("%v", b)})
		}
	}
	add("op_count", inc.opCount, bf.opCount)
	add("tokens_in", inc.tokensIn, bf.tokensIn)
	add("tokens_out", inc.tokensOut, bf.tokensOut)
	add("tokens_cache_read", inc.tokensCacheRead, bf.tokensCacheRead)
	add("tokens_cache_write", inc.tokensCacheWr, bf.tokensCacheWr)
	add("cost_usd", inc.costUSD, bf.costUSD)
	add("failures", inc.failures, bf.failures)
	add("duration_us", inc.durationUS, bf.durationUS)
	add("session_starts", inc.sessionStarts, bf.sessionStarts)
	return out
}

// sortedKeys returns a snapshot's PKs in the canonical rollup order so failure
// output is deterministic.
func sortedKeys(m map[rollupKey]rollupVals) []rollupKey {
	keys := make([]rollupKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.bucketTS != b.bucketTS {
			return a.bucketTS < b.bucketTS
		}
		if a.sourceFormat != b.sourceFormat {
			return a.sourceFormat < b.sourceFormat
		}
		if a.dimension != b.dimension {
			return a.dimension < b.dimension
		}
		return a.dimensionValue < b.dimensionValue
	})
	return keys
}

// fmtKey renders a rollupKey for failure messages.
func fmtKey(k rollupKey) string {
	return fmt.Sprintf("(bucket=%d format=%s dim=%s val=%q)", k.bucketTS, k.sourceFormat, k.dimension, k.dimensionValue)
}
