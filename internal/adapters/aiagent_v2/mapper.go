package aiagent_v2

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// maxChildSessionDepth caps recursive opTree descent so a pathological
// snapshot cannot blow the stack or fill memory unbounded. Real data
// observed up to depth ~6; 32 leaves wide headroom while still bounding
// adversarial inputs. Exceed yields a SourceErrorEvent and the descent
// for that subtree stops.
const maxChildSessionDepth = 32

// stepIndexOffset shifts step indices into a high integer band so they
// never collide with turn indices on the canonical `turns.seq` column.
// See `adapter-aiagent-v2.md` §Canonical Model Gaps item 2.
const stepIndexOffset = 10000

// mapContext carries the per-file invariants every recursive emit
// needs. It is constructed once per snapshot and threaded through the
// walk; map-internal mutation is confined to err which surfaces a
// single fatal-per-file condition (e.g. depth overflow on the root).
type mapContext struct {
	sourceID string
	originID string
	filename string
	// sessionsRoot is the filesystem root for the v2 source (the same
	// directory `Adapter.root` points at). Used to resolve relative
	// `payload.ref.path` values against the canonical sessions tree
	// with the standard traversal guard. Empty string disables payload
	// ref resolution (tests that don't care can pass "").
	sessionsRoot string
	rootTs       int64
	rootTrace    string
}

// mapSnapshot converts a full opTree snapshot into the canonical event
// stream the ingester consumes. The function is pure: no I/O, no
// goroutines. Events emerge in opTree depth-first order so the
// canonical-events.md "chronological within a session" guarantee holds.
//
// onError surfaces non-fatal per-record conditions (depth cap exceeded,
// op time parse hiccups). Returns a fatal error only when the envelope
// itself is unusable.
func mapSnapshot(snap snapshot, sourceID, originID, sessionsRoot, filename string, onError func(error)) []canonical.Event {
	ctx := mapContext{
		sourceID:     sourceID,
		originID:     originID,
		filename:     filename,
		sessionsRoot: sessionsRoot,
		rootTs:       msToMicros(snap.OpTree.StartedAt),
		rootTrace:    snap.OpTree.TraceID,
	}
	out := make([]canonical.Event, 0, 64)
	mapSession(&ctx, snap.OpTree, "", "", canonical.KindRoot, snap.Version, 0, &out, onError)
	return out
}

// mapSession emits the events for one session subtree. depth=0 is the
// root session; recursion happens via `op.childSession` and increments
// depth. The path string is the stable per-session-tree path used to
// derive the SourceSeq via FNV-64; together with `originId` it
// uniquely identifies every emitted event across rescans.
//
// parentNativeID is empty for the root session; otherwise it is the
// parent's traceId. parentOpKey is the parent op's opId when this is a
// recursive descent from `op.childSession`.
func mapSession(ctx *mapContext, node opTree, parentNativeID, parentOpKey string, kind canonical.SessionKind, version, depth int, out *[]canonical.Event, onError func(error)) {
	if depth > maxChildSessionDepth {
		onError(fmt.Errorf("aiagent_v2: child session depth %d exceeds cap %d (file %s, trace %s)", depth, maxChildSessionDepth, ctx.filename, node.TraceID))
		return
	}
	sessionPath := node.TraceID
	startedTs := msToMicros(node.StartedAt)
	if startedTs == 0 {
		startedTs = ctx.rootTs
	}

	// rootNative inherits from ctx on recursive descents (every level
	// in a v2 file shares the same originId-derived root) and is the
	// node's own traceId at the file root.
	rootNative := node.TraceID
	if parentNativeID != "" {
		rootNative = ctx.rootTrace
	}

	*out = append(*out, buildSessionStarted(ctx, node, parentNativeID, parentOpKey, kind, rootNative, version, sessionPath, startedTs))

	// Turns first (sorted by their natural index), then steps. Within
	// each, ops are emitted depth-first; embedded child sessions emerge
	// at the recursion point.
	mapTurns(ctx, node, depth, sessionPath, out, onError)
	mapSteps(ctx, node, depth, sessionPath, out, onError)

	// Session finalization is determined by `endedAt` + `success`.
	if node.EndedAt != nil {
		*out = append(*out, buildSessionFinalized(ctx, node, sessionPath))
	}
	if node.Success != nil && !*node.Success && node.Error != "" {
		// Surface the free-text error string as a session-scoped ERR log
		// so the UI's Logs tab carries the message even though the
		// SessionFinalized row's ErrorMessage already has it.
		*out = append(*out, canonical.LogEntryEvent{
			EventBase:       baseEvent(ctx, sessionPath+"::sessionError", endTsOrStarted(node)),
			SessionNativeID: node.TraceID,
			Severity:        "ERR",
			Source:          Format,
			Message:         node.Error,
		})
	}
}

