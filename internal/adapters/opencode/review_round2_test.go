package opencode

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the SOW-0005 ROUND-2 external-review fixes that are expressible
// at the PURE-MAPPER layer (no DB): P1-B (last-turn-error session finalize, not a
// sticky OR), P1-C (tool error → canonical "failed" + ErrorClass/Message), P2-A
// (error PRESENCE is terminal, not a non-empty name), P2-D (patch re-emit carries
// the full op identity), and P2-F (overflow clamps + WARN). The DB-level fixes
// (P1-A cursor split, P2-B N+1 parts, P2-C migrations err, P2-E time_compacting,
// P3-C SourceProgress single-emit) are pinned in the tailer/store test files.

// asgMsgErr builds an assistant message with a custom session id and an OPTIONAL
// error object; errPtr nil means no error, a present (possibly empty-Name) error
// exercises the error-PRESENCE path. It mirrors asgMsg but lets the test control
// the session id (for multi-turn P1-B) and inject a raw error object.
func asgMsgErr(id, sessionID string, createdMs int64, completedMs *int64, errObj map[string]any) messageRow {
	d := map[string]any{
		"role":       "assistant",
		"providerID": "the-alias",
		"modelID":    "the-model",
		"agent":      "test-agent",
		"tokens":     tokenCounts{},
		"time":       map[string]any{"created": createdMs},
		"finish":     "stop",
	}
	if completedMs != nil {
		d["time"] = map[string]any{"created": createdMs, "completed": *completedMs}
	}
	if errObj != nil {
		d["error"] = errObj
	}
	raw, _ := json.Marshal(d)
	return messageRow{ID: id, SessionID: sessionID, TimeCreatedMs: createdMs, TimeUpdatedMs: createdMs, Data: raw}
}

// sessionFinal returns the single SessionFinalizedEvent in the stream, or nil
// when the session stayed running (no finalize).
func sessionFinal(evs []canonical.Event) *canonical.SessionFinalizedEvent {
	for i := range evs {
		if f, ok := evs[i].(canonical.SessionFinalizedEvent); ok {
			return &f
		}
	}
	return nil
}

// --- P1-B: failure is the LAST assistant turn's state, not a sticky OR --------

// TestP1B_SessionRecoversWhenLastTurnSucceeds pins P1-B: a session whose turn 1
// errored but whose turn 2 succeeded is NOT finalized failed — the sticky
// failError is CLEARED by the recovering turn. (Both turns are terminal: turn 1
// via its error, turn 2 via a completed ts.)
func TestP1B_SessionRecoversWhenLastTurnSucceeds(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_x", 0) // not archived → terminal decided by last turn
	comp := int64(3000)
	turn1 := asgMsgErr("msg_1", "ses_x", 2000, &comp, map[string]any{"name": "ProviderError"})
	turn2 := asgMsgErr("msg_2", "ses_x", 4000, ptr(5000), nil) // recovered
	evs := run(t, s, []messageWithParts{mwp(turn1), mwp(turn2)})

	if fin := sessionFinal(evs); fin != nil {
		t.Fatalf("session finalized %s after a recovering last turn; want NO finalize (P1-B)", fin.Status)
	}
	// Both turns still finalize at the turn level (turn 1 failed, turn 2 completed).
	tf := turnFinals(evs)
	if len(tf) != 2 {
		t.Fatalf("turn finals = %d, want 2", len(tf))
	}
}

// TestP1B_SessionFailsWhenLastTurnErrors pins the converse: a session whose LAST
// turn errored IS finalized failed, carrying that turn's error class.
func TestP1B_SessionFailsWhenLastTurnErrors(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_x", 0)
	turn1 := asgMsgErr("msg_1", "ses_x", 2000, ptr(3000), nil) // clean
	turn2 := asgMsgErr("msg_2", "ses_x", 4000, ptr(5000), map[string]any{"name": "FatalError"})
	evs := run(t, s, []messageWithParts{mwp(turn1), mwp(turn2)})

	fin := sessionFinal(evs)
	if fin == nil {
		t.Fatal("session whose last turn errored was not finalized failed (P1-B)")
	}
	if fin.Status != canonical.StatusFailed {
		t.Fatalf("session status = %q, want failed", fin.Status)
	}
	if fin.ErrorClass != "FatalError" {
		t.Fatalf("session ErrorClass = %q, want FatalError (the LAST turn's error)", fin.ErrorClass)
	}
}

