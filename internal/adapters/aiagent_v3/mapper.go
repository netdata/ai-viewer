package aiagent_v3

import (
	"fmt"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// subEventBits is the maximum number of sub-events the mapper allocates
// per ledger record. Used to pack (ledgerSeq, subIdx) into a single
// uint64 that is monotonic per file. This is a stable per-event identifier
// (observability counter + log attribution); the ingester does NOT use it
// for ordering or dedup — see ingester.md §Dedup and Idempotency. A fixed
// 12-bit stride bounds the sub-event index per ledger record.
const subEventBits = 12

// maxSubEventsPerRecord is the cap that subEventBits implies. A
// turn_end with > 4095 sub-events would overflow; we cap and surface a
// parse error rather than silently aliasing seq values.
const maxSubEventsPerRecord = 1 << subEventBits // 4096

// errSubEventOverflow signals that a turn_end record produced more
// sub-events than the SourceSeq packing supports. Rare in practice; the
// 99.9th percentile turn has < 20 ops + payloads combined.
var errSubEventOverflow = fmt.Errorf("aiagent_v3: ledger record exceeded %d sub-events", maxSubEventsPerRecord)

// mapRecord converts a parsed v3 record into the slice of canonical
// events the ingester consumes. Pure function; no I/O. The sourceID is
// carried into EventBase for downstream attribution; sessionRoot is the
// configured source root path used for payload URI resolution.
func mapRecord(rec record, sourceID, sessionRoot string) ([]canonical.Event, error) {
	tsUs, err := parseTsToMicros(rec.Common.Ts)
	if err != nil {
		return nil, fmt.Errorf("parse ts: %w", err)
	}
	base := canonical.EventBase{
		SourceID:  sourceID,
		SourceSeq: packSeq(rec.Common.Seq, 0),
		Ts:        tsUs,
	}
	switch rec.Common.RecordType {
	case recSessionStart:
		return mapSessionStart(rec, base), nil
	case recTurnStart:
		return mapTurnStart(rec, base), nil
	case recTurnEnd:
		return mapTurnEnd(rec, base, sessionRoot)
	case recSessionSummary:
		return mapSessionSummary(rec, base, tsUs), nil
	case recSessionError:
		return mapSessionError(rec, base, tsUs), nil
	default:
		// Unreachable: parseLine refuses unknown record types.
		return nil, fmt.Errorf("aiagent_v3: unhandled record type %q", rec.Common.RecordType)
	}
}

// packSeq packs (ledgerSeq, subIdx) into a single uint64 monotonic per
// (source, session). subIdx is 0..maxSubEventsPerRecord-1. Caller MUST
// guard sub-event count against maxSubEventsPerRecord.
func packSeq(ledgerSeq uint64, subIdx uint64) uint64 {
	return ledgerSeq<<subEventBits | (subIdx & (maxSubEventsPerRecord - 1))
}

// parseTsToMicros decodes an RFC3339(/Nano) ISO-8601 timestamp string into
// UNIX microseconds. The v3 producer always writes UTC with millisecond
// precision (`writer.ts:147`); the parser accepts nano precision too.
func parseTsToMicros(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("ts %q: %w", s, err)
	}
	return t.UnixMicro(), nil
}

// headendToKind maps v3 headendId to canonical.SessionKind per spec §5.2.
func headendToKind(headend string) canonical.SessionKind {
	switch headend {
	case "cli", "api", "web", "embed", "slack":
		return canonical.KindRoot
	case "tool_output":
		return canonical.KindToolInternal
	default:
		// sub-agent, history_compaction, and any other future headend
		// fall back to sub_agent — conservative since parentSessionId
		// is set on these in practice.
		return canonical.KindSubAgent
	}
}

func mapSessionStart(rec record, base canonical.EventBase) []canonical.Event {
	body := rec.SessionStart
	extras := make(map[string]any, 5+len(body.Attributes))
	if body.HeadendID != "" {
		extras["headendId"] = body.HeadendID
	}
	if body.CallPath != "" {
		extras["callPath"] = body.CallPath
	}
	if body.ParentOpID != "" {
		extras["parentOpId"] = body.ParentOpID
	}
	// originId is the root session's id; surface it in extras so
	// consumers reading sessions.extras_json can see it without joining
	// to root_native_id (spec §3.1).
	if rec.Common.OriginID != "" {
		extras["originId"] = rec.Common.OriginID
	}
	extras["capturePayloads"] = body.CapturePayloads
	for k, v := range body.Attributes {
		extras["attr."+k] = v
	}
	ev := canonical.SessionStartedEvent{
		EventBase:      base,
		NativeID:       rec.Common.SessionID,
		RootNativeID:   rec.Common.OriginID,
		ParentNativeID: body.ParentSessionID,
		ParentOpKey:    body.ParentOpID,
		Kind:           headendToKind(body.HeadendID),
		AgentName:      body.AgentID,
		CallPath:       body.CallPath,
		Extras:         extras,
	}
	return []canonical.Event{ev}
}