// mapTurns walks node.Turns in stable index order and emits canonical
// events for each.
func mapTurns(ctx *mapContext, node opTree, depth int, sessionPath string, out *[]canonical.Event, onError func(error)) {
	for i := range node.Turns {
		t := node.Turns[i]
		turnPath := fmt.Sprintf("%s::T:%d", sessionPath, t.Index)
		turnStart := msToMicros(t.StartedAt)
		if turnStart == 0 {
			turnStart = ctx.rootTs
		}
		*out = append(*out, canonical.TurnStartedEvent{
			EventBase:       baseEvent(ctx, turnPath+"::start", turnStart),
			SessionNativeID: node.TraceID,
			Seq:             t.Index,
		})
		var (
			turnTokensIn, turnTokensOut   int64
			turnCacheRead, turnCacheWrite int64
			turnCostUSD                   float64
		)
		for j := range t.Ops {
			op := t.Ops[j]
			opPath := fmt.Sprintf("%s::O:%d:%s", turnPath, j, op.OpID)
			tIn, tOut, tCacheR, tCacheW, cost := mapOp(ctx, op, node.TraceID, t.Index, j, depth, opPath, out, onError)
			turnTokensIn += tIn
			turnTokensOut += tOut
			turnCacheRead += tCacheR
			turnCacheWrite += tCacheW
			turnCostUSD += cost
		}
		if t.EndedAt != nil {
			*out = append(*out, canonical.TurnFinalizedEvent{
				EventBase:        baseEvent(ctx, turnPath+"::end", msToMicros(*t.EndedAt)),
				SessionNativeID:  node.TraceID,
				Seq:              t.Index,
				Status:           turnStatusFromOps(t.Ops),
				EndTs:            msToMicros(*t.EndedAt),
				TokensIn:         turnTokensIn,
				TokensOut:        turnTokensOut,
				TokensCacheRead:  turnCacheRead,
				TokensCacheWrite: turnCacheWrite,
				CostUSD:          turnCostUSD,
			})
		}
	}
}

// mapSteps walks node.Steps and emits a canonical turn per step using
// a reserved offset so step seqs do not collide with turn seqs. The
// step's kind lives in `extras_json.step_kind` on the produced events
// (carried on a SessionUpdatedEvent that lands once per step run).
func mapSteps(ctx *mapContext, node opTree, depth int, sessionPath string, out *[]canonical.Event, onError func(error)) {
	for i := range node.Steps {
		s := node.Steps[i]
		stepSeq := stepIndexOffset + s.Index
		stepPath := fmt.Sprintf("%s::S:%d", sessionPath, s.Index)
		stepStart := msToMicros(s.StartedAt)
		if stepStart == 0 {
			stepStart = ctx.rootTs
		}
		*out = append(*out, canonical.TurnStartedEvent{
			EventBase:       baseEvent(ctx, stepPath+"::start", stepStart),
			SessionNativeID: node.TraceID,
			Seq:             stepSeq,
		})
		// Surface step_kind via SessionUpdatedEvent extras so the
		// ingester writes it into sessions.extras_json once per step
		// observed.
		if s.Kind != "" {
			*out = append(*out, canonical.SessionUpdatedEvent{
				EventBase: baseEvent(ctx, stepPath+"::kind", stepStart),
				NativeID:  node.TraceID,
				Extras: map[string]any{
					"step." + fmt.Sprintf("%d", s.Index) + ".kind": s.Kind,
				},
			})
		}
		var (
			stepTokensIn, stepTokensOut   int64
			stepCacheRead, stepCacheWrite int64
			stepCostUSD                   float64
		)
		for j := range s.Ops {
			op := s.Ops[j]
			opPath := fmt.Sprintf("%s::O:%d:%s", stepPath, j, op.OpID)
			tIn, tOut, tCacheR, tCacheW, cost := mapOp(ctx, op, node.TraceID, stepSeq, j, depth, opPath, out, onError)
			stepTokensIn += tIn
			stepTokensOut += tOut
			stepCacheRead += tCacheR
			stepCacheWrite += tCacheW
			stepCostUSD += cost
		}
		if s.EndedAt != nil {
			*out = append(*out, canonical.TurnFinalizedEvent{
				EventBase:        baseEvent(ctx, stepPath+"::end", msToMicros(*s.EndedAt)),
				SessionNativeID:  node.TraceID,
				Seq:              stepSeq,
				Status:           turnStatusFromOps(s.Ops),
				EndTs:            msToMicros(*s.EndedAt),
				TokensIn:         stepTokensIn,
				TokensOut:        stepTokensOut,
				TokensCacheRead:  stepCacheRead,
				TokensCacheWrite: stepCacheWrite,
				CostUSD:          stepCostUSD,
			})
		}
	}
}

