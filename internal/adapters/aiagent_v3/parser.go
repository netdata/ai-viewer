package aiagent_v3

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// formatVersion is the v3 ledger envelope version declared by the producer
// (`ai-agent.git/src/evidence/types.ts:1`). The parser rejects records
// whose declared version differs so future format bumps fail loud.
const formatVersion = 3

// errUnknownRecordType is returned by parseLine when the record's
// recordType is not one of the documented v3 types. The caller treats
// this as a non-fatal parse error (forwarded via opts.OnError per spec
// §3 and §adapter-contract.md "Error Handling").
var errUnknownRecordType = errors.New("aiagent_v3: unknown record type")

// recordType discriminates the parsed v3 ledger record variants.
type recordType string

const (
	recSessionStart   recordType = "session_start"
	recTurnStart      recordType = "turn_start"
	recTurnEnd        recordType = "turn_end"
	recSessionSummary recordType = "session_summary"
	recSessionError   recordType = "session_error"
)

// commonFields are present on every v3 record.
type commonFields struct {
	Version    int        `json:"version"`
	RecordType recordType `json:"recordType"`
	Seq        uint64     `json:"seq"`
	Ts         string     `json:"ts"`
	OriginID   string     `json:"originId"`
	SessionID  string     `json:"sessionId"`
}