// TestP1B_ArchivedWinsOverError pins that archival still wins over a last-turn
// error: an archived session is completed regardless of its last turn.
func TestP1B_ArchivedWinsOverError(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_x", 9000) // archived
	turn := asgMsgErr("msg_1", "ses_x", 2000, ptr(3000), map[string]any{"name": "Whatever"})
	evs := run(t, s, []messageWithParts{mwp(turn)})

	fin := sessionFinal(evs)
	if fin == nil || fin.Status != canonical.StatusCompleted {
		t.Fatalf("archived session must finalize completed regardless of a last-turn error, got %+v", fin)
	}
}

// --- P2-A: error PRESENCE is terminal/failed, even with an empty name ---------

// TestP2A_EmptyNameErrorIsTerminalFailed pins P2-A: an error OBJECT with an EMPTY
// name still makes the turn terminal+failed (error presence, not name), and the
// ErrorClass defaults to the safe class label rather than blanking.
func TestP2A_EmptyNameErrorIsTerminalFailed(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_x", 0)
	// Error object present but name is "" — and NO completed ts, NO step-finish, so
	// ONLY error-presence can make this turn terminal.
	turn := asgMsgErr("msg_1", "ses_x", 2000, nil, map[string]any{"data": map[string]any{"detail": "x"}})
	evs := run(t, s, []messageWithParts{mwp(turn)})

	tf := turnFinals(evs)
	if len(tf) != 1 {
		t.Fatalf("turn finals = %d, want 1 (empty-name error is terminal — P2-A)", len(tf))
	}
	if tf[0].Status != "failed" {
		t.Fatalf("turn status = %q, want failed (error presence — P2-A)", tf[0].Status)
	}
	if tf[0].ErrorClass != defaultErrorClass {
		t.Fatalf("turn ErrorClass = %q, want %q default (P2-A)", tf[0].ErrorClass, defaultErrorClass)
	}
	// The session also finalizes failed with the default class.
	fin := sessionFinal(evs)
	if fin == nil || fin.Status != canonical.StatusFailed || fin.ErrorClass != defaultErrorClass {
		t.Fatalf("session finalize = %+v, want failed + %q (P2-A)", fin, defaultErrorClass)
	}
}

// --- P2-D: patch-enrichment re-emit carries the full op identity --------------

// TestP2D_PatchReEmitCarriesIdentity pins P2-D: when a patch part lands inside a
// step, the LLM op's re-emit (OpStarted carrying the patch extras before the
// finalize) must carry the SAME Name/Model/Provider/ProviderAlias as the original
// OpStarted — so the ingest writer's UNCONDITIONAL ops.name update does not blank
// the name. There are then TWO OpStarted for the LLM op's seq, both fully
// identified, and the second carries the patch extras.
func TestP2D_PatchReEmitCarriesIdentity(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_x", 0)
	patch, _ := json.Marshal(map[string]any{"type": "patch", "hash": "abc123", "files": []string{"/a/b.go"}})
	patchPart := partRow{ID: "prt_patch", MessageID: "msg_a", SessionID: "ses_x", Data: patch}
	msg := asgMsg("msg_a", 1500, ptr(3000), "the-alias", "the-model", tokenCounts{Input: 10}, 0.01, "stop", "")
	evs := run(t, s, []messageWithParts{mwp(msg,
		stepStart("prt_ss"),
		patchPart,
		stepFinish("prt_sf", 10, 5, 0, 0, 0, 0.01),
	)})

	// Collect the LLM OpStarted events (kind=llm). There must be TWO at the same
	// seq: the original and the patch-enrichment re-emit.
	var llmStarts []canonical.OpStartedEvent
	for _, st := range opStarts(evs) {
		if st.Kind == canonical.OpLLM {
			llmStarts = append(llmStarts, st)
		}
	}
	if len(llmStarts) != 2 {
		t.Fatalf("LLM OpStarted count = %d, want 2 (original + patch re-emit — P2-D)", len(llmStarts))
	}
	for i, st := range llmStarts {
		if st.Name != "the-model" || st.Model != "the-model" {
			t.Errorf("LLM OpStarted[%d] Name/Model = %q/%q, want the-model (P2-D: re-emit must keep identity)", i, st.Name, st.Model)
		}
		if st.Provider != "the-alias" || st.ProviderAlias != "the-alias" {
			t.Errorf("LLM OpStarted[%d] Provider/Alias = %q/%q, want the-alias (P2-D)", i, st.Provider, st.ProviderAlias)
		}
		if st.Seq != llmStarts[0].Seq {
			t.Errorf("LLM OpStarted[%d] Seq = %d, want %d (same op re-emit)", i, st.Seq, llmStarts[0].Seq)
		}
	}
	// The re-emit (second) must carry the patch extras.
	if llmStarts[1].Extras["patch_hash"] != "abc123" {
		t.Errorf("patch re-emit Extras[patch_hash] = %v, want abc123", llmStarts[1].Extras["patch_hash"])
	}
}

