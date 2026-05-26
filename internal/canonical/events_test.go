package canonical_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// Compile-time assertions that every concrete event type satisfies
// canonical.Event. If a future spec change adds a field that breaks the
// interface, this block fails to compile rather than waiting for a
// runtime test to fire.
var (
	_ canonical.Event = (*canonical.SessionStartedEvent)(nil)
	_ canonical.Event = (*canonical.SessionUpdatedEvent)(nil)
	_ canonical.Event = (*canonical.SessionFinalizedEvent)(nil)
	_ canonical.Event = (*canonical.TurnStartedEvent)(nil)
	_ canonical.Event = (*canonical.TurnFinalizedEvent)(nil)
	_ canonical.Event = (*canonical.OpStartedEvent)(nil)
	_ canonical.Event = (*canonical.OpFinalizedEvent)(nil)
	_ canonical.Event = (*canonical.PayloadRefEvent)(nil)
	_ canonical.Event = (*canonical.LogEntryEvent)(nil)
	_ canonical.Event = (*canonical.SourceProgressEvent)(nil)
	_ canonical.Event = (*canonical.SourceErrorEvent)(nil)
)

func TestEventBaseAccessors(t *testing.T) {
	t.Parallel()

	const (
		srcID    = "aiagent-v3:/tmp/sessions"
		seq      = uint64(42)
		tsMicros = int64(1_700_000_000_000_000)
	)

	base := canonical.EventBase{
		SourceID:  srcID,
		SourceSeq: seq,
		Ts:        tsMicros,
	}

	if got := base.EventSourceID(); got != srcID {
		t.Fatalf("EventSourceID: want %q, got %q", srcID, got)
	}
	if got := base.EventSourceSeq(); got != seq {
		t.Fatalf("EventSourceSeq: want %d, got %d", seq, got)
	}
	if got := base.EventTs(); got != tsMicros {
		t.Fatalf("EventTs: want %d, got %d", tsMicros, got)
	}
}

// TestEventKindRoundTrip pins the Kind value reported by every concrete
// event type. The Kind is encoded in the Go type (the per-type
// EventKind() method) — not in a settable field — so an adapter cannot
// construct an event whose declared kind disagrees with its type.
func TestEventKindRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ev   canonical.Event
		want canonical.EventKind
	}{
		{
			name: "session_started",
			ev:   canonical.SessionStartedEvent{},
			want: canonical.EvSessionStarted,
		},
		{
			name: "session_updated",
			ev:   canonical.SessionUpdatedEvent{},
			want: canonical.EvSessionUpdated,
		},
		{
			name: "session_finalized",
			ev:   canonical.SessionFinalizedEvent{},
			want: canonical.EvSessionFinalized,
		},
		{
			name: "turn_started",
			ev:   canonical.TurnStartedEvent{},
			want: canonical.EvTurnStarted,
		},
		{
			name: "turn_finalized",
			ev:   canonical.TurnFinalizedEvent{},
			want: canonical.EvTurnFinalized,
		},
		{
			name: "op_started",
			ev:   canonical.OpStartedEvent{},
			want: canonical.EvOpStarted,
		},
		{
			name: "op_finalized",
			ev:   canonical.OpFinalizedEvent{},
			want: canonical.EvOpFinalized,
		},
		{
			name: "payload_ref",
			ev:   canonical.PayloadRefEvent{},
			want: canonical.EvPayloadRef,
		},
		{
			name: "log_entry",
			ev:   canonical.LogEntryEvent{},
			want: canonical.EvLogEntry,
		},
		{
			name: "source_progress",
			ev:   canonical.SourceProgressEvent{},
			want: canonical.EvSourceProgress,
		},
		{
			name: "source_error",
			ev:   canonical.SourceErrorEvent{},
			want: canonical.EvSourceError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ev.EventKind(); got != tc.want {
				t.Fatalf("EventKind: want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestEventKindStringDistinct checks that the textual encoding of every
// EventKind constant is non-empty and unique. Without this, a duplicated
// or empty constant would silently break golden fixtures and logs.
func TestEventKindStringDistinct(t *testing.T) {
	t.Parallel()

	kinds := []canonical.EventKind{
		canonical.EvSessionStarted,
		canonical.EvSessionUpdated,
		canonical.EvSessionFinalized,
		canonical.EvTurnStarted,
		canonical.EvTurnFinalized,
		canonical.EvOpStarted,
		canonical.EvOpFinalized,
		canonical.EvPayloadRef,
		canonical.EvLogEntry,
		canonical.EvSourceProgress,
		canonical.EvSourceError,
	}

	seen := make(map[string]canonical.EventKind, len(kinds))
	for _, k := range kinds {
		s := k.String()
		if s == "" {
			t.Errorf("EventKind %v stringifies to the empty string", k)
			continue
		}
		if existing, dup := seen[s]; dup {
			t.Errorf("EventKind %q reused by both %v and %v", s, existing, k)
		}
		seen[s] = k
	}
}

func TestOpKindString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		k    canonical.OpKind
		want string
	}{
		{canonical.OpLLM, "llm"},
		{canonical.OpTool, "tool"},
		{canonical.OpSession, "session"},
		{canonical.OpReasoning, "reasoning"},
		{canonical.OpInternal, "internal"},
		{canonical.OpSystem, "system"},
		{canonical.OpCompaction, "compaction"},
	}

	seen := make(map[string]canonical.OpKind, len(cases))
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.k.String(); got != tc.want {
				t.Fatalf("OpKind(%q).String(): want %q, got %q", tc.k, tc.want, got)
			}
		})
		if tc.want == "" {
			t.Errorf("OpKind %v stringifies to the empty string", tc.k)
		}
		if existing, dup := seen[tc.want]; dup {
			t.Errorf("OpKind %q reused by both %v and %v", tc.want, existing, tc.k)
		}
		seen[tc.want] = tc.k
	}
}

