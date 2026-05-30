package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// errUnknownRecordType is the sentinel wrapped by unknownTypeError so callers
// can detect the "skip and surface as parse error" case via errors.Is for an
// unknown TOP-LEVEL RolloutItem.type. The concrete unknownTypeError carries the
// offending type string so the scanner (a later chunk) emits exactly one
// SourceError per distinct variant per session (spec adapter-codex.md:220).
var errUnknownRecordType = errors.New("codex: unknown record type")

// errUnknownPayloadType is the sibling sentinel for an unknown NESTED
// payload.type inside a known top-level type (spec adapter-codex.md:221). It is
// deliberately distinct from errUnknownRecordType so dedup keys for top-level
// and nested unknowns never collide.
var errUnknownPayloadType = errors.New("codex: unknown payload type")

// unknownTypeError reports a line whose top-level `type` discriminator is not a
// documented codex RolloutItem type. It wraps errUnknownRecordType (for
// errors.Is) and exposes the raw Type so callers dedup one SourceError per
// distinct unknown variant per session.
type unknownTypeError struct {
	Type string
}

func (e *unknownTypeError) Error() string {
	return fmt.Sprintf("%s: %q", errUnknownRecordType.Error(), e.Type)
}

// Unwrap lets errors.Is(err, errUnknownRecordType) match.
func (e *unknownTypeError) Unwrap() error { return errUnknownRecordType }

// unknownPayloadTypeError reports a nested payload.type that is not documented
// for its owning top-level type. Owner is the top-level type (e.g.
// "response_item") and Type is the offending nested discriminator, so the dedup
// key is "<Owner>/<Type>" and a nested name never collides across owners.
type unknownPayloadTypeError struct {
	Owner string
	Type  string
}

func (e *unknownPayloadTypeError) Error() string {
	return fmt.Sprintf("%s: %s/%q", errUnknownPayloadType.Error(), e.Owner, e.Type)
}

// Unwrap lets errors.Is(err, errUnknownPayloadType) match.
func (e *unknownPayloadTypeError) Unwrap() error { return errUnknownPayloadType }

// recordType discriminates the top-level RolloutItem variants. Values are
// verbatim from the producer's serde tag (openai/codex protocol.rs:2705-2734,
// 2849-2854; serde tag="type", content="payload", rename_all="snake_case").
type recordType string

const (
	recSessionMeta  recordType = "session_meta"
	recTurnContext  recordType = "turn_context"
	recResponseItem recordType = "response_item"
	recEventMsg     recordType = "event_msg"
	recCompacted    recordType = "compacted"
)

