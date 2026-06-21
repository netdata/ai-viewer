package codex

// Tests for SOW-0089 chunk 5c — codex sub-turn splitting at user_input
// boundaries. The mapper closes the active codex turn (synthetic
// TurnFinalizedEvent, status=completed) when a NEW user_input arrives within
// the same codex task that already saw a user_input — i.e. the operator
// sees each user/assistant exchange as its own turn, while the codex task
// itself remains the unit of work the CLI is doing.

import (
	"fmt"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// tsCtxSub / tsUserA / tsUserB are fixed timestamps so the assertions are
// deterministic. The first user_input fires at tsUserA within the codex
// task; the second user_input (the split trigger) fires at tsUserB.
const (
	tsCtxSub      = "2025-11-20T16:59:09.000Z"
	tsUserASub    = "2025-11-20T16:59:11.000Z"
	tsUserBSub    = "2025-11-20T16:59:21.000Z"
	tsAsstASub    = "2025-11-20T16:59:13.000Z"
	tsAsstBSub    = "2025-11-20T16:59:23.000Z"
	tsTaskDoneSub = "2025-11-20T16:59:30.000Z"
)

// TestMapper_SubTurnSplitOnSecondUserInput pins SOW-0089 chunk 5c acceptance:
// a codex file with TWO user prompts in ONE codex task produces THREE turns:
//
//   - Turn #1: contains the first user_input + the assistant response.
//     Synthetically finalized when the second user_input arrives.
//   - Turn #2: contains the second user_input + its assistant response.
//     Synthetically finalized when task_complete arrives.
//   - Turn #3: empty "tail" turn the codex task_complete opens to be
//     finalize-able (task_complete targets the activeTurnID; if we just
//     finalized the last sub-turn, the task_complete must have somewhere
//     to land).
//
// In practice the codex file rarely has anything between the last
// user/assistant exchange and task_complete — that "tail" turn will
// usually be empty. The operator sees two non-empty sub-turns (1 and 2) and
// one empty turn 3 that the UI can render as a placeholder.
func TestMapper_SubTurnSplitOnSecondUserInput(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sub-split-sid")
	lines := []string{
		metaLine("sub-split-sid", `"exec"`),
		// Open the codex task: turn_context + task_started.
		`{"timestamp":"` + tsCtxSub + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5","sandbox_policy":{"type":"workspace-write"},"effort":"high","approval_policy":"never"}}`,
		`{"timestamp":"` + tsCtxSub + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1763657940,"model_context_window":258400}}`,

		// First user prompt → sub-turn #1 (no prior user_input → no split).
		`{"timestamp":"` + tsUserASub + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}`,
		// First assistant reply.
		`{"timestamp":"` + tsAsstASub + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,

		// Second user prompt → SUB-TURN SPLIT. The active turn (carrying
		// the first user/assistant exchange) must finalize BEFORE the new
		// user_input lands in a fresh sub-turn.
		`{"timestamp":"` + tsUserBSub + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second request"}]}}`,
		// Second assistant reply (lives in sub-turn #2).
		`{"timestamp":"` + tsAsstBSub + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}`,

		// task_complete: closes the active turn (sub-turn #2). The mapper
		// opens an empty tail turn so the finalize has a target.
		`{"timestamp":"` + tsTaskDoneSub + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":1763657970,"duration_ms":30000}}`,
	}
	events := runLines(t, m, lines)

	// We expect: at least 3 TurnStarted (sub-turn #1, sub-turn #2, tail),
	// at least 2 TurnFinalized (the synthetic splits + task_complete's
	// close of the tail). The exact count depends on what the mapper does
	// for the empty tail — it might or might not emit a TurnStartedEvent
	// for it. We assert on TurnFinalized since that's the user-visible
	// "this turn is closed" signal.
	finalized := 0
	for _, ev := range events {
		if tf, ok := ev.(canonical.TurnFinalizedEvent); ok {
			finalized++
			// Every finalized turn carries Status="completed" — a sub-split
			// is NOT a failure, it's a visualization boundary.
			if tf.Status != "completed" {
				t.Errorf("TurnFinalized.Seq=%d Status = %q, want completed", tf.Seq, tf.Status)
			}
		}
	}
	if finalized < 2 {
		t.Errorf("TurnFinalized count = %d, want >= 2 (one per sub-split)", finalized)
		for _, ev := range events {
			fmt.Printf("  %s seq=%d\n", ev.EventKind(), seqOf(ev))
		}
	}

	// The op sequencing: turn #1 should hold ops with Seq values within
	// the sub-turn #1 range; turn #2 should hold ops within sub-turn #2.
	// We verify by mapping op.StartedEvent.TurnSeq and checking each user_input
	// lands in a different turn.
	seenUserInputTurns := make(map[int]bool)
	for _, ev := range events {
		os, ok := ev.(canonical.OpStartedEvent)
		if !ok || os.Kind != canonical.OpInternal || os.Name != "user_input" {
			continue
		}
		seenUserInputTurns[os.TurnSeq] = true
	}
	if len(seenUserInputTurns) < 2 {
		t.Errorf("user_input ops appeared in %d turns, want >= 2", len(seenUserInputTurns))
	}

	// The two user_inputs should land in DIFFERENT turns. If they share
	// a TurnSeq, the split didn't fire.
	if len(seenUserInputTurns) == 1 {
		t.Errorf("both user_input ops share TurnSeq=%v — sub-turn split did NOT fire", seenUserInputTurns)
		for _, ev := range events {
			fmt.Printf("  %s seq=%d\n", ev.EventKind(), seqOf(ev))
		}
	}
}

// seqOf extracts a TurnSeq-like field from any event for debug logging.
func seqOf(ev canonical.Event) int {
	switch e := ev.(type) {
	case canonical.TurnStartedEvent:
		return e.Seq
	case canonical.TurnFinalizedEvent:
		return e.Seq
	case canonical.OpStartedEvent:
		return e.Seq
	case canonical.OpFinalizedEvent:
		return e.Seq
	default:
		return 0
	}
}

var _ = time.Second // keep imports stable across edits

// TestMapper_SubTurnSplitDeferredOnOpenToolCall pins SOW-0089 chunk 5c's
// safety rule: a sub-turn split is DEFERRED while a function_call is in
// flight (no corresponding *_output yet). The split happens on the NEXT
// user_input after the tool call resolves.
//
// Without this guard, splitting mid-tool-call would orphan the open
// function_call — its *_output would land in the new sub-turn, but
// openOps tracks ops by turn_id, and the in-flight op's turn_id is the
// prior turn. The state would be inconsistent.
//
// This test pins that the split happens on user_input #3 (after the tool
// resolved), NOT on user_input #2 (mid-tool).
func TestMapper_SubTurnSplitDeferredOnOpenToolCall(t *testing.T) {
	t.Parallel()
	m := newTestMapper("defer-split-sid")
	lines := []string{
		metaLine("defer-split-sid", `"exec"`),
		`{"timestamp":"` + tsCtxSub + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5","sandbox_policy":{"type":"workspace-write"}}}`,
		`{"timestamp":"` + tsCtxSub + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		// User #1 → sub-turn #1 opens.
		`{"timestamp":"` + tsUserASub + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"q1"}]}}`,
		// Assistant with a tool call (no *_output yet — IN FLIGHT).
		`{"timestamp":"` + tsAsstASub + `","type":"response_item","payload":{"type":"function_call","call_id":"call-1","name":"shell","arguments":"echo hi"}}`,
		// User #2 → SPLIT IS DEFERRED because call-1 is still in flight.
		// The user_input lands in the SAME sub-turn as user #1.
		`{"timestamp":"` + tsUserBSub + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"q2"}]}}`,
		// Now call-1 resolves with its output.
		`{"timestamp":"` + tsAsstBSub + `","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"hi\\n"}}`,
		// User #3 → split FIRES now (no tool in flight).
		`{"timestamp":"` + tsTaskDoneSub + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"q3"}]}}`,
	}
	events := runLines(t, m, lines)

	// Count user_input ops per TurnSeq. We expect 2 turns carrying user_inputs
	// (turn 1: q1+q2, turn 2: q3). NOT 3.
	turnUserInputs := make(map[int]int)
	for _, ev := range events {
		os, ok := ev.(canonical.OpStartedEvent)
		if !ok || os.Kind != canonical.OpInternal || os.Name != "user_input" {
			continue
		}
		turnUserInputs[os.TurnSeq]++
	}
	if len(turnUserInputs) != 2 {
		t.Errorf("user_inputs split across %d turns, want 2 (q1+q2 together, q3 alone)", len(turnUserInputs))
	}
	if turnUserInputs[1] != 2 {
		t.Errorf("first turn user_input count = %d, want 2 (q1 + q2 deferred)", turnUserInputs[1])
	}
	if turnUserInputs[2] != 1 {
		t.Errorf("second turn user_input count = %d, want 1 (q3 split)", turnUserInputs[2])
	}
}