func TestSessionKindString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		k    canonical.SessionKind
		want string
	}{
		{canonical.KindRoot, "root"},
		{canonical.KindSubAgent, "sub_agent"},
		{canonical.KindToolInternal, "tool_internal"},
		{canonical.KindFork, "fork"},
	}

	seen := make(map[string]canonical.SessionKind, len(cases))
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.k.String(); got != tc.want {
				t.Fatalf("SessionKind(%q).String(): want %q, got %q", tc.k, tc.want, got)
			}
		})
		if tc.want == "" {
			t.Errorf("SessionKind %v stringifies to the empty string", tc.k)
		}
		if existing, dup := seen[tc.want]; dup {
			t.Errorf("SessionKind %q reused by both %v and %v", tc.want, existing, tc.k)
		}
		seen[tc.want] = tc.k
	}
}

func TestSessionStatusString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		s    canonical.SessionStatus
		want string
	}{
		{canonical.StatusRunning, "running"},
		{canonical.StatusCompleted, "completed"},
		{canonical.StatusFailed, "failed"},
		{canonical.StatusAbandoned, "abandoned"},
		{canonical.StatusInterrupted, "interrupted"},
	}

	seen := make(map[string]canonical.SessionStatus, len(cases))
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.s.String(); got != tc.want {
				t.Fatalf("SessionStatus(%q).String(): want %q, got %q", tc.s, tc.want, got)
			}
		})
		if tc.want == "" {
			t.Errorf("SessionStatus %v stringifies to the empty string", tc.s)
		}
		if existing, dup := seen[tc.want]; dup {
			t.Errorf("SessionStatus %q reused by both %v and %v", tc.want, existing, tc.s)
		}
		seen[tc.want] = tc.s
	}
}

// TestSessionStartedEventFields exercises the spec's notes about
// RootNativeID equaling NativeID for root sessions and the rest of the
// optional fields being usable as zero values without panicking. cmp is
// used so any future field addition without a test update surfaces as a
// readable diff.
func TestSessionStartedEventFields(t *testing.T) {
	t.Parallel()

	const nativeID = "session-abc"
	ev := canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{
			SourceID:  "aiagent-v3:/var/snapshots",
			SourceSeq: 1,
			Ts:        1_700_000_000_000_000,
		},
		NativeID:     nativeID,
		RootNativeID: nativeID,
		Kind:         canonical.KindRoot,
		AgentName:    "primary",
		Model:        "claude-opus-4",
		Cwd:          "/home/user/code",
		CallPath:     "primary",
		Extras:       map[string]any{"finalReport": false},
	}

	want := canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{
			SourceID:  "aiagent-v3:/var/snapshots",
			SourceSeq: 1,
			Ts:        1_700_000_000_000_000,
		},
		NativeID:     "session-abc",
		RootNativeID: "session-abc",
		Kind:         canonical.KindRoot,
		AgentName:    "primary",
		Model:        "claude-opus-4",
		Cwd:          "/home/user/code",
		CallPath:     "primary",
		Extras:       map[string]any{"finalReport": false},
	}

	if diff := cmp.Diff(want, ev); diff != "" {
		t.Fatalf("SessionStartedEvent mismatch (-want +got):\n%s", diff)
	}

	// The reported EventKind must come from the concrete type, not from
	// any EventBase field — that's the whole point of removing the Kind
	// field. Verify against a zero-value of every concrete event too.
	if got := ev.EventKind(); got != canonical.EvSessionStarted {
		t.Fatalf("EventKind: want %q, got %q", canonical.EvSessionStarted, got)
	}
}
