package aiagent_v2

import (
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapSession emits one session subtree. depth=0 is the root; child
// recursion happens through session-kind ops with embedded childSession.
func (m *mapEmitter) mapSession(v sessionVisit) {
	if v.depth > maxChildSessionDepth {
		m.report(fmt.Errorf("aiagent_v2: child session depth %d exceeds cap %d (file %s, trace %s)", v.depth, maxChildSessionDepth, m.ctx.filename, v.node.TraceID))
		return
	}

	sessionPath := v.node.TraceID
	sessionPointer := v.jsonPointer
	if sessionPointer == "" {
		sessionPointer = "/opTree"
	}
	startedTs := sessionStartedTs(m.ctx, v.node)
	startInput := sessionStartedInput{
		node:           v.node,
		parentNativeID: v.parentNativeID,
		parentOpKey:    v.parentOpKey,
		kind:           v.kind,
		rootNative:     sessionRootNative(m.ctx, v.node, v.parentNativeID),
		version:        v.version,
		depth:          v.depth,
		sessionPath:    sessionPath,
		startedTs:      startedTs,
	}
	m.append(buildSessionStarted(m.ctx, startInput))

	m.mapTurns(v.node, v.depth, sessionPath, sessionPointer)
	m.mapSteps(v.node, v.depth, sessionPath, sessionPointer)
	m.emitSessionFinalized(v.node, sessionPath)
	m.emitSessionErrorLog(v.node, sessionPath, sessionPointer)
}

type sessionStartedInput struct {
	node           opTree
	parentNativeID string
	parentOpKey    string
	kind           canonical.SessionKind
	rootNative     string
	version        int
	depth          int
	sessionPath    string
	startedTs      int64
}

func sessionStartedTs(ctx *mapContext, node opTree) int64 {
	startedTs := msToMicros(node.StartedAt)
	if startedTs == 0 {
		return ctx.rootTs
	}
	return startedTs
}

func sessionRootNative(ctx *mapContext, node opTree, parentNativeID string) string {
	if parentNativeID != "" {
		return ctx.rootTrace
	}
	return node.TraceID
}

// buildSessionStarted constructs the SessionStartedEvent for one
// session node. extras_json carries the v2-only metadata that the
// canonical schema does not have first-class fields for.
func buildSessionStarted(ctx *mapContext, in sessionStartedInput) canonical.SessionStartedEvent {
	// v2 is a full-snapshot format, so the first LLM op is known at
	// SessionStarted time and no follow-up SessionUpdated is needed.
	return canonical.SessionStartedEvent{
		EventBase:      baseEvent(ctx, in.sessionPath+"::start", in.startedTs),
		NativeID:       in.node.TraceID,
		RootNativeID:   in.rootNative,
		ParentNativeID: in.parentNativeID,
		ParentOpKey:    in.parentOpKey,
		Kind:           in.kind,
		AgentName:      in.node.AgentID,
		Model:          firstLLMModel(in.node, in.depth),
		CallPath:       in.node.CallPath,
		Extras:         sessionStartedExtras(ctx, in),
	}
}

func sessionStartedExtras(ctx *mapContext, in sessionStartedInput) map[string]any {
	extras := baseSessionExtras(ctx, in)
	addOptionalSessionExtras(extras, in.node)
	addSessionDiagnostics(ctx, in, extras)
	return extras
}

func baseSessionExtras(ctx *mapContext, in sessionStartedInput) map[string]any {
	return map[string]any{
		"version":      in.version,
		"filename":     ctx.filename,
		"originId":     ctx.originID,
		"sessionTitle": in.node.SessionTitle,
		"latestStatus": in.node.LatestStatus,
	}
}

func addOptionalSessionExtras(extras map[string]any, node opTree) {
	if node.CallPath != "" {
		extras["callPath"] = node.CallPath
	}
	if node.ID != "" {
		extras["nodeId"] = node.ID
	}
	if len(node.Attributes) > 0 {
		extras["attributes"] = node.Attributes
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
}

func addSessionDiagnostics(ctx *mapContext, in sessionStartedInput, extras map[string]any) {
	if in.node.TraceID != ctx.originID && in.parentNativeID == "" {
		// Diagnostic per `adapter-aiagent-v2.md` Edge Cases item 9.
		extras["filename_originid_mismatch"] = true
	}
}

func (m *mapEmitter) emitSessionFinalized(node opTree, sessionPath string) {
	if node.EndedAt == nil {
		return
	}
	m.append(buildSessionFinalized(m.ctx, node, sessionPath))
}

// buildSessionFinalized emits the terminal SessionFinalized for a
// session that has an `endedAt`. Status is derived from `success` and
// `error` per `adapter-aiagent-v2.md`.
func buildSessionFinalized(ctx *mapContext, node opTree, sessionPath string) canonical.SessionFinalizedEvent {
	endTs := endTsOrStarted(node)
	return canonical.SessionFinalizedEvent{
		EventBase:    baseEvent(ctx, sessionPath+"::end", endTs),
		NativeID:     node.TraceID,
		Status:       mapSessionStatus(node),
		ErrorMessage: node.Error,
		EndTs:        endTs,
	}
}

func (m *mapEmitter) emitSessionErrorLog(node opTree, sessionPath string, sessionPointer string) {
	if node.Success == nil || *node.Success || node.Error == "" {
		return
	}
	extras, err := aiAgentV2LogParityExtras(m.ctx, sessionPointer+"/error")
	if err != nil {
		m.report(err)
	}
	m.append(canonical.LogEntryEvent{
		EventBase:       baseEvent(m.ctx, sessionPath+"::sessionError", endTsOrStarted(node)),
		SessionNativeID: node.TraceID,
		Severity:        "ERR",
		Source:          Format,
		Message:         node.Error,
		Extras:          extras,
	})
}
