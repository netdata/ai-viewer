package codex

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// runLines parses each JSONL line and feeds it to the mapper in order, returning
// the full emitted canonical event stream. Lines that parse to skip=true (blank,
// ghost_snapshot, missing nested type) are skipped exactly as the scanner would.
// A parse error fails the test (the synthetic fixtures are well-formed). The
// per-record line number is threaded (1-based) so PayloadRef anchors are
// realistic. This mirrors how the scanner (Chunk C) will drive the mapper.
func runLines(t *testing.T, m *fileMapper, lines []string) []canonical.Event {
	t.Helper()
	var out []canonical.Event
	for i, line := range lines {
		rec, skip, err := parseLine([]byte(line))
		if err != nil {
			t.Fatalf("line %d parseLine(%q): %v", i+1, line, err)
		}
		if skip {
			continue
		}
		m.setLineNo(i + 1)
		evs, mErr := m.mapRecord(rec)
		if mErr != nil {
			t.Fatalf("line %d mapRecord: %v", i+1, mErr)
		}
		out = append(out, evs...)
	}
	return out
}

// newTestMapper builds a root-session mapper with synthetic ids. absPath is set
// so PayloadRef URIs are exercised; root containment is Chunk D's concern.
func newTestMapper(nativeID string) *fileMapper {
	return newFileMapper(mapperConfig{
		sourceID: "codex:/test/sessions",
		absPath:  "/test/sessions/2025/11/20/rollout-" + nativeID + ".jsonl",
		nativeID: nativeID,
	})
}

// countKind returns how many events have the given kind.
func countKind(events []canonical.Event, kind canonical.EventKind) int {
	n := 0
	for _, ev := range events {
		if ev.EventKind() == kind {
			n++
		}
	}
	return n
}

// opStarts returns every OpStartedEvent in the stream.
func opStarts(events []canonical.Event) []canonical.OpStartedEvent {
	var out []canonical.OpStartedEvent
	for _, ev := range events {
		if s, ok := ev.(canonical.OpStartedEvent); ok {
			out = append(out, s)
		}
	}
	return out
}

// opFinals returns every OpFinalizedEvent in the stream.
func opFinals(events []canonical.Event) []canonical.OpFinalizedEvent {
	var out []canonical.OpFinalizedEvent
	for _, ev := range events {
		if f, ok := ev.(canonical.OpFinalizedEvent); ok {
			out = append(out, f)
		}
	}
	return out
}

// turnFinals returns every TurnFinalizedEvent in the stream.
func turnFinals(events []canonical.Event) []canonical.TurnFinalizedEvent {
	var out []canonical.TurnFinalizedEvent
	for _, ev := range events {
		if f, ok := ev.(canonical.TurnFinalizedEvent); ok {
			out = append(out, f)
		}
	}
	return out
}

// firstStarted returns the first SessionStartedEvent, or fails.
func firstStarted(t *testing.T, events []canonical.Event) canonical.SessionStartedEvent {
	t.Helper()
	for _, ev := range events {
		if s, ok := ev.(canonical.SessionStartedEvent); ok {
			return s
		}
	}
	t.Fatal("no SessionStartedEvent in stream")
	return canonical.SessionStartedEvent{}
}

// firstReasoning returns the first OpReasoning OpStartedEvent, or fails.
func firstReasoning(t *testing.T, events []canonical.Event) canonical.OpStartedEvent {
	t.Helper()
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpReasoning {
			return s
		}
	}
	t.Fatal("no reasoning op in stream")
	return canonical.OpStartedEvent{}
}