// envelope is the shared RolloutLine wrapper present on every line. The flatten
// in Rust (struct RolloutLine { timestamp, #[serde(flatten)] item }) lands the
// tag at top level (`type`) and the content under `payload`.
type envelope struct {
	TS      string          `json:"timestamp"`
	Type    recordType      `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// record is the parsed codex line. Env is always populated; exactly one typed
// payload pointer is non-nil for the variants the mapper (later chunk)
// consumes. Raw holds the verbatim line bytes so the mapper can build
// file://...#L<line> PayloadRefs without re-typing every nested variant.
type record struct {
	Env          envelope
	SessionMeta  *sessionMetaPayload
	TurnContext  *turnContextPayload
	ResponseItem *responseItemPayload
	EventMsg     *eventMsgPayload
	Compacted    *compactedPayload
	Raw          []byte
}

// Type returns the top-level RolloutItem discriminator.
func (r record) Type() recordType { return r.Env.Type }

// Timestamp returns the envelope timestamp (RFC3339 UTC) — the canonical time
// source for the line (spec adapter-codex.md:56-60).
func (r record) Timestamp() string { return r.Env.TS }

// parseLine decodes one JSONL line into a record. Whitespace-only / empty lines
// return (record{}, true, nil) to signal "skip silently". Malformed JSON or a
// missing top-level type returns a wrapped error. An unknown top-level type
// returns errUnknownRecordType (wrapped); an unknown nested payload.type returns
// errUnknownPayloadType (wrapped). A catch-all/no-op nested variant (e.g.
// ghost_snapshot) or an absent payload/nested-type returns (record, true, nil).
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
	case recSessionMeta:
		var p sessionMetaPayload
		if err := decodePayload(env.Payload, &p); err != nil {
			return record{}, false, fmt.Errorf("decode session_meta: %w", err)
		}
		rec.SessionMeta = &p
	case recTurnContext:
		var p turnContextPayload
		if err := decodePayload(env.Payload, &p); err != nil {
			return record{}, false, fmt.Errorf("decode turn_context: %w", err)
		}
		rec.TurnContext = &p
	case recResponseItem:
		return decodeResponseItem(rec)
	case recEventMsg:
		return decodeEventMsg(rec)
	case recCompacted:
		var p compactedPayload
		if err := decodePayload(env.Payload, &p); err != nil {
			return record{}, false, fmt.Errorf("decode compacted: %w", err)
		}
		rec.Compacted = &p
	default:
		return record{}, false, &unknownTypeError{Type: string(env.Type)}
	}
	return rec, false, nil
}

// decodePayload unmarshals a payload body into dst, tolerating an absent body
// (a known top-level type with no payload is not an error — it just carries no
// nested data). A bare JSON null is likewise treated as absent.
func decodePayload(payload json.RawMessage, dst any) error {
	body := bytes.TrimSpace(payload)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return nil
	}
	return json.Unmarshal(body, dst)
}

// nestedType extracts only the nested payload.type discriminator without
// committing to a full typed decode. Returns "" when the payload is absent or
// carries no type (both tolerated by the callers).
func nestedType(payload json.RawMessage) (string, error) {
	body := bytes.TrimSpace(payload)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return "", nil
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", err
	}
	return probe.Type, nil
}

// decodeResponseItem handles the response_item top-level type: it reads the
// nested discriminator, classifies it (known / catch-all-no-op / unknown), and
// only fully decodes the known variants. Unknown nested types surface
// errUnknownPayloadType; catch-all variants (ResponseItem::Other upstream, e.g.
// ghost_snapshot) skip silently per rule #21.
func decodeResponseItem(rec record) (record, bool, error) {
	nt, err := nestedType(rec.Env.Payload)
	if err != nil {
		return record{}, false, fmt.Errorf("decode response_item payload type: %w", err)
	}
	if nt == "" {
		return rec, true, nil
	}
	if _, ok := responseItemNoOp[nt]; ok {
		return rec, true, nil
	}
	if _, ok := responseItemTypes[nt]; !ok {
		return record{}, false, &unknownPayloadTypeError{Owner: string(recResponseItem), Type: nt}
	}
	var p responseItemPayload
	if err := decodePayload(rec.Env.Payload, &p); err != nil {
		return record{}, false, fmt.Errorf("decode response_item: %w", err)
	}
	rec.ResponseItem = &p
	return rec, false, nil
}

// decodeEventMsg handles the event_msg top-level type, mirroring
// decodeResponseItem: known nested types decode; catch-all/no-op variants skip
// silently; unknown nested types surface errUnknownPayloadType.
func decodeEventMsg(rec record) (record, bool, error) {
	nt, err := nestedType(rec.Env.Payload)
	if err != nil {
		return record{}, false, fmt.Errorf("decode event_msg payload type: %w", err)
	}
	if nt == "" {
		return rec, true, nil
	}
	if _, ok := eventMsgNoOp[nt]; ok {
		return rec, true, nil
	}
	if _, ok := eventMsgTypes[nt]; !ok {
		return record{}, false, &unknownPayloadTypeError{Owner: string(recEventMsg), Type: nt}
	}
	var p eventMsgPayload
	if err := decodePayload(rec.Env.Payload, &p); err != nil {
		return record{}, false, fmt.Errorf("decode event_msg: %w", err)
	}
	rec.EventMsg = &p
	return rec, false, nil
}
