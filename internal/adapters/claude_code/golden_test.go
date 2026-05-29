package claude_code

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite golden expected.jsonl files for the claude-code adapter")

// goldenEvent is the wire shape written into expected.jsonl: the kind
// discriminator plus the concrete payload. Resilient to field additions on
// the canonical types — updating with -update-golden picks up new fields.
type goldenEvent struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// rootPlaceholder replaces the test's absolute root in SourceID strings so
// golden files are portable across workstations and CI.
const rootPlaceholder = "<ROOT>"

// TestGolden runs every scenario directory under testdata/claude_code/ that
// contains an INPUT/ subtree and asserts Scan produces the canonical events
// recorded in expected.jsonl. Run with -update-golden to refresh.
func TestGolden(t *testing.T) {
	t.Parallel()

	base := filepath.Join("..", "..", "..", "testdata", "claude_code")
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

func runGoldenScenario(t *testing.T, scenarioDir string) {
	t.Helper()
	inputDir := filepath.Join(scenarioDir, "INPUT")
	if _, err := os.Stat(inputDir); err != nil {
		t.Skipf("INPUT directory missing: %v", err)
		return
	}
	absRoot, err := filepath.Abs(inputDir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	absRoot = filepath.Clean(absRoot)

	a, err := New(absRoot, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 4096)
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

// encodeEvents serialises events one goldenEvent per line, with the
// absolute test-machine root replaced by rootPlaceholder so golden files
// are portable. SourceID (which embeds the root) is rewritten.
func encodeEvents(events []canonical.Event, absRoot string) ([]byte, error) {
	var b strings.Builder
	for _, ev := range events {
		payload, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("marshal %T: %w", ev, err)
		}
		s := string(payload)
		s = strings.ReplaceAll(s, sourceIDPrefix+absRoot, sourceIDPrefix+rootPlaceholder)

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
