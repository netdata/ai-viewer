package parity

import (
	"bytes"
	"fmt"
	"sort"
)

func (s *claudeCodeSourceState) recordClaudeCodeUser(rec claudeCodeSourceRecord, line []byte, lineNo int64, tsUs int64) ([]Artifact, error) {
	if boolPtr(rec.IsMeta) {
		log := s.logEntry("meta-user", lineNo)
		return []Artifact{log}, nil
	}
	if boolPtr(rec.IsCompactSummary) {
		log := s.logEntry("compaction-summary", lineNo)
		payloads, err := s.inlineArtifact(line, lineNo, "/message/content", ClassLogEntry)
		if err != nil {
			return nil, err
		}
		return append([]Artifact{log}, payloads...), nil
	}
	var msg claudeCodeUserMessage
	if err := decodeJSONPayload(rec.Message, &msg); err != nil {
		return nil, fmt.Errorf("decode user.message: %w", err)
	}
	if claudeCodeRawJSONString(msg.Content) {
		return s.recordClaudeCodeUserPrompt(line, lineNo, tsUs)
	}
	blocks, err := claudeCodeContentBlocks(msg.Content)
	if err != nil {
		return nil, err
	}
	return s.recordClaudeCodeToolResults(rec, line, lineNo, blocks, tsUs)
}

func (s *claudeCodeSourceState) recordClaudeCodeUserPrompt(line []byte, lineNo int64, tsUs int64) ([]Artifact, error) {
	if s.turnOpen && !s.turnFinalized {
		// Claude Code can start a new user turn without a turn_duration. Mirror
		// the adapter's source-derived visual boundary by closing the previous
		// turn at the next prompt timestamp.
		turn, err := s.turnBoundary("completed", tsUs)
		if err != nil {
			return nil, err
		}
		s.turnOpen = false
		s.turnFinalized = true
		artifacts, err := s.startClaudeCodeUserTurn(line, lineNo, tsUs)
		if err != nil {
			return nil, err
		}
		return append([]Artifact{turn}, artifacts...), nil
	}
	return s.startClaudeCodeUserTurn(line, lineNo, tsUs)
}

func (s *claudeCodeSourceState) startClaudeCodeUserTurn(line []byte, lineNo int64, tsUs int64) ([]Artifact, error) {
	s.turnSeq++
	s.turnOpen = true
	s.turnFinalized = false
	s.turnStartedAt = tsUs
	s.opSeq = 0
	artifacts := make([]Artifact, 0, 2)
	s.opSeq++
	op, err := s.opBoundary(s.opSeq, "internal", "user_input", "completed", tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, op)
	payload, err := s.inlineArtifact(line, lineNo, "/message/content", ClassUserPrompt)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, payload...)
	return artifacts, nil
}

func (s *claudeCodeSourceState) recordClaudeCodeAssistant(rec claudeCodeSourceRecord, line []byte, lineNo int64, tsUs int64) ([]Artifact, error) {
	var msg claudeCodeAssistantMessage
	if err := decodeJSONPayload(rec.Message, &msg); err != nil {
		return nil, fmt.Errorf("decode assistant.message: %w", err)
	}
	if msg.Model == "" || msg.Model == "<synthetic>" {
		log := s.logEntry("synthetic-assistant", lineNo)
		return []Artifact{log}, nil
	}
	s.ensureClaudeCodeTurn(tsUs)
	var artifacts []Artifact
	s.opSeq++
	llmOpSeq := s.opSeq
	llmOp, err := s.opBoundary(llmOpSeq, "llm", msg.Model, "completed", tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, llmOp)
	for i, block := range msg.Content {
		if block.Type == "text" {
			payloads, err := s.inlineArtifact(line, lineNo, fmt.Sprintf("/message/content/%d/text", i), ClassAssistantMessage)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, payloads...)
		}
	}
	for i, block := range msg.Content {
		if block.Type != "thinking" {
			continue
		}
		s.opSeq++
		reasoning, err := s.opBoundary(s.opSeq, "reasoning", "", "completed", tsUs, ptrInt64(tsUs))
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, reasoning)
		payloads, err := s.inlineArtifact(line, lineNo, fmt.Sprintf("/message/content/%d/thinking", i), ClassReasoningText)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, payloads...)
	}
	for i, block := range msg.Content {
		if block.Type != "tool_use" {
			continue
		}
		toolArtifacts, err := s.recordClaudeCodeToolUse(line, lineNo, i, block, tsUs)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, toolArtifacts...)
	}
	return artifacts, nil
}

