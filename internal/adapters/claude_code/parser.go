package claude_code

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// errUnknownRecordType is the sentinel wrapped by unknownTypeError so callers
// can detect the "skip and surface as parse error" case via errors.Is. The
// concrete unknownTypeError additionally carries the offending `type` string
// so the caller can dedup one SourceError per distinct variant (spec §3.12).
var errUnknownRecordType = errors.New("claude_code: unknown record type")

// unknownTypeError reports a record whose `type` discriminator is not a
// documented claude-code type. It wraps errUnknownRecordType (for errors.Is)
// and exposes the raw Type so streamLines can emit exactly one SourceError per
// distinct unknown variant per scan (acceptance #2).
type unknownTypeError struct {
	Type string
}

func (e *unknownTypeError) Error() string {
	return fmt.Sprintf("%s: %q", errUnknownRecordType.Error(), e.Type)
}

// Unwrap lets errors.Is(err, errUnknownRecordType) match.
func (e *unknownTypeError) Unwrap() error { return errUnknownRecordType }

// recordType discriminates the parsed claude-code JSONL record variants.
// Values are verbatim from the producer's `type` discriminator
// (jarmuine/claude-code src/types/logs.ts:297-317).
type recordType string

const (
	recUser                recordType = "user"
	recAssistant           recordType = "assistant"
	recSystem              recordType = "system"
	recAttachment          recordType = "attachment"
	recQueueOperation      recordType = "queue-operation"
	recLastPrompt          recordType = "last-prompt"
	recAITitle             recordType = "ai-title"
	recCustomTitle         recordType = "custom-title"
	recPermissionMode      recordType = "permission-mode"
	recPRLink              recordType = "pr-link"
	recBridgeSession       recordType = "bridge-session"
	recFileHistorySnapshot recordType = "file-history-snapshot"
)

// knownNoOpTypes are record types the producer's Entry union declares but
// whose semantics the adapter intentionally ignores (no event, no error):
// internal context-collapse markers and metadata that does not map to the
// canonical model. Tolerated silently so a future producer that emits them
// does not flood /api/health (spec §3.12). They are distinct from truly
// unknown types, which DO surface one SourceError per variant.
var knownNoOpTypes = map[recordType]struct{}{
	"summary":                 {},
	"task-summary":            {},
	"tag":                     {},
	"agent-name":              {},
	"agent-color":             {},
	"agent-setting":           {},
	"mode":                    {},
	"worktree-state":          {},
	"content-replacement":     {},
	"attribution-snapshot":    {},
	"speculation-accept":      {},
	"marble-origami-commit":   {},
	"marble-origami-snapshot": {},
}

// envelope carries the shared fields present across record variants. Most
// are optional because metadata-snapshot records (last-prompt, ai-title,
// custom-title, permission-mode, bridge-session, file-history-snapshot)
// lack uuid/timestamp/cwd entirely (spec §3 "Records that LACK timestamp
// and uuid").
type envelope struct {
	Type              recordType      `json:"type"`
	UUID              string          `json:"uuid"`
	ParentUUID        *string         `json:"parentUuid"`
	LogicalParentUUID *string         `json:"logicalParentUuid"`
	IsSidechain       bool            `json:"isSidechain"`
	AgentID           string          `json:"agentId"`
	PromptID          string          `json:"promptId"`
	SessionID         string          `json:"sessionId"`
	Cwd               string          `json:"cwd"`
	UserType          string          `json:"userType"`
	Entrypoint        string          `json:"entrypoint"`
	Timestamp         string          `json:"timestamp"`
	Version           string          `json:"version"`
	GitBranch         string          `json:"gitBranch"`
	Slug              string          `json:"slug"`
	IsMeta            *bool           `json:"isMeta"`
	IsCompactSummary  *bool           `json:"isCompactSummary"`
	RequestID         string          `json:"requestId"`
	Subtype           string          `json:"subtype"`
	Message           json.RawMessage `json:"message"`
}

// userMessage is the polymorphic `message` body of a `user` record.
// Content is either a string (operator prompt) or an array of blocks
// (tool_result, text, image). Decoded lazily into contentString /
// contentBlocks by classifyUserContent.
type userMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock is one element of an assistant/user content array. Only the
// fields the mapper consumes are decoded; unknown block types pass through
// as Type only.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// assistantMessage is the `message` body of an `assistant` record.
type assistantMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Usage      *assistantUsage `json:"usage"`
	Content    []contentBlock  `json:"content"`
}

