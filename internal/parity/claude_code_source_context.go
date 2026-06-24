package parity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type claudeCodeChildCompletion struct {
	completed bool
	endedAt   int64
}

type claudeCodeSourceContext struct {
	toolUseToAgentByParent map[string]map[string]string
	childCompletions       map[string]claudeCodeChildCompletion
}

func buildClaudeCodeSourceContext(root string, transcripts []claudeCodeTranscript) (claudeCodeSourceContext, error) {
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		if os.IsNotExist(err) {
			return claudeCodeSourceContext{}, nil
		}
		return claudeCodeSourceContext{}, fmt.Errorf("resolve root %q: %w", root, err)
	}
	sourceContext := claudeCodeSourceContext{
		toolUseToAgentByParent: map[string]map[string]string{},
		childCompletions:       map[string]claudeCodeChildCompletion{},
	}
	for _, transcript := range transcripts {
		if transcript.kind != "sub_agent" {
			continue
		}
		if err := addClaudeCodeSourceSidecar(sourceContext, resolvedRoot, transcript); err != nil {
			return claudeCodeSourceContext{}, err
		}
		completion, err := inspectClaudeCodeChildCompletion(transcript.path)
		if err != nil {
			return claudeCodeSourceContext{}, err
		}
		sourceContext.childCompletions[transcript.nativeSessionID] = completion
	}
	return sourceContext, nil
}

func addClaudeCodeSourceSidecar(sourceContext claudeCodeSourceContext, resolvedRoot string, transcript claudeCodeTranscript) error {
	meta, ok, err := readClaudeCodeSourceMeta(resolvedRoot, transcript)
	if err != nil {
		return err
	}
	if !ok || meta.ToolUseID == "" || transcript.agentID == "" {
		return nil
	}
	byParent := sourceContext.toolUseToAgentByParent[transcript.parentNativeSessionID]
	if byParent == nil {
		byParent = map[string]string{}
		sourceContext.toolUseToAgentByParent[transcript.parentNativeSessionID] = byParent
	}
	byParent[meta.ToolUseID] = transcript.agentID
	return nil
}

type claudeCodeSourceMeta struct {
	ToolUseID string `json:"toolUseId"`
}

const claudeCodeSourceMetaReadMax = 1 * 1024 * 1024

func readClaudeCodeSourceMeta(resolvedRoot string, transcript claudeCodeTranscript) (claudeCodeSourceMeta, bool, error) {
	metaPath := strings.TrimSuffix(transcript.path, ".jsonl") + ".meta.json"
	resolvedPath, ok, err := claudeCodeResolveWithinRoot(resolvedRoot, metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return claudeCodeSourceMeta{}, false, nil
		}
		return claudeCodeSourceMeta{}, false, fmt.Errorf("resolve claude-code source meta %q: %w", metaPath, err)
	}
	if !ok {
		return claudeCodeSourceMeta{}, false, fmt.Errorf("claude-code source meta %q resolves outside root", metaPath)
	}
	raw, err := readClaudeCodeSourceMetaCapped(resolvedPath)
	if err != nil {
		return claudeCodeSourceMeta{}, false, fmt.Errorf("read claude-code source meta %q: %w", metaPath, err)
	}
	var meta claudeCodeSourceMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return claudeCodeSourceMeta{}, false, fmt.Errorf("decode claude-code source meta %q: %w", metaPath, err)
	}
	return meta, true, nil
}

func readClaudeCodeSourceMetaCapped(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- path is resolved under the configured projects root by readClaudeCodeSourceMeta before this capped read.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if info, statErr := file.Stat(); statErr == nil && info.Size() > claudeCodeSourceMetaReadMax {
		return nil, fmt.Errorf("claude-code source meta exceeds %d bytes", claudeCodeSourceMetaReadMax)
	}
	return readClaudeCodeSourceMetaLimited(file)
}

func readClaudeCodeSourceMetaLimited(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, claudeCodeSourceMetaReadMax+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > claudeCodeSourceMetaReadMax {
		return nil, fmt.Errorf("claude-code source meta exceeds %d bytes", claudeCodeSourceMetaReadMax)
	}
	return raw, nil
}

func inspectClaudeCodeChildCompletion(path string) (claudeCodeChildCompletion, error) {
	file, err := os.Open(path) // #nosec G304 -- transcript paths come from listClaudeCodeTranscripts after source-root containment checks.
	if err != nil {
		return claudeCodeChildCompletion{}, fmt.Errorf("open claude-code child transcript: %w", err)
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	var completion claudeCodeChildCompletion
	for {
		line, readErr := readClaudeCodeSourceLine(reader)
		if readErr != nil && len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return claudeCodeChildCompletion{}, fmt.Errorf("read claude-code child transcript: %w", readErr)
		}
		line = trimLineEnding(line)
		next, err := inspectClaudeCodeCompletionLine(line)
		if err != nil {
			return claudeCodeChildCompletion{}, fmt.Errorf("%s: %w", path, err)
		}
		if next != nil {
			completion = *next
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return completion, nil
}

func inspectClaudeCodeCompletionLine(line []byte) (*claudeCodeChildCompletion, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var rec claudeCodeSourceRecord
	if err := decodeJSON(trimmed, &rec); err != nil {
		return nil, fmt.Errorf("decode record: %w", err)
	}
	tsUs, err := parseClaudeCodeRecordTimestamp(rec)
	if err != nil {
		return nil, err
	}
	completion := claudeCodeChildCompletion{}
	if rec.Type != "assistant" {
		return &completion, nil
	}
	var msg claudeCodeAssistantMessage
	if err := decodeJSONPayload(rec.Message, &msg); err != nil {
		return nil, fmt.Errorf("decode assistant.message: %w", err)
	}
	if len(msg.Content) > 0 && msg.Content[0].Type == "text" {
		completion.completed = true
		completion.endedAt = tsUs
	}
	return &completion, nil
}
