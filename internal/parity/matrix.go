package parity

import "fmt"

// MatrixAvailability describes adapter-level source visibility for one class.
type MatrixAvailability string

const (
	// MatrixAvailable means the native source carries complete proof material.
	MatrixAvailable MatrixAvailability = "available"
	// MatrixSourceUnavailable means the source proves an artifact existed but lacks bytes.
	MatrixSourceUnavailable MatrixAvailability = "source_unavailable"
	// MatrixSourceEmpty means the source can explicitly carry an empty artifact.
	MatrixSourceEmpty MatrixAvailability = "source_empty"
	// MatrixPartialSource means the source can explicitly mark partial artifacts.
	MatrixPartialSource MatrixAvailability = "partial_source"
	// MatrixRedacted means the source can explicitly mark redacted artifacts.
	MatrixRedacted MatrixAvailability = "redacted"
	// MatrixCompactedAway means the source can explicitly mark compacted artifacts.
	MatrixCompactedAway MatrixAvailability = "compacted_away"
	// MatrixNotSourceVisible means the source format has no artifact for the class.
	MatrixNotSourceVisible MatrixAvailability = "not_source_visible"
	// MatrixUnknown means SOW-0097 has not closed the adapter/class contract yet.
	MatrixUnknown MatrixAvailability = "unknown"
)

// MatrixRow is the machine-readable adapter availability matrix row.
type MatrixRow struct {
	Adapter                 string
	Class                   ArtifactClass
	Availability            []MatrixAvailability
	HashDomains             []HashDomain
	CanonicalRepresentation string
	SelectorRule            string
	Evidence                string
}

// AllArtifactClasses returns the complete parity artifact class catalog.
func AllArtifactClasses() []ArtifactClass {
	return []ArtifactClass{
		ClassSessionBoundary,
		ClassTurnBoundary,
		ClassOpBoundary,
		ClassUserPrompt,
		ClassUserImage,
		ClassAssistantMessage,
		ClassReasoningText,
		ClassLLMRequest,
		ClassLLMResponse,
		ClassLLMSDKRequest,
		ClassLLMSDKResponse,
		ClassToolRequest,
		ClassToolResponse,
		ClassLLMError,
		ClassToolError,
		ClassSubagentLink,
		ClassSystemOp,
		ClassCompactionEvent,
		ClassSessionMetadata,
		ClassLogEntry,
		ClassAttachmentMetadata,
		ClassPatchMetadata,
	}
}

// AdapterAvailabilityMatrices returns matrix rows keyed by adapter format.
func AdapterAvailabilityMatrices() map[string][]MatrixRow {
	matrices := map[string][]MatrixRow{
		"aiagent_v2":  aiagentV2MatrixRows(),
		"aiagent_v3":  aiagentV3MatrixRows(),
		"claude-code": claudeCodeMatrixRows(),
		"codex":       codexMatrixRows(),
		"opencode":    opencodeMatrixRows(),
	}
	out := make(map[string][]MatrixRow, len(matrices))
	for adapter, rows := range matrices {
		out[adapter] = append([]MatrixRow(nil), rows...)
	}
	return out
}

func validateArtifactAgainstMatrix(a Artifact) error {
	if a.Availability == AvailabilitySourceCorrupt || a.Availability == AvailabilityUnverifiable {
		return nil
	}
	row, ok := matrixRowFor(a.Adapter, a.Class)
	if !ok {
		return fmt.Errorf("adapter %q class %q is missing from availability matrix", a.Adapter, a.Class)
	}
	if matrixRowAllows(row, MatrixUnknown) {
		return nil
	}
	if !matrixRowAllows(row, matrixAvailabilityFromArtifact(a.Availability)) {
		return fmt.Errorf("availability %q is not allowed by adapter matrix", a.Availability)
	}
	if a.HashDomain != "" && len(row.HashDomains) > 0 && !matrixRowAllowsHash(row, a.HashDomain) {
		return fmt.Errorf("hash_domain %q is not allowed by adapter matrix", a.HashDomain)
	}
	return nil
}

