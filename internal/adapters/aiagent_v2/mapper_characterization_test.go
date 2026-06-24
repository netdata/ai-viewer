package aiagent_v2

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestMap_MixedSnapshotPinsTraversalSourceSeqAndRollups(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	const originID = "mixed-origin"
	snap := mixedMapperSnapshot()
	var errs []error

	events := mapSnapshot(snap, "test-source", originID, root, originID+".json.gz", func(e error) {
		errs = append(errs, e)
	})
	if len(errs) != 0 {
		t.Fatalf("mapSnapshot errors: %v", errs)
	}

	assertMixedEventLocks(t, events, originID, mixedExpectedEventLocks())
	assertMixedSessionStartVersions(t, events, map[string]int{
		"root-trace":  2,
		"child-trace": 0,
	})

	replayed := mapSnapshot(snap, "test-source", originID, root, originID+".json.gz", func(e error) {
		t.Fatalf("replay mapSnapshot error: %v", e)
	})
	if diff := cmp.Diff(mixedEventIdentities(events), mixedEventIdentities(replayed)); diff != "" {
		t.Fatalf("replay changed event identities (-first +second):\n%s", diff)
	}

	wantLocations := []string{
		"file://" + filepath.ToSlash(filepath.Join(root, "payloads", "root", "request.json.gz")),
		"file://" + filepath.ToSlash(filepath.Join(root, "payloads", "root", "response.json.gz")),
		mixedInlinePayloadLocation(root, originID+".json.gz", "/opTree/turns/1/ops/1/childSession/turns/0/ops/0/request/payload"),
		mixedInlinePayloadLocation(root, originID+".json.gz", "/opTree/turns/1/ops/1/childSession/turns/0/ops/0/response/payload"),
		mixedInlinePayloadLocation(root, originID+".json.gz", "/opTree/steps/0/ops/0/request/payload"),
		mixedInlinePayloadLocation(root, originID+".json.gz", "/opTree/steps/0/ops/0/response/payload"),
	}
	if diff := cmp.Diff(wantLocations, mixedPayloadLocations(events)); diff != "" {
		t.Fatalf("payload locations mismatch (-want +got):\n%s", diff)
	}
}

func mixedInlinePayloadLocation(root string, filename string, pointer string) string {
	uri := url.URL{Scheme: "file", Path: filepath.Join(root, filename)}
	values := uri.Query()
	values.Set("json_pointer", pointer)
	uri.RawQuery = values.Encode()
	return uri.String()
}

type mixedEventLock struct {
	key  string
	path string
}