func (s *claudeCodeSourceState) recordClaudeCodeToolUse(line []byte, lineNo int64, index int, block claudeCodeContentBlock, tsUs int64) ([]Artifact, error) {
	s.opSeq++
	if block.Name == "Agent" {
		return s.recordClaudeCodeAgentToolUse(line, lineNo, index, block, tsUs, s.opSeq)
	}
	opName, namespace := claudeCodeSplitToolName(block.Name)
	s.openTools[block.ID] = claudeCodeOpenTool{
		turnSeq:       s.turnSeq,
		opSeq:         s.opSeq,
		kind:          "tool",
		name:          opName,
		toolNamespace: namespace,
		startedAt:     tsUs,
	}
	return s.inlineArtifact(line, lineNo, fmt.Sprintf("/message/content/%d/input", index), ClassToolRequest)
}

func (s *claudeCodeSourceState) recordClaudeCodeAgentToolUse(line []byte, lineNo int64, index int, block claudeCodeContentBlock, tsUs int64, opSeq int64) ([]Artifact, error) {
	name := block.Name
	if desc := claudeCodeAgentDescription(block.Input); desc != "" {
		name = desc
	}
	childNativeID := s.claudeCodeAgentChildNativeID(block.ID)
	s.openTools[block.ID] = claudeCodeOpenTool{
		turnSeq:       s.turnSeq,
		opSeq:         opSeq,
		kind:          "session",
		name:          name,
		toolNamespace: "builtin",
		startedAt:     tsUs,
		childNativeID: childNativeID,
	}
	var artifacts []Artifact
	payloads, err := s.inlineArtifact(line, lineNo, fmt.Sprintf("/message/content/%d/input", index), ClassToolRequest)
	if err != nil {
		return nil, err
	}
	artifacts = append(artifacts, payloads...)
	if childNativeID != "" {
		link, err := s.subagentLink(opSeq, childNativeID, lineNo)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, link)
	}
	return artifacts, nil
}

func (s *claudeCodeSourceState) claudeCodeAgentChildNativeID(toolUseID string) string {
	agentID := s.toolUseToAgent[toolUseID]
	if agentID == "" {
		return ""
	}
	return s.nativeSessionID + ":agent:" + agentID
}

func (s *claudeCodeSourceState) recordClaudeCodeToolResults(rec claudeCodeSourceRecord, line []byte, lineNo int64, blocks []claudeCodeContentBlock, tsUs int64) ([]Artifact, error) {
	var artifacts []Artifact
	toolUseResultEmitted := false
	for i, block := range blocks {
		if block.Type == "text" {
			userText, err := s.recordClaudeCodeUserTextBlock(line, lineNo, i, tsUs)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, userText...)
			continue
		}
		if block.Type == "image" {
			userImage, err := s.recordClaudeCodeUserImageBlock(line, lineNo, i, tsUs)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, userImage...)
			continue
		}
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		open, ok := s.openTools[block.ToolUseID]
		if !ok {
			continue
		}
		payloads, err := s.inlineArtifactAtTurn(open.turnSeq, line, lineNo, fmt.Sprintf("/message/content/%d/content", i), ClassToolResponse)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, payloads...)
		if len(bytes.TrimSpace(rec.ToolUseResult)) > 0 && !bytes.Equal(bytes.TrimSpace(rec.ToolUseResult), []byte("null")) && !toolUseResultEmitted {
			echo, err := s.inlineArtifactAtTurn(open.turnSeq, line, lineNo, "/toolUseResult", ClassToolResponse)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, echo...)
			toolUseResultEmitted = true
		}
		status := "completed"
		if block.IsError {
			status = "failed"
		}
		op, err := s.opBoundaryAtTurn(open.turnSeq, open.opSeq, open.kind, open.name, open.toolNamespace, status, open.startedAt, ptrInt64(tsUs))
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, op)
		if block.IsError {
			errorArtifact, err := s.opErrorAtTurn(open.turnSeq, open.opSeq, open.kind, "tool_error", "", lineNo)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, errorArtifact)
		}
		delete(s.openTools, block.ToolUseID)
	}
	return artifacts, nil
}