// mapOp emits the OpStarted + OpFinalized + per-log entry + recursive
// child session for a single op. Returns the op's token / cost
// contribution so the caller can roll them up into the turn or step
// finalize event.
func mapOp(ctx *mapContext, op operationNode, sessionTrace string, turnSeq, opSeq, depth int, opPath string, out *[]canonical.Event, onError func(error)) (int64, int64, int64, int64, float64) {
	startUs := msToMicros(op.StartedAt)
	if startUs == 0 {
		startUs = ctx.rootTs
	}
	endUs := startUs
	if op.EndedAt != nil {
		endUs = msToMicros(*op.EndedAt)
	}

	kind := mapOpKind(op.Kind)

	started := canonical.OpStartedEvent{
		EventBase:       baseEvent(ctx, opPath+"::start", startUs),
		SessionNativeID: sessionTrace,
		TurnSeq:         turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            kind,
		Name:            attrString(op.Attributes, "name"),
		Provider:        attrString(op.Attributes, "provider"),
		Model:           attrString(op.Attributes, "model"),
		Extras:          opStartedExtras(op),
	}
	if kind == canonical.OpTool {
		started.ToolNamespace = attrString(op.Attributes, "provider")
	}
	if op.ChildSession != nil {
		started.ChildSessionNativeID = op.ChildSession.TraceID
	} else if op.ChildSessionRef != nil {
		started.ChildSessionNativeID = op.ChildSessionRef.SessionID
	}
	*out = append(*out, started)

	finalized := buildOpFinalized(ctx, op, sessionTrace, turnSeq, opSeq, endUs, opPath)
	*out = append(*out, finalized)

	// Payload refs: when the producer wrote `op.request.payload.ref` or
	// `op.response.payload.ref`, surface a PayloadRefEvent so the
	// presenter can resolve the artifact on demand. Inline payloads
	// (no ref) are deferred — see spec §Canonical Model Gaps item 10.
	emitPayloadRefs(ctx, op, sessionTrace, turnSeq, opSeq, kind, opPath, out, onError)

	// Reasoning: an LLM op with `reasoning.final` set spawns a nested
	// reasoning op (canonical OpReasoning) that hangs off the LLM op
	// so the UI can render the reasoning span under its parent without
	// inflating per-op accounting. The reasoning op carries no tokens
	// of its own — its accounting is owned by the LLM op.
	if kind == canonical.OpLLM && op.Reasoning != nil && op.Reasoning.Final != "" {
		emitReasoningOp(ctx, op, sessionTrace, turnSeq, opSeq, startUs, endUs, opPath, out)
	}

	// Emit log entries attached to this op.
	for li := range op.Logs {
		l := op.Logs[li]
		ts := msToMicros(l.Timestamp)
		if ts == 0 {
			ts = endUs
		}
		*out = append(*out, canonical.LogEntryEvent{
			EventBase:       baseEvent(ctx, fmt.Sprintf("%s::log:%d", opPath, li), ts),
			SessionNativeID: sessionTrace,
			TurnSeq:         turnSeq,
			OpSeq:           opSeq,
			Severity:        normaliseSeverity(l.Severity),
			Source:          Format,
			Message:         l.Message,
			Extras:          logExtras(l),
		})
	}

	// Failed op with error attribute → severity ERR log so the UI's
	// Logs tab surfaces the failure message.
	if op.Status == "failed" {
		if msg := attrString(op.Attributes, "error"); msg != "" {
			*out = append(*out, canonical.LogEntryEvent{
				EventBase:       baseEvent(ctx, opPath+"::failure", endUs),
				SessionNativeID: sessionTrace,
				TurnSeq:         turnSeq,
				OpSeq:           opSeq,
				Severity:        "ERR",
				Source:          Format,
				Message:         msg,
			})
		}
	}

	// Recurse into embedded child session (the v2 sub-agent pattern).
	if op.ChildSession != nil {
		mapSession(ctx, *op.ChildSession, sessionTrace, op.OpID, canonical.KindSubAgent, 0, depth+1, out, onError)
	}

	return finalized.TokensIn, finalized.TokensOut, finalized.TokensCacheRead, finalized.TokensCacheWrite, finalized.CostUSD
}