func mapTurnStart(rec record, base canonical.EventBase) []canonical.Event {
	body := rec.TurnStart
	ev := canonical.TurnStartedEvent{
		EventBase:       base,
		SessionNativeID: rec.Common.SessionID,
		Seq:             body.Turn,
	}
	return []canonical.Event{ev}
}

func mapTurnEnd(rec record, base canonical.EventBase, sessionRoot string) ([]canonical.Event, error) {
	body := rec.TurnEnd
	// Reserve sub-event slots: TurnFinalized + per-op (Started + Finalized)
	// + per-payload-ref + per-warning + per-error log entry + per
	// childSessions synthesized SessionStartedEvent. Compute an upper
	// bound first to guard overflow before allocating.
	upper := 1 + len(body.Ops)*2 + len(body.Warnings) + len(body.Errors)
	for _, op := range body.Ops {
		upper += len(op.PayloadRefs)
		upper += len(op.ChildSessions)
	}
	if upper > maxSubEventsPerRecord {
		return nil, errSubEventOverflow
	}
	out := make([]canonical.Event, 0, upper)
	subIdx := uint64(0)
	advance := func() canonical.EventBase {
		nb := base
		nb.SourceSeq = packSeq(rec.Common.Seq, subIdx)
		subIdx++
		return nb
	}

	// Turn-level aggregates from body.Accounting (may be nil for
	// system-only turns; spec §3.4).
	tfin := canonical.TurnFinalizedEvent{
		EventBase:       advance(),
		SessionNativeID: rec.Common.SessionID,
		Seq:             body.Turn,
		Status:          mapTurnStatus(body.Status),
		EndTs:           base.Ts,
	}
	if body.Accounting != nil {
		tfin.TokensIn = body.Accounting.TokensIn
		tfin.TokensOut = body.Accounting.TokensOut
		tfin.TokensCacheRead = body.Accounting.TokensCacheRead
		tfin.TokensCacheWrite = body.Accounting.TokensCacheWrite
		if body.Accounting.CostUSD != nil {
			tfin.CostUSD = *body.Accounting.CostUSD
		}
	}
	out = append(out, tfin)

	// Per-op fan-out (spec §5.3): emit OpStarted + OpFinalized + per-payload
	// PayloadRef. parentOpSeq is always -1 in v3 (intra-turn nesting not
	// exposed by the source). For session-kind ops with childSessions, also
	// synthesize a SessionStartedEvent per child so the ingester can build
	// the parent→child link without depending on the child's own
	// session_start arriving first (spec §5.1, §8.1 parent-side path).
	for i := range body.Ops {
		op := body.Ops[i]
		opEvents, perr := mapOp(rec, op, base.SourceID, sessionRoot, &subIdx)
		if perr != nil {
			return nil, perr
		}
		out = append(out, opEvents...)
		for _, child := range op.ChildSessions {
			out = append(out, synthesizedChildSessionStarted(rec, child, advance()))
		}
	}

	// Warnings / errors as LogEntry rows (spec §9.5).
	for _, msg := range body.Warnings {
		out = append(out, canonical.LogEntryEvent{
			EventBase:       advance(),
			SessionNativeID: rec.Common.SessionID,
			TurnSeq:         body.Turn,
			Severity:        "WRN",
			Source:          Format,
			Message:         msg,
		})
	}
	for _, msg := range body.Errors {
		out = append(out, canonical.LogEntryEvent{
			EventBase:       advance(),
			SessionNativeID: rec.Common.SessionID,
			TurnSeq:         body.Turn,
			Severity:        "ERR",
			Source:          Format,
			Message:         msg,
		})
	}
	return out, nil
}