// assistantUsage mirrors message.usage. Pointers distinguish absent from
// zero only where it matters; counts default to 0 which is correct for the
// effective-input computation.
type assistantUsage struct {
	InputTokens              int64           `json:"input_tokens"`
	OutputTokens             int64           `json:"output_tokens"`
	CacheCreationInputTokens int64           `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64           `json:"cache_read_input_tokens"`
	ServerToolUse            json.RawMessage `json:"server_tool_use"`
	ServiceTier              string          `json:"service_tier"`
}

// compactMetadata is the body of a system.subtype=="compact_boundary"
// record (spec §3.3, §9). trigger is tolerated as any string.
type compactMetadata struct {
	Trigger          string          `json:"trigger"`
	PreTokens        int64           `json:"preTokens"`
	PostTokens       int64           `json:"postTokens"`
	DurationMs       int64           `json:"durationMs"`
	PreservedSegment json.RawMessage `json:"preservedSegment"`
	PreservedMessage json.RawMessage `json:"preservedMessages"`
}

// systemBody carries the subtype-specific fields the mapper consumes. Most
// are decoded opaquely; only compact_boundary and turn_duration drive
// canonical events beyond a LogEntry.
type systemBody struct {
	Content     string           `json:"content"`
	Compact     *compactMetadata `json:"compactMetadata"`
	DurationMs  *int64           `json:"durationMs"`
	APIError    json.RawMessage  `json:"error"`
	RetryMs     *int64           `json:"retryInMs"`
	RetryNumber *int64           `json:"retryAttempt"`
}

// record is the parsed claude-code record. Envelope is always populated;
// the typed bodies are non-nil only for the variants the mapper consumes.
type record struct {
	Env       envelope
	User      *userMessage
	Assistant *assistantMessage
	System    *systemBody
	// HasToolUseResult reports whether a `user` record carried a top-level
	// toolUseResult body (the structured tool-result echo, spec §3.1). The
	// mapper emits a PayloadRefEvent for it on the finalized tool op (§5.4).
	HasToolUseResult bool
	// Raw is the verbatim line bytes (sans trailing newline). Used to
	// surface metadata-snapshot field values (lastPrompt, title, etc.) and
	// attachment bodies without re-typing every variant; decoded on demand by
	// the mapper's snapshot/payload paths.
	Raw []byte
}

// attachmentBody carries the fields of an `attachment` record the mapper's
// log path consumes (spec §3.4, §338). A `file` attachment's filename and
// displayPath are surfaced in the attachment LogEntry's extras; no PayloadRef
// is emitted for any attachment (it has no owning op — P1.1b).
type attachmentBody struct {
	Type        string `json:"type"`
	Filename    string `json:"filename"`
	DisplayPath string `json:"displayPath"`
}

// decodeAttachment extracts the `attachment` sub-object from a record's raw
// bytes. Returns the zero attachmentBody on any decode failure (defensive).
func decodeAttachment(raw []byte) attachmentBody {
	if len(raw) == 0 {
		return attachmentBody{}
	}
	var wrapper struct {
		Attachment attachmentBody `json:"attachment"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return attachmentBody{}
	}
	return wrapper.Attachment
}

// parseLine decodes one JSONL line into a record. Whitespace-only / empty
// lines return (record{}, true, nil) to signal "skip silently". Malformed
// JSON returns a wrapped error. A known-but-ignored type (knownNoOpTypes)
// returns (record, true, nil) — skipped without surfacing. An unknown type
// returns errUnknownRecordType wrapped so callers detect the
// "skip and surface as parse error" case via errors.Is.
func parseLine(line []byte) (record, bool, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return record{}, true, nil
	}

	var env envelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return record{}, false, fmt.Errorf("decode envelope: %w", err)
	}
	if env.Type == "" {
		return record{}, false, errors.New("record.type is required")
	}

	rec := record{Env: env, Raw: append([]byte(nil), trimmed...)}
	switch env.Type {
	case recUser:
		var msg userMessage
		if len(env.Message) > 0 {
			if err := json.Unmarshal(env.Message, &msg); err != nil {
				return record{}, false, fmt.Errorf("decode user.message: %w", err)
			}
		}
		rec.User = &msg
		// Detect a top-level toolUseResult body (spec §3.1) so the mapper can
		// emit a PayloadRef for it (§5.4). A bare `null` value does not count.
		var probe struct {
			ToolUseResult json.RawMessage `json:"toolUseResult"`
		}
		if json.Unmarshal(trimmed, &probe) == nil {
			tur := bytes.TrimSpace(probe.ToolUseResult)
			rec.HasToolUseResult = len(tur) > 0 && !bytes.Equal(tur, []byte("null"))
		}
	case recAssistant:
		var msg assistantMessage
		if len(env.Message) > 0 {
			if err := json.Unmarshal(env.Message, &msg); err != nil {
				return record{}, false, fmt.Errorf("decode assistant.message: %w", err)
			}
		}
		rec.Assistant = &msg
	case recSystem:
		var body systemBody
		if err := json.Unmarshal(trimmed, &body); err != nil {
			return record{}, false, fmt.Errorf("decode system: %w", err)
		}
		rec.System = &body
	case recAttachment, recQueueOperation, recLastPrompt, recAITitle,
		recCustomTitle, recPermissionMode, recPRLink, recBridgeSession,
		recFileHistorySnapshot:
		// Consumed straight from Raw by the mapper's snapshot/log paths;
		// no extra typed body needed.
	default:
		if _, ok := knownNoOpTypes[env.Type]; ok {
			// Declared-but-ignored producer type: skip without surfacing.
			return rec, true, nil
		}
		return record{}, false, &unknownTypeError{Type: string(env.Type)}
	}
	return rec, false, nil
}

// classifyUserContent decodes a user record's polymorphic message.content
// into either a string prompt (returns string, nil, true) or an array of
// content blocks (returns "", blocks, false). A nil/absent content yields
// ("", nil, false). The bool reports whether the content was a string.
func classifyUserContent(msg *userMessage) (string, []contentBlock, bool) {
	if msg == nil || len(msg.Content) == 0 {
		return "", nil, false
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return s, nil, true
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		return "", blocks, false
	}
	return "", nil, false
}

// boolValue returns the dereferenced value of an *bool, or false when nil.
func boolValue(b *bool) bool { return b != nil && *b }