func matrixRowFor(adapter string, class ArtifactClass) (MatrixRow, bool) {
	rows, ok := AdapterAvailabilityMatrices()[adapter]
	if !ok {
		return MatrixRow{}, false
	}
	for _, row := range rows {
		if row.Class == class {
			return row, true
		}
	}
	return MatrixRow{}, false
}

func matrixAvailabilityFromArtifact(availability Availability) MatrixAvailability {
	return MatrixAvailability(availability)
}

func matrixRowAllows(row MatrixRow, availability MatrixAvailability) bool {
	for _, candidate := range row.Availability {
		if candidate == availability {
			return true
		}
	}
	return false
}

func matrixRowAllowsHash(row MatrixRow, domain HashDomain) bool {
	for _, candidate := range row.HashDomains {
		if candidate == domain {
			return true
		}
	}
	return false
}

type adapterMatrixBuilder struct {
	adapter string
	rows    map[ArtifactClass]MatrixRow
}

func newAdapterMatrixBuilder(adapter string) *adapterMatrixBuilder {
	rows := make(map[ArtifactClass]MatrixRow, len(AllArtifactClasses()))
	for _, class := range AllArtifactClasses() {
		rows[class] = MatrixRow{
			Adapter:                 adapter,
			Class:                   class,
			Availability:            []MatrixAvailability{MatrixUnknown},
			CanonicalRepresentation: "open SOW-0097 gap",
			SelectorRule:            "class contract not closed yet",
			Evidence:                "adapter spec machine-readable matrix rows",
		}
	}
	return &adapterMatrixBuilder{adapter: adapter, rows: rows}
}

func (b *adapterMatrixBuilder) set(class ArtifactClass, availability []MatrixAvailability, hashes []HashDomain, canonical string, selector string, evidence string) {
	b.rows[class] = MatrixRow{
		Adapter:                 b.adapter,
		Class:                   class,
		Availability:            append([]MatrixAvailability(nil), availability...),
		HashDomains:             append([]HashDomain(nil), hashes...),
		CanonicalRepresentation: canonical,
		SelectorRule:            selector,
		Evidence:                evidence,
	}
}

func (b *adapterMatrixBuilder) notSourceVisible(class ArtifactClass, evidence string) {
	b.set(class, []MatrixAvailability{MatrixNotSourceVisible}, nil, "none", "source format has no separate artifact", evidence)
}

func (b *adapterMatrixBuilder) rowsInClassOrder() []MatrixRow {
	rows := make([]MatrixRow, 0, len(b.rows))
	for _, class := range AllArtifactClasses() {
		rows = append(rows, b.rows[class])
	}
	return rows
}