// synthesizedChildSessionStarted builds a SessionStartedEvent for a
// child session referenced from a parent record's childSessions[]. The
// ingester upserts on NativeID so the child's own session_start (when
// it later arrives) reconciles with this synthesized event. Per spec
// §5.1 / §8.1 this is the parent-side resolver path that protects the
// 3.2% of sub-agent sessions whose own ledger lacks parentSessionId.
func synthesizedChildSessionStarted(rec record, child childSessionRef, base canonical.EventBase) canonical.SessionStartedEvent {
	rootID := child.OriginID
	if rootID == "" {
		rootID = rec.Common.OriginID
	}
	extras := map[string]any{
		"synthesizedFromParent": true,
	}
	if child.LedgerPath != "" {
		extras["ledgerPath"] = child.LedgerPath
	}
	if child.Status != "" {
		extras["status"] = child.Status
	}
	if child.CallPath != "" {
		extras["callPath"] = child.CallPath
	}
	if rec.Common.OriginID != "" {
		extras["originId"] = rec.Common.OriginID
	}
	return canonical.SessionStartedEvent{
		EventBase:      base,
		NativeID:       child.SessionID,
		RootNativeID:   rootID,
		ParentNativeID: rec.Common.SessionID,
		ParentOpKey:    child.ParentOpID,
		Kind:           canonical.KindSubAgent,
		AgentName:      child.AgentID,
		CallPath:       child.CallPath,
		Extras:         extras,
	}
}

func mapSessionSummary(rec record, base canonical.EventBase, tsUs int64) []canonical.Event {
	body := rec.SessionSummary
	status := canonical.StatusCompleted
	if body.Status == "failed" {
		status = canonical.StatusFailed
	}
	// subIdx counts SUB-events strictly after the SessionFinalized at
	// SourceSeq=packSeq(seq, 0). Replaces the prior hardcoded 1/2
	// allocations so additions (e.g. synthesized children) stay monotone.
	subIdx := uint64(1)
	advance := func() canonical.EventBase {
		nb := base
		nb.SourceSeq = packSeq(rec.Common.Seq, subIdx)
		subIdx++
		return nb
	}
	events := []canonical.Event{
		canonical.SessionFinalizedEvent{
			EventBase:    base,
			NativeID:     rec.Common.SessionID,
			Status:       status,
			ErrorMessage: body.Error,
			EndTs:        tsUs,
		},
	}
	// If finalReport metadata appears, surface it via a SessionUpdated
	// event so the canonical row's extras_json receives the format /
	// captured flags (spec §10 gap 3).
	if body.FinalReport != nil {
		extras := map[string]any{
			"finalReport.captured": body.FinalReport.Captured,
		}
		if body.FinalReport.Format != "" {
			extras["finalReport.format"] = body.FinalReport.Format
		}
		events = append(events, canonical.SessionUpdatedEvent{
			EventBase: advance(),
			NativeID:  rec.Common.SessionID,
			Extras:    extras,
		})
	}
	if body.Status == "failed" && body.Error != "" {
		events = append(events, canonical.LogEntryEvent{
			EventBase:       advance(),
			SessionNativeID: rec.Common.SessionID,
			Severity:        "ERR",
			Source:          Format,
			Message:         body.Error,
		})
	}
	// Synthesize SessionStartedEvent for every child listed in the
	// summary (spec §5.1). The summary deduplicates per-turn child refs,
	// so this covers any child whose parent op-level ref might have been
	// missed (rare/edge per spec §3.5). The ingester upserts on NativeID
	// so this is idempotent vs the child's own session_start.
	for _, child := range body.ChildSessions {
		events = append(events, synthesizedChildSessionStarted(rec, child, advance()))
	}
	return events
}

func mapSessionError(rec record, base canonical.EventBase, tsUs int64) []canonical.Event {
	body := rec.SessionError
	return []canonical.Event{
		canonical.SessionFinalizedEvent{
			EventBase:    base,
			NativeID:     rec.Common.SessionID,
			Status:       canonical.StatusFailed,
			ErrorClass:   "session_error",
			ErrorMessage: body.Error,
			EndTs:        tsUs,
		},
		canonical.LogEntryEvent{
			EventBase: canonical.EventBase{
				SourceID:  base.SourceID,
				SourceSeq: packSeq(rec.Common.Seq, 1),
				Ts:        tsUs,
			},
			SessionNativeID: rec.Common.SessionID,
			Severity:        "ERR",
			Source:          Format,
			Message:         body.Error,
		},
	}
}
