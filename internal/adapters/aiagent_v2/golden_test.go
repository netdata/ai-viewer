package aiagent_v2

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

var updateGolden = flag.Bool("update-golden", false, "rewrite golden expected.jsonl files for the v2 adapter")

// goldenEvent is the wire shape written into expected.jsonl. The
// discriminator is the canonical EventKind; the payload is the marshal
// of the concrete event type so future additive fields land in the
// golden via `-update-golden` rather than as test breakage.
type goldenEvent struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// rootPlaceholder is substituted for the test's absolute root path in
// SourceID strings so golden files are portable across workstations.
const rootPlaceholder = "<ROOT>"

// TestGolden runs every scenario under testdata/aiagent_v2/ that has an
// INPUT/ subdirectory, drives the adapter's Scan over it, and compares
// emitted events (minus SourceProgress and SourceError noise) against
// expected.jsonl. Re-run with -update-golden to refresh.
func TestGolden(t *testing.T) {
	t.Parallel()
	base := filepath.Join("..", "..", "..", "testdata", "aiagent_v2")
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
		t.Skipf("INPUT missing: %v", err)
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

	// Filter SourceProgressEvent: it carries the absolute cursor which
	// differs by path. Filter SourceErrorEvent too — its File field
	// references the absolute path which is not portable; we cover
	// SourceErrorEvent emission in scanner_test.go instead.
	filtered := make([]canonical.Event, 0, len(events))
	for _, ev := range events {
		switch ev.(type) {
		case canonical.SourceProgressEvent, canonical.SourceErrorEvent:
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

// encodeEvents serialises events as one goldenEvent per line. The
// adapter's SourceID embeds the absolute root path; rewrite it to
// rootPlaceholder so the golden is portable.
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
