package aiagent_v3

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

var updateGolden = flag.Bool("update-golden", false, "rewrite golden expected.jsonl files for the v3 adapter")

// goldenEvent is the wire shape written into expected.jsonl. We capture
// just the discriminator and the concrete payload so the golden file
// stays grep-friendly and resilient to field additions on the canonical
// types: tests assert on whatever fields the producer emits today, and
// updating the golden with -update-golden picks up new fields atomically.
type goldenEvent struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

const (
	// rootPlaceholder is substituted for the test's absolute root path
	// in PayloadRefEvent.LocationURI strings written into expected.jsonl,
	// so the golden file is portable across workstations and CI.
	rootPlaceholder = "<ROOT>"
)

// TestGolden runs every scenario directory under testdata/aiagent_v3/
// containing a session/ subtree and asserts that Scan produces the same
// canonical events recorded in expected.jsonl. Run with -update-golden
// to refresh.
func TestGolden(t *testing.T) {
	t.Parallel()

	base := filepath.Join("..", "..", "..", "testdata", "aiagent_v3")
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
	out := make(chan canonical.Event, 1024)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)

	// Filter out SourceProgress events from the golden — their cursor
	// payload encodes file offsets that depend on byte counts the
	// producer of expected.jsonl can verify but does not need to lock
	// down byte-for-byte.
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

// encodeEvents serialises events as one goldenEvent per line, with the
// absolute test-machine root replaced by rootPlaceholder so golden
// files are portable. Both SourceID (which embeds the root) and
// LocationURI (file:// URIs under the root) are rewritten.
func encodeEvents(events []canonical.Event, absRoot string) ([]byte, error) {
	var b strings.Builder
	slashRoot := filepath.ToSlash(absRoot)
	for _, ev := range events {
		payload, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("marshal %T: %w", ev, err)
		}
		s := string(payload)
		// LocationURI rewrite: file://<absRoot> → file://<ROOT>.
		s = strings.ReplaceAll(s, "file://"+slashRoot, "file://"+rootPlaceholder)
		// SourceID rewrite: aiagent_v3:<absRoot> → aiagent_v3:<ROOT>.
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