func (s *claudeCodeSourceState) recordClaudeCodeUserTextBlock(line []byte, lineNo int64, index int, tsUs int64) ([]Artifact, error) {
	s.ensureClaudeCodeTurn(tsUs)
	s.opSeq++
	op, err := s.opBoundary(s.opSeq, "internal", "user_input", "completed", tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	payloads, err := s.inlineArtifact(line, lineNo, fmt.Sprintf("/message/content/%d/text", index), ClassUserPrompt)
	if err != nil {
		return nil, err
	}
	return append([]Artifact{op}, payloads...), nil
}

func (s *claudeCodeSourceState) recordClaudeCodeUserImageBlock(line []byte, lineNo int64, index int, tsUs int64) ([]Artifact, error) {
	s.ensureClaudeCodeTurn(tsUs)
	s.opSeq++
	op, err := s.opBoundary(s.opSeq, "internal", "user_input", "completed", tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	payloads, err := s.inlineArtifact(line, lineNo, fmt.Sprintf("/message/content/%d", index), ClassUserImage)
	if err != nil {
		return nil, err
	}
	return append([]Artifact{op}, payloads...), nil
}

func (s *claudeCodeSourceState) recordClaudeCodeSystem(rec claudeCodeSourceRecord, _ []byte, lineNo int64, tsUs int64) ([]Artifact, error) {
	switch rec.Subtype {
	case "api_error":
		return s.recordClaudeCodeAPIError(rec, lineNo, tsUs)
	case "compact_boundary":
		s.ensureClaudeCodeTurn(tsUs)
		s.opSeq++
		end := tsUs
		if rec.CompactMetadata != nil {
			end = tsUs + rec.CompactMetadata.DurationMs*1000
		}
		op, err := s.opBoundary(s.opSeq, "compaction", "compaction", "completed", tsUs, ptrInt64(end))
		if err != nil {
			return nil, err
		}
		compaction, err := s.compactionEvent(s.opSeq, rec.CompactMetadata, tsUs, end, lineNo)
		if err != nil {
			return nil, err
		}
		log := s.logEntry("compact_boundary", lineNo)
		return []Artifact{op, compaction, log}, nil
	case "turn_duration":
		if !s.turnOpen {
			return nil, nil
		}
		turn, err := s.turnBoundary("completed", tsUs)
		if err != nil {
			return nil, err
		}
		s.turnFinalized = true
		return []Artifact{turn}, nil
	default:
		if !claudeCodeLoggedSystemSubtype(rec.Subtype) {
			return nil, nil
		}
		artifact, err := s.systemOp(rec, lineNo, tsUs)
		if err != nil {
			return nil, err
		}
		return []Artifact{artifact}, nil
	}
}

func (s *claudeCodeSourceState) recordClaudeCodeAttachment(line []byte, lineNo int64) ([]Artifact, error) {
	artifact, err := s.attachmentMetadata(line, lineNo)
	if err != nil {
		return nil, err
	}
	return []Artifact{artifact}, nil
}

func (s *claudeCodeSourceState) recordClaudeCodeAPIError(rec claudeCodeSourceRecord, lineNo int64, tsUs int64) ([]Artifact, error) {
	s.ensureClaudeCodeTurn(tsUs)
	s.opSeq++
	opSeq := s.opSeq
	op, err := s.opBoundary(opSeq, "llm", "api_error", "failed", tsUs, ptrInt64(tsUs))
	if err != nil {
		return nil, err
	}
	info := claudeCodeSourceAPIErrorInfo(rec)
	errorArtifact, err := s.opError(opSeq, "llm", claudeCodeSourceAPIErrorClass(info), claudeCodeSourceAPIErrorMessage(rec, info), lineNo)
	if err != nil {
		return nil, err
	}
	log := s.logEntry("api_error", lineNo)
	return []Artifact{op, errorArtifact, log}, nil
}

func (s *claudeCodeSourceState) ensureClaudeCodeTurn(tsUs int64) {
	if s.turnOpen {
		return
	}
	s.turnSeq = 1
	s.turnOpen = true
	s.turnFinalized = false
	s.turnStartedAt = tsUs
	s.opSeq = 0
}

func (s *claudeCodeSourceState) finalizeClaudeCodeAtEOF() ([]Artifact, error) {
	var artifacts []Artifact
	if s.sessionStarted {
		openToolArtifacts, err := s.finalizeClaudeCodeOpenToolsAtEOF()
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, openToolArtifacts...)
		session, err := s.sessionBoundary()
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, session)
		if s.turnOpen && !s.turnFinalized {
			turn, err := s.turnBoundary("running", 0)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, turn)
		}
	}
	metadata, ok, err := s.sessionMetadataArtifact()
	if err != nil {
		return nil, err
	}
	if ok {
		artifacts = append(artifacts, metadata)
	}
	return artifacts, nil
}

func (s *claudeCodeSourceState) finalizeClaudeCodeOpenToolsAtEOF() ([]Artifact, error) {
	if len(s.openTools) == 0 {
		return nil, nil
	}
	tools := make([]claudeCodeOpenTool, 0, len(s.openTools))
	for _, open := range s.openTools {
		tools = append(tools, open)
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].turnSeq == tools[j].turnSeq {
			return tools[i].opSeq < tools[j].opSeq
		}
		return tools[i].turnSeq < tools[j].turnSeq
	})
	artifacts := make([]Artifact, 0, len(tools))
	for _, open := range tools {
		status := "running"
		var endedAt *int64
		if open.kind == "session" && open.childNativeID != "" {
			if completion, ok := s.childCompletions[open.childNativeID]; ok && completion.completed {
				status = "completed"
				endedAt = &completion.endedAt
			}
		}
		op, err := s.opBoundaryAtTurn(open.turnSeq, open.opSeq, open.kind, open.name, open.toolNamespace, status, open.startedAt, endedAt)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, op)
	}
	return artifacts, nil
}
