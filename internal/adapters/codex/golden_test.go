package codex

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite golden expected.jsonl files for the codex adapter")

// goldenEvent is the wire shape written into expected.jsonl: the kind
// discriminator plus the concrete payload. Resilient to field additions on the
// canonical types — updating with -update-golden picks up new fields. Mirrors
// claude_code/golden_test.go.
type goldenEvent struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// rootPlaceholder replaces the test's absolute root in SourceID and
// PayloadRef.LocationURI strings so golden files are portable across
// workstations and CI AND carry no operator filesystem path. Mirrors
// claude_code.
const rootPlaceholder = "<ROOT>"

// TestGolden runs every scenario directory under testdata/codex/ that contains
// an INPUT/ subtree and asserts Scan produces the canonical events recorded in
// expected.jsonl. Run with -update-golden to refresh. Mirrors claude_code's
// auto-discovering harness; the codex INPUT subtree is a $CODEX_HOME root whose
// sessions/YYYY/MM/DD/rollout-*.jsonl files the adapter walks (the adapter is
// rooted at INPUT/<codex-home>/sessions — see scenarioRoot).
func TestGolden(t *testing.T) {
	t.Parallel()

	base := filepath.Join("..", "..", "..", "testdata", "codex")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir %s: %v", base, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runGoldenScenario(t, filepath.Join(base, name))
		})
	}
}

// scenarioRoot returns the sessions root the adapter is rooted at for a
// scenario: INPUT/<codex-home>/sessions. The fixture lays the rollout files
// under INPUT/<codex-home>/sessions/YYYY/MM/DD/, mirroring a real $CODEX_HOME,
// and the adapter is constructed on the sessions dir (SOW C#3: location =
// $CODEX_HOME/sessions). The single codex-home dir under INPUT/ is discovered by
// name so a scenario need not hard-code it. A scenario whose INPUT has no
// sessions/ subtree falls back to INPUT itself (defensive; the staleness
// scenario still nests under sessions/).
func scenarioRoot(t *testing.T, inputDir string) string {
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

func runGoldenScenario(t *testing.T, scenarioDir string) {
	t.Helper()
	inputDir := filepath.Join(scenarioDir, "INPUT")
	if _, err := os.Stat(inputDir); err != nil {
		t.Skipf("INPUT directory missing: %v", err)
		return
	}

	// The crash/stale scenario needs a stale mtime so the synthetic
	// failed/incomplete finalize fires (rule #23). Golden fixtures cannot carry
	// an mtime in git, so the harness ages every rollout under a scenario whose
	// name marks it stale (the "h_crash_stale" suffix). Aging in the test (not a
	// fixture artifact) keeps the input bytes deterministic while exercising the
	// stale path; a non-stale scenario leaves mtimes fresh so no clean-EOF
	// finalize is asserted (SOW C#3).
	if strings.Contains(filepath.Base(scenarioDir), "crash_stale") {
		ageRolloutsStale(t, inputDir)
	}

	sessionsRoot := scenarioRoot(t, inputDir)
	absRoot, err := filepath.Abs(sessionsRoot)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	absRoot = filepath.Clean(absRoot)

	a, err := New(absRoot, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 8192)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)

	filtered := make([]canonical.Event, 0, len(events))
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); ok {
			continue
		}
		filtered = append(filtered, ev)
	}

	encoded, err := encodeEvents(filtered, absRoot)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	goldenPath := filepath.Join(scenarioDir, "expected.jsonl")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, encoded, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden: %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update-golden to create)", goldenPath, err)
	}
	if string(want) != string(encoded) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
			goldenPath, string(want), string(encoded))
	}
}

// staleMtime is the FIXED mtime the crash/stale scenario's rollout is aged to.
// It must be (a) far enough in the past that "now - mtime >= 1 h" always holds
// (rule #23) and (b) a constant so the synthetic-finalize EndTs (the scanner
// stamps it from the file mtime, mapper_finalize.go) is DETERMINISTIC across
// runs — a wall-clock-relative mtime would make the golden EndTs change every
// run and never match. 2026-04-01T11:00:00Z is two hours after the fixture's
// session start and a fixed absolute instant well over 1 h before any plausible
// test run.
var staleMtime = time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC)

// ageRolloutsStale sets the mtime of every rollout file under inputDir to the
// fixed staleMtime so the scanner's "mtime stale >= 1 h" EOF-finalize path fires
// deterministically (rule #23). Operates on the working-tree copy only; the
// committed bytes are unchanged.
func ageRolloutsStale(t *testing.T, inputDir string) {
	t.Helper()
	stale := staleMtime
	_ = filepath.WalkDir(inputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // walk best-effort; a stat error just leaves mtime fresh
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if cErr := os.Chtimes(path, stale, stale); cErr != nil {
			t.Fatalf("chtimes %s: %v", path, cErr)
		}
		return nil
	})
}

// encodeEvents serialises events one goldenEvent per line, with the absolute
// test-machine root replaced by rootPlaceholder so golden files are portable
// AND carry no operator filesystem path (sensitive-data hygiene). Two fields
// embed the root: SourceID ("codex:<root>") and PayloadRef.LocationURI
// ("file://<resolved-root>/..."). The latter is symlink-resolved by the adapter,
// so the resolved form of the root is also rewritten. Mirrors claude_code.
func encodeEvents(events []canonical.Event, absRoot string) ([]byte, error) {
	resolvedRoot := absRoot
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		resolvedRoot = r
	}
	var b strings.Builder
	for _, ev := range events {
		payload, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("marshal %T: %w", ev, err)
		}
		s := string(payload)
		// Rewrite every embedding of the absolute root to the portable
		// placeholder: SourceID's "codex:<root>" prefix and any LocationURI
		// "file://<root>" (raw or symlink-resolved).
		s = strings.ReplaceAll(s, sourceIDPrefix+absRoot, sourceIDPrefix+rootPlaceholder)
		s = strings.ReplaceAll(s, "file://"+filepath.ToSlash(resolvedRoot), "file://"+rootPlaceholder)
		s = strings.ReplaceAll(s, "file://"+filepath.ToSlash(absRoot), "file://"+rootPlaceholder)

		ge := goldenEvent{Kind: string(ev.EventKind()), Payload: json.RawMessage(s)}
		enc, err := json.Marshal(ge)
		if err != nil {
			return nil, err
		}
		b.Write(enc)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}
