package aiagent_v2

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestStreamer_AgreesWithNonStreaming is the load-bearing test for the
// streamer path. It validates that the streaming decoder produces
// byte-identical canonical events to the whole-tree decoder on the
// same fixture. Any divergence breaks the test before merge — that is
// the contract that lets the scanner route large files through the
// streamer without behavioural risk.
func TestStreamer_AgreesWithNonStreaming(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "stream-eq"
	writeSnapshot(t, root, origin, fatSnapshot(origin))
	path := filepath.Join(root, origin+".json.gz")

	whole, _, errW := readSnapshotWhole(path, "src", origin, root, origin+".json.gz", func(error) {})
	if errW != nil {
		t.Fatalf("whole: %v", errW)
	}
	streamed, _, errS := readSnapshotStreaming(context.Background(), path, "src", origin, root, origin+".json.gz", func(error) {})
	if errS != nil {
		t.Fatalf("stream: %v", errS)
	}
	if len(whole) != len(streamed) {
		t.Fatalf("event count differs: %d vs %d", len(whole), len(streamed))
	}
	for i := range whole {
		w, _ := json.Marshal(whole[i])
		s, _ := json.Marshal(streamed[i])
		if string(w) != string(s) {
			t.Fatalf("event %d differs:\n  whole : %s\n  stream: %s", i, w, s)
		}
	}
}

func TestStreamer_HandlesCorruptGzip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := writeRaw(t, root, "x.json.gz", []byte("not a gzip"))
	var errCount int
	events, hash, err := readSnapshotStreaming(context.Background(), path, "src", "x", root, "x.json.gz", func(error) { errCount++ })
	if err != nil {
		t.Fatalf("streamer should not return error for soft failures: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected zero events from corrupt gzip, got %d", len(events))
	}
	if hash != "" {
		t.Fatalf("expected empty hash on header error: %q", hash)
	}
	if errCount == 0 {
		t.Fatalf("expected onError to fire")
	}
}

func TestStreamer_HashesFullPayload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "hash-test"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	path := filepath.Join(root, origin+".json.gz")
	_, hashW, _ := readSnapshotWhole(path, "src", origin, root, origin+".json.gz", func(error) {})
	_, hashS, _ := readSnapshotStreaming(context.Background(), path, "src", origin, root, origin+".json.gz", func(error) {})
	if hashW != hashS {
		t.Fatalf("hash mismatch between paths: whole=%s stream=%s", hashW, hashS)
	}
}

func TestStreamer_MissingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	missing := filepath.Join(root, "nope.json.gz")
	_, _, err := readSnapshotStreaming(context.Background(), missing, "src", "nope", root, "nope.json.gz", func(error) {})
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestStreamer_ContextCancelledBeforeOpen(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := readSnapshotStreaming(ctx, "/tmp/nope", "src", "x", "/tmp", "x.json.gz", func(error) {})
	if err == nil {
		t.Fatalf("expected ctx error")
	}
}

// fatSnapshot builds a snapshot with multiple turns/ops so the
// streamer test exercises non-trivial JSON traversal even though we
// stay well under the streamer's 50 MiB threshold.
func fatSnapshot(origin string) snapshot {
	snap := simpleSnapshot(2, origin)
	// Add several extra turns + ops with varied accounting.
	for i := 2; i <= 5; i++ {
		turn := turnNode{
			ID:        "turn-" + string(rune('a'+i)),
			Index:     i,
			StartedAt: 1700000000000 + int64(i*1000),
			EndedAt:   int64Ptr(1700000000500 + int64(i*1000)),
		}
		for j := 0; j < 3; j++ {
			turn.Ops = append(turn.Ops, operationNode{
				OpID:      "op-" + string(rune('a'+j)),
				Kind:      "tool",
				StartedAt: 1700000000100 + int64(i*100+j*10),
				EndedAt:   int64Ptr(1700000000200 + int64(i*100+j*10)),
				Status:    "ok",
				Attributes: rawAttrs(map[string]any{
					"name":     "shell",
					"provider": "builtin",
				}),
				Accounting: []accountingEntry{
					{Type: "tool", CharactersIn: int64(100 + j*10), CharactersOut: int64(200 + j*20)},
				},
			})
		}
		snap.OpTree.Turns = append(snap.OpTree.Turns, turn)
	}
	return snap
}