func aiagentV2MatrixRows() []MatrixRow {
	b := newAdapterMatrixBuilder("aiagent_v2")
	b.set(ClassSessionBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row", "session:<traceId>", "adapter-aiagent-v2.md Source Manifest Parity")
	b.set(ClassTurnBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "turns row", "turn:<turn.index> or turn:<10000+step.index>", "adapter-aiagent-v2.md Source Manifest Parity")
	b.set(ClassOpBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "ops row", "op:<turn_seq>:<op_seq>", "adapter-aiagent-v2.md Source Manifest Parity")
	b.notSourceVisible(ClassUserPrompt, "adapter-aiagent-v2.md request payload contract")
	b.notSourceVisible(ClassUserImage, "adapter-aiagent-v2.md request payload contract")
	b.set(ClassAssistantMessage, av(MatrixAvailable), hd(HashCanonicalJSON), "sessions.extras_json.final_report", "session:<traceId>:final_report", "adapter-aiagent-v2.md finalReport")
	b.set(ClassReasoningText, av(MatrixAvailable), hd(HashSemanticText), "reasoning op extras", "op:<turn_seq>:<op_seq>:reasoning.final", "adapter-aiagent-v2.md reasoning.final")
	b.set(ClassLLMRequest, av(MatrixAvailable, MatrixSourceUnavailable, MatrixPartialSource), hd(HashRawBytes, HashCanonicalJSON, HashSemanticText), "payload_refs.kind=llm_request", "producer ref path, inline snapshot JSON pointer, or metadata-only ordinal", "adapter-aiagent-v2.md payload refs")
	b.set(ClassLLMResponse, av(MatrixAvailable, MatrixSourceUnavailable, MatrixPartialSource), hd(HashRawBytes, HashCanonicalJSON, HashSemanticText), "payload_refs.kind=llm_response", "producer ref path, inline snapshot JSON pointer, or metadata-only ordinal", "adapter-aiagent-v2.md payload refs")
	b.set(ClassLLMSDKRequest, av(MatrixAvailable, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=llm_sdk_request", "producer SDK ref path or metadata-only ordinal", "adapter-aiagent-v2.md payload refs")
	b.set(ClassLLMSDKResponse, av(MatrixAvailable, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=llm_sdk_response", "producer SDK ref path or metadata-only ordinal", "adapter-aiagent-v2.md payload refs")
	b.set(ClassToolRequest, av(MatrixAvailable, MatrixSourceUnavailable, MatrixPartialSource), hd(HashRawBytes, HashCanonicalJSON, HashSemanticText), "payload_refs.kind=tool_request", "producer ref path, inline snapshot JSON pointer, or metadata-only ordinal", "adapter-aiagent-v2.md payload refs")
	b.set(ClassToolResponse, av(MatrixAvailable, MatrixSourceUnavailable, MatrixPartialSource), hd(HashRawBytes, HashCanonicalJSON, HashSemanticText), "payload_refs.kind=tool_response", "producer ref path, inline snapshot JSON pointer, or metadata-only ordinal", "adapter-aiagent-v2.md payload refs")
	b.set(ClassLLMError, av(MatrixAvailable), hd(HashIdentityJSON), "failed LLM ops row", "op:<turn_seq>:<op_seq>:error", "adapter-aiagent-v2.md failed ops")
	b.set(ClassToolError, av(MatrixAvailable), hd(HashIdentityJSON), "failed tool/session ops row", "op:<turn_seq>:<op_seq>:error", "adapter-aiagent-v2.md failed ops")
	b.set(ClassSubagentLink, av(MatrixAvailable), hd(HashIdentityJSON), "parent session op child link", "op:<turn_seq>:<op_seq>:child_session:<child_traceId>", "adapter-aiagent-v2.md childSession")
	b.set(ClassSystemOp, av(MatrixAvailable), hd(HashIdentityJSON), "ops.kind=system row", "op:<turn_seq>:<op_seq>:system", "adapter-aiagent-v2.md Source Manifest Parity")
	b.set(ClassCompactionEvent, av(MatrixAvailable), hd(HashIdentityJSON), "history-compaction step session op row plus ops.extras_json", "op:<turn_seq>:<op_seq>:compaction", "adapter-aiagent-v2.md Source Manifest Parity")
	b.set(ClassSessionMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row plus sessions.extras_json", "session:<traceId>:metadata", "adapter-aiagent-v2.md Source Manifest Parity")
	b.set(ClassLogEntry, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "log_entries rows with parity extras", "source snapshot file URI plus JSON pointer", "adapter-aiagent-v2.md op/session logs")
	b.notSourceVisible(ClassAttachmentMetadata, "adapter-aiagent-v2.md opTree schema")
	b.notSourceVisible(ClassPatchMetadata, "adapter-aiagent-v2.md opTree schema")
	return b.rowsInClassOrder()
}

func aiagentV3MatrixRows() []MatrixRow {
	b := newAdapterMatrixBuilder("aiagent_v3")
	b.set(ClassSessionBoundary, av(MatrixAvailable, MatrixPartialSource), hd(HashIdentityJSON), "sessions row", "session:<sessionId>", "adapter-aiagent-v3.md Source Manifest Parity")
	b.set(ClassTurnBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "turns row", "turn:<turnNo>", "adapter-aiagent-v3.md Source Manifest Parity")
	b.set(ClassOpBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "ops row", "op:<turnNo>:<opIndex>", "adapter-aiagent-v3.md Source Manifest Parity")
	b.notSourceVisible(ClassUserPrompt, "adapter-aiagent-v3.md payloadRefs schema")
	b.notSourceVisible(ClassUserImage, "adapter-aiagent-v3.md payloadRefs schema")
	b.notSourceVisible(ClassAssistantMessage, "adapter-aiagent-v3.md payloadRefs schema")
	b.set(ClassReasoningText, av(MatrixAvailable, MatrixSourceEmpty, MatrixSourceUnavailable), hd(HashSemanticText), "payload_refs.kind=reasoning_stream", "producer payload ref path or metadata-only ordinal", "adapter-aiagent-v3.md payloadRefs")
	b.set(ClassLLMRequest, av(MatrixAvailable, MatrixSourceEmpty, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=llm_request", "producer payload ref path or metadata-only ordinal", "adapter-aiagent-v3.md payloadRefs")
	b.set(ClassLLMResponse, av(MatrixAvailable, MatrixSourceEmpty, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=llm_response", "producer payload ref path or metadata-only ordinal", "adapter-aiagent-v3.md payloadRefs")
	b.set(ClassLLMSDKRequest, av(MatrixAvailable, MatrixSourceEmpty, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=sdk_request", "producer SDK ref path or metadata-only ordinal", "adapter-aiagent-v3.md SDK aliases")
	b.set(ClassLLMSDKResponse, av(MatrixAvailable, MatrixSourceEmpty, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=sdk_response", "producer SDK ref path or metadata-only ordinal", "adapter-aiagent-v3.md SDK aliases")
	b.set(ClassToolRequest, av(MatrixAvailable, MatrixSourceEmpty, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=tool_request", "producer payload ref path or metadata-only ordinal", "adapter-aiagent-v3.md payloadRefs")
	b.set(ClassToolResponse, av(MatrixAvailable, MatrixSourceEmpty, MatrixSourceUnavailable), hd(HashRawBytes), "payload_refs.kind=tool_response", "producer payload ref path or metadata-only ordinal", "adapter-aiagent-v3.md payloadRefs")
	b.set(ClassLLMError, av(MatrixAvailable), hd(HashIdentityJSON), "failed LLM ops row", "op:<turnNo>:<opIndex>:error", "adapter-aiagent-v3.md failed ops")
	b.set(ClassToolError, av(MatrixAvailable), hd(HashIdentityJSON), "failed tool/session ops row", "op:<turnNo>:<opIndex>:error", "adapter-aiagent-v3.md failed ops")
	b.set(ClassSubagentLink, av(MatrixAvailable), hd(HashIdentityJSON), "parent op child session link", "op:<turnNo>:<opIndex>:child_session:<childSessionId>", "adapter-aiagent-v3.md childSessions")
	b.set(ClassSystemOp, av(MatrixAvailable), hd(HashIdentityJSON), "ops.kind=system row", "op:<turnNo>:<opIndex>:system", "adapter-aiagent-v3.md Source Manifest Parity")
	b.set(ClassCompactionEvent, av(MatrixAvailable), hd(HashIdentityJSON), "history-compaction session ops row plus ops.extras_json", "op:<turnNo>:<opIndex>:compaction", "adapter-aiagent-v3.md Source Manifest Parity")
	b.set(ClassSessionMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row plus sessions.extras_json", "session:<sessionId>:metadata", "adapter-aiagent-v3.md Source Manifest Parity")
	b.set(ClassLogEntry, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "log_entries rows with parity extras", "ledger seq warning/error JSON pointer", "adapter-aiagent-v3.md warnings/errors")
	b.notSourceVisible(ClassAttachmentMetadata, "adapter-aiagent-v3.md ledger schema")
	b.notSourceVisible(ClassPatchMetadata, "adapter-aiagent-v3.md ledger schema")
	return b.rowsInClassOrder()
}

func claudeCodeMatrixRows() []MatrixRow {
	b := newAdapterMatrixBuilder("claude-code")
	b.set(ClassSessionBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row", "session:<native_session_id>", "adapter-claude-code.md parity table")
	b.set(ClassTurnBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "turns row", "turn:<seq>", "adapter-claude-code.md parity table")
	b.set(ClassOpBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "ops row", "op:<turn_seq>:<op_seq>", "adapter-claude-code.md parity table")
	b.set(ClassUserPrompt, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "internal user-input op payload", "line:<line>:/message/content[/<i>/text]", "adapter-claude-code.md parity table")
	b.set(ClassUserImage, av(MatrixAvailable), hd(HashCanonicalJSON), "internal user-input op payload", "line:<line>:/message/content/<i>", "adapter-claude-code.md parity table")
	b.set(ClassAssistantMessage, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "LLM op response payload", "line:<line>:/message/content/<i>/text", "adapter-claude-code.md parity table")
	b.set(ClassReasoningText, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "reasoning op payload", "line:<line>:/message/content/<i>/thinking", "adapter-claude-code.md parity table")
	b.set(ClassLLMRequest, av(MatrixSourceUnavailable), nil, "none", "provider request envelope not persisted", "adapter-claude-code.md parity table")
	b.set(ClassLLMResponse, av(MatrixSourceUnavailable), nil, "none for provider envelope", "provider response envelope not persisted", "adapter-claude-code.md parity table")
	b.notSourceVisible(ClassLLMSDKRequest, "adapter-claude-code.md transcript schema")
	b.notSourceVisible(ClassLLMSDKResponse, "adapter-claude-code.md transcript schema")
	b.set(ClassToolRequest, av(MatrixAvailable, MatrixSourceEmpty), hd(HashCanonicalJSON, HashSemanticText), "tool/session op payload", "line:<line>:/message/content/<i>/input", "adapter-claude-code.md parity table")
	b.set(ClassToolResponse, av(MatrixAvailable, MatrixSourceEmpty), hd(HashCanonicalJSON, HashSemanticText), "finalized tool op payload", "line:<line>:/message/content/<i>/content or /toolUseResult", "adapter-claude-code.md parity table")
	b.set(ClassLLMError, av(MatrixAvailable), hd(HashIdentityJSON), "failed LLM op", "op:<turn_seq>:<op_seq>:error", "adapter-claude-code.md parity table")
	b.set(ClassToolError, av(MatrixAvailable), hd(HashIdentityJSON), "failed tool op", "op:<turn_seq>:<op_seq>:error", "adapter-claude-code.md parity table")
	b.set(ClassSubagentLink, av(MatrixAvailable), hd(HashIdentityJSON), "parent Agent op child session id", "op:<turn_seq>:<op_seq>:child_session:<parentSessionId>:agent:<agentId>", "adapter-claude-code.md sidecars")
	b.set(ClassSystemOp, av(MatrixAvailable), hd(HashIdentityJSON), "logged system record row", "line:<line>:/system", "adapter-claude-code.md parity table")
	b.set(ClassCompactionEvent, av(MatrixAvailable), hd(HashIdentityJSON), "compaction op metadata", "op:<turn_seq>:<op_seq>:compaction", "adapter-claude-code.md compaction table")
	b.set(ClassSessionMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row plus sessions.extras_json", "session:<sessionId>:metadata", "adapter-claude-code.md parity table")
	b.set(ClassLogEntry, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "compaction/log payload", "line:<line>:/log or line:<line>:/message/content", "adapter-claude-code.md parity table")
	b.set(ClassAttachmentMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "attachment log row", "line:<line>:/attachment", "adapter-claude-code.md attachment table")
	b.notSourceVisible(ClassPatchMetadata, "adapter-claude-code.md transcript schema")
	return b.rowsInClassOrder()
}

func codexMatrixRows() []MatrixRow {
	b := newAdapterMatrixBuilder("codex")
	b.set(ClassSessionBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row", "session:<session_meta.payload.id> or session:<legacy-header.id>", "adapter-codex.md initial classes")
	b.set(ClassTurnBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "turns row", "turn:<seq>", "adapter-codex.md initial classes")
	b.set(ClassOpBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "ops row", "op:<turn_seq>:<op_seq>", "adapter-codex.md initial classes")
	b.set(ClassUserPrompt, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText, HashCanonicalJSON), "internal user-input op payload", "source line/file selector plus user-content pointer", "adapter-codex.md initial classes")
	b.set(ClassUserImage, av(MatrixAvailable), hd(HashCanonicalJSON), "internal user-input op plus payload_refs.kind=tool_request", "source line selector plus JSON pointer to image block/reference/detail", "adapter-codex.md user_image parity")
	b.set(ClassAssistantMessage, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "LLM message op payload", "source line/file selector plus assistant-text pointer", "adapter-codex.md initial classes")
	b.set(ClassReasoningText, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "reasoning op payload", "source line/file selector plus reasoning pointer", "adapter-codex.md initial classes")
	b.notSourceVisible(ClassLLMRequest, "adapter-codex.md rollout item schema")
	b.notSourceVisible(ClassLLMResponse, "adapter-codex.md rollout item schema")
	b.notSourceVisible(ClassLLMSDKRequest, "adapter-codex.md rollout item schema")
	b.notSourceVisible(ClassLLMSDKResponse, "adapter-codex.md rollout item schema")
	b.set(ClassLLMError, av(MatrixNotSourceVisible), nil, "none as an LLM op error; generic errors are log_entry", "source format has no provider/model error envelope tied to an LLM op", "adapter-codex.md event_msg.error")
	b.set(ClassToolRequest, av(MatrixAvailable, MatrixSourceUnavailable, MatrixSourceEmpty), hd(HashSemanticText, HashCanonicalJSON, HashRawBytes), "tool op payload", "source line/file selector plus tool-input pointer or whole record", "adapter-codex.md initial classes")
	b.set(ClassToolResponse, av(MatrixAvailable, MatrixSourceUnavailable, MatrixSourceEmpty), hd(HashSemanticText, HashCanonicalJSON), "finalized tool op payload", "source line/file selector plus output pointer", "adapter-codex.md initial classes")
	b.set(ClassToolError, av(MatrixAvailable), hd(HashIdentityJSON), "failed ops row", "op:<turn_seq>:<op_seq>:error", "adapter-codex.md initial classes")
	b.set(ClassSubagentLink, av(MatrixAvailable), hd(HashIdentityJSON), "session spawn op child id", "op:<turn_seq>:<op_seq>:child_session:<new_thread_id>", "adapter-codex.md collab spawn")
	b.set(ClassSystemOp, av(MatrixAvailable), hd(HashIdentityJSON), "log_entries rows for lifecycle/review/default metadata events", "log:<scope>:<timestamp>:<severity>:<source-hash>:<message-hash>", "adapter-codex.md system-op events")
	b.set(ClassCompactionEvent, av(MatrixAvailable), hd(HashIdentityJSON), "compaction op metadata", "op:<turn_seq>:<op_seq>:compaction", "adapter-codex.md compaction rules")
	b.set(ClassSessionMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row plus sessions.extras_json", "session:<session_meta.payload.id>:metadata", "adapter-codex.md session_meta parity")
	b.set(ClassLogEntry, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText, HashRawBytes), "log row or log payload ref", "line:<line>:/payload/message or whole-line compaction body", "adapter-codex.md initial classes")
	b.notSourceVisible(ClassAttachmentMetadata, "adapter-codex.md rollout item schema")
	b.notSourceVisible(ClassPatchMetadata, "adapter-codex.md rollout item schema")
	return b.rowsInClassOrder()
}

func opencodeMatrixRows() []MatrixRow {
	b := newAdapterMatrixBuilder("opencode")
	b.set(ClassSessionBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row", "session:<session.id>", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassTurnBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "turns row", "turn:<seq>", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassOpBoundary, av(MatrixAvailable), hd(HashIdentityJSON), "ops row", "op:<turn_seq>:<op_seq>", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassUserPrompt, av(MatrixAvailable, MatrixSourceUnavailable), hd(HashSemanticText), "internal user-input op payload", "input:<id>:prompt.text or metadata-only canonical ordinal", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassUserImage, av(MatrixAvailable), hd(HashCanonicalJSON), "internal user-input op plus payload_refs.kind=tool_request over image file objects", "input:<id>:prompt.files.<index>", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassAssistantMessage, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "LLM op response payload", "part:<id>:text", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassReasoningText, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "reasoning op payload", "part:<id>:text", "adapter-opencode.md Source Manifest Parity")
	b.notSourceVisible(ClassLLMRequest, "adapter-opencode.md payload field map")
	b.notSourceVisible(ClassLLMResponse, "adapter-opencode.md payload field map")
	b.notSourceVisible(ClassLLMSDKRequest, "adapter-opencode.md payload field map")
	b.notSourceVisible(ClassLLMSDKResponse, "adapter-opencode.md payload field map")
	b.set(ClassToolRequest, av(MatrixAvailable, MatrixSourceEmpty), hd(HashCanonicalJSON, HashSemanticText), "tool op payload", "part:<id>:state.input", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassToolResponse, av(MatrixAvailable, MatrixSourceEmpty), hd(HashCanonicalJSON, HashSemanticText), "finalized tool op payload", "part:<id>:state.output", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassLLMError, av(MatrixAvailable), hd(HashIdentityJSON), "failed opencode turn plus terminal session error detail when present", "turn:<turnSeq>:assistant_error", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassToolError, av(MatrixAvailable), hd(HashIdentityJSON), "failed tool op", "op:<turn_seq>:<op_seq>:error", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassSubagentLink, av(MatrixAvailable), hd(HashIdentityJSON), "task session op child id", "op:<turn_seq>:<op_seq>:child_session:<child_session_id>", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassSystemOp, av(MatrixAvailable), hd(HashIdentityJSON), "session-scoped log row with session_message_id parity extras", "session_message:<id>:system_op", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassCompactionEvent, av(MatrixAvailable), hd(HashIdentityJSON), "compaction part log row plus part_id extras", "part:<id>:compaction", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassSessionMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "sessions row plus first-class session columns and sessions.extras_json", "session:<session.id>:metadata", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassLogEntry, av(MatrixAvailable, MatrixSourceEmpty), hd(HashSemanticText), "log row for compaction/retry/file parts and known session_message rows", "part log id or session_message:<id>:log via parity extras", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassAttachmentMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "file part log row plus part_id extras", "part:<id>:file", "adapter-opencode.md Source Manifest Parity")
	b.set(ClassPatchMetadata, av(MatrixAvailable), hd(HashIdentityJSON), "source patch part matched to LLM op extras_json.patches[] metadata", "part:<id>:patch", "adapter-opencode.md Source Manifest Parity")
	return b.rowsInClassOrder()
}

func av(values ...MatrixAvailability) []MatrixAvailability {
	return values
}

func hd(values ...HashDomain) []HashDomain {
	return values
}