// buildOpFinalized assembles the OpFinalizedEvent with accounting +
// payload sizes pulled from the op's first accounting entry and its
// request/response sizes.
func buildOpFinalized(ctx *mapContext, op operationNode, sessionTrace string, turnSeq, opSeq int, endUs int64, opPath string) canonical.OpFinalizedEvent {
	ev := canonical.OpFinalizedEvent{
		EventBase:       baseEvent(ctx, opPath+"::end", endUs),
		SessionNativeID: sessionTrace,
		TurnSeq:         turnSeq,
		Seq:             opSeq,
		Status:          mapOpStatus(op.Status),
		ErrorClass:      attrString(op.Attributes, "error"),
		EndTs:           endUs,
	}
	if len(op.Accounting) > 0 {
		applyAccounting(&ev, op.Accounting[0])
	}
	if op.Request != nil {
		ev.BytesIn = op.Request.Size
	}
	if op.Response != nil {
		ev.BytesOut = op.Response.Size
	}
	// Tool ops use char-count accounting; surface CharsIn/CharsOut
	// alongside whatever byte counts the request/response sizes carry.
	if mapOpKind(op.Kind) == canonical.OpTool && len(op.Accounting) > 0 {
		acc := op.Accounting[0]
		if acc.CharactersIn > 0 {
			ev.CharsIn = acc.CharactersIn
		}
		if acc.CharactersOut > 0 {
			ev.CharsOut = acc.CharactersOut
		}
	}
	return ev
}

// applyAccounting copies an llm-type accounting entry into the op
// finalize event. Tool entries route through CharsIn/CharsOut in the
// caller; the token / cost fields here only fire for llm rows.
//
// CtxUsed represents the total context-window tokens consumed by the
// call: input prompt tokens + cache reads (which the model still has
// to materialise in-context) + output tokens (which occupy the
// context as they are streamed back). Matches the canonical-events
// definition of "tokens that occupied the model's context at any
// point during the call" and aligns with how the same accounting
// rolls up at the turn/session level.
func applyAccounting(ev *canonical.OpFinalizedEvent, acc accountingEntry) {
	if acc.Type != "llm" || acc.Tokens == nil {
		return
	}
	ev.TokensIn = acc.Tokens.InputTokens
	ev.TokensOut = acc.Tokens.OutputTokens
	ev.TokensCacheRead = acc.Tokens.CacheReadInputTokens + acc.Tokens.CachedTokens
	ev.TokensCacheWrite = acc.Tokens.CacheWriteInputTokens
	ev.CostUSD = acc.CostUSD
	ev.CtxUsed = acc.Tokens.InputTokens + ev.TokensCacheRead + acc.Tokens.OutputTokens
}

// opStartedExtras builds the map for OpStartedEvent.Extras carrying
// adapter-specific signals (latency, cache token counts, original kind
// before mapping system→system).
func opStartedExtras(op operationNode) map[string]any {
	out := map[string]any{}
	if op.Kind != "" {
		out["original_kind"] = op.Kind
	}
	if op.Reasoning != nil && op.Reasoning.Final != "" {
		out["reasoning.final"] = op.Reasoning.Final
	}
	if op.Reasoning != nil && op.Reasoning.ChunkCount > 0 {
		out["reasoning.chunkCount"] = op.Reasoning.ChunkCount
	}
	if op.ChildSessionRef != nil {
		out["childSessionRef"] = op.ChildSessionRef.SessionID
	}
	if op.ChildSessionSummary != nil {
		out["childSessionSummary"] = op.ChildSessionSummary
	}
	if len(op.Accounting) > 0 {
		acc := op.Accounting[0]
		if acc.Tokens != nil && (acc.Tokens.CacheReadInputTokens > 0 || acc.Tokens.CachedTokens > 0) {
			out["tokensCacheRead"] = acc.Tokens.CacheReadInputTokens + acc.Tokens.CachedTokens
		}
		if acc.Tokens != nil && acc.Tokens.CacheWriteInputTokens > 0 {
			out["tokensCacheWrite"] = acc.Tokens.CacheWriteInputTokens
		}
	}
	return out
}

