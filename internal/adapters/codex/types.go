package codex

import (
	"bytes"
	"encoding/json"
)

// This file defines the typed payload bodies for each top-level RolloutItem
// variant and the known-nested-type sets the parser dispatches against. Only
// the fields the parser-level contract and the later mapper consume are
// decoded; every struct tolerates unknown sibling fields (encoding/json drops
// them) so a newer codex CLI never hard-fails a line (spec adapter-codex.md
// §"Versioning / Forward Compatibility").

// sourceKind classifies a session_meta source enum into the canonical session
// kind buckets (spec adapter-codex.md:292): root, sub_agent, tool_internal,
// plus an explicit forward-compat "other" for unrecognized object shapes.
type sourceKind string

const (
	sourceRoot     sourceKind = "root"
	sourceSubagent sourceKind = "sub_agent"
	sourceInternal sourceKind = "tool_internal"
	sourceOther    sourceKind = "other"
)

// stringSourceKinds maps the bare-string SessionSource variants to their kind.
// Unrecognized bare strings fall through to sourceOther (forward-compat).
var stringSourceKinds = map[string]sourceKind{
	"cli":     sourceRoot,
	"vscode":  sourceRoot,
	"exec":    sourceRoot,
	"mcp":     sourceRoot,
	"unknown": sourceRoot,
}

// gitInfo is the optional git block flattened onto session_meta
// (protocol.rs:2856-2867). repository_url is sensitive in real files; fixtures
// sanitize it to git@github.com:example/example.git.
type gitInfo struct {
	CommitHash    string `json:"commit_hash"`
	Branch        string `json:"branch"`
	RepositoryURL string `json:"repository_url"`
}

// sessionMetaPayload is the SessionMetaLine body (protocol.rs:2638-2703). Only
// the load-bearing fields are typed; the rest stay accessible via the record's
// Raw for the mapper's Extras path.
type sessionMetaPayload struct {
	ID            string          `json:"id"`
	ForkedFromID  string          `json:"forked_from_id"`
	Timestamp     string          `json:"timestamp"`
	Cwd           string          `json:"cwd"`
	Originator    string          `json:"originator"`
	CLIVersion    string          `json:"cli_version"`
	ThreadSource  string          `json:"thread_source"`
	AgentNickname string          `json:"agent_nickname"`
	AgentRole     string          `json:"agent_role"`
	ModelProvider string          `json:"model_provider"`
	Source        json.RawMessage `json:"source"`
	Git           *gitInfo        `json:"git"`
}

// classifySource resolves the polymorphic source enum into a canonical
// sourceKind and, for a sub-agent thread_spawn, the parent ThreadId (else "").
// Tolerates every shape in adapter-codex.md:114-122 and never panics on an
// unknown object — it returns sourceOther so the line is still ingested.
func (p *sessionMetaPayload) classifySource() (sourceKind, string) {
	body := bytes.TrimSpace(p.Source)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return sourceRoot, ""
	}
	// Bare string form: "cli" | "exec" | "vscode" | "mcp" | "unknown".
	var s string
	if json.Unmarshal(body, &s) == nil {
		if k, ok := stringSourceKinds[s]; ok {
			return k, ""
		}
		return sourceOther, ""
	}
	// Object form: exactly one of custom / internal / subagent / other.
	var obj struct {
		Custom   json.RawMessage `json:"custom"`
		Internal json.RawMessage `json:"internal"`
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(body, &obj) != nil {
		return sourceOther, ""
	}
	switch {
	case len(obj.Custom) > 0:
		return sourceRoot, ""
	case len(obj.Internal) > 0:
		return sourceInternal, ""
	case len(obj.Subagent) > 0:
		return sourceSubagent, parentFromSubagent(obj.Subagent)
	default:
		return sourceOther, ""
	}
}