// payloadRef mirrors EvidencePayloadRef from
// ai-agent.git/src/evidence/types.ts:62-76. Captured-false refs may
// omit path/sha/bytes; consumers must defend.
type payloadRef struct {
	Kind            string `json:"kind"`
	OpID            string `json:"opId"`
	Turn            int    `json:"turn"`
	OpIndex         int    `json:"opIndex"`
	Format          string `json:"format"`
	Compression     string `json:"compression,omitempty"`
	Path            string `json:"path,omitempty"`
	OriginalBytes   *int64 `json:"originalBytes,omitempty"`
	CompressedBytes *int64 `json:"compressedBytes,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	Captured        bool   `json:"captured"`
	Truncated       bool   `json:"truncated"`
	Redacted        bool   `json:"redacted"`
}

// accounting mirrors EvidenceAccountingSummary. CostUSD is a pointer so
// the parser can distinguish "field absent" from "explicitly zero".
type accounting struct {
	TokensIn         int64    `json:"tokensIn"`
	TokensOut        int64    `json:"tokensOut"`
	TokensCacheRead  int64    `json:"tokensCacheRead"`
	TokensCacheWrite int64    `json:"tokensCacheWrite"`
	CostUSD          *float64 `json:"costUsd,omitempty"`
}

// childSessionRef mirrors EvidenceChildSessionRef.
type childSessionRef struct {
	SessionID       string `json:"sessionId"`
	OriginID        string `json:"originId"`
	ParentSessionID string `json:"parentSessionId"`
	ParentOpID      string `json:"parentOpId"`
	LedgerPath      string `json:"ledgerPath"`
	Status          string `json:"status"`
	AgentID         string `json:"agentId,omitempty"`
	CallPath        string `json:"callPath,omitempty"`
}

// opSummary mirrors EvidenceOperationSummary.
type opSummary struct {
	OpID          string            `json:"opId"`
	OpIndex       int               `json:"opIndex"`
	Kind          string            `json:"kind"`
	Status        string            `json:"status"`
	StartedAt     string            `json:"startedAt,omitempty"`
	EndedAt       string            `json:"endedAt,omitempty"`
	DurationMs    *int64            `json:"durationMs,omitempty"`
	Name          string            `json:"name,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	Model         string            `json:"model,omitempty"`
	PayloadRefs   []payloadRef      `json:"payloadRefs,omitempty"`
	ChildSessions []childSessionRef `json:"childSessions,omitempty"`
	Accounting    *accounting       `json:"accounting,omitempty"`
	Attributes    map[string]any    `json:"attributes,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// finalReport mirrors EvidenceSessionSummaryBody.finalReport.
type finalReport struct {
	Format   string `json:"format,omitempty"`
	Captured bool   `json:"captured"`
}

// sessionStartBody mirrors EvidenceSessionStartBody.
type sessionStartBody struct {
	AgentID         string         `json:"agentId,omitempty"`
	CallPath        string         `json:"callPath,omitempty"`
	ParentSessionID string         `json:"parentSessionId,omitempty"`
	ParentOpID      string         `json:"parentOpId,omitempty"`
	HeadendID       string         `json:"headendId,omitempty"`
	CapturePayloads bool           `json:"capturePayloads"`
	Attributes      map[string]any `json:"attributes,omitempty"`
}

// turnStartBody mirrors EvidenceTurnStartBody.
type turnStartBody struct {
	Turn       int            `json:"turn"`
	Attempt    *int           `json:"attempt,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// turnEndBody mirrors EvidenceTurnEndBody.
type turnEndBody struct {
	Turn       int            `json:"turn"`
	Status     string         `json:"status"`
	Ops        []opSummary    `json:"ops"`
	Accounting *accounting    `json:"accounting,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Errors     []string       `json:"errors,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// sessionSummaryBody mirrors EvidenceSessionSummaryBody.
type sessionSummaryBody struct {
	Status        string            `json:"status"`
	Accounting    *accounting       `json:"accounting,omitempty"`
	ChildSessions []childSessionRef `json:"childSessions,omitempty"`
	FinalReport   *finalReport      `json:"finalReport,omitempty"`
	Attributes    map[string]any    `json:"attributes,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// sessionErrorBody mirrors EvidenceSessionErrorBody.
type sessionErrorBody struct {
	Error      string         `json:"error"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// record is the parsed-and-validated v3 ledger record. Exactly one of the
// *Body pointers is non-nil; the discriminator is Common.RecordType.
type record struct {
	Common         commonFields
	SessionStart   *sessionStartBody
	TurnStart      *turnStartBody
	TurnEnd        *turnEndBody
	SessionSummary *sessionSummaryBody
	SessionError   *sessionErrorBody
}

// parseLine decodes one JSONL line into a record. Whitespace-only / empty
// lines return (record{}, nil, true) to signal "skip silently"; malformed
// JSON or missing required fields return a wrapped error. Unknown
// recordTypes return errUnknownRecordType wrapped so callers can detect
// the "skip and surface as parse error" case via errors.Is.
func parseLine(line []byte) (record, bool, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return record{}, true, nil
	}

	var common commonFields
	if err := json.Unmarshal(trimmed, &common); err != nil {
		return record{}, false, fmt.Errorf("decode envelope: %w", err)
	}
	if err := validateCommon(common); err != nil {
		return record{}, false, err
	}

	rec := record{Common: common}
	switch common.RecordType {
	case recSessionStart:
		var body sessionStartBody
		if err := json.Unmarshal(trimmed, &body); err != nil {
			return record{}, false, fmt.Errorf("decode session_start: %w", err)
		}
		rec.SessionStart = &body
	case recTurnStart:
		var body turnStartBody
		if err := json.Unmarshal(trimmed, &body); err != nil {
			return record{}, false, fmt.Errorf("decode turn_start: %w", err)
		}
		if body.Turn < 1 {
			return record{}, false, fmt.Errorf("turn_start.turn must be >= 1 (got %d)", body.Turn)
		}
		rec.TurnStart = &body
	case recTurnEnd:
		var body turnEndBody
		if err := json.Unmarshal(trimmed, &body); err != nil {
			return record{}, false, fmt.Errorf("decode turn_end: %w", err)
		}
		if body.Turn < 1 {
			return record{}, false, fmt.Errorf("turn_end.turn must be >= 1 (got %d)", body.Turn)
		}
		if body.Status == "" {
			return record{}, false, errors.New("turn_end.status is required")
		}
		rec.TurnEnd = &body
	case recSessionSummary:
		var body sessionSummaryBody
		if err := json.Unmarshal(trimmed, &body); err != nil {
			return record{}, false, fmt.Errorf("decode session_summary: %w", err)
		}
		if body.Status == "" {
			return record{}, false, errors.New("session_summary.status is required")
		}
		rec.SessionSummary = &body
	case recSessionError:
		var body sessionErrorBody
		if err := json.Unmarshal(trimmed, &body); err != nil {
			return record{}, false, fmt.Errorf("decode session_error: %w", err)
		}
		rec.SessionError = &body
	default:
		return record{}, false, fmt.Errorf("%w: %q", errUnknownRecordType, common.RecordType)
	}

	return rec, false, nil
}

// validateCommon checks the envelope fields required on every record per
// spec §3.1. Returns nil if the record is structurally acceptable.
func validateCommon(c commonFields) error {
	if c.Version == 0 {
		return errors.New("envelope.version is required")
	}
	if c.Version != formatVersion {
		return fmt.Errorf("envelope.version=%d not supported (want %d)", c.Version, formatVersion)
	}
	if c.RecordType == "" {
		return errors.New("envelope.recordType is required")
	}
	if c.Seq < 1 {
		return fmt.Errorf("envelope.seq must be >= 1 (got %d)", c.Seq)
	}
	if c.Ts == "" {
		return errors.New("envelope.ts is required")
	}
	if c.OriginID == "" {
		return errors.New("envelope.originId is required")
	}
	if c.SessionID == "" {
		return errors.New("envelope.sessionId is required")
	}
	return nil
}