func logExtras(l logEntry) map[string]any {
	if l.Path == "" {
		return nil
	}
	return map[string]any{"path": l.Path}
}

// buildSessionStarted constructs the SessionStartedEvent for one
// session node. extras_json carries the v2-only metadata that the
// canonical schema does not have first-class fields for.
func buildSessionStarted(ctx *mapContext, node opTree, parentNativeID, parentOpKey string, kind canonical.SessionKind, rootNative string, version int, sessionPath string, startedTs int64) canonical.SessionStartedEvent {
	extras := map[string]any{
		"version":      version,
		"filename":     ctx.filename,
		"originId":     ctx.originID,
		"sessionTitle": node.SessionTitle,
		"latestStatus": node.LatestStatus,
	}
	if node.CallPath != "" {
		extras["callPath"] = node.CallPath
	}
	if node.ID != "" {
		extras["nodeId"] = node.ID
	}
	if len(node.Totals) > 0 {
		extras["totals"] = node.Totals
	}
	if len(node.FinalReport) > 0 {
		extras["final_report"] = node.FinalReport
	}
	if len(node.PluginMetas) > 0 {
		extras["plugin_metas"] = node.PluginMetas
	}
	if node.TraceID != ctx.originID && parentNativeID == "" {
		// Diagnostic per `adapter-aiagent-v2.md` §Edge Cases item 9.
		extras["filename_originid_mismatch"] = true
	}
	// Pre-pass for the session's model. v2 is a full-snapshot format,
	// so the first LLM op (DFS through turns/steps/childSessions) is
	// discoverable at the time we build the SessionStarted. Avoids
	// shipping a follow-up SessionUpdated for the common case where
	// the snapshot already contains at least one LLM op.
	return canonical.SessionStartedEvent{
		EventBase:      baseEvent(ctx, sessionPath+"::start", startedTs),
		NativeID:       node.TraceID,
		RootNativeID:   rootNative,
		ParentNativeID: parentNativeID,
		ParentOpKey:    parentOpKey,
		Kind:           kind,
		AgentName:      node.AgentID,
		Model:          firstLLMModel(node),
		CallPath:       node.CallPath,
		Extras:         extras,
	}
}

// buildSessionFinalized emits the terminal SessionFinalized for a
// session that has an `endedAt`. Status is derived from `success` and
// `error` per `adapter-aiagent-v2.md`.
func buildSessionFinalized(ctx *mapContext, node opTree, sessionPath string) canonical.SessionFinalizedEvent {
	endTs := endTsOrStarted(node)
	status := mapSessionStatus(node)
	return canonical.SessionFinalizedEvent{
		EventBase:    baseEvent(ctx, sessionPath+"::end", endTs),
		NativeID:     node.TraceID,
		Status:       status,
		ErrorMessage: node.Error,
		EndTs:        endTs,
	}
}

// mapSessionStatus encodes the four-way decision tree from the spec.
// Both success and endedAt absent → in-progress (Running). Success
// true → Completed. Success false (with or without error) → Failed.
// EndedAt set with no success indicator → Interrupted.
func mapSessionStatus(node opTree) canonical.SessionStatus {
	if node.Success != nil {
		if *node.Success {
			return canonical.StatusCompleted
		}
		return canonical.StatusFailed
	}
	if node.EndedAt != nil {
		// Process exited mid-turn — interrupted per spec.
		return canonical.StatusInterrupted
	}
	if len(node.Turns) == 0 && len(node.Steps) == 0 {
		return canonical.StatusAbandoned
	}
	return canonical.StatusRunning
}

