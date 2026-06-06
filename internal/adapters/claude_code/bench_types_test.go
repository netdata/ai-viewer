package claude_code

import "encoding/json"

type benchEnvelope struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid,omitempty"`
	IsSidechain bool            `json:"isSidechain,omitempty"`
	AgentID     string          `json:"agentId,omitempty"`
	SessionID   string          `json:"sessionId,omitempty"`
	Timestamp   string          `json:"timestamp,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"`
}

type benchUserMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type benchAssistantMessage struct {
	ID         string              `json:"id"`
	Role       string              `json:"role"`
	Model      string              `json:"model"`
	StopReason string              `json:"stop_reason"`
	Usage      benchAssistantUsage `json:"usage"`
	Content    []benchContentBlock `json:"content"`
}

type benchAssistantUsage struct {
	InputTokens              int64           `json:"input_tokens"`
	OutputTokens             int64           `json:"output_tokens"`
	CacheCreationInputTokens int64           `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64           `json:"cache_read_input_tokens"`
	ServerToolUse            json.RawMessage `json:"server_tool_use"`
	ServiceTier              string          `json:"service_tier"`
}

type benchServerToolUse struct {
	WebSearchRequests int64 `json:"web_search_requests"`
}

type benchContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type benchReadInput struct {
	FilePath string `json:"file_path"`
}

type benchAgentInput struct {
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
}

type benchSubagentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	ToolUseID   string `json:"toolUseId"`
}