func mixedExpectedEventLocks() []mixedEventLock {
	return []mixedEventLock{
		{"SS:root-trace:root=root-trace:parent=:op=:kind=root:model=model-root", "root-trace::start"},
		{"TS:root-trace:0", "root-trace::T:0::start"},
		{"OS:root-trace:0:0:-1:system:rkind=:child=:orig=system:reason=", "root-trace::T:0::O:0:init-system::start"},
		{"OF:root-trace:0:0:completed:0/0:0/0:0.00:0/0:0/0", "root-trace::T:0::O:0:init-system::end"},
		{"TF:root-trace:0:completed:0/0:0/0:0.00", "root-trace::T:0::end"},
		{"TS:root-trace:1", "root-trace::T:1::start"},
		{"OS:root-trace:1:0:-1:llm:rkind=:child=:orig=llm:reason=root reasoning summary", "root-trace::T:1::O:0:root-llm::start"},
		{"OF:root-trace:1:0:completed:10/5:2/1:0.25:100/200:0/0", "root-trace::T:1::O:0:root-llm::end"},
		{"PR:root-trace:1:0:llm_request:100/50:req-sha", "root-trace::T:1::O:0:root-llm::payload:request"},
		{"PR:root-trace:1:0:llm_response:200/80:resp-sha", "root-trace::T:1::O:0:root-llm::payload:response"},
		{"OS:root-trace:1:2:0:reasoning:rkind=summary:child=:orig=:reason=root reasoning summary", "root-trace::T:1::O:0:root-llm::reasoning::start"},
		{"OF:root-trace:1:2:completed:0/0:0/0:0.00:0/0:0/0", "root-trace::T:1::O:0:root-llm::reasoning::end"},
		{"OS:root-trace:1:1:-1:session:rkind=:child=child-trace:orig=session:reason=", "root-trace::T:1::O:1:spawn-child::start"},
		{"OF:root-trace:1:1:completed:0/0:0/0:0.00:0/0:0/0", "root-trace::T:1::O:1:spawn-child::end"},
		{"SS:child-trace:root=root-trace:parent=root-trace:op=spawn-child:kind=sub_agent:model=", "child-trace::start"},
		{"TS:child-trace:1", "child-trace::T:1::start"},
		{"OS:child-trace:1:0:-1:tool:rkind=:child=:orig=tool:reason=", "child-trace::T:1::O:0:child-tool::start"},
		{"OF:child-trace:1:0:completed:0/0:0/0:0.00:70/110:7/11", "child-trace::T:1::O:0:child-tool::end"},
		{"PR:child-trace:1:0:tool_request:15/0:", "child-trace::T:1::O:0:child-tool::payload:request"},
		{"PR:child-trace:1:0:tool_response:15/0:", "child-trace::T:1::O:0:child-tool::payload:response"},
		{"TF:child-trace:1:completed:0/0:0/0:0.00", "child-trace::T:1::end"},
		{"SF:child-trace:completed:1700000008800000", "child-trace::end"},
		{"TF:root-trace:1:completed:10/5:2/1:0.25", "root-trace::T:1::end"},
		{"TS:root-trace:10000", "root-trace::S:0::start"},
		{"SU:root-trace:internal", "root-trace::S:0::kind"},
		{"OS:root-trace:10000:0:-1:tool:rkind=:child=:orig=tool:reason=", "root-trace::S:0::O:0:step-tool::start"},
		{"OF:root-trace:10000:0:completed:0/0:0/0:0.00:333/444:123/456", "root-trace::S:0::O:0:step-tool::end"},
		{"PR:root-trace:10000:0:tool_request:15/0:", "root-trace::S:0::O:0:step-tool::payload:request"},
		{"PR:root-trace:10000:0:tool_response:15/0:", "root-trace::S:0::O:0:step-tool::payload:response"},
		{"OS:root-trace:10000:1:-1:llm:rkind=:child=:orig=llm:reason=", "root-trace::S:0::O:1:step-llm::start"},
		{"OF:root-trace:10000:1:completed:30/7:3/2:0.50:0/0:0/0", "root-trace::S:0::O:1:step-llm::end"},
		{"TF:root-trace:10000:completed:30/7:3/2:0.50", "root-trace::S:0::end"},
		{"SF:root-trace:completed:1700000010000000", "root-trace::end"},
	}
}

func assertMixedEventLocks(t *testing.T, events []canonical.Event, originID string, locks []mixedEventLock) {
	t.Helper()

	got := make([]string, 0, len(events))
	want := make([]string, 0, len(locks))
	for _, ev := range events {
		got = append(got, mixedEventKey(ev))
	}
	for _, lock := range locks {
		want = append(want, lock.key)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("event order/fields mismatch (-want +got):\n%s", diff)
	}
	for i, lock := range locks {
		wantSeq := mixedExpectedSourceSeq(originID, lock.path)
		if gotSeq := events[i].EventSourceSeq(); gotSeq != wantSeq {
			t.Fatalf("event %d %s SourceSeq = %d, want %d from path %q", i, lock.key, gotSeq, wantSeq, lock.path)
		}
	}
}

func assertMixedSessionStartVersions(t *testing.T, events []canonical.Event, want map[string]int) {
	t.Helper()

	got := make(map[string]int, len(want))
	for _, ev := range events {
		ss, ok := ev.(canonical.SessionStartedEvent)
		if !ok {
			continue
		}
		if _, track := want[ss.NativeID]; !track {
			continue
		}
		version, ok := ss.Extras["version"].(int)
		if !ok {
			t.Fatalf("SessionStarted %s Extras[version] = %T(%v), want int", ss.NativeID, ss.Extras["version"], ss.Extras["version"])
		}
		got[ss.NativeID] = version
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("SessionStarted Extras.version mismatch (-want +got):\n%s", diff)
	}
}

func mixedEventKey(ev canonical.Event) string {
	if key, ok := mixedSessionEventKey(ev); ok {
		return key
	}
	if key, ok := mixedTurnEventKey(ev); ok {
		return key
	}
	if key, ok := mixedOpEventKey(ev); ok {
		return key
	}
	if key, ok := mixedPayloadEventKey(ev); ok {
		return key
	}
	return fmt.Sprintf("%T", ev)
}