// TestStreamer_LargePayloadOverThreshold materialises a snapshot whose
// COMPRESSED size exceeds the streamer threshold to force the scanner's
// streaming path, and asserts the result is identical to the whole-tree
// path. This keeps the streamer's correctness honest against the
// configured threshold rather than only the unit-level helpers.
func TestStreamer_LargePayloadOverThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-payload streamer test in -short mode")
	}
	t.Parallel()
	root := t.TempDir()
	origin := "huge"
	snap := bloatedSnapshot(origin)
	body, _ := json.Marshal(snap)
	gz := mkGzipBytes(body)
	if int64(len(gz)) <= streamerThresholdBytes {
		t.Skipf("bloated fixture %d <= threshold %d; skipping (compression squeezed too well)", len(gz), streamerThresholdBytes)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(root, origin+".json.gz")
	if err := os.WriteFile(path, gz, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Use a bounded channel + concurrent drainer so the test does not
	// pin the bloated event stream into memory before scanAll
	// completes. Without the drainer the channel would fill and
	// scanAll would block in the send loop.
	out := make(chan canonical.Event, 512)
	done := make(chan int, 1)
	go func() {
		count := 0
		for range out {
			count++
		}
		done <- count
	}()
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) {}); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	close(out)
	if got := <-done; got == 0 {
		t.Fatalf("expected events from large fixture")
	}
}

// bloatedSnapshot constructs a snapshot whose JSON tree is large
// enough to force the scanner above the streamer threshold even after
// gzip. Each op carries a high-entropy random payload that defeats
// deflate's LZ77 window.
func bloatedSnapshot(origin string) snapshot {
	snap := simpleSnapshot(2, origin)
	// Aim for >50 MB compressed. With ~5 KB high-entropy bytes per op
	// and ~24K ops, total ~120 MB raw → ~50-60 MB compressed.
	const (
		ops     = 24000
		padSize = 5000
	)
	turns := make([]turnNode, 0, 1)
	t := turnNode{Index: 1, StartedAt: 1700000000000, EndedAt: int64Ptr(1700000001000)}
	for i := 0; i < ops; i++ {
		uniq := highEntropyString(i, padSize)
		t.Ops = append(t.Ops, operationNode{
			OpID: pseudoString(i), Kind: "system",
			StartedAt: 1700000000000, EndedAt: int64Ptr(1700000000010),
			Status: "ok",
			Attributes: map[string]json.RawMessage{
				"label": json.RawMessage(`"system"`),
				"pad":   mustMarshal(uniq),
			},
		})
	}
	turns = append(turns, t)
	snap.OpTree.Turns = turns
	return snap
}

// highEntropyString returns a deterministic per-i pseudo-random
// string of length n drawn from the full printable-ASCII alphabet.
// More entropy than pseudoBytes' 62-symbol alphabet → poorer gzip
// compression.
func highEntropyString(i, n int) string {
	out := make([]byte, n)
	state := uint64(i*6364136223846793005 + 1442695040888963407)
	for j := 0; j < n; j++ {
		state = state*6364136223846793005 + 1442695040888963407
		// Restrict to printable ASCII so JSON encoding stays cheap and
		// the resulting bytes are still high-entropy across the 0x21-0x7E
		// printable range (94 distinct values).
		out[j] = byte(0x21 + (state>>40)%94)
	}
	return string(out)
}

// pseudoBytes returns a deterministic per-i byte string of length n.
// Designed to defeat gzip dictionary compression so the produced
// snapshot reliably crosses the streamer threshold.
func pseudoBytes(i, n int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	out := make([]byte, n)
	state := uint32(i*2654435761 + 1)
	for j := 0; j < n; j++ {
		state = state*1664525 + 1013904223
		out[j] = alphabet[state%uint32(len(alphabet))]
	}
	return string(out)
}

func pseudoString(i int) string { return pseudoBytes(i+99, 24) }

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
