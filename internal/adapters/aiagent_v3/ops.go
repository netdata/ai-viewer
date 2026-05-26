package aiagent_v3

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapOp emits the OpStarted, OpFinalized, and per-payload-ref events for
// a single op. subIdx is advanced for each emitted event so the caller
// can continue allocating monotone packed SourceSeq values after the op
// returns. Returns an error if op timestamps are unparseable or a
// payload ref escapes the configured root.
func mapOp(rec record, op opSummary, sourceID, sessionRoot string, subIdx *uint64) ([]canonical.Event, error) {
	startUs, endUs, err := parseOpTimes(rec, op)
	if err != nil {
		return nil, err
	}
	advance := func(ts int64) canonical.EventBase {
		b := canonical.EventBase{
			SourceID:  sourceID,
			SourceSeq: packSeq(rec.Common.Seq, *subIdx),
			Ts:        ts,
		}
		*subIdx++
		return b
	}
	turnSeq := rec.TurnEnd.Turn

	started := buildOpStarted(rec, op, advance(startUs), turnSeq)
	finalized := buildOpFinalized(op, advance(endUs), rec.Common.SessionID, turnSeq, endUs)
	events := make([]canonical.Event, 0, 2+len(op.PayloadRefs))
	events = append(events, started, finalized)

	for _, ref := range op.PayloadRefs {
		ev, perr := buildPayloadRefEvent(rec, op, ref, sessionRoot, advance(endUs), turnSeq)
		if perr != nil {
			return nil, perr
		}
		events = append(events, ev)
	}
	return events, nil
}

func buildOpStarted(rec record, op opSummary, base canonical.EventBase, turnSeq int) canonical.OpStartedEvent {
	extras := buildOpStartedExtras(op)
	var childID string
	if len(op.ChildSessions) > 0 {
		childID = op.ChildSessions[0].SessionID
	}
	toolNamespace := ""
	if op.Kind == string(canonical.OpTool) {
		toolNamespace = op.Provider
	}
	return canonical.OpStartedEvent{
		EventBase:            base,
		SessionNativeID:      rec.Common.SessionID,
		TurnSeq:              turnSeq,
		Seq:                  op.OpIndex,
		ParentOpSeq:          -1,
		Kind:                 canonical.OpKind(op.Kind),
		Name:                 op.Name,
		ToolNamespace:        toolNamespace,
		Model:                op.Model,
		Provider:             op.Provider,
		ChildSessionNativeID: childID,
		Extras:               extras,
	}
}

func buildOpStartedExtras(op opSummary) map[string]any {
	extras := map[string]any{}
	if op.Accounting != nil {
		// Cache-token counters per spec §10 gap 1.
		extras["tokensCacheRead"] = op.Accounting.TokensCacheRead
		extras["tokensCacheWrite"] = op.Accounting.TokensCacheWrite
	}
	for k, v := range op.Attributes {
		extras["attr."+k] = v
	}
	if len(op.ChildSessions) > 1 {
		// Spec §10 gap 8: stash additional children in extras.
		extra := make([]string, 0, len(op.ChildSessions)-1)
		for i := 1; i < len(op.ChildSessions); i++ {
			extra = append(extra, op.ChildSessions[i].SessionID)
		}
		extras["additionalChildSessions"] = extra
	}
	return extras
}

func buildOpFinalized(op opSummary, base canonical.EventBase, sessionID string, turnSeq int, endUs int64) canonical.OpFinalizedEvent {
	ev := canonical.OpFinalizedEvent{
		EventBase:       base,
		SessionNativeID: sessionID,
		TurnSeq:         turnSeq,
		Seq:             op.OpIndex,
		Status:          mapOpStatus(op.Status),
		ErrorMessage:    op.Error,
		EndTs:           endUs,
	}
	applyOpAccounting(&ev, op.Accounting)
	applyOpBytes(&ev, op.PayloadRefs)
	return ev
}

func applyOpAccounting(ev *canonical.OpFinalizedEvent, acc *accounting) {
	if acc == nil {
		return
	}
	ev.TokensIn = acc.TokensIn
	ev.TokensOut = acc.TokensOut
	ev.TokensCacheRead = acc.TokensCacheRead
	ev.TokensCacheWrite = acc.TokensCacheWrite
	if acc.CostUSD != nil {
		ev.CostUSD = *acc.CostUSD
	}
	// Best-effort context-window-in estimate per spec §5.3.
	ev.CtxUsed = acc.TokensIn + acc.TokensCacheRead
}

func applyOpBytes(ev *canonical.OpFinalizedEvent, refs []payloadRef) {
	for _, ref := range refs {
		if ref.OriginalBytes == nil {
			continue
		}
		switch ref.Kind {
		case "llm_request", "sdk_request", "tool_request":
			ev.BytesIn += *ref.OriginalBytes
		case "llm_response", "sdk_response", "reasoning_stream", "tool_response":
			ev.BytesOut += *ref.OriginalBytes
		}
	}
}

func buildPayloadRefEvent(rec record, op opSummary, ref payloadRef, sessionRoot string, base canonical.EventBase, turnSeq int) (canonical.PayloadRefEvent, error) {
	resolved, err := resolvePayloadPath(sessionRoot, ref)
	if err != nil {
		return canonical.PayloadRefEvent{}, fmt.Errorf("payload ref %s: %w", ref.Kind, err)
	}
	origBytes := int64(-1)
	if ref.OriginalBytes != nil {
		origBytes = *ref.OriginalBytes
	}
	storedBytes := int64(0)
	if ref.CompressedBytes != nil {
		storedBytes = *ref.CompressedBytes
	}
	compression := ""
	if ref.Captured {
		compression = ref.Compression
	}
	return canonical.PayloadRefEvent{
		EventBase:       base,
		SessionNativeID: rec.Common.SessionID,
		TurnSeq:         turnSeq,
		OpSeq:           op.OpIndex,
		PayloadKind:     ref.Kind,
		Format:          ref.Format,
		Compression:     compression,
		LocationURI:     resolved.LocationURI,
		OriginalBytes:   origBytes,
		StoredBytes:     storedBytes,
		SHA256:          ref.SHA256,
	}, nil
}

// parseOpTimes returns startUs and endUs for an op, defaulting to the
// record's ts when the op omits its own (per spec §3.4.1 the producer
// always includes both in real data, but the type system marks them
// optional; we defend).
func parseOpTimes(rec record, op opSummary) (int64, int64, error) {
	recTs, err := parseTsToMicros(rec.Common.Ts)
	if err != nil {
		return 0, 0, err
	}
	startUs := recTs
	if op.StartedAt != "" {
		v, perr := parseTsToMicros(op.StartedAt)
		if perr != nil {
			return 0, 0, fmt.Errorf("op.startedAt: %w", perr)
		}
		startUs = v
	}
	endUs := recTs
	if op.EndedAt != "" {
		v, perr := parseTsToMicros(op.EndedAt)
		if perr != nil {
			return 0, 0, fmt.Errorf("op.endedAt: %w", perr)
		}
		endUs = v
	}
	return startUs, endUs, nil
}

func mapTurnStatus(s string) string {
	switch s {
	case "ok":
		return "completed"
	case "failed":
		return "failed"
	case "running":
		return "running"
	default:
		return s
	}
}

func mapOpStatus(s string) string {
	switch s {
	case "ok":
		return "completed"
	case "failed":
		return "failed"
	case "running":
		return "running"
	default:
		return s
	}
}