func mixedSessionEventKey(ev canonical.Event) (string, bool) {
	switch v := ev.(type) {
	case canonical.SessionStartedEvent:
		return fmt.Sprintf("SS:%s:root=%s:parent=%s:op=%s:kind=%s:model=%s",
			v.NativeID, v.RootNativeID, v.ParentNativeID, v.ParentOpKey, v.Kind, v.Model), true
	case canonical.SessionUpdatedEvent:
		return fmt.Sprintf("SU:%s:%s", v.NativeID, mixedStringExtra(v.Extras, "step.0.kind")), true
	case canonical.SessionFinalizedEvent:
		return fmt.Sprintf("SF:%s:%s:%d", v.NativeID, v.Status, v.EndTs), true
	default:
		return "", false
	}
}

func mixedTurnEventKey(ev canonical.Event) (string, bool) {
	switch v := ev.(type) {
	case canonical.TurnStartedEvent:
		return fmt.Sprintf("TS:%s:%d", v.SessionNativeID, v.Seq), true
	case canonical.TurnFinalizedEvent:
		return fmt.Sprintf("TF:%s:%d:%s:%d/%d:%d/%d:%.2f",
			v.SessionNativeID, v.Seq, v.Status, v.TokensIn, v.TokensOut, v.TokensCacheRead, v.TokensCacheWrite, v.CostUSD), true
	default:
		return "", false
	}
}

func mixedOpEventKey(ev canonical.Event) (string, bool) {
	switch v := ev.(type) {
	case canonical.OpStartedEvent:
		return fmt.Sprintf("OS:%s:%d:%d:%d:%s:rkind=%s:child=%s:orig=%s:reason=%s",
			v.SessionNativeID, v.TurnSeq, v.Seq, v.ParentOpSeq, v.Kind, v.ReasoningKind,
			v.ChildSessionNativeID, mixedStringExtra(v.Extras, "original_kind"), mixedStringExtra(v.Extras, "reasoning.final")), true
	case canonical.OpFinalizedEvent:
		return fmt.Sprintf("OF:%s:%d:%d:%s:%d/%d:%d/%d:%.2f:%d/%d:%d/%d",
			v.SessionNativeID, v.TurnSeq, v.Seq, v.Status, v.TokensIn, v.TokensOut, v.TokensCacheRead,
			v.TokensCacheWrite, v.CostUSD, v.BytesIn, v.BytesOut, v.CharsIn, v.CharsOut), true
	default:
		return "", false
	}
}

func mixedPayloadEventKey(ev canonical.Event) (string, bool) {
	v, ok := ev.(canonical.PayloadRefEvent)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("PR:%s:%d:%d:%s:%d/%d:%s",
		v.SessionNativeID, v.TurnSeq, v.OpSeq, v.PayloadKind, v.OriginalBytes, v.StoredBytes, v.SHA256), true
}

func mixedEventIdentities(events []canonical.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, fmt.Sprintf("%s seq=%d ts=%d", mixedEventKey(ev), ev.EventSourceSeq(), ev.EventTs()))
	}
	return out
}

func mixedPayloadLocations(events []canonical.Event) []string {
	out := make([]string, 0, 2)
	for _, ev := range events {
		if pr, ok := ev.(canonical.PayloadRefEvent); ok {
			out = append(out, pr.LocationURI)
		}
	}
	return out
}

func mixedStringExtra(extras map[string]any, key string) string {
	if extras == nil {
		return ""
	}
	got, _ := extras[key].(string)
	return got
}

func mixedExpectedSourceSeq(originID, path string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(originID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(path))
	return h.Sum64() & 0x7FFFFFFFFFFFFFFF
}

func mixedMapperSnapshot() snapshot {
	snap := simpleSnapshot(2, "root-trace")
	snap.OpTree.ID = "root-node"
	snap.OpTree.AgentID = "root-agent"
	snap.OpTree.Turns = []turnNode{mixedInitTurn(), mixedUserTurn()}
	snap.OpTree.Steps = []stepNode{mixedInternalStep()}
	return snap
}

func mixedInitTurn() turnNode {
	return turnNode{
		ID:        "turn-0",
		Index:     0,
		StartedAt: 1700000000100,
		EndedAt:   int64Ptr(1700000000500),
		Attributes: rawAttrs(map[string]any{
			"system": true,
			"label":  "init",
		}),
		Ops: []operationNode{{
			OpID:      "init-system",
			Kind:      "system",
			StartedAt: 1700000000200,
			EndedAt:   int64Ptr(1700000000400),
			Status:    "ok",
			Attributes: rawAttrs(map[string]any{
				"name": "init",
			}),
		}},
	}
}