// --- P2-F: overflow on crafted token values clamps + WARNs --------------------

// TestP2F_HugeTokenDeltaClampsAndWarns pins P2-F at the step-finish token level:
// two step-finish snapshots whose CUMULATIVE inputs, subtracted, would overflow
// int64 are clamped (not wrapped) and surface a WARN. The crafted sequence is a
// huge positive cumulative after a negative one (a corrupt DB value), so the
// delta a-b overflows positive → clamps to MaxInt64.
func TestP2F_HugeTokenDeltaClampsAndWarns(t *testing.T) {
	t.Parallel()
	var warns []error
	cums := []tokenCounts{
		{Input: math.MinInt64 + 1}, // a corrupt negative cumulative
		{Input: math.MaxInt64},     // next cumulative: MaxInt64 - (MinInt64+1) overflows positive
	}
	got := computeStepDeltas(cums, func(e error) { warns = append(warns, e) })
	if len(got) != 2 {
		t.Fatalf("deltas len = %d, want 2", len(got))
	}
	// The second delta must clamp to MaxInt64, NOT wrap to a negative/small value.
	if got[1].Input != math.MaxInt64 {
		t.Errorf("overflowing delta = %d, want MaxInt64 (clamp, no wrap — P2-F)", got[1].Input)
	}
	if len(warns) == 0 {
		t.Error("overflowing token delta did not surface a WARN (P2-F)")
	}
	foundInput := false
	for _, w := range warns {
		if strings.Contains(w.Error(), "tokens.input") && strings.Contains(w.Error(), "overflow") {
			foundInput = true
		}
	}
	if !foundInput {
		t.Errorf("WARN set %v missing a tokens.input overflow message", warns)
	}

	// Negative overflow: a very negative cumulative after a large positive one. The
	// subtraction underflows; the result must clamp to 0 (negative token counts are
	// meaningless), still with a WARN.
	var negWarns []error
	neg := computeStepDeltas([]tokenCounts{
		{Input: math.MaxInt64},     // first delta = MaxInt64
		{Input: math.MinInt64 + 1}, // MinInt64+1 - MaxInt64 underflows negative
	}, func(e error) { negWarns = append(negWarns, e) })
	if neg[1].Input != 0 {
		t.Errorf("underflowing delta = %d, want 0 (clamp, no wrap — P2-F)", neg[1].Input)
	}
	if len(negWarns) == 0 {
		t.Error("underflowing token delta did not surface a WARN (P2-F)")
	}
}

// TestP2F_MsToMicrosSaturates pins P2-F at the timestamp level: a crafted huge ms
// value saturates at math.MaxInt64 rather than WRAPPING (a wrapped timestamp goes
// negative and reorders events nonsensically).
func TestP2F_MsToMicrosSaturates(t *testing.T) {
	t.Parallel()
	if got := msToMicros(math.MaxInt64); got != math.MaxInt64 {
		t.Errorf("msToMicros(MaxInt64) = %d, want MaxInt64 (saturate, no wrap — P2-F)", got)
	}
	// A normal value still converts ×1000.
	if got := msToMicros(1500); got != 1_500_000 {
		t.Errorf("msToMicros(1500) = %d, want 1500000", got)
	}
	// A huge-but-in-range value just below the saturation threshold still ×1000.
	safe := int64(math.MaxInt64/1000) - 1
	if got := msToMicros(safe); got != safe*1000 {
		t.Errorf("msToMicros(%d) = %d, want %d", safe, got, safe*1000)
	}
}