// mapOpKind translates source op kind strings to canonical OpKind.
// `system` lands on OpSystem; `session` on OpSession; `tool` on OpTool;
// `llm` on OpLLM. Unknown kinds fall through unchanged so a future
// producer addition is visible rather than silently re-mapped.
func mapOpKind(s string) canonical.OpKind {
	switch s {
	case "llm":
		return canonical.OpLLM
	case "tool":
		return canonical.OpTool
	case "session":
		return canonical.OpSession
	case "system":
		return canonical.OpSystem
	default:
		return canonical.OpKind(s)
	}
}

// mapOpStatus normalises op terminal status. v2 only writes `ok` or
// `failed`; absent status means running (the op hasn't finished yet in
// the snapshot we read).
func mapOpStatus(s string) string {
	switch s {
	case "ok":
		return "completed"
	case "failed":
		return "failed"
	case "":
		return "running"
	default:
		return s
	}
}

// turnStatusFromOps derives a turn-level status from its constituent
// ops. failed if any op failed; completed if every op completed;
// running otherwise.
func turnStatusFromOps(ops []operationNode) string {
	if len(ops) == 0 {
		return "completed"
	}
	allCompleted := true
	for i := range ops {
		switch ops[i].Status {
		case "failed":
			return "failed"
		case "ok":
			continue
		default:
			allCompleted = false
		}
	}
	if allCompleted {
		return "completed"
	}
	return "running"
}

