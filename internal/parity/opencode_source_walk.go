package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *opencodeSourceState) recordUserPrompt(state *opencodeSourceTurnState, user opencodeSourceUserPrompt) error {
	state.opSeq++
	tsUs := opencodeMsToUS(user.message.TimeCreatedMs)
	state.ops[state.opSeq] = opencodeSourceOp{
		kind:      "internal",
		name:      "user_input",
		status:    "completed",
		startedAt: tsUs,
		endedAt:   ptrInt64(tsUs),
	}
	if user.input != nil {
		artifact, err := opencodeInputPayloadArtifact(s.sourceID, s.dbPath, state.scope, *user.input, ClassUserPrompt, "prompt.text")
		if err != nil {
			if opencodeRecoverableJSONError(err) {
				return s.emit(opencodeSourceCorruptionArtifact(
					s.sourceID,
					s.dbPath,
					state.scope.sessionID,
					opencodeNativeTurnID(state.scope.turnSeq),
					"session_input",
					user.input.ID,
					"prompt",
					"valid opencode session_input prompt JSON",
					user.input.Prompt,
				))
			}
			return err
		}
		if err := s.emit(artifact); err != nil {
			return err
		}
		imageFields, err := opencodeInputImageFileFields(user.input.Prompt)
		if err != nil {
			if opencodeRecoverableJSONError(err) {
				return s.emit(opencodeSourceCorruptionArtifact(
					s.sourceID,
					s.dbPath,
					state.scope.sessionID,
					opencodeNativeTurnID(state.scope.turnSeq),
					"session_input",
					user.input.ID,
					"prompt",
					"valid opencode session_input prompt JSON",
					user.input.Prompt,
				))
			}
			return err
		}
		for _, field := range imageFields {
			imageArtifact, err := opencodeInputPayloadArtifact(s.sourceID, s.dbPath, state.scope, *user.input, ClassUserImage, field)
			if err != nil {
				return err
			}
			if err := s.emit(imageArtifact); err != nil {
				return err
			}
		}
		return nil
	}
	return s.emit(opencodeSourceUnavailablePayload(
		s.sourceID,
		s.dbPath,
		state.scope,
		state.opSeq,
		ClassUserPrompt,
		"tool_request",
	))
}

func opencodeInputImageFileFields(raw []byte) ([]string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("opencode prompt is empty")
	}
	var prompt struct {
		Files []json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &prompt); err != nil {
		return nil, fmt.Errorf("decode opencode prompt files: %w", err)
	}
	out := make([]string, 0, len(prompt.Files))
	for i, file := range prompt.Files {
		var meta struct {
			MIME string `json:"mime"`
		}
		if err := json.Unmarshal(file, &meta); err != nil {
			return nil, fmt.Errorf("decode opencode prompt file %d: %w", i, err)
		}
		if strings.HasPrefix(strings.ToLower(meta.MIME), "image/") {
			out = append(out, fmt.Sprintf("prompt.files.%d", i))
		}
	}
	return out, nil
}

func (s *opencodeSourceState) recordReasoning(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	state.opSeq++
	startUs := opencodeMsToUS(data.Time.Start)
	if startUs == 0 {
		startUs = opencodeMsToUS(part.TimeCreatedMs)
	}
	status := "running"
	var endedAt *int64
	if data.Time.End != nil {
		endUs := opencodeMsToUS(*data.Time.End)
		if endUs < startUs {
			endUs = startUs
		}
		endedAt = ptrInt64(endUs)
		status = "completed"
	}
	state.ops[state.opSeq] = opencodeSourceOp{
		kind:      "reasoning",
		status:    status,
		startedAt: startUs,
		endedAt:   endedAt,
	}
	if data.Text != "" {
		return s.appendPayload(state.scope, part, ClassReasoningText, "text")
	}
	return nil
}

func (s *opencodeSourceState) recordAssistantText(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	if state.llmOpSeq == 0 || data.Text == "" {
		return nil
	}
	return s.appendPayload(state.scope, part, ClassAssistantMessage, "text")
}

func (s *opencodeSourceState) recordTool(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	if data.Tool == "task" {
		if childID := opencodeTaskChildSessionID(data); childID != "" {
			state.opSeq++
			state.ops[state.opSeq] = opencodeSourceOp{
				kind:      "session",
				status:    "running",
				startedAt: opencodeToolStartUS(data, part),
				childID:   childID,
			}
		}
	}

	state.opSeq++
	seq := state.opSeq
	name, namespace := opencodeToolNameNamespace(data.Tool)
	status, endedAt, errMessage, hasOutput := opencodeToolTerminal(data)
	state.ops[seq] = opencodeSourceOp{
		kind:          "tool",
		name:          name,
		toolNamespace: namespace,
		status:        status,
		startedAt:     opencodeToolStartUS(data, part),
		endedAt:       endedAt,
		errorClass:    opencodeToolErrorClass(status),
		errorMessage:  errMessage,
	}
	if opencodeToolHasInput(data) {
		if err := s.appendPayload(state.scope, part, ClassToolRequest, "state.input"); err != nil {
			return err
		}
	}
	if hasOutput {
		if err := s.appendPayload(state.scope, part, ClassToolResponse, "state.output"); err != nil {
			return err
		}
	}
	return nil
}