func mixedUserTurn() turnNode {
	return turnNode{
		ID:        "turn-1",
		Index:     1,
		StartedAt: 1700000001000,
		EndedAt:   int64Ptr(1700000009000),
		Ops:       []operationNode{mixedRootLLMOp(), mixedChildSessionOp()},
	}
}

func mixedRootLLMOp() operationNode {
	return operationNode{
		OpID:      "root-llm",
		Kind:      "llm",
		StartedAt: 1700000002000,
		EndedAt:   int64Ptr(1700000005000),
		Status:    "ok",
		Attributes: rawAttrs(map[string]any{
			"name":     "root-call",
			"provider": "anthropic",
			"model":    "model-root",
		}),
		Accounting: []accountingEntry{{
			Type:    "llm",
			Model:   "model-root",
			CostUSD: 0.25,
			Tokens: &tokens{
				InputTokens:           10,
				OutputTokens:          5,
				CacheReadInputTokens:  2,
				CacheWriteInputTokens: 1,
			},
		}},
		Reasoning: &reasoning{Final: "root reasoning summary"},
		Request: &opPayload{
			Kind:    "llm",
			Payload: json.RawMessage(`{"ref":"payloads/root/request.json.gz","format":"json","compression":"gzip","originalBytes":100,"storedBytes":50,"sha256":"req-sha"}`),
			Size:    100,
		},
		Response: &opPayload{
			Payload: json.RawMessage(`{"ref":"payloads/root/response.json.gz","format":"json","compression":"gzip","storedBytes":80,"sha256":"resp-sha"}`),
			Size:    200,
		},
	}
}

func mixedChildSessionOp() operationNode {
	return operationNode{
		OpID:      "spawn-child",
		Kind:      "session",
		StartedAt: 1700000006000,
		EndedAt:   int64Ptr(1700000008900),
		Status:    "ok",
		Attributes: rawAttrs(map[string]any{
			"name":     "child-agent",
			"provider": "subagent",
			"kind":     "agent",
		}),
		ChildSession: &opTree{
			ID:        "child-node",
			TraceID:   "child-trace",
			AgentID:   "child-agent",
			StartedAt: 1700000006100,
			EndedAt:   int64Ptr(1700000008800),
			Success:   boolPtr(true),
			Turns: []turnNode{{
				ID:        "child-turn-1",
				Index:     1,
				StartedAt: 1700000006200,
				EndedAt:   int64Ptr(1700000008700),
				Ops:       []operationNode{mixedToolOp("child-tool", 1700000006300, 1700000008600, 70, 110, 7, 11)},
			}},
		},
	}
}

func mixedInternalStep() stepNode {
	return stepNode{
		ID:        "step-0",
		Index:     0,
		Kind:      "internal",
		StartedAt: 1700000009100,
		EndedAt:   int64Ptr(1700000009900),
		Ops: []operationNode{
			mixedToolOp("step-tool", 1700000009200, 1700000009400, 333, 444, 123, 456),
			mixedStepLLMOp(),
		},
	}
}

func mixedToolOp(opID string, startMs, endMs, bytesIn, bytesOut, charsIn, charsOut int64) operationNode {
	return operationNode{
		OpID:      opID,
		Kind:      "tool",
		StartedAt: startMs,
		EndedAt:   int64Ptr(endMs),
		Status:    "ok",
		Attributes: rawAttrs(map[string]any{
			"name":     opID,
			"provider": "builtin",
		}),
		Accounting: []accountingEntry{{
			Type:          "tool",
			CharactersIn:  charsIn,
			CharactersOut: charsOut,
		}},
		Request:  &opPayload{Payload: json.RawMessage(`{"inline":true}`), Size: bytesIn},
		Response: &opPayload{Payload: json.RawMessage(`{"inline":true}`), Size: bytesOut},
	}
}

func mixedStepLLMOp() operationNode {
	return operationNode{
		OpID:      "step-llm",
		Kind:      "llm",
		StartedAt: 1700000009500,
		EndedAt:   int64Ptr(1700000009800),
		Status:    "ok",
		Attributes: rawAttrs(map[string]any{
			"name":     "step-call",
			"provider": "openai",
			"model":    "step-model",
		}),
		Accounting: []accountingEntry{{
			Type:    "llm",
			Model:   "step-model",
			CostUSD: 0.5,
			Tokens: &tokens{
				InputTokens:           30,
				OutputTokens:          7,
				CacheReadInputTokens:  3,
				CacheWriteInputTokens: 2,
			},
		}},
	}
}