// attrString pulls a JSON-encoded string value out of the attribute
// map. Returns "" when the key is absent or the value is not a string.
func attrString(attrs map[string]json.RawMessage, key string) string {
	raw, ok := attrs[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// normaliseSeverity maps producer severity codes onto the canonical
// four-level scale (DBG/INF/WRN/ERR). Unknown inputs default to INF so
// the UI still shows the log row.
func normaliseSeverity(s string) string {
	switch strings.ToUpper(s) {
	case "VRB", "DBG", "DEBUG":
		return "DBG"
	case "INF", "INFO":
		return "INF"
	case "WRN", "WARN", "WARNING":
		return "WRN"
	case "ERR", "ERROR", "FATAL":
		return "ERR"
	default:
		return "INF"
	}
}

// msToMicros converts a producer millisecond timestamp into canonical
// UNIX microseconds. Returns 0 when the input is non-positive so
// callers can detect "unset" and substitute the snapshot's root ts.
func msToMicros(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms * 1000
}

// endTsOrStarted returns endedAt (when set) or startedAt as the
// timestamp for a session-level event. Used for the SessionFinalized's
// Ts so a session with only startedAt still produces a coherent event.
func endTsOrStarted(node opTree) int64 {
	if node.EndedAt != nil {
		return msToMicros(*node.EndedAt)
	}
	return msToMicros(node.StartedAt)
}

// baseEvent constructs the canonical EventBase for an event whose
// per-file-path identity is `path`. SourceSeq is FNV-64 of
// `originId::path` — a deterministic, stable-across-rescans per-node
// identifier (observability counter; NOT a dedup gate). Re-emission on
// rescan is absorbed by the ingester's SQL-layer idempotent upserts,
// which key on each table's natural identity, not on SourceSeq. See
// ingester.md §Dedup and Idempotency.
func baseEvent(ctx *mapContext, path string, ts int64) canonical.EventBase {
	return canonical.EventBase{
		SourceID:  ctx.sourceID,
		SourceSeq: seqForPath(ctx.originID, path),
		Ts:        ts,
	}
}

// seqForPath returns a stable 63-bit SourceSeq derived from the
// (originId, path) tuple. FNV-64 chosen over xxhash for stdlib-only
// dependency footprint; collision probability across one source's
// events is negligible at this scale (294K files × ~thousand events
// each ≪ 2^63).
func seqForPath(originID, path string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(originID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(path))
	// Mask off the sign bit so any downstream conversion to int64 stays
	// positive without losing meaningful entropy.
	return h.Sum64() & 0x7FFFFFFFFFFFFFFF
}

// payloadRef mirrors the v3 EvidencePayloadRef shape inside a v2
// `op.request.payload` / `op.response.payload`. Newer v2 producers (the
// transitional cohort that wrote both ledger and snapshot) embed
// `{ ref, format, compression, originalBytes, storedBytes, sha256 }`
// instead of the inline payload body. Fields that are absent on the
// wire stay at their zero values; the caller treats Path=="" as
// "no ref present".
type payloadRef struct {
	Ref           string `json:"ref"`
	Path          string `json:"path,omitempty"`
	Format        string `json:"format,omitempty"`
	Compression   string `json:"compression,omitempty"`
	OriginalBytes int64  `json:"originalBytes,omitempty"`
	StoredBytes   int64  `json:"storedBytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
}

// extractPayloadRef decodes an op's request/response `payload` raw JSON
// into a payloadRef when the object carries `ref` and `path` (the v2
// ref-form). Returns ok=false when the payload is absent, inline,
// or otherwise not a ref descriptor. Defensive against malformed
// payloads — a JSON decode failure means "not a ref", not a parse
// error to surface.
func extractPayloadRef(raw json.RawMessage) (payloadRef, bool) {
	if len(raw) == 0 {
		return payloadRef{}, false
	}
	// Cheap probe: a ref descriptor is always a JSON object whose first
	// non-space byte is `{`. Skip the work otherwise (inline arrays,
	// strings, base64 blobs).
	for _, b := range raw {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		if b != '{' {
			return payloadRef{}, false
		}
		break
	}
	var ref payloadRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return payloadRef{}, false
	}
	// Treat `ref` as the canonical key; `path` is the resolvable
	// filesystem relative path. Older shapes may use one and not the
	// other — accept either as long as a path is available.
	if ref.Path == "" {
		ref.Path = ref.Ref
	}
	if ref.Path == "" {
		return payloadRef{}, false
	}
	return ref, true
}

// resolvePayloadPath joins root + ref.Path with the same traversal
// guard the v3 adapter uses (mirrors aiagent_v3/payloads.go:25-52).
// Returns ("file://<abs>", nil) on success or ("", err) when the
// cleaned path escapes the root.
func resolvePayloadPath(root, refPath string) (string, error) {
	if root == "" || refPath == "" {
		return "", nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("aiagent_v2: resolve root %q: %w", root, err)
	}
	cleanedRoot := filepath.Clean(absRoot)
	// Producer paths use forward slashes (path.posix.join in
	// ai-agent.git/src/paths.ts). Normalise to the host separator
	// before joining.
	relParts := strings.Split(refPath, "/")
	joined := filepath.Join(append([]string{cleanedRoot}, relParts...)...)
	cleaned := filepath.Clean(joined)
	rel, err := filepath.Rel(cleanedRoot, cleaned)
	if err != nil {
		return "", fmt.Errorf("aiagent_v2: relative %q: %w", refPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("aiagent_v2: payload path escapes root: %q", refPath)
	}
	return "file://" + filepath.ToSlash(cleaned), nil
}

// payloadKindForSide returns the canonical PayloadKind string for the
// (op-kind, side) pair. v2 only distinguishes llm vs tool at the op
// level; SDK and reasoning streams have no v2 equivalent.
func payloadKindForSide(kind canonical.OpKind, side string) string {
	if kind == canonical.OpTool {
		if side == "request" {
			return "tool_request"
		}
		return "tool_response"
	}
	if side == "request" {
		return "llm_request"
	}
	return "llm_response"
}

// emitPayloadRefs surfaces PayloadRefEvent for each side of the op
// (request and response) that carries a ref-form payload. When a side
// is inline (no ref), the event is skipped silently — inline-payload
// extraction is deferred per spec §Canonical Model Gaps item 10.
// A traversal-guard rejection produces a SourceErrorEvent via onError;
// the rest of the op emission continues.
func emitPayloadRefs(ctx *mapContext, op operationNode, sessionTrace string, turnSeq, opSeq int, kind canonical.OpKind, opPath string, out *[]canonical.Event, onError func(error)) {
	if op.Request != nil {
		if ref, ok := extractPayloadRef(op.Request.Payload); ok {
			appendPayloadRef(ctx, ref, sessionTrace, turnSeq, opSeq, payloadKindForSide(kind, "request"), opPath+"::payload:request", op.Request.Size, out, onError)
		}
	}
	if op.Response != nil {
		if ref, ok := extractPayloadRef(op.Response.Payload); ok {
			appendPayloadRef(ctx, ref, sessionTrace, turnSeq, opSeq, payloadKindForSide(kind, "response"), opPath+"::payload:response", op.Response.Size, out, onError)
		}
	}
}

// appendPayloadRef builds and appends a PayloadRefEvent for one
// resolved ref. Path-escape rejections become non-fatal onError calls
// so a single malformed ref cannot poison the rest of the file.
func appendPayloadRef(ctx *mapContext, ref payloadRef, sessionTrace string, turnSeq, opSeq int, payloadKind, path string, fallbackBytes int64, out *[]canonical.Event, onError func(error)) {
	location, err := resolvePayloadPath(ctx.sessionsRoot, ref.Path)
	if err != nil {
		onError(err)
		return
	}
	origBytes := ref.OriginalBytes
	if origBytes == 0 {
		// Producer occasionally omits originalBytes on the ref but the
		// op-level request/response.size carries the same number. Fall
		// back so the event is still useful for size aggregation.
		origBytes = fallbackBytes
	}
	*out = append(*out, canonical.PayloadRefEvent{
		EventBase:       baseEvent(ctx, path, msToMicrosOrFallback(0, ctx.rootTs)),
		SessionNativeID: sessionTrace,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		PayloadKind:     payloadKind,
		Format:          ref.Format,
		Compression:     ref.Compression,
		LocationURI:     location,
		OriginalBytes:   origBytes,
		StoredBytes:     ref.StoredBytes,
		SHA256:          ref.SHA256,
	})
}

// emitReasoningOp surfaces an OpStarted/OpFinalized pair representing
// the model's reasoning span as a nested op of the parent LLM op. v2
// stores only the reasoning summary (`reasoning.final`); the raw
// chain-of-thought stream is codex-specific. The reasoning op itself
// carries no token accounting — it inherits its span from the LLM
// op's [startUs, endUs] window because v2 does not time reasoning
// separately. ParentOpSeq pins the nesting so the UI renders the
// reasoning under its LLM op.
func emitReasoningOp(ctx *mapContext, op operationNode, sessionTrace string, turnSeq, parentOpSeq int, startUs, endUs int64, opPath string, out *[]canonical.Event) {
	reasoningPath := opPath + "::reasoning"
	*out = append(*out, canonical.OpStartedEvent{
		EventBase:       baseEvent(ctx, reasoningPath+"::start", startUs),
		SessionNativeID: sessionTrace,
		TurnSeq:         turnSeq,
		Seq:             parentOpSeq,
		ParentOpSeq:     parentOpSeq,
		Kind:            canonical.OpReasoning,
		ReasoningKind:   "summary",
		Extras: map[string]any{
			"reasoning.final": op.Reasoning.Final,
		},
	})
	*out = append(*out, canonical.OpFinalizedEvent{
		EventBase:       baseEvent(ctx, reasoningPath+"::end", endUs),
		SessionNativeID: sessionTrace,
		TurnSeq:         turnSeq,
		Seq:             parentOpSeq,
		Status:          "completed",
		EndTs:           endUs,
	})
}

// firstLLMModel walks the opTree depth-first and returns the first
// `attributes.model` it finds on an LLM op. v2 sets the model on the
// op's attribute bag (and replicates it on the accounting entry); we
// prefer the attribute so the model is discoverable even on ops that
// failed before producing an accounting row. Returns "" when no LLM
// op carries a model.
func firstLLMModel(node opTree) string {
	for i := range node.Turns {
		if m := firstLLMModelFromOps(node.Turns[i].Ops); m != "" {
			return m
		}
	}
	for i := range node.Steps {
		if m := firstLLMModelFromOps(node.Steps[i].Ops); m != "" {
			return m
		}
	}
	return ""
}

func firstLLMModelFromOps(ops []operationNode) string {
	for i := range ops {
		op := ops[i]
		if op.Kind == "llm" {
			if m := attrString(op.Attributes, "model"); m != "" {
				return m
			}
			// Fallback to accounting entry when attributes omit model.
			if len(op.Accounting) > 0 && op.Accounting[0].Model != "" {
				return op.Accounting[0].Model
			}
		}
		if op.ChildSession != nil {
			if m := firstLLMModel(*op.ChildSession); m != "" {
				return m
			}
		}
	}
	return ""
}

// msToMicrosOrFallback returns msToMicros(ms) when ms is positive,
// otherwise the provided fallback. Used for PayloadRefEvent timestamps
// where the producer does not record a per-payload time and the
// surrounding op's start/end is the best signal.
func msToMicrosOrFallback(ms, fallback int64) int64 {
	if ms <= 0 {
		return fallback
	}
	return ms * 1000
}