func (s *opencodeSourceState) recordCompaction(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	if err := s.appendOpencodeCompactionEvent(state.scope, state.llmOpSeq, part, data); err != nil {
		return err
	}
	return s.appendOpencodeLogEntry(state.scope, state.llmOpSeq, part, "INF", opencodeCompactionLogMessage(data.Auto))
}

func (s *opencodeSourceState) recordRetryLog(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	return s.appendOpencodeLogEntry(state.scope, state.llmOpSeq, part, "WRN", opencodeRetryLogMessage(data))
}

func (s *opencodeSourceState) recordFileAttachment(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	if data.URL == "" && data.Filename == "" && data.MIME == "" {
		return nil
	}
	if err := s.appendOpencodeAttachmentMetadata(state.scope, state.llmOpSeq, part, data); err != nil {
		return err
	}
	return s.appendOpencodeLogEntry(state.scope, state.llmOpSeq, part, "INF", "file attachment")
}

func (s *opencodeSourceState) recordPatchMetadata(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	return s.appendOpencodePatchMetadata(state.scope, state.llmOpSeq, part, data)
}

func (s *opencodeSourceState) appendPayload(scope opencodeSourceTurnScope, part opencodeSourcePart, class ArtifactClass, field string) error {
	artifact, err := opencodePayloadArtifact(s.sourceID, s.dbPath, scope, part, class, field)
	if err != nil {
		return err
	}
	artifact.NativeTurnID = opencodeNativeTurnID(scope.turnSeq)
	artifact.CanonicalOpID = ""
	return s.emit(artifact)
}

func (state *opencodeSourceTurnState) openLLM(startMs int64) {
	startUs := opencodeMsToUS(startMs)
	if state.llmOpen {
		op := state.ops[state.llmOpSeq]
		endUs := startUs
		if endUs < op.startedAt {
			endUs = op.startedAt
		}
		op.status = "cancelled"
		op.endedAt = ptrInt64(endUs)
		state.ops[state.llmOpSeq] = op
	}
	state.opSeq++
	state.llmOpSeq = state.opSeq
	state.llmOpen = true
	state.ops[state.opSeq] = opencodeSourceOp{
		kind:      "llm",
		name:      state.scope.modelID,
		status:    "running",
		startedAt: startUs,
	}
}

func (state *opencodeSourceTurnState) closeLLM(endMs int64) {
	if state.llmOpSeq == 0 {
		return
	}
	op := state.ops[state.llmOpSeq]
	endUs := opencodeMsToUS(endMs)
	if endUs < op.startedAt {
		endUs = op.startedAt
	}
	op.status = "completed"
	op.endedAt = ptrInt64(endUs)
	state.ops[state.llmOpSeq] = op
	state.llmOpen = false
}

func opencodeToolStartUS(data opencodeSourcePartData, part opencodeSourcePart) int64 {
	if data.State != nil && data.State.Time.Start > 0 {
		return opencodeMsToUS(data.State.Time.Start)
	}
	return opencodeMsToUS(part.TimeCreatedMs)
}

func opencodeToolTerminal(data opencodeSourcePartData) (status string, endedAt *int64, errorMessage string, hasOutput bool) {
	if data.State == nil {
		return "running", nil, "", false
	}
	switch data.State.Status {
	case "completed":
		return "completed", opencodeToolEndUS(data), "", data.State.Output != ""
	case "error":
		return "failed", opencodeToolEndUS(data), data.State.Error, data.State.Output != ""
	case "running", "pending", "":
		return "running", nil, "", false
	default:
		if data.State.Time.End != nil {
			return "completed", opencodeToolEndUS(data), data.State.Error, data.State.Output != ""
		}
		return "running", nil, "", false
	}
}

func opencodeToolEndUS(data opencodeSourcePartData) *int64 {
	if data.State == nil || data.State.Time.End == nil {
		return nil
	}
	startUs := opencodeMsToUS(data.State.Time.Start)
	endUs := opencodeMsToUS(*data.State.Time.End)
	if endUs < startUs {
		endUs = startUs
	}
	return ptrInt64(endUs)
}

func opencodeToolErrorClass(status string) string {
	if status == "failed" {
		return "error"
	}
	return ""
}

func opencodeToolHasInput(data opencodeSourcePartData) bool {
	if data.State == nil {
		return false
	}
	body := bytes.TrimSpace(data.State.Input)
	return len(body) > 0 && !bytes.Equal(body, []byte("null"))
}

func opencodeTaskChildSessionID(data opencodeSourcePartData) string {
	if data.State == nil || len(bytes.TrimSpace(data.State.Metadata)) == 0 {
		return ""
	}
	var metadata struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(data.State.Metadata, &metadata); err != nil {
		return ""
	}
	return metadata.SessionID
}