// parentFromSubagent extracts parent_thread_id from a subagent source. The
// subagent value is either a bare string ("review"|"compact"|...) carrying no
// parent, or an object {thread_spawn:{parent_thread_id,...}}.
func parentFromSubagent(raw json.RawMessage) string {
	var nested struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"thread_spawn"`
	}
	if json.Unmarshal(raw, &nested) != nil {
		return ""
	}
	return nested.ThreadSpawn.ParentThreadID
}

// turnContextPayload is the TurnContextItem body (protocol.rs:2745-2776). Rich
// sandbox/approval/network policy is preserved opaquely via the record's Raw;
// only the discriminating/load-bearing fields are typed here.
type turnContextPayload struct {
	TurnID         string          `json:"turn_id"`
	Cwd            string          `json:"cwd"`
	Model          string          `json:"model"`
	Effort         string          `json:"effort"`
	ApprovalPolicy string          `json:"approval_policy"`
	SandboxPolicy  json.RawMessage `json:"sandbox_policy"`
}

// sandboxType returns the sandbox_policy.type discriminator (workspace-write |
// danger-full-access | read-only | newer values), or "" when absent. Unknown
// values pass through verbatim (forward-compat, adapter-codex.md:222).
func (p *turnContextPayload) sandboxType() string {
	body := bytes.TrimSpace(p.SandboxPolicy)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return ""
	}
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return probe.Type
}

// responseItemPayload is the ResponseItem body (models.rs:750-903). The mapper
// reads the remaining variant-specific fields off the record's Raw; the parser
// only needs the discriminator plus the correlation keys it shares broadly.
type responseItemPayload struct {
	Type             string          `json:"type"`
	Role             string          `json:"role"`
	Name             string          `json:"name"`
	CallID           string          `json:"call_id"`
	Arguments        string          `json:"arguments"`
	EncryptedContent json.RawMessage `json:"encrypted_content"`
	Summary          json.RawMessage `json:"summary"`
	Content          json.RawMessage `json:"content"`
	Output           json.RawMessage `json:"output"`
}

// eventMsgPayload is the EventMsg body (protocol.rs:1133-1328). As with
// responseItemPayload, only the discriminator plus the broadly-shared
// correlation keys are typed; the mapper reads the rest off Raw.
type eventMsgPayload struct {
	Type    string          `json:"type"`
	TurnID  string          `json:"turn_id"`
	CallID  string          `json:"call_id"`
	Message string          `json:"message"`
	Text    string          `json:"text"`
	Reason  string          `json:"reason"`
	Info    json.RawMessage `json:"info"`
}

// compactedPayload is the top-level CompactedItem body (protocol.rs:2705-2734).
type compactedPayload struct {
	Message            string            `json:"message"`
	ReplacementHistory []json.RawMessage `json:"replacement_history"`
}

// replacementHistorySize returns the count of replacement_history entries (0
// when null/absent). The mapper surfaces this in the compaction op's Extras.
func (p *compactedPayload) replacementHistorySize() int { return len(p.ReplacementHistory) }

// responseItemTypes is the set of nested response_item payload.type values the
// adapter recognizes (spec adapter-codex.md:149-162; the persisted allowlist in
// policy.rs:67-85 plus the legacy local_shell_* pair for old .json files).
var responseItemTypes = map[string]struct{}{
	"message":                 {},
	"reasoning":               {},
	"function_call":           {},
	"function_call_output":    {},
	"custom_tool_call":        {},
	"custom_tool_call_output": {},
	"tool_search_call":        {},
	"tool_search_output":      {},
	"web_search_call":         {},
	"image_generation_call":   {},
	"compaction":              {},
	"context_compaction":      {},
	"local_shell_call":        {}, // legacy .json only (adapter-codex.md:153)
	"local_shell_call_output": {}, // legacy .json only
}

// responseItemNoOp are nested response_item variants the upstream
// ResponseItem::Other catch-all (#[serde(other)], models.rs:901) absorbs and
// the adapter intentionally strips without surfacing (rule #21,
// adapter-codex.md:163,378). Distinct from truly unknown types, which DO
// surface one SourceError per variant.
var responseItemNoOp = map[string]struct{}{
	"ghost_snapshot": {},
}

// eventMsgTypes is the set of nested event_msg payload.type values the adapter
// recognizes across Limited and Extended persistence modes (spec
// adapter-codex.md:173-204; policy.rs:135-220). Aliases turn_started /
// turn_complete are included alongside task_started / task_complete.
var eventMsgTypes = map[string]struct{}{
	"user_message":                {},
	"agent_message":               {},
	"agent_reasoning":             {},
	"agent_reasoning_raw_content": {},
	"patch_apply_end":             {},
	"token_count":                 {},
	"thread_goal_updated":         {},
	"context_compacted":           {},
	"entered_review_mode":         {},
	"exited_review_mode":          {},
	"mcp_tool_call_end":           {},
	"thread_rolled_back":          {},
	"turn_aborted":                {},
	"task_started":                {},
	"turn_started":                {}, // alias of task_started
	"task_complete":               {},
	"turn_complete":               {}, // alias of task_complete
	"web_search_end":              {},
	"image_generation_end":        {},
	"item_completed":              {},
	// Extended mode (policy.rs:135-220).
	"error":                      {},
	"guardian_assessment":        {},
	"exec_command_end":           {},
	"view_image_tool_call":       {},
	"dynamic_tool_call_request":  {},
	"dynamic_tool_call_response": {},
	// Sub-agent collab lifecycle ends (F3): collab_agent_spawn_end carries the
	// parent→child spawn link (sender_thread_id→new_thread_id); collab_close_end
	// and collab_waiting_end are recognized so they never SourceError (they map to
	// a DBG log, no canonical op — real corpus: 5 spawn / 72 close / 74 waiting).
	"collab_agent_spawn_end": {},
	"collab_close_end":       {},
	"collab_waiting_end":     {},
}

// eventMsgNoOp are nested event_msg variants the adapter recognizes but
// intentionally ignores without surfacing (rule #21 ghost_snapshot;
// realtime_conversation_* voice subsystem, adapter-codex.md:507).
var eventMsgNoOp = map[string]struct{}{
	"ghost_snapshot": {},
}