// ptr returns a pointer to an int64 literal for the optional completed-ts args.
func ptr(v int64) *int64 { return &v }

// --- round-3 P2-1: ms→µs clamp now WARNs; ctx_used add saturates + WARNs -------

// TestP2_1_MsToMicrosWarnsOnClamp pins round-3 P2-1: a session whose time_created
// is a crafted huge ms value clamps to MaxInt64 (as before, P2-F) AND now surfaces
// a WARN via the wired onWarn (it was a SILENT saturation before P2-1). The pure
// msToMicros (no warn channel) still saturates silently — covered by
// TestP2F_MsToMicrosSaturates.
func TestP2_1_MsToMicrosWarnsOnClamp(t *testing.T) {
	t.Parallel()
	s := rootSession("ses_clamp", 0)
	s.TimeCreatedMs = math.MaxInt64 // *1000 would overflow → clamp + WARN

	var warns []error
	evs, err := mapSession(testSourceID, s, nil, WithOnWarn(func(e error) { warns = append(warns, e) }))
	if err != nil {
		t.Fatalf("mapSession: %v", err)
	}
	// SessionStarted's Ts must be the saturated MaxInt64, never a wrapped negative.
	ss, ok := evs[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("first event = %T, want SessionStartedEvent", evs[0])
	}
	if ss.Ts != math.MaxInt64 {
		t.Errorf("clamped SessionStarted.Ts = %d, want MaxInt64 (saturate, no wrap)", ss.Ts)
	}
	foundTs := false
	for _, w := range warns {
		if strings.Contains(w.Error(), "session.time_created") && strings.Contains(w.Error(), "overflow") {
			foundTs = true
		}
	}
	if !foundTs {
		t.Errorf("clamped timestamp did not surface a WARN naming the field; warns=%v", warns)
	}

	// A NON-overflowing timestamp emits NO clamp WARN (no false positives).
	var clean []error
	_, _ = mapSession(testSourceID, rootSession("ses_ok", 0), nil, WithOnWarn(func(e error) { clean = append(clean, e) }))
	for _, w := range clean {
		if strings.Contains(w.Error(), "overflow") {
			t.Errorf("a normal session timestamp wrongly warned of overflow: %v", w)
		}
	}
}

// TestP2_1_CtxUsedAddSaturatesAndWarns pins round-3 P2-1's second half: the
// ctx_used = tokens.input + tokens.cache.read ADDITION saturates at MaxInt64 with
// a WARN on a crafted overflowing pair, instead of wrapping to a negative
// ctx_used. addClampWarn is the arithmetic; this asserts both the clamp and the
// WARN, plus that a normal pair adds cleanly with no WARN.
func TestP2_1_CtxUsedAddSaturatesAndWarns(t *testing.T) {
	t.Parallel()
	var warns []error
	onWarn := func(e error) { warns = append(warns, e) }

	// MaxInt64 + 1 overflows positive → clamp to MaxInt64 + WARN.
	if got := addClampWarn(math.MaxInt64, 1, "ctx_used", onWarn); got != math.MaxInt64 {
		t.Errorf("addClampWarn(MaxInt64,1) = %d, want MaxInt64 (saturate, no wrap)", got)
	}
	if len(warns) == 0 {
		t.Fatal("overflowing ctx_used add did not surface a WARN (P2-1)")
	}
	if !strings.Contains(warns[0].Error(), "ctx_used") || !strings.Contains(warns[0].Error(), "overflow") {
		t.Errorf("WARN = %q, want one naming ctx_used overflow", warns[0].Error())
	}

	// A normal pair adds cleanly with no WARN.
	var clean []error
	if got := addClampWarn(100, 250, "ctx_used", func(e error) { clean = append(clean, e) }); got != 350 {
		t.Errorf("addClampWarn(100,250) = %d, want 350", got)
	}
	if len(clean) != 0 {
		t.Errorf("normal ctx_used add wrongly warned: %v", clean)
	}
}
